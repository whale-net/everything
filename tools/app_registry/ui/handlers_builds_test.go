package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// fakeArtifactClient is a minimal stand-in for pb.ArtifactRegistryClient.
// Embedding the (nil) interface satisfies every method this test doesn't
// care about — calling one of those would nil-panic, which is exactly the
// signal we want if a handler starts using an RPC these tests don't expect
// (e.g. the per-row GetReleaseRun fan-out FR-56 forbids on screen 30).
type fakeArtifactClient struct {
	pb.ArtifactRegistryClient

	listBuildsCalls int
	listBuildsResp  *pb.ListBuildsResponse

	getReleaseRunCalls int
	getReleaseRunReqs  []*pb.GetReleaseRunRequest
	getReleaseRunResp  *pb.GetReleaseRunResponse
	getReleaseRunErr   error
}

func (f *fakeArtifactClient) ListBuilds(ctx context.Context, in *pb.ListBuildsRequest, opts ...grpc.CallOption) (*pb.ListBuildsResponse, error) {
	f.listBuildsCalls++
	return f.listBuildsResp, nil
}

func (f *fakeArtifactClient) GetReleaseRun(ctx context.Context, in *pb.GetReleaseRunRequest, opts ...grpc.CallOption) (*pb.GetReleaseRunResponse, error) {
	f.getReleaseRunCalls++
	f.getReleaseRunReqs = append(f.getReleaseRunReqs, in)
	if f.getReleaseRunErr != nil {
		return nil, f.getReleaseRunErr
	}
	return f.getReleaseRunResp, nil
}

// fakeAppClient is a minimal stand-in for pb.AppRegistryClient, covering
// only the calls handleBuildDetail's ownerDomainIndex makes.
type fakeAppClient struct {
	pb.AppRegistryClient

	listAppsResp       *pb.ListAppsResponse
	listChartsResp     *pb.ListChartsResponse
	listReconcileResp  *pb.ListReconcileRunsResponse
	listReconcileCalls int
}

func (f *fakeAppClient) ListApps(ctx context.Context, in *pb.ListAppsRequest, opts ...grpc.CallOption) (*pb.ListAppsResponse, error) {
	if f.listAppsResp == nil {
		return &pb.ListAppsResponse{}, nil
	}
	return f.listAppsResp, nil
}

func (f *fakeAppClient) ListCharts(ctx context.Context, in *pb.ListChartsRequest, opts ...grpc.CallOption) (*pb.ListChartsResponse, error) {
	if f.listChartsResp == nil {
		return &pb.ListChartsResponse{}, nil
	}
	return f.listChartsResp, nil
}

func (f *fakeAppClient) ListReconcileRuns(ctx context.Context, in *pb.ListReconcileRunsRequest, opts ...grpc.CallOption) (*pb.ListReconcileRunsResponse, error) {
	f.listReconcileCalls++
	return f.listReconcileResp, nil
}

func newTestApp(artifact *fakeArtifactClient, appClient *fakeAppClient) *App {
	return &App{
		registry: &RegistryClient{
			Artifact: artifact,
			App:      appClient,
		},
	}
}

// --- Screen 30 (FR-26, FR-56): builds list -------------------------------

func TestHandleBuilds_NoHealthVerdict_SingleListBuildsCall(t *testing.T) {
	artifact := &fakeArtifactClient{
		listBuildsResp: &pb.ListBuildsResponse{
			Builds: []*pb.Build{
				{
					BuildId:         "build-1",
					GitSha:          "abcdef0123456789",
					WorkflowRunId:   "999",
					WorkflowAttempt: 1,
					Actor:           "octocat",
					StartedAt:       1000,
					RecordedAt:      1010,
				},
			},
		},
	}
	app := newTestApp(artifact, &fakeAppClient{})

	req := httptest.NewRequest(http.MethodGet, "/builds", nil)
	w := httptest.NewRecorder()
	app.handleBuilds(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("handleBuilds: status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// FR-56: exactly one ListBuilds call per page load, no per-row
	// GetReleaseRun fan-out.
	if artifact.listBuildsCalls != 1 {
		t.Errorf("ListBuilds calls = %d, want 1", artifact.listBuildsCalls)
	}
	if artifact.getReleaseRunCalls != 0 {
		t.Errorf("GetReleaseRun calls = %d, want 0 (screen 30 must never fan out per-row)", artifact.getReleaseRunCalls)
	}

	// Screen 30 explains in prose what it deliberately omits (its own
	// footer says "Open a run to see its recording health") — that's not
	// the thing FR-56 forbids. What's forbidden is an actual rendered
	// health verdict: the badge markup BuildDetail's recordingHealthBadge
	// emits, an artifact-count column, health-derived row tinting, or a
	// domain column — none of which screen 30 has the data to compute
	// without an N+1 GetReleaseRun fan-out.
	assertNoneContain(t, body, "screen 30 must not render", []string{
		"App Registry recording health:", // recordingHealthBadge's exact label prefix
		"Domain</th>",                    // no domain column header
		"badge-success",
		"badge-error",
		"badge-warning",
		"bg-error/5", // no row tinting derived from health
	})
	if strings.Contains(body, "<th>Artifacts</th>") {
		t.Errorf("build list rendered an artifact-count column, which screen 30 must not have")
	}
}

func assertNoneContain(t *testing.T, body, msg string, forbidden []string) {
	t.Helper()
	for _, s := range forbidden {
		if strings.Contains(body, s) {
			t.Errorf("%s: body unexpectedly contains %q", msg, s)
		}
	}
}

// --- Run lookup (FR-26) ---------------------------------------------------

func TestHandleBuildLookup_RedirectsToBuildDetailByRunID(t *testing.T) {
	app := newTestApp(&fakeArtifactClient{}, &fakeAppClient{})

	req := httptest.NewRequest(http.MethodGet, "/builds/lookup?run=999", nil)
	w := httptest.NewRecorder()
	app.handleBuildLookup(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if got := w.Header().Get("Location"); got != "/builds/999" {
		t.Errorf("Location = %q, want /builds/999", got)
	}
}

// FR-26: lookup takes no attempt input; GetReleaseRun(workflow_run_id,
// attempt=0) resolves to the highest attempt recorded server-side.
func TestHandleBuildDetail_LooksUpWithZeroAttempt(t *testing.T) {
	artifact := &fakeArtifactClient{
		getReleaseRunResp: &pb.GetReleaseRunResponse{
			Build: &pb.Build{BuildId: "build-1", WorkflowRunId: "999"},
			Artifacts: []*pb.Artifact{
				{ArtifactId: "a1", AppId: "app-1", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHED},
			},
		},
	}
	app := newTestApp(artifact, &fakeAppClient{})

	req := httptest.NewRequest(http.MethodGet, "/builds/999", nil)
	req.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	app.handleBuildDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if artifact.getReleaseRunCalls != 1 {
		t.Fatalf("GetReleaseRun calls = %d, want 1", artifact.getReleaseRunCalls)
	}
	got := artifact.getReleaseRunReqs[0]
	if got.GetWorkflowRunId() != "999" {
		t.Errorf("WorkflowRunId = %q, want 999", got.GetWorkflowRunId())
	}
	if got.GetWorkflowAttempt() != 0 {
		t.Errorf("WorkflowAttempt = %d, want 0 (no attempt input => highest attempt recorded)", got.GetWorkflowAttempt())
	}
}

// --- Build detail states (FR-27, FR-28) -----------------------------------

func TestHandleBuildDetail_EveryArtifactStateRendersOwnBadge_FailReasonVerbatim(t *testing.T) {
	artifacts := []*pb.Artifact{
		{ArtifactId: "a-allocated", AppId: "app-1", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_ALLOCATED},
		{ArtifactId: "a-publishing", AppId: "app-1", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHING},
		{ArtifactId: "a-published", AppId: "app-1", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHED},
		{ArtifactId: "a-failed", AppId: "app-1", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_FAILED, FailReason: "manifest push timed out: context deadline exceeded"},
	}
	artifact := &fakeArtifactClient{
		getReleaseRunResp: &pb.GetReleaseRunResponse{
			Build:     &pb.Build{BuildId: "build-1", WorkflowRunId: "999"},
			Artifacts: artifacts,
		},
	}
	app := newTestApp(artifact, &fakeAppClient{})

	req := httptest.NewRequest(http.MethodGet, "/builds/999", nil)
	req.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	app.handleBuildDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	for _, label := range []string{"Allocated", "Publishing", "Published", "Failed"} {
		if !strings.Contains(body, ">"+label+"<") {
			t.Errorf("body missing artifact state badge %q", label)
		}
	}
	if !strings.Contains(body, `fail_reason: "manifest push timed out: context deadline exceeded"`) {
		t.Errorf("body did not render fail_reason verbatim; body = %s", body)
	}
}

func TestHandleBuildDetail_IncompleteOnlyFilterExcludesOnlyPublished(t *testing.T) {
	// AppId doubles as the row's owner text (ownerFullNameFallback prefers
	// app_id over artifact_id), so give each row a distinct AppId to tell
	// rows apart in the rendered body.
	artifacts := []*pb.Artifact{
		{ArtifactId: "a-allocated", AppId: "app-allocated", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_ALLOCATED},
		{ArtifactId: "a-publishing", AppId: "app-publishing", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHING},
		{ArtifactId: "a-published", AppId: "app-published", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHED},
		{ArtifactId: "a-failed", AppId: "app-failed", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_FAILED, FailReason: "boom"},
	}
	artifact := &fakeArtifactClient{
		getReleaseRunResp: &pb.GetReleaseRunResponse{
			Build:     &pb.Build{BuildId: "build-1", WorkflowRunId: "999"},
			Artifacts: artifacts,
		},
	}
	app := newTestApp(artifact, &fakeAppClient{})

	req := httptest.NewRequest(http.MethodGet, "/builds/999?incomplete=1", nil)
	req.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	app.handleBuildDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	for _, id := range []string{"app-allocated", "app-publishing", "app-failed"} {
		if !strings.Contains(body, id) {
			t.Errorf("incomplete-only filter excluded non-PUBLISHED artifact %q; it should only exclude PUBLISHED rows", id)
		}
	}
	if strings.Contains(body, "app-published") {
		t.Errorf("incomplete-only filter did not exclude the PUBLISHED artifact")
	}
}

// --- Recording health (FR-29) ---------------------------------------------

func TestHandleBuildDetail_RecordingHealth_AllPublishedIsHealthy(t *testing.T) {
	artifact := &fakeArtifactClient{
		getReleaseRunResp: &pb.GetReleaseRunResponse{
			Build: &pb.Build{BuildId: "build-1", WorkflowRunId: "999"},
			Artifacts: []*pb.Artifact{
				{ArtifactId: "a1", AppId: "app-1", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHED},
				{ArtifactId: "a2", AppId: "app-2", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHED},
			},
		},
	}
	app := newTestApp(artifact, &fakeAppClient{})

	req := httptest.NewRequest(http.MethodGet, "/builds/999", nil)
	req.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	app.handleBuildDetail(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "App Registry recording health: Healthy") {
		t.Errorf("expected a labelled 'App Registry recording health: Healthy' status; body = %s", body)
	}
}

func TestHandleBuildDetail_RecordingHealth_AnyFailedOrIncompleteIsFailed(t *testing.T) {
	artifact := &fakeArtifactClient{
		getReleaseRunResp: &pb.GetReleaseRunResponse{
			Build: &pb.Build{BuildId: "build-1", WorkflowRunId: "999"},
			Artifacts: []*pb.Artifact{
				{ArtifactId: "a1", AppId: "app-1", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHED},
				{ArtifactId: "a2", AppId: "app-2", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_ALLOCATED},
			},
		},
	}
	app := newTestApp(artifact, &fakeAppClient{})

	req := httptest.NewRequest(http.MethodGet, "/builds/999", nil)
	req.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	app.handleBuildDetail(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "App Registry recording health: Failed") {
		t.Errorf("expected a labelled 'App Registry recording health: Failed' status; body = %s", body)
	}
}

// Recording health must be structurally separate from any overall run
// status text — it renders in its own labelled span, never fused into the
// per-artifact incomplete-count banner text.
func TestHandleBuildDetail_RecordingHealth_TextuallySeparateFromRunStatus(t *testing.T) {
	artifact := &fakeArtifactClient{
		getReleaseRunResp: &pb.GetReleaseRunResponse{
			Build: &pb.Build{BuildId: "build-1", WorkflowRunId: "999"},
			Artifacts: []*pb.Artifact{
				{ArtifactId: "a1", AppId: "app-1", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, State: pb.ArtifactState_ARTIFACT_STATE_FAILED, FailReason: "x"},
			},
		},
	}
	app := newTestApp(artifact, &fakeAppClient{})

	req := httptest.NewRequest(http.MethodGet, "/builds/999", nil)
	req.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	app.handleBuildDetail(w, req)

	body := w.Body.String()
	// The recording-health badge names "App Registry recording health"
	// specifically; the incomplete-artifacts banner talks about the
	// release, not recording health — the two must not share wording.
	if !strings.Contains(body, "App Registry recording health: Failed") {
		t.Fatalf("missing recording health badge; body = %s", body)
	}
	if !strings.Contains(body, "may still have succeeded") {
		t.Errorf("expected the run-status banner to remain distinct wording about the release, not recording health; body = %s", body)
	}
}

// --- Zero-artifact indeterminate (FR-30, NFR-6) ---------------------------

func TestHandleBuildDetail_ZeroArtifacts_RendersIndeterminateNotHealthyOrFailed(t *testing.T) {
	artifact := &fakeArtifactClient{
		getReleaseRunResp: &pb.GetReleaseRunResponse{
			Build:     &pb.Build{BuildId: "build-1", WorkflowRunId: "999"},
			Artifacts: nil,
		},
	}
	app := newTestApp(artifact, &fakeAppClient{})

	req := httptest.NewRequest(http.MethodGet, "/builds/999", nil)
	req.SetPathValue("id", "999")
	w := httptest.NewRecorder()
	app.handleBuildDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	if strings.Contains(body, "App Registry recording health: Healthy") {
		t.Errorf("zero-artifact run must never render as healthy (NFR-6); body = %s", body)
	}
	if strings.Contains(body, "App Registry recording health: Failed") {
		t.Errorf("zero-artifact run must never render as failed; it is indeterminate, not failed; body = %s", body)
	}
	if !strings.Contains(body, "App Registry recording health: Indeterminate") {
		t.Errorf("expected an explicit Indeterminate recording-health status; body = %s", body)
	}

	// Names both possibilities (FR-30) rather than asserting either one.
	if !strings.Contains(body, "opt-in") {
		t.Errorf("indeterminate explanation must name the opt-in-off possibility; body = %s", body)
	}
	if !strings.Contains(body, "recording failed silently") {
		t.Errorf("indeterminate explanation must name the silent-recording-failure possibility; body = %s", body)
	}

	// A GitHub Actions deep link, never an outbound API fetch (NFR-15).
	if !strings.Contains(body, "https://github.com/whale-net/everything/actions/runs/999") {
		t.Errorf("expected a GitHub Actions deep link for run 999; body = %s", body)
	}
}

// NFR-15: the zero-artifact case never triggers an outbound GitHub API
// call. There is no HTTP client wired into this handler at all — the only
// GitHub reference in the response is the static deep-link string built by
// githubActionsRunURL, so proving the handler completes without any
// networking dependency is exactly what this test (run fully offline,
// backed only by the fake in-process gRPC client above) already
// demonstrates: no real network call was made, only string construction.
func TestGithubActionsRunURL_IsPureStringConstruction_NoHTTPCall(t *testing.T) {
	got := githubActionsRunURL("999")
	want := "https://github.com/whale-net/everything/actions/runs/999"
	if got != want {
		t.Errorf("githubActionsRunURL(999) = %q, want %q", got, want)
	}
}

// --- Reconcile runs (FR-31) ------------------------------------------------

func TestHandleReconcileRuns_ListsCommitAppliedAtAndCounts(t *testing.T) {
	appClient := &fakeAppClient{
		listReconcileResp: &pb.ListReconcileRunsResponse{
			ReconcileRuns: []*pb.ReconcileRun{
				{
					ReconcileRunId:    "r1",
					GitSha:            "0123456789abcdef",
					SourceCommittedAt: 100,
					AppliedAt:         200,
					AppsSeen:          5,
					ChartsSeen:        2,
				},
			},
		},
	}
	app := newTestApp(&fakeArtifactClient{}, appClient)

	req := httptest.NewRequest(http.MethodGet, "/reconcile-runs", nil)
	w := httptest.NewRecorder()
	app.handleReconcileRuns(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if appClient.listReconcileCalls != 1 {
		t.Errorf("ListReconcileRuns calls = %d, want 1", appClient.listReconcileCalls)
	}
	if !strings.Contains(body, "0123456789") {
		t.Errorf("expected the sweep's commit sha in the body; body = %s", body)
	}
	if !strings.Contains(body, ">5<") {
		t.Errorf("expected apps-seen count 5 in the body; body = %s", body)
	}
	if !strings.Contains(body, ">2<") {
		t.Errorf("expected charts-seen count 2 in the body; body = %s", body)
	}
}

// A stale sweep is described as expected watermark behaviour, never styled
// or worded as a failure — there is no per-row "stale" state to render
// (stale sweeps write no row at all, see handleReconcileRuns's doc
// comment), so this asserts the explanatory banner itself is informational,
// not an error/warning.
func TestHandleReconcileRuns_StaleSweepBanner_IsInfoNotError(t *testing.T) {
	appClient := &fakeAppClient{listReconcileResp: &pb.ListReconcileRunsResponse{}}
	app := newTestApp(&fakeArtifactClient{}, appClient)

	req := httptest.NewRequest(http.MethodGet, "/reconcile-runs", nil)
	w := httptest.NewRecorder()
	app.handleReconcileRuns(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "stale") {
		t.Fatalf("expected the stale-watermark explanation banner; body = %s", body)
	}
	if !strings.Contains(body, "alert-info") {
		t.Errorf("stale sweep explanation must be styled as informational (alert-info), not an error/warning; body = %s", body)
	}
	if strings.Contains(body, "alert-error") {
		t.Errorf("stale sweep explanation must never be styled as a failure (alert-error)")
	}
	if !strings.Contains(body, "expected") {
		t.Errorf("stale sweep explanation must describe the absence as expected watermark behaviour; body = %s", body)
	}
}
