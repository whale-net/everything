package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// This file covers issue #890's Implementation-phase wiring: the actual
// TriggerRelease submit call (success + FR5's "already releasing"
// rejection), FR4/NFR5 permission gating (mirroring
// TestHandleRollbackShow_DoesNotReachAPIWhenClientSideGateDenies in
// role_gating_test.go), and FR10/FR6's status-page retry action.

// --- fakes ------------------------------------------------------------

// releaseAppClient is a minimal stand-in for pb.AppRegistryClient, feeding
// resolveReleaseScope (release_scope.go) a single-app catalog so scope
// resolution in these tests is deterministic without exercising every case
// release_scope_test.go already covers.
type releaseAppClient struct {
	pb.AppRegistryClient
	apps []*pb.App
}

func (f *releaseAppClient) ListApps(ctx context.Context, in *pb.ListAppsRequest, opts ...grpc.CallOption) (*pb.ListAppsResponse, error) {
	return &pb.ListAppsResponse{Apps: f.apps}, nil
}

func (f *releaseAppClient) ListCharts(ctx context.Context, in *pb.ListChartsRequest, opts ...grpc.CallOption) (*pb.ListChartsResponse, error) {
	return &pb.ListChartsResponse{}, nil
}

// fakeReleaseClient is a minimal stand-in for pb.ReleaseRegistryClient.
// triggerErr forces TriggerRelease to fail (used for both the FR5
// already-releasing case and a generic failure case); triggerCalls records
// every call made, which is the whole point of the NFR-14/FR4 gating tests
// below (a denied caller must produce zero calls).
type fakeReleaseClient struct {
	pb.ReleaseRegistryClient

	triggerResp  *pb.TriggerReleaseResponse
	triggerErr   error
	triggerCalls []*pb.TriggerReleaseRequest

	getResp *pb.GetReleaseResponse
	getErr  error

	listAttemptsResp *pb.ListReleaseAttemptsResponse
	listAttemptsErr  error
}

func (f *fakeReleaseClient) TriggerRelease(ctx context.Context, in *pb.TriggerReleaseRequest, opts ...grpc.CallOption) (*pb.TriggerReleaseResponse, error) {
	f.triggerCalls = append(f.triggerCalls, in)
	if f.triggerErr != nil {
		return nil, f.triggerErr
	}
	if f.triggerResp != nil {
		return f.triggerResp, nil
	}
	return &pb.TriggerReleaseResponse{ReleaseRunId: "run-1"}, nil
}

func (f *fakeReleaseClient) GetRelease(ctx context.Context, in *pb.GetReleaseRequest, opts ...grpc.CallOption) (*pb.GetReleaseResponse, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getResp != nil {
		return f.getResp, nil
	}
	return &pb.GetReleaseResponse{ReleaseRunId: in.GetReleaseRunId()}, nil
}

func (f *fakeReleaseClient) ListReleaseAttempts(ctx context.Context, in *pb.ListReleaseAttemptsRequest, opts ...grpc.CallOption) (*pb.ListReleaseAttemptsResponse, error) {
	if f.listAttemptsErr != nil {
		return nil, f.listAttemptsErr
	}
	if f.listAttemptsResp != nil {
		return f.listAttemptsResp, nil
	}
	return &pb.ListReleaseAttemptsResponse{}, nil
}

func oneAppCatalog() []*pb.App {
	return []*pb.App{
		{
			AppId:    "a1",
			FullName: "platform-worker",
			Name:     "worker",
			Domain:   "platform",
			Status:   pb.AppStatus_APP_STATUS_ACTIVE,
			AppType:  "service",
		},
	}
}

func releaseSubmitForm(scope, do string) *strings.Reader {
	v := url.Values{"scope": {scope}, "do": {do}}
	return strings.NewReader(v.Encode())
}

func newReleaseTriggerRequest(t *testing.T, scope, do string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/releases/trigger", releaseSubmitForm(scope, do))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// --- FR4/NFR5: permission gating ---------------------------------------

// A caller with no session/roles at all (a bare httptest request, exactly
// like TestHandleRollbackShow_DoesNotReachAPIWhenClientSideGateDenies uses)
// must never reach TriggerRelease -- the client-side gate
// (releaseTriggerGate, handlers_release.go) must deny before any RPC call.
func TestHandleReleaseTriggerSubmit_DeniedWithNoUserInContext_NeverCallsTriggerRelease(t *testing.T) {
	rel := &fakeReleaseClient{}
	app := &App{registry: &RegistryClient{
		App:     &releaseAppClient{apps: oneAppCatalog()},
		Release: rel,
	}}

	req := newReleaseTriggerRequest(t, "platform-worker", "trigger")
	w := httptest.NewRecorder()
	app.handleReleaseTriggerSubmit(w, req)

	if len(rel.triggerCalls) != 0 {
		t.Fatalf("TriggerRelease calls = %d, want 0 -- FR4/NFR5: a caller holding no role must be denied before the RPC, body = %s", len(rel.triggerCalls), w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Requires role") {
		t.Errorf("expected the denied state to name the missing role; body = %s", w.Body.String())
	}
}

// The GET/show path must deny identically -- a denied caller should never
// even see the form to fill in, matching handleRollbackShow.
func TestHandleReleaseTriggerShow_DeniedWithNoUserInContext_RendersReadOnlyBanner(t *testing.T) {
	app := &App{registry: &RegistryClient{
		App:     &releaseAppClient{apps: oneAppCatalog()},
		Release: &fakeReleaseClient{},
	}}

	req := httptest.NewRequest(http.MethodGet, "/releases/trigger", nil)
	w := httptest.NewRecorder()
	app.handleReleaseTriggerShow(w, req)

	if !strings.Contains(w.Body.String(), "Read-only") {
		t.Errorf("expected the denied state's read-only banner; body = %s", w.Body.String())
	}
}

// A caller holding every role (htmxauth's AuthModeNone dev sentinel, routed
// through the real middleware exactly like
// TestHandlePromoteSubmit_Commit_ReachesAPIWithNoUserInContext /
// devUserAuth in role_gating_test.go / handlers_promote_rollback_test.go)
// must reach TriggerRelease.
func TestHandleReleaseTriggerSubmit_Confirmed_AllowedCaller_CallsTriggerRelease(t *testing.T) {
	rel := &fakeReleaseClient{triggerResp: &pb.TriggerReleaseResponse{ReleaseRunId: "run-42"}}
	app := &App{registry: &RegistryClient{
		App:     &releaseAppClient{apps: oneAppCatalog()},
		Release: rel,
	}}

	req := newReleaseTriggerRequest(t, "platform-worker", "trigger")
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleReleaseTriggerSubmit)(w, req)

	if len(rel.triggerCalls) != 1 {
		t.Fatalf("TriggerRelease calls = %d, want 1 -- an allowed caller's confirmed submit must reach the API; body = %s", len(rel.triggerCalls), w.Body.String())
	}
	if got := rel.triggerCalls[0].GetTargets(); len(got) != 1 || got[0].GetOwnerFullName() != "platform-worker" {
		t.Errorf("TriggerRelease targets = %+v, want exactly [platform-worker]", got)
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (redirect to the new release's status page)", w.Code, http.StatusSeeOther)
	}
	if loc := w.Header().Get("Location"); loc != "/releases/run-42" {
		t.Errorf("Location = %q, want /releases/run-42 (FR-success redirect to release_run_id's status page)", loc)
	}
}

// The "Resolve scope" (preview) step -- do=resolve, or any value other than
// "trigger" -- must resolve the scope but never call TriggerRelease, even
// for an allowed caller. This is what makes the preview step safe to hit
// repeatedly.
func TestHandleReleaseTriggerSubmit_ResolveOnly_NeverCallsTriggerRelease(t *testing.T) {
	rel := &fakeReleaseClient{}
	app := &App{registry: &RegistryClient{
		App:     &releaseAppClient{apps: oneAppCatalog()},
		Release: rel,
	}}

	req := newReleaseTriggerRequest(t, "platform-worker", "resolve")
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleReleaseTriggerSubmit)(w, req)

	if len(rel.triggerCalls) != 0 {
		t.Fatalf("TriggerRelease calls = %d, want 0 -- the resolve-only step must never call TriggerRelease", len(rel.triggerCalls))
	}
	if !strings.Contains(w.Body.String(), "platform-worker") {
		t.Errorf("expected the resolved target to appear in the preview; body = %s", w.Body.String())
	}
}

// --- FR5: already-releasing rejection ------------------------------------

func TestHandleReleaseTriggerSubmit_AlreadyReleasing_SurfacesSpecificRejection(t *testing.T) {
	rel := &fakeReleaseClient{
		triggerErr: status.Errorf(codes.FailedPrecondition,
			"platform-worker (image) is already releasing under release_run run-9 (target state \"building\")"),
	}
	app := &App{registry: &RegistryClient{
		App:     &releaseAppClient{apps: oneAppCatalog()},
		Release: rel,
	}}

	req := newReleaseTriggerRequest(t, "platform-worker", "trigger")
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleReleaseTriggerSubmit)(w, req)

	if len(rel.triggerCalls) != 1 {
		t.Fatalf("TriggerRelease calls = %d, want 1", len(rel.triggerCalls))
	}
	body := w.Body.String()
	if !strings.Contains(body, "Already releasing") {
		t.Errorf("expected a specific 'Already releasing' message (FR5), not a generic failure; body = %s", body)
	}
	if !strings.Contains(body, "run-9") {
		t.Errorf("expected the in-flight release to be identified by its release_run id (FR5); body = %s", body)
	}
	if strings.Contains(body, "Transport or server failure") {
		t.Errorf("FR5's rejection must not be rendered as a generic transport/server failure; body = %s", body)
	}
	// The failed call must never redirect -- the operator stays on the
	// trigger form with the rejection visible.
	if w.Code == http.StatusSeeOther {
		t.Errorf("status = %d, must not redirect on a FailedPrecondition rejection", w.Code)
	}
}

// A non-FailedPrecondition TriggerRelease failure still surfaces (via
// grpcErrorMessage) but must be distinguishable from the FR5 case above --
// it must NOT claim "Already releasing".
func TestHandleReleaseTriggerSubmit_GenericFailure_DoesNotClaimAlreadyReleasing(t *testing.T) {
	rel := &fakeReleaseClient{triggerErr: status.Error(codes.Unavailable, "connection refused")}
	app := &App{registry: &RegistryClient{
		App:     &releaseAppClient{apps: oneAppCatalog()},
		Release: rel,
	}}

	req := newReleaseTriggerRequest(t, "platform-worker", "trigger")
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleReleaseTriggerSubmit)(w, req)

	body := w.Body.String()
	if strings.Contains(body, "Already releasing") {
		t.Errorf("a transport failure must not be mislabeled as FR5's already-releasing rejection; body = %s", body)
	}
	if !strings.Contains(body, "Transport or server failure") {
		t.Errorf("expected the generic transport-failure message; body = %s", body)
	}
}

// --- FR2: digest only valid against exactly one resolved target ---------

func TestHandleReleaseTriggerSubmit_DigestAgainstMultipleTargets_RejectedBeforeCallingAPI(t *testing.T) {
	rel := &fakeReleaseClient{}
	app := &App{registry: &RegistryClient{
		App: &releaseAppClient{apps: []*pb.App{
			{AppId: "a1", FullName: "platform-worker", Name: "worker", Domain: "platform", Status: pb.AppStatus_APP_STATUS_ACTIVE},
			{AppId: "a2", FullName: "platform-api", Name: "api", Domain: "platform", Status: pb.AppStatus_APP_STATUS_ACTIVE},
		}},
		Release: rel,
	}}

	v := url.Values{"scope": {"platform"}, "digest": {"sha256:deadbeef"}, "do": {"trigger"}}
	req := httptest.NewRequest(http.MethodPost, "/releases/trigger", strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleReleaseTriggerSubmit)(w, req)

	if len(rel.triggerCalls) != 0 {
		t.Fatalf("TriggerRelease calls = %d, want 0 -- a digest against a multi-target scope must be rejected client-side", len(rel.triggerCalls))
	}
	if !strings.Contains(w.Body.String(), "exactly one target") {
		t.Errorf("expected the digest-scope-mismatch validation message; body = %s", w.Body.String())
	}
}

// TestHandleReleaseTriggerSubmit_DigestPin_ShowsCommitLinkedToGitHub covers
// the Trigger Release Draft step's Commit column: pinning a single
// resolved target to a digest must resolve and render that digest's
// commit as a GitHub deep link (resolveDigestCommit, handlers_release.go),
// using the same GetArtifact(digest) lookup screen 21 already relies on.
// Prevents an accidental release off the wrong digest -- an opaque hex
// string, easy to mis-paste -- by letting the operator verify the commit
// before triggering.
func TestHandleReleaseTriggerSubmit_DigestPin_ShowsCommitLinkedToGitHub(t *testing.T) {
	artifact := &fakeArtifactClient{
		getArtifactResps: map[string]*pb.GetArtifactResponse{
			"sha256:deadbeef": {
				Artifact: &pb.Artifact{ArtifactId: "art-1", Digest: "sha256:deadbeef"},
				Build:    &pb.Build{BuildId: "build-1", GitSha: "cafefeedbeef1234"},
			},
		},
	}
	app := &App{registry: &RegistryClient{
		App:      &releaseAppClient{apps: oneAppCatalog()},
		Release:  &fakeReleaseClient{},
		Artifact: artifact,
	}}

	form := url.Values{"target": {"app:platform-worker"}, "digest": {"sha256:deadbeef"}, "do": {"resolve"}}
	req := newReleaseTriggerPickerRequest(t, form)
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleReleaseTriggerSubmit)(w, req)

	if len(artifact.getArtifactReqs) != 1 || artifact.getArtifactReqs[0].GetDigest() != "sha256:deadbeef" {
		t.Fatalf("expected one GetArtifact(digest=sha256:deadbeef) call, got %+v", artifact.getArtifactReqs)
	}
	body := w.Body.String()
	if !strings.Contains(body, "https://github.com/whale-net/everything/commit/cafefeedbeef1234") {
		t.Errorf("expected a GitHub commit deep link for the pinned digest; body = %s", body)
	}
	if !strings.Contains(body, "cafefeed") {
		t.Errorf("expected the (truncated) git_sha to render; body = %s", body)
	}
}

// A digest that GetArtifact can't resolve must degrade to "unknown" in the
// Commit column rather than failing the whole Draft preview.
func TestHandleReleaseTriggerSubmit_DigestPin_UnresolvableDigestShowsUnknown(t *testing.T) {
	app := &App{registry: &RegistryClient{
		App:      &releaseAppClient{apps: oneAppCatalog()},
		Release:  &fakeReleaseClient{},
		Artifact: &fakeArtifactClient{},
	}}

	form := url.Values{"target": {"app:platform-worker"}, "digest": {"sha256:unknowndigest"}, "do": {"resolve"}}
	req := newReleaseTriggerPickerRequest(t, form)
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleReleaseTriggerSubmit)(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "unknown") {
		t.Errorf("expected the unresolvable digest to degrade to \"unknown\"; body = %s", body)
	}
}

// --- status page: retry re-submits via the same TriggerRelease path -----

func TestReleaseStatusRetryForm_PostsScopeToTriggerEndpoint(t *testing.T) {
	rel := &fakeReleaseClient{
		getResp: &pb.GetReleaseResponse{ReleaseRunId: "run-7", RequestedScope: "platform"},
	}
	app := &App{registry: &RegistryClient{Release: rel}}

	req := httptest.NewRequest(http.MethodGet, "/releases/run-7", nil)
	req.SetPathValue("id", "run-7")
	w := httptest.NewRecorder()
	app.handleReleaseStatus(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `action="/releases/trigger"`) {
		t.Errorf("expected the retry control to post to /releases/trigger (FR6/FR11: same code path as a fresh trigger), body = %s", body)
	}
	if !strings.Contains(body, `value="platform"`) {
		t.Errorf("expected the retry control to carry forward this run's requested_scope; body = %s", body)
	}
	if !strings.Contains(body, `name="do"`) || !strings.Contains(body, `value="trigger"`) {
		t.Errorf("expected the retry control to submit do=trigger so it reaches the confirmed-trigger branch directly; body = %s", body)
	}
}

// TestReleaseStatusPage_ShowsTargetCommitLinkedToGitHub covers the release-
// status screen's Commit column: for a target whose build_id resolves via
// GetBuild, the page must render the (truncated) git_sha and a GitHub
// commit deep link (helps catch releasing/promoting the wrong commit
// before it ships) -- and for a build_id GetBuild can't resolve, it must
// degrade to "unknown" rather than fail the whole page.
func TestReleaseStatusPage_ShowsTargetCommitLinkedToGitHub(t *testing.T) {
	rel := &fakeReleaseClient{
		getResp: &pb.GetReleaseResponse{
			ReleaseRunId: "run-7",
			Targets: []*pb.ReleaseRunTarget{
				{OwnerFullName: "platform-worker", BuildId: "build-ok"},
				{OwnerFullName: "platform-api", BuildId: "build-missing"},
			},
		},
	}
	artifact := &fakeArtifactClient{
		getBuildResps: map[string]*pb.GetBuildResponse{
			"build-ok": {Build: &pb.Build{BuildId: "build-ok", GitSha: "deadbeefcafefeed"}},
		},
	}
	app := &App{registry: &RegistryClient{Release: rel, Artifact: artifact}}

	req := httptest.NewRequest(http.MethodGet, "/releases/run-7", nil)
	req.SetPathValue("id", "run-7")
	w := httptest.NewRecorder()
	app.handleReleaseStatus(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "https://github.com/whale-net/everything/commit/deadbeefcafefeed") {
		t.Errorf("expected a GitHub commit deep link for the resolved build; body = %s", body)
	}
	if !strings.Contains(body, "deadbeef") {
		t.Errorf("expected the (truncated) git_sha to render; body = %s", body)
	}
	if !strings.Contains(body, "unknown") {
		t.Errorf("expected the unresolvable target's build_id to degrade to \"unknown\" rather than fail the page; body = %s", body)
	}
	if len(artifact.getBuildReqs) != 2 {
		t.Errorf("expected one GetBuild call per distinct build_id (2 targets), got %d", len(artifact.getBuildReqs))
	}
}

func TestReleaseStatusPage_HasManualRefreshLink(t *testing.T) {
	rel := &fakeReleaseClient{getResp: &pb.GetReleaseResponse{ReleaseRunId: "run-7"}}
	app := &App{registry: &RegistryClient{Release: rel}}

	req := httptest.NewRequest(http.MethodGet, "/releases/run-7", nil)
	req.SetPathValue("id", "run-7")
	w := httptest.NewRecorder()
	app.handleReleaseStatus(w, req)

	if !strings.Contains(w.Body.String(), `href="/releases/run-7"`) {
		t.Errorf("expected a manual-refresh link back to this same release's status page (no polling precedent exists in this UI); body = %s", w.Body.String())
	}
}

// --- issue #889 follow-up: the picker + per-target version selection ----

func newReleaseTriggerPickerRequest(t *testing.T, form url.Values) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/releases/trigger", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// The picker's "Resolve selection" step ("target" checkboxes instead of the
// old free-form "scope" field) must behave exactly like the pre-#889
// resolve step: render the Draft preview, never call TriggerRelease, and
// echo the original selection as a hidden field so the confirm step can
// re-resolve it.
func TestHandleReleaseTriggerSubmit_PickerResolve_RendersDraftWithoutCallingTriggerRelease(t *testing.T) {
	rel := &fakeReleaseClient{}
	app := &App{registry: &RegistryClient{
		App:     &releaseAppClient{apps: oneAppCatalog()},
		Release: rel,
	}}

	req := newReleaseTriggerPickerRequest(t, url.Values{"target": {"app:platform-worker"}, "do": {"resolve"}})
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleReleaseTriggerSubmit)(w, req)

	if len(rel.triggerCalls) != 0 {
		t.Fatalf("TriggerRelease calls = %d, want 0 -- the resolve-only step must never call TriggerRelease", len(rel.triggerCalls))
	}
	body := w.Body.String()
	if !strings.Contains(body, "platform-worker") {
		t.Errorf("expected the resolved target to appear in the Draft step; body = %s", body)
	}
	if !strings.Contains(body, `value="app:platform-worker"`) {
		t.Errorf("expected the Draft step to echo the original picker selection as a hidden field; body = %s", body)
	}
}

// TestHandleReleaseTriggerSubmit_PickerTrigger_ThreadsPerTargetVersionSelection
// is the direct regression test for issue #889's per-target Draft-page
// picker: two targets in one batch, one on an explicit hardcoded version
// and one on a specific bump type, must each reach TriggerRelease with
// their own distinct version_selection -- never the same value, never
// dropped.
func TestHandleReleaseTriggerSubmit_PickerTrigger_ThreadsPerTargetVersionSelection(t *testing.T) {
	rel := &fakeReleaseClient{triggerResp: &pb.TriggerReleaseResponse{ReleaseRunId: "run-99"}}
	app := &App{registry: &RegistryClient{
		App: &releaseAppClient{apps: []*pb.App{
			{AppId: "a1", FullName: "platform-worker", Name: "worker", Domain: "platform", Status: pb.AppStatus_APP_STATUS_ACTIVE},
			{AppId: "a2", FullName: "platform-api", Name: "api", Domain: "platform", Status: pb.AppStatus_APP_STATUS_ACTIVE},
		}},
		Release: rel,
	}}

	form := url.Values{
		"target":                    {"app:platform-worker", "app:platform-api"},
		"mode__platform-worker":     {"explicit"},
		"explicit__platform-worker": {"v9.9.9"},
		"mode__platform-api":        {"bump"},
		"bump__platform-api":        {"major"},
		"do":                        {"trigger"},
	}
	req := newReleaseTriggerPickerRequest(t, form)
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleReleaseTriggerSubmit)(w, req)

	if len(rel.triggerCalls) != 1 {
		t.Fatalf("TriggerRelease calls = %d, want 1; body = %s", len(rel.triggerCalls), w.Body.String())
	}
	byOwner := map[string]string{}
	for _, tgt := range rel.triggerCalls[0].GetTargets() {
		byOwner[tgt.GetOwnerFullName()] = tgt.GetVersionSelection()
	}
	if got := byOwner["platform-worker"]; got != "v9.9.9" {
		t.Errorf("platform-worker version_selection = %q, want v9.9.9", got)
	}
	if got := byOwner["platform-api"]; got != "major" {
		t.Errorf("platform-api version_selection = %q, want major", got)
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
}

// A target left on the default "Auto-bump" / "+patch" selection must reach
// TriggerRelease with an EMPTY version_selection, not the literal string
// "patch" -- empty is what preserves today's unchanged default behavior
// server-side (ResolvePlan passes --increment-patch for the whole batch
// when nothing overrides it) rather than forcing every default-left target
// through the per-target bump-type path for no reason.
func TestHandleReleaseTriggerSubmit_DefaultPatchBump_SendsEmptyVersionSelection(t *testing.T) {
	rel := &fakeReleaseClient{triggerResp: &pb.TriggerReleaseResponse{ReleaseRunId: "run-1"}}
	app := &App{registry: &RegistryClient{
		App:     &releaseAppClient{apps: oneAppCatalog()},
		Release: rel,
	}}

	req := newReleaseTriggerPickerRequest(t, url.Values{"target": {"app:platform-worker"}, "do": {"trigger"}})
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleReleaseTriggerSubmit)(w, req)

	if len(rel.triggerCalls) != 1 {
		t.Fatalf("TriggerRelease calls = %d, want 1", len(rel.triggerCalls))
	}
	targets := rel.triggerCalls[0].GetTargets()
	if len(targets) != 1 || targets[0].GetVersionSelection() != "" {
		t.Errorf("expected version_selection empty for an untouched target, got %+v", targets)
	}
}

// An "Explicit" mode with no version text entered must be rejected before
// ever reaching TriggerRelease -- silently falling back to auto-patch-bump
// would not be what the operator who chose "Explicit" intended.
func TestHandleReleaseTriggerSubmit_ExplicitModeWithEmptyValue_RejectedBeforeCallingAPI(t *testing.T) {
	rel := &fakeReleaseClient{}
	app := &App{registry: &RegistryClient{
		App:     &releaseAppClient{apps: oneAppCatalog()},
		Release: rel,
	}}

	form := url.Values{
		"target":                    {"app:platform-worker"},
		"mode__platform-worker":     {"explicit"},
		"explicit__platform-worker": {""},
		"do":                        {"trigger"},
	}
	req := newReleaseTriggerPickerRequest(t, form)
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleReleaseTriggerSubmit)(w, req)

	if len(rel.triggerCalls) != 0 {
		t.Fatalf("TriggerRelease calls = %d, want 0 -- an empty explicit version must be rejected before the RPC", len(rel.triggerCalls))
	}
	if !strings.Contains(w.Body.String(), "explicit version is required") {
		t.Errorf("expected a clear validation message; body = %s", w.Body.String())
	}
}

// A "domain:" picker token must resolve to every app/chart under that
// domain, mirroring resolveReleaseScope's single-domain-name behavior.
func TestHandleReleaseTriggerSubmit_PickerDomainToken_ResolvesEverythingUnderIt(t *testing.T) {
	rel := &fakeReleaseClient{triggerResp: &pb.TriggerReleaseResponse{ReleaseRunId: "run-1"}}
	app := &App{registry: &RegistryClient{
		App: &releaseAppClient{apps: []*pb.App{
			{AppId: "a1", FullName: "platform-worker", Name: "worker", Domain: "platform", Status: pb.AppStatus_APP_STATUS_ACTIVE},
			{AppId: "a2", FullName: "platform-api", Name: "api", Domain: "platform", Status: pb.AppStatus_APP_STATUS_ACTIVE},
		}},
		Release: rel,
	}}

	req := newReleaseTriggerPickerRequest(t, url.Values{"target": {"domain:platform"}, "do": {"trigger"}})
	w := httptest.NewRecorder()
	devUserAuth(t).RequireAuthFunc(app.handleReleaseTriggerSubmit)(w, req)

	if len(rel.triggerCalls) != 1 {
		t.Fatalf("TriggerRelease calls = %d, want 1", len(rel.triggerCalls))
	}
	if got := rel.triggerCalls[0].GetTargets(); len(got) != 2 {
		t.Errorf("expected both platform apps resolved from the domain token, got %d: %+v", len(got), got)
	}
}

// devUserAuth is declared in handlers_promote_rollback_test.go and reused
// here -- see that file's doc comment for why RequireAuthFunc is the only
// supported way to inject a *htmxauth.UserInfo into a handler test.

// --- release history --------------------------------------------------

// TestHandleReleaseHistory_RendersEveryAttempt covers the release-history
// screen: it must call ListReleaseAttempts unscoped (no owner_full_name --
// this screen shows every owner's attempts, unlike the owner-scoped
// ListReleases) and render each returned run, including its overall status
// derived from per-target state.
func TestHandleReleaseHistory_RendersEveryAttempt(t *testing.T) {
	rel := &fakeReleaseClient{
		listAttemptsResp: &pb.ListReleaseAttemptsResponse{
			Releases: []*pb.GetReleaseResponse{
				{
					ReleaseRunId:   "run-ok",
					TriggeredBy:    "alice",
					RequestedScope: "platform-worker",
					Targets: []*pb.ReleaseRunTarget{
						{OwnerFullName: "platform-worker", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED},
					},
				},
				{
					ReleaseRunId:   "run-failed",
					TriggeredBy:    "bob",
					RequestedScope: "platform-api",
					Targets: []*pb.ReleaseRunTarget{
						{OwnerFullName: "platform-api", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_FAILED},
					},
				},
			},
		},
	}
	app := &App{registry: &RegistryClient{Release: rel}}

	req := httptest.NewRequest(http.MethodGet, "/releases", nil)
	w := httptest.NewRecorder()
	app.handleReleaseHistory(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "run-ok") {
		t.Errorf("expected the succeeded run's id in the body; body = %s", body)
	}
	if !strings.Contains(body, "run-failed") {
		t.Errorf("expected the failed run's id in the body; body = %s", body)
	}
	if !strings.Contains(body, "Succeeded") {
		t.Errorf("expected a Succeeded badge; body = %s", body)
	}
	if !strings.Contains(body, "Failed") {
		t.Errorf("expected a Failed badge; body = %s", body)
	}
}

// TestHandleReleaseHistory_RPCFailure_RendersErrorBanner covers the load-
// failure path, mirroring handleBuilds/handleReconcileRuns's error
// handling.
func TestHandleReleaseHistory_RPCFailure_RendersErrorBanner(t *testing.T) {
	rel := &fakeReleaseClient{listAttemptsErr: status.Error(codes.Unavailable, "db down")}
	app := &App{registry: &RegistryClient{Release: rel}}

	req := httptest.NewRequest(http.MethodGet, "/releases", nil)
	w := httptest.NewRecorder()
	app.handleReleaseHistory(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "alert-error") {
		t.Errorf("expected an error banner on RPC failure; body = %s", body)
	}
}
