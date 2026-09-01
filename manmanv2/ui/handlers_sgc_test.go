package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// This file guards #1530's handleSGCDetail wiring: the "Status & Connect"
// page data (DeploymentStatus/ConnectAddresses/ConnectAddressUnavailable)
// must be derived from the sessions/server the handler already fetches --
// via components.LatestSession/ComputeDeploymentStatus/
// ComputeConnectAddresses (NFR1) -- and must keep landing on FR7's
// "unavailable" state, not an error page, when GetServer fails (the
// existing tolerate-and-continue behavior the handler had before this
// task).
//
// fakeManManAPIClient/fakeWorkshopServiceClient embed their respective nil
// gRPC client interfaces and override only the RPCs handleSGCDetail's call
// graph actually reaches for these scenarios (see grpc_client.go's thin
// ControlClient wrappers for which underlying client -- api vs workshop --
// each helper method uses). Any call to an un-overridden method panics on
// the nil embedded interface, which is deliberate: it would fail the test
// loudly rather than silently returning a zero value if a scenario's call
// graph ever grows.
//
// mutation-tested (verified red, by hand, then reverted): changing
// handleSGCDetail's `deploymentStatus := components.ComputeDeploymentStatus(latestSession)`
// to `deploymentStatus := components.DeploymentStatus(latestSession.GetStatus())`
// (passing the raw Session.status straight through instead of going
// through the fail-closed helper) made
// TestHandleSGCDetail_PendingSession_RendersStoppedNotRunning fail --
// rendering "Pending" instead of "Stopped/Offline" -- while compiling
// cleanly; reverting restored green.

type fakeManManAPIClient struct {
	manmanpb.ManManAPIClient

	sgc          *manmanpb.ServerGameConfig
	server       *manmanpb.Server
	getServerErr error
	sessions     []*manmanpb.Session
}

func (f *fakeManManAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	return &manmanpb.ListServersResponse{}, nil
}

func (f *fakeManManAPIClient) GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	return &manmanpb.GetServerGameConfigResponse{Config: f.sgc}, nil
}

func (f *fakeManManAPIClient) GetServer(ctx context.Context, in *manmanpb.GetServerRequest, opts ...grpc.CallOption) (*manmanpb.GetServerResponse, error) {
	if f.getServerErr != nil {
		return nil, f.getServerErr
	}
	return &manmanpb.GetServerResponse{Server: f.server}, nil
}

func (f *fakeManManAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	// No GameConfig configured for these tests: handleSGCDetail tolerates
	// this (gameConfig stays nil) and skips the Game/volumes/backup-config
	// fetches that depend on it -- keeping this fake's surface limited to
	// what these scenarios exercise.
	return &manmanpb.GetGameConfigResponse{}, nil
}

func (f *fakeManManAPIClient) ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	return &manmanpb.ListSessionsResponse{Sessions: f.sessions}, nil
}

func (f *fakeManManAPIClient) ListGameConfigVolumes(ctx context.Context, in *manmanpb.ListGameConfigVolumesRequest, opts ...grpc.CallOption) (*manmanpb.ListGameConfigVolumesResponse, error) {
	return &manmanpb.ListGameConfigVolumesResponse{}, nil
}

func (f *fakeManManAPIClient) ListBackups(ctx context.Context, in *manmanpb.ListBackupsRequest, opts ...grpc.CallOption) (*manmanpb.ListBackupsResponse, error) {
	return &manmanpb.ListBackupsResponse{}, nil
}

type fakeWorkshopServiceClient struct {
	manmanpb.WorkshopServiceClient
}

func (f *fakeWorkshopServiceClient) ListInstallations(ctx context.Context, in *manmanpb.ListInstallationsRequest, opts ...grpc.CallOption) (*manmanpb.ListInstallationsResponse, error) {
	return &manmanpb.ListInstallationsResponse{}, nil
}

func (f *fakeWorkshopServiceClient) ListSGCLibraries(ctx context.Context, in *manmanpb.ListSGCLibrariesRequest, opts ...grpc.CallOption) (*manmanpb.ListSGCLibrariesResponse, error) {
	return &manmanpb.ListSGCLibrariesResponse{}, nil
}

func (f *fakeWorkshopServiceClient) GetSGCLibraryAttachments(ctx context.Context, in *manmanpb.GetSGCLibraryAttachmentsRequest, opts ...grpc.CallOption) (*manmanpb.GetSGCLibraryAttachmentsResponse, error) {
	return &manmanpb.GetSGCLibraryAttachmentsResponse{}, nil
}

// newTestApp builds an App wired to fake gRPC clients. Since this test
// lives in package main, it can set ControlClient's unexported api/
// workshop fields directly -- no interface refactor of ControlClient is
// needed for this white-box test.
func newTestApp(api *fakeManManAPIClient, workshop *fakeWorkshopServiceClient) *App {
	return &App{
		grpc: &ControlClient{api: api, workshop: workshop},
	}
}

func renderSGCDetail(t *testing.T, app *App, sgcID string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/sgc/"+sgcID, nil)
	w := httptest.NewRecorder()
	app.handleSGCDetail(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleSGCDetail status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	return w.Body.String()
}

// statusConnectSection isolates the "Status & Connect" card's markup from
// the rest of the rendered page, mirroring
// manmanv2/ui/pages/sgc_detail_status_connect_test.go's helper of the same
// name. Scoping matters here specifically: the unrelated "Deployment Info"
// card renders port bindings in "container_port:host_port/protocol" form
// (e.g. "25565:25565/TCP"), which would otherwise falsely satisfy a
// page-wide "no dangling :port fragment" assertion.
func statusConnectSection(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, "Status &amp; Connect")
	if start < 0 {
		t.Fatalf("expected a 'Status & Connect' heading in rendered body, got %q", body)
	}
	end := strings.Index(body[start:], "<!-- SGC Info -->")
	if end < 0 {
		t.Fatalf("expected a '<!-- SGC Info -->' marker after the Status & Connect card, got %q", body)
	}
	return body[start : start+end]
}

// TestHandleSGCDetail_RunningWithAddress_PopulatesConnectAddresses covers
// the "running session + address yields a non-empty ConnectAddresses" case
// from the task's Testing section: the address computed from the already-
// fetched server/sessions/port-bindings reaches the rendered page.
func TestHandleSGCDetail_RunningWithAddress_PopulatesConnectAddresses(t *testing.T) {
	api := &fakeManManAPIClient{
		sgc: &manmanpb.ServerGameConfig{
			ServerGameConfigId: 42,
			ServerId:           7,
			Status:             "active",
			PortBindings: []*manmanpb.PortBinding{
				{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
			},
		},
		server:   &manmanpb.Server{ServerId: 7, Name: "Alpha", HostPublicAddress: "play.example.com"},
		sessions: []*manmanpb.Session{{SessionId: 1, StartedAt: 100, Status: "running"}},
	}
	app := newTestApp(api, &fakeWorkshopServiceClient{})

	body := renderSGCDetail(t, app, "42")
	section := statusConnectSection(t, body)

	if !strings.Contains(section, "play.example.com:25565") {
		t.Errorf("expected the computed connect address to reach the rendered page, got section: %s", section)
	}
	if !strings.Contains(section, "Running") {
		t.Errorf("expected the Running status label, got section: %s", section)
	}
}

// TestHandleSGCDetail_GetServerFailure_RendersUnavailable covers the
// "a GetServer failure yields ConnectAddressUnavailable" case: server stays
// nil (GetHostPublicAddress() on nil returns ""), and the handler must land
// on FR7's unavailable state rather than erroring the page (the existing
// tolerate-GetServer-failure behavior it already had).
func TestHandleSGCDetail_GetServerFailure_RendersUnavailable(t *testing.T) {
	api := &fakeManManAPIClient{
		sgc: &manmanpb.ServerGameConfig{
			ServerGameConfigId: 43,
			ServerId:           8,
			Status:             "active",
			PortBindings: []*manmanpb.PortBinding{
				{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
			},
		},
		getServerErr: context.DeadlineExceeded,
		sessions:     []*manmanpb.Session{{SessionId: 1, StartedAt: 100, Status: "running"}},
	}
	app := newTestApp(api, &fakeWorkshopServiceClient{})

	body := renderSGCDetail(t, app, "43")
	section := statusConnectSection(t, body)

	if !strings.Contains(section, "Connect address unavailable") {
		t.Errorf("expected the unavailable message when GetServer fails, got section: %s", section)
	}
	// Scoped to the Status & Connect card: the unrelated Deployment Info
	// card legitimately renders "25565:25565/TCP" (container:host/protocol)
	// from sgc.PortBindings directly, which is not this section.
	if strings.Contains(section, ":25565") {
		t.Errorf("expected no dangling port fragment when the host is unknown, got section: %s", section)
	}
}

// sessionHistorySection isolates the "Session history" card's markup from
// the rest of the rendered page, mirroring
// manmanv2/ui/pages/sgc_detail_sessions_test.go's helper of the same name.
// Scoping matters here specifically: the unrelated Danger Zone further
// down the page also references /sessions/{id}/stop hrefs and session ids.
func sessionHistorySection(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, ">Session history<")
	if start < 0 {
		t.Fatalf("expected a 'Session history' heading in rendered body, got %q", body)
	}
	end := strings.Index(body[start:], "Danger Zone: intentionally kept")
	if end < 0 {
		t.Fatalf("expected a Danger Zone marker after the Session history card, got %q", body)
	}
	return body[start : start+end]
}

// TestHandleSGCDetail_SessionsSortedNewestFirst covers #1531's handler-side
// ordering requirement: handleSGCDetail must sort ListSessionsWithFilters'
// result via components.SortSessionsNewestFirst before handing it to the
// page (NFR1), so sgc_detail.templ -- which does not re-sort, see
// manmanv2/ui/pages/sgc_detail_sessions_test.go -- renders newest-first
// regardless of the RPC's own return order. fakeManManAPIClient.ListSessions
// returns f.sessions verbatim (no sorting of its own), so an out-of-order
// fixture here only renders in order if the handler did the sorting.
//
// mutation-tested (verified red, by hand, then reverted): commenting out
// the `components.SortSessionsNewestFirst(sessions)` call in
// handleSGCDetail made this test fail -- session ids rendered in the fake's
// original (unsorted) order -- while compiling cleanly; reverting restored
// green.
func TestHandleSGCDetail_SessionsSortedNewestFirst(t *testing.T) {
	api := &fakeManManAPIClient{
		sgc: &manmanpb.ServerGameConfig{
			ServerGameConfigId: 50,
			ServerId:           11,
			Status:             "active",
		},
		server: &manmanpb.Server{ServerId: 11, Name: "Theta", HostPublicAddress: "play.example.com"},
		// Deliberately out of started_at order, as returned by the RPC.
		sessions: []*manmanpb.Session{
			{SessionId: 1, StartedAt: 100, Status: "stopped"},
			{SessionId: 2, StartedAt: 300, Status: "stopped"},
			{SessionId: 3, StartedAt: 200, Status: "stopped"},
		},
	}
	app := newTestApp(api, &fakeWorkshopServiceClient{})

	body := renderSGCDetail(t, app, "50")
	section := sessionHistorySection(t, body)

	idx2 := strings.Index(section, "/sessions/2")
	idx3 := strings.Index(section, "/sessions/3")
	idx1 := strings.Index(section, "/sessions/1")
	if idx2 < 0 || idx3 < 0 || idx1 < 0 {
		t.Fatalf("expected all three session ids to render, got section: %s", section)
	}
	if !(idx2 < idx3 && idx3 < idx1) {
		t.Errorf("expected handleSGCDetail to sort Sessions newest-first (2 [started_at=300], 3 [200], 1 [100]) before rendering, got indices 2=%d 3=%d 1=%d in section: %s", idx2, idx3, idx1, section)
	}
}

// TestHandleSGCDetail_PendingSession_RendersStoppedNotRunning is the red/
// green anchor documented at the top of this file: a pending latest
// session must roll up to stopped/offline through
// components.ComputeDeploymentStatus, not leak the raw "pending" status
// string through as if it were meaningful on its own.
func TestHandleSGCDetail_PendingSession_RendersStoppedNotRunning(t *testing.T) {
	api := &fakeManManAPIClient{
		sgc: &manmanpb.ServerGameConfig{
			ServerGameConfigId: 44,
			ServerId:           9,
			Status:             "active",
		},
		server:   &manmanpb.Server{ServerId: 9, Name: "Beta", HostPublicAddress: "play.example.com"},
		sessions: []*manmanpb.Session{{SessionId: 1, StartedAt: 100, Status: "pending"}},
	}
	app := newTestApp(api, &fakeWorkshopServiceClient{})

	body := renderSGCDetail(t, app, "44")
	section := statusConnectSection(t, body)

	if !strings.Contains(section, "Stopped/Offline") {
		t.Errorf("expected Stopped/Offline for a pending latest session, got section: %s", section)
	}
	// The label alone isn't sufficient to catch a raw-status leak:
	// deploymentStatusLabel already falls back to "Stopped/Offline" for any
	// non-running value, pending included. The badge colour comes from
	// statusBadgeVariant(string(status)) though, and "pending" maps to
	// badge-warning while the fail-closed DeploymentStopped value maps to
	// badge-neutral -- so this is the assertion that actually catches a
	// raw Session.status pass-through regression.
	if !strings.Contains(section, "badge-neutral") {
		t.Errorf("expected the stopped rollup's neutral badge colour (not a raw-status colour like badge-warning for \"pending\"), got section: %s", section)
	}
	if strings.Contains(section, ">Running<") {
		t.Errorf("expected no Running label for a pending latest session, got section: %s", section)
	}
}
