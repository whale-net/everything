package repository

import "testing"

// TestChart_ResolveArgoApplicationName covers what WritebackWorkflow's
// ArgoCD Application name derivation depends on (see
// worker/writeback/workflow.go's RenderedState.ArgoApplicationName and
// architecture/12-writeback-outbox-temporal.md's "ArgoCD Application name"
// section): an environment absent from ArgoApplicationNameOverrides falls
// back to the "<FullName>-<environment>" convention; an environment present
// in the map returns its override verbatim; and -- the scenario a
// single per-chart template couldn't express -- two environments on the
// same chart can hold completely unrelated override names, each
// independent of the other and of the convention.
func TestChart_ResolveArgoApplicationName(t *testing.T) {
	tests := []struct {
		name           string
		chart          Chart
		environmentKey string
		want           string
	}{
		{
			name:           "no override falls back to convention",
			chart:          Chart{Domain: "acme", Name: "foo"},
			environmentKey: "stage",
			want:           "acme-foo-stage",
		},
		{
			name: "override for this environment is returned verbatim",
			chart: Chart{Domain: "acme", Name: "foo", ArgoApplicationNameOverrides: map[string]string{
				"prod": "legacy-foo-prod",
			}},
			environmentKey: "prod",
			want:           "legacy-foo-prod",
		},
		{
			name: "environment absent from a non-empty overrides map still falls back to convention",
			chart: Chart{Domain: "acme", Name: "foo", ArgoApplicationNameOverrides: map[string]string{
				"prod": "legacy-foo-prod",
			}},
			environmentKey: "dev",
			want:           "acme-foo-dev",
		},
		{
			name: "dev and prod overrides share no naming pattern -- each is independent",
			chart: Chart{Domain: "acme", Name: "foo", ArgoApplicationNameOverrides: map[string]string{
				"dev":  "foo-dev-app",
				"prod": "prod-svc-foo",
			}},
			environmentKey: "prod",
			want:           "prod-svc-foo",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.chart.ResolveArgoApplicationName(tc.environmentKey); got != tc.want {
				t.Errorf("ResolveArgoApplicationName(%q) = %q, want %q", tc.environmentKey, got, tc.want)
			}
		})
	}
}

// TestChart_ResolveArgoApplicationName_DevProdIndependent is the exact
// dev-vs-prod scenario a single per-chart template (the design this
// superseded) couldn't express: setting dev's override must never affect
// prod's, and vice versa, whether or not they share any naming
// relationship.
func TestChart_ResolveArgoApplicationName_DevProdIndependent(t *testing.T) {
	chart := Chart{Domain: "acme", Name: "foo", ArgoApplicationNameOverrides: map[string]string{
		"dev": "foo-dev-app",
	}}
	if got := chart.ResolveArgoApplicationName("dev"); got != "foo-dev-app" {
		t.Errorf("dev: ResolveArgoApplicationName = %q, want %q", got, "foo-dev-app")
	}
	if got := chart.ResolveArgoApplicationName("prod"); got != "acme-foo-prod" {
		t.Errorf("prod (no override set): ResolveArgoApplicationName = %q, want convention %q", got, "acme-foo-prod")
	}
}

// TestArgoRevisionMatches covers the revision forms ArgoCD reports: chart
// versions with or without a leading "v", comma-joined multi-source lists,
// and short vs. full git SHAs -- without letting one version prefix-match
// another.
func TestArgoRevisionMatches(t *testing.T) {
	cases := []struct {
		observed string
		expected []string
		want     bool
	}{
		{"0.0.39", []string{"v0.0.39"}, true},
		{"v0.0.39", []string{"0.0.39"}, true},
		{"abc1234def,0.0.39", []string{"0.0.39"}, true},
		{"0.0.38", []string{"0.0.39"}, false},
		{"1.10.1000", []string{"1.10.100"}, false},
		{"abc1234def5678", []string{"", "abc1234"}, true},
		{"", []string{"0.0.39"}, false},
		{"0.0.39", []string{""}, false},
	}
	for _, c := range cases {
		if got := ArgoRevisionMatches(c.observed, c.expected...); got != c.want {
			t.Errorf("ArgoRevisionMatches(%q, %q) = %v, want %v", c.observed, c.expected, got, c.want)
		}
	}
}

// TestClassifyArgoObservation_PinsToRevision proves a Synced/Healthy/
// Succeeded (or Failed) observation for the previous release is pending,
// not this promotion's outcome, while legacy rows without revisions keep
// the revision-blind rule.
func TestClassifyArgoObservation_PinsToRevision(t *testing.T) {
	healthy := func(syncRev, opRev string) PromotionSyncEvent {
		return PromotionSyncEvent{SyncStatus: "Synced", HealthStatus: "Healthy", OperationPhase: "Succeeded", SyncRevision: syncRev, OperationRevision: opRev}
	}
	cases := []struct {
		name string
		e    PromotionSyncEvent
		want PromotionSyncOutcome
	}{
		{"previous release", healthy("0.0.38", "0.0.38"), PromotionSyncOutcomePending},
		{"synced but operation still previous", healthy("0.0.39", "0.0.38"), PromotionSyncOutcomePending},
		{"this release", healthy("0.0.39", "0.0.39"), PromotionSyncOutcomeSyncedHealthy},
		{"legacy row without revisions", healthy("", ""), PromotionSyncOutcomeSyncedHealthy},
		{"previous release hook failed", PromotionSyncEvent{SyncStatus: "Synced", HealthStatus: "Healthy", OperationPhase: "Failed", SyncRevision: "0.0.38", OperationRevision: "0.0.38"}, PromotionSyncOutcomePending},
		{"this release hook failed", PromotionSyncEvent{SyncStatus: "Synced", HealthStatus: "Healthy", OperationPhase: "Failed", SyncRevision: "0.0.39", OperationRevision: "0.0.39"}, PromotionSyncOutcomeSyncFailed},
		{"this release degraded", PromotionSyncEvent{SyncStatus: "Synced", HealthStatus: "Degraded", OperationPhase: "Succeeded", SyncRevision: "0.0.39", OperationRevision: "0.0.39"}, PromotionSyncOutcomeSyncFailed},
	}
	for _, c := range cases {
		if got := ClassifyArgoObservation(c.e, "0.0.39", ""); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if got := ClassifyArgoObservation(healthy("0.0.38", "0.0.38")); got != PromotionSyncOutcomeSyncedHealthy {
		t.Errorf("no expected revision: got %q, want revision-blind synced_healthy", got)
	}
}
