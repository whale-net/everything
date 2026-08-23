package repository

import "testing"

// TestChart_ResolveArgoApplicationName covers the three cases
// WritebackWorkflow's ArgoCD Application name derivation depends on (see
// worker/writeback/workflow.go's RenderedState.ArgoApplicationName and
// architecture/12-writeback-outbox-temporal.md's "ArgoCD Application name"
// section): no override falls back to the "<FullName>-<environment>"
// convention, an override with an "{environment}" token substitutes it, and
// a plain literal override (no token, a single-environment ad-hoc
// deployment) is returned verbatim regardless of environmentKey.
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
			name:           "override substitutes {environment}",
			chart:          Chart{Domain: "acme", Name: "foo", ArgoApplicationNameTemplate: "legacy-foo-{environment}"},
			environmentKey: "prod",
			want:           "legacy-foo-prod",
		},
		{
			name:           "override with no {environment} token is verbatim",
			chart:          Chart{Domain: "acme", Name: "foo", ArgoApplicationNameTemplate: "totally-different-app"},
			environmentKey: "prod",
			want:           "totally-different-app",
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
