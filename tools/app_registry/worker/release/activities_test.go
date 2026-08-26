package release

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// TestActivities_DispatchBuild_HeterogeneousVersions_ReturnsClearError proves
// the "batch must resolve to one uniform version" limitation documented in
// github.go's package doc comment is actually enforced when going through
// the real Activities.DispatchBuild entry point (not just uniformVersion in
// isolation, which github_test.go's TestUniformVersion already covers) --
// see issue #889's Testing-phase instruction to verify this specifically.
// This plan has no RawJSON, so it exercises DispatchBuild's flat-input
// fallback branch -- the only branch that still needs one uniform version
// (issue #889 follow-up); see
// TestActivities_DispatchBuild_HeterogeneousVersionsWithRawJSON_DispatchesSuccessfully
// below for the RawJSON branch, which must accept exactly this same
// heterogeneous input.
func TestActivities_DispatchBuild_HeterogeneousVersions_ReturnsClearError(t *testing.T) {
	a := &Activities{GitHub: newTestDispatcher(t, http.NewServeMux()), Registry: newTestRegistry(t), MetadataRegistry: newTestMetadataRegistry(t)}
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

// TestActivities_DispatchBuild_HeterogeneousVersionsWithRawJSON_DispatchesSuccessfully
// is the issue #889 follow-up counterpart to the test above: once
// plan.RawJSON is populated (the normal ResolvePlan path, and the only path
// a per-target version picker on the release-trigger UI actually needs),
// DispatchBuild must NOT reject a heterogeneous plan.Versions map --
// uniformVersion is scoped to the flat-input fallback only (see github.go's
// package doc comment "DispatchBuild's version-uniformity limitation -- now
// scoped to the flat-input fallback only").
func TestActivities_DispatchBuild_HeterogeneousVersionsWithRawJSON_DispatchesSuccessfully(t *testing.T) {
	var sawInputs map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release-v2.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		sawInputs, _ = body["inputs"].(map[string]any)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release-v2.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workflow_runs":[{"id":202,"html_url":"https://example/202","created_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}]}`))
	})

	repo := newTestRegistry(t)
	seedApp(t, repo, "demo", "widget")
	_, err := repo.Reconcile(context.Background(), nil, []*appmetapb.ChartManifest{
		{Domain: "demo", Name: "achart"},
	}, repository.ReconcileSource{DiscoveredAt: 1}, false)
	require.NoError(t, err)

	rawJSON := []byte(`{"matrix":{"include":[{"app":"widget","domain":"demo","version":"v2.0.0"}]},"apps":["demo-widget"],"versions":{"demo-widget":"v2.0.0","demo-achart":"v1.0.1"},"event_type":"workflow_dispatch","has_specs":false}`)

	a := &Activities{GitHub: newTestDispatcher(t, mux), Registry: repo, MetadataRegistry: newTestMetadataRegistry(t)}
	plan := ResolvedPlan{
		ReleaseRunID: "release-run-2",
		Versions: map[string]string{
			"image:demo-widget": "v2.0.0",
			"chart:demo-achart": "v1.0.1",
		},
		RawJSON: rawJSON,
	}
	ref, err := a.DispatchBuild(context.Background(), plan, nil)
	require.NoError(t, err)
	require.Equal(t, "202", ref.RunID)
	require.Equal(t, string(rawJSON), sawInputs["resolved_plan"])

	_, hasVersion := sawInputs["version"]
	require.False(t, hasVersion, "version (a single flat string) must never be sent for a heterogeneous batch -- resolved_plan carries each target's own version instead")
}

// TestActivities_DispatchBuild_DigestOverrides_ReturnsUnimplementedError
// covers FR2's out-of-scope digest-override input: a non-empty
// digestOverrides must fail clearly, never silently build fresh or silently
// ignore it (see activities.go's DispatchBuild doc comment).
func TestActivities_DispatchBuild_DigestOverrides_ReturnsUnimplementedError(t *testing.T) {
	a := &Activities{GitHub: newTestDispatcher(t, http.NewServeMux()), MetadataRegistry: newTestMetadataRegistry(t)}
	plan := ResolvedPlan{Versions: map[string]string{"image:demo-widget": "v1.0.0"}}
	_, err := a.DispatchBuild(context.Background(), plan, map[string]string{"image:demo-widget": "sha256:abc"})
	require.Error(t, err)
	require.ErrorContains(t, err, "digest overrides")
}

// TestActivities_DispatchBuild_GitHubNotConfigured_ReturnsClearError covers
// worker/main.go's "leave GitHub nil unless configured" path -- DispatchBuild
// must fail fast rather than nil-panic.
func TestActivities_DispatchBuild_GitHubNotConfigured_ReturnsClearError(t *testing.T) {
	a := &Activities{MetadataRegistry: newTestMetadataRegistry(t)}
	plan := ResolvedPlan{Versions: map[string]string{"image:demo-widget": "v1.0.0"}}
	_, err := a.DispatchBuild(context.Background(), plan, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "not configured")
}

// TestActivities_DispatchBuild_EmptyPlan_ReturnsClearError covers a
// ResolvePlan result with no versions reaching DispatchBuild.
func TestActivities_DispatchBuild_EmptyPlan_ReturnsClearError(t *testing.T) {
	a := &Activities{GitHub: newTestDispatcher(t, http.NewServeMux()), Registry: newTestRegistry(t), MetadataRegistry: newTestMetadataRegistry(t)}
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
	var sawRef string
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release-v2.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		sawInputs, _ = body["inputs"].(map[string]any)
		sawRef, _ = body["ref"].(string)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release-v2.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workflow_runs":[{"id":99,"html_url":"https://example/99","created_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}]}`))
	})

	// FR9-FR11: DispatchBuild resolves each target's owner against
	// app_build_log via a.Registry -- seed identity for both targets (no
	// app_build_log row for either, so resolveDispatchRef falls back to
	// a.GitHub.Config.Ref, "main" -- see newTestDispatcher).
	repo := newTestRegistry(t)
	seedApp(t, repo, "demo", "widget")
	_, err := repo.Reconcile(context.Background(), nil, []*appmetapb.ChartManifest{
		{Domain: "demo", Name: "achart"},
	}, repository.ReconcileSource{DiscoveredAt: 1}, false)
	require.NoError(t, err)

	a := &Activities{GitHub: newTestDispatcher(t, mux), Registry: repo, MetadataRegistry: newTestMetadataRegistry(t)}
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
	require.Equal(t, "main", sawRef, "workflow_dispatch's `ref` must always be the branch, never a resolved commit SHA -- see github.go's Dispatch doc comment")
	require.Equal(t, "main", sawInputs["build_ref"], "no app_build_log row exists for either target, so FR10's fallback to Config.Ref is forwarded as the build_ref input")

	// issue #901: the image-kind entry from plan.Versions must also be
	// forwarded as a JSON app_versions input, keyed by bare OwnerFullName
	// (not the "image:" TargetKey-prefixed form) -- this is what
	// release-helm-charts' --app-versions flag expects.
	require.NotEmpty(t, sawInputs["app_versions"])
	var gotAppVersions map[string]string
	require.NoError(t, json.Unmarshal([]byte(sawInputs["app_versions"].(string)), &gotAppVersions))
	require.Equal(t, map[string]string{"demo-widget": "v1.2.3"}, gotAppVersions)
}

// TestActivities_DispatchBuild_ResolvedPlan_ForwardsRawJSONVerbatim covers
// issue #927: when plan.RawJSON is populated (the normal ResolvePlan path,
// see plan.go), DispatchBuild must forward it verbatim as a single
// `resolved_plan` input instead of collapsing plan.Versions into
// apps/version/app_versions inputs -- those must be absent, since
// release-v2.yml's plan-release job now derives everything it needs from
// resolved_plan for this path. helm_charts is still sent directly (it is
// not part of plan-release's re-derivation this issue eliminates --
// release-helm-charts reads it straight off the dispatch input).
func TestActivities_DispatchBuild_ResolvedPlan_ForwardsRawJSONVerbatim(t *testing.T) {
	var sawInputs map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release-v2.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		sawInputs, _ = body["inputs"].(map[string]any)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release-v2.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workflow_runs":[{"id":101,"html_url":"https://example/101","created_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}]}`))
	})

	repo := newTestRegistry(t)
	seedApp(t, repo, "demo", "widget")
	_, err := repo.Reconcile(context.Background(), nil, []*appmetapb.ChartManifest{
		{Domain: "demo", Name: "achart"},
	}, repository.ReconcileSource{DiscoveredAt: 1}, false)
	require.NoError(t, err)

	rawJSON := []byte(`{"matrix":{"include":[{"app":"widget","domain":"demo","version":"v1.2.3"}]},"apps":["demo-widget"],"version":"v1.2.3","versions":{"demo-widget":"v1.2.3","demo-achart":"v1.2.3"},"event_type":"workflow_dispatch","has_specs":false}`)

	a := &Activities{GitHub: newTestDispatcher(t, mux), Registry: repo, MetadataRegistry: newTestMetadataRegistry(t)}
	plan := ResolvedPlan{
		ReleaseRunID: "release-run-3",
		Versions: map[string]string{
			"image:demo-widget": "v1.2.3",
			"chart:demo-achart": "v1.2.3",
		},
		RawJSON: rawJSON,
	}
	ref, err := a.DispatchBuild(context.Background(), plan, nil)
	require.NoError(t, err)
	require.Equal(t, "101", ref.RunID)

	require.Equal(t, string(rawJSON), sawInputs["resolved_plan"])
	require.Equal(t, "demo-achart", sawInputs["helm_charts"], "helm_charts is still sent directly -- release-helm-charts reads it straight off the dispatch input, unrelated to plan-release's re-derivation")
	require.Equal(t, "false", sawInputs["dry_run"])

	_, hasApps := sawInputs["apps"]
	require.False(t, hasApps, "apps must not be sent once resolved_plan carries the full matrix -- it would be redundant re-derivation input")
	_, hasVersion := sawInputs["version"]
	require.False(t, hasVersion, "version must not be sent once resolved_plan carries it (resolved_plan.version)")
	_, hasAppVersions := sawInputs["app_versions"]
	require.False(t, hasAppVersions, "app_versions must not be sent once resolved_plan carries the full versions map")
}

// TestActivities_DispatchBuild_RetriedCall_ReturnsExistingRunWithoutRedispatching
// is the migration-023 regression test: a second DispatchBuild call for the
// same release run (simulating Temporal's at-least-once activity retry --
// e.g. after a transient network error on the dispatch POST itself, or on
// findDispatchedRun's polling, per that method's doc comment) must return
// the SAME BuildRef without hitting GitHub's dispatches endpoint again.
// This is the fix for a real production incident (run 32691150140,
// "Canceling since a higher priority waiting request for release-v2
// exists"): release-v2.yml's concurrency group only keeps one run queued
// behind the one in progress, so a second `workflow_dispatch` for the same
// release evicts its own first, still-queued dispatch.
func TestActivities_DispatchBuild_RetriedCall_ReturnsExistingRunWithoutRedispatching(t *testing.T) {
	dispatchCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release-v2.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		dispatchCount++
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release-v2.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workflow_runs":[{"id":303,"html_url":"https://example/303","created_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}]}`))
	})

	repo := newTestRegistry(t)
	seedApp(t, repo, "demo", "widget")
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})

	a := &Activities{GitHub: newTestDispatcher(t, mux), Registry: repo}
	plan := ResolvedPlan{
		ReleaseRunID: run.ReleaseRunID,
		Versions:     map[string]string{"image:demo-widget": "v1.0.0"},
	}

	first, err := a.DispatchBuild(context.Background(), plan, nil)
	require.NoError(t, err)
	require.Equal(t, "303", first.RunID)
	require.Equal(t, 1, dispatchCount)

	second, err := a.DispatchBuild(context.Background(), plan, nil)
	require.NoError(t, err)
	require.Equal(t, first, second, "a retried DispatchBuild must return the identical BuildRef")
	require.Equal(t, 1, dispatchCount, "a retried DispatchBuild must not call workflow_dispatch a second time")
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
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release-v2.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		sawInputs, _ = body["inputs"].(map[string]any)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/workflows/release-v2.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workflow_runs":[{"id":100,"html_url":"https://example/100","created_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}]}`))
	})

	repo := newTestRegistry(t)
	_, err := repo.Reconcile(context.Background(), nil, []*appmetapb.ChartManifest{
		{Domain: "demo", Name: "achart"},
	}, repository.ReconcileSource{DiscoveredAt: 1}, false)
	require.NoError(t, err)

	a := &Activities{GitHub: newTestDispatcher(t, mux), Registry: repo, MetadataRegistry: newTestMetadataRegistry(t)}
	plan := ResolvedPlan{
		ReleaseRunID: "release-run-2",
		Versions:     map[string]string{"chart:demo-achart": "v1.2.3"},
	}
	_, err = a.DispatchBuild(context.Background(), plan, nil)
	require.NoError(t, err)
	_, ok := sawInputs["app_versions"]
	require.False(t, ok, "expected no app_versions input for a chart-only batch, got %v", sawInputs["app_versions"])
}
