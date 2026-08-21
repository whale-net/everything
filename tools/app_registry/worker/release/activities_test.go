package release

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestActivities_DispatchBuild_HeterogeneousVersions_ReturnsClearError proves
// the "batch must resolve to one uniform version" limitation documented in
// github.go's package doc comment is actually enforced when going through
// the real Activities.DispatchBuild entry point (not just uniformVersion in
// isolation, which github_test.go's TestUniformVersion already covers) --
// see issue #889's Testing-phase instruction to verify this specifically.
func TestActivities_DispatchBuild_HeterogeneousVersions_ReturnsClearError(t *testing.T) {
	a := &Activities{GitHub: newTestDispatcher(t, http.NewServeMux())}
	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions: map[string]string{
			"image:demo-widget": "v1.0.0",
			"chart:demo-achart": "v1.0.1",
		},
	}
	_, err := a.DispatchBuild(context.Background(), plan, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "heterogeneous versions")
	require.ErrorContains(t, err, "dispatch build")
}

// TestActivities_DispatchBuild_DigestOverrides_ReturnsUnimplementedError
// covers FR2's out-of-scope digest-override input: a non-empty
// digestOverrides must fail clearly, never silently build fresh or silently
// ignore it (see activities.go's DispatchBuild doc comment).
func TestActivities_DispatchBuild_DigestOverrides_ReturnsUnimplementedError(t *testing.T) {
	a := &Activities{GitHub: newTestDispatcher(t, http.NewServeMux())}
	plan := ResolvedPlan{Versions: map[string]string{"image:demo-widget": "v1.0.0"}}
	_, err := a.DispatchBuild(context.Background(), plan, map[string]string{"image:demo-widget": "sha256:abc"})
	require.Error(t, err)
	require.ErrorContains(t, err, "digest overrides")
}

// TestActivities_DispatchBuild_GitHubNotConfigured_ReturnsClearError covers
// worker/main.go's "leave GitHub nil unless configured" path -- DispatchBuild
// must fail fast rather than nil-panic.
func TestActivities_DispatchBuild_GitHubNotConfigured_ReturnsClearError(t *testing.T) {
	a := &Activities{}
	plan := ResolvedPlan{Versions: map[string]string{"image:demo-widget": "v1.0.0"}}
	_, err := a.DispatchBuild(context.Background(), plan, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "not configured")
}

// TestActivities_DispatchBuild_EmptyPlan_ReturnsClearError covers a
// ResolvePlan result with no versions reaching DispatchBuild.
func TestActivities_DispatchBuild_EmptyPlan_ReturnsClearError(t *testing.T) {
	a := &Activities{GitHub: newTestDispatcher(t, http.NewServeMux())}
	_, err := a.DispatchBuild(context.Background(), ResolvedPlan{}, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "no versions")
}

// TestActivities_DispatchBuild_HappyPath_DispatchesUniformVersionWithSplitTargets
// proves the uniform-version happy path actually reaches GitHub with the
// correctly-split apps/helm_charts inputs (github_test.go's
// TestSplitPlanTargets/TestUniformVersion only test those helpers in
// isolation; this proves DispatchBuild wires them together correctly).
func TestActivities_DispatchBuild_HappyPath_DispatchesUniformVersionWithSplitTargets(t *testing.T) {
	var sawInputs map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		sawInputs, _ = body["inputs"].(map[string]any)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workflow_runs":[{"id":99,"html_url":"https://example/99","created_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}]}`))
	})

	a := &Activities{GitHub: newTestDispatcher(t, mux)}
	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions: map[string]string{
			"image:demo-widget": "v1.2.3",
			"chart:demo-achart": "v1.2.3",
		},
	}
	ref, err := a.DispatchBuild(context.Background(), plan, nil)
	require.NoError(t, err)
	require.Equal(t, "99", ref.RunID)
	require.Equal(t, "v1.2.3", sawInputs["version"])
	require.Equal(t, "demo-widget", sawInputs["apps"])
	require.Equal(t, "demo-achart", sawInputs["helm_charts"])

	// issue #901: the image-kind entry from plan.Versions must also be
	// forwarded as a JSON app_versions input, keyed by bare OwnerFullName
	// (not the "image:" TargetKey-prefixed form) -- this is what
	// release-helm-charts' --app-versions flag expects.
	require.NotEmpty(t, sawInputs["app_versions"])
	var gotAppVersions map[string]string
	require.NoError(t, json.Unmarshal([]byte(sawInputs["app_versions"].(string)), &gotAppVersions))
	require.Equal(t, map[string]string{"demo-widget": "v1.2.3"}, gotAppVersions)
}

// TestActivities_DispatchBuild_AppVersions_OmittedWhenNoImageTargets proves
// a chart-only batch (no image-kind entries in plan.Versions) never emits
// an app_versions input at all, rather than an empty/misleading one.
func TestActivities_DispatchBuild_AppVersions_OmittedWhenNoImageTargets(t *testing.T) {
	var sawInputs map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		sawInputs, _ = body["inputs"].(map[string]any)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workflow_runs":[{"id":100,"html_url":"https://example/100","created_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}]}`))
	})

	a := &Activities{GitHub: newTestDispatcher(t, mux)}
	plan := ResolvedPlan{
		ReleaseRunID: "release-run-2",
		Versions:     map[string]string{"chart:demo-achart": "v1.2.3"},
	}
	_, err := a.DispatchBuild(context.Background(), plan, nil)
	require.NoError(t, err)
	_, ok := sawInputs["app_versions"]
	require.False(t, ok, "expected no app_versions input for a chart-only batch, got %v", sawInputs["app_versions"])
}
