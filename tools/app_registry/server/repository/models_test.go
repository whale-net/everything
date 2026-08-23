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
