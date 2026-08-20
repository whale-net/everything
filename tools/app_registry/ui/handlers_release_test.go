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

// devUserAuth is declared in handlers_promote_rollback_test.go and reused
// here -- see that file's doc comment for why RequireAuthFunc is the only
// supported way to inject a *htmxauth.UserInfo into a handler test.
