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

// This file guards task #2369 (root plan #2359, manmanv2 M6): the
// Infrastructure page's FR2 host list with drain-state badges and its FR3
// drain / FR4 undrain one-action controls, additive alongside /servers
// (NFR6).
//
// fakeInfrastructureAPIClient is scoped to handleInfrastructure's and
// handleInfrastructureAction's call graph: ListServers,
// ListAllocatedPorts, DrainServer, UndrainServer, plus GetServer and
// ListServerGameConfigs for the /servers and /servers/<id> regression
// checks (NFR6). Any call to an un-overridden ManManAPIClient method
// panics on the nil embedded interface, deliberately -- same convention
// as fakeGamesAPIClient (handlers_games_test.go).
type fakeInfrastructureAPIClient struct {
	manmanpb.ManManAPIClient

	servers        []*manmanpb.Server
	allocatedPorts map[int64][]*manmanpb.AllocatedPort
	listPortsErr   error

	drainCalls   []int64
	undrainCalls []int64
	drainResult  *manmanpb.Server
	undrainResult *manmanpb.Server
}

func newFakeInfrastructureAPIClient() *fakeInfrastructureAPIClient {
	return &fakeInfrastructureAPIClient{allocatedPorts: map[int64][]*manmanpb.AllocatedPort{}}
}

func (f *fakeInfrastructureAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	return &manmanpb.ListServersResponse{Servers: f.servers}, nil
}

func (f *fakeInfrastructureAPIClient) ListAllocatedPorts(ctx context.Context, in *manmanpb.ListAllocatedPortsRequest, opts ...grpc.CallOption) (*manmanpb.ListAllocatedPortsResponse, error) {
	if f.listPortsErr != nil {
		return nil, f.listPortsErr
	}
	return &manmanpb.ListAllocatedPortsResponse{Ports: f.allocatedPorts[in.GetServerId()]}, nil
}

func (f *fakeInfrastructureAPIClient) DrainServer(ctx context.Context, in *manmanpb.DrainServerRequest, opts ...grpc.CallOption) (*manmanpb.DrainServerResponse, error) {
	f.drainCalls = append(f.drainCalls, in.GetServerId())
	result := f.drainResult
	if result == nil {
		result = &manmanpb.Server{ServerId: in.GetServerId(), DrainState: "draining"}
	}
	return &manmanpb.DrainServerResponse{Server: result}, nil
}

func (f *fakeInfrastructureAPIClient) UndrainServer(ctx context.Context, in *manmanpb.UndrainServerRequest, opts ...grpc.CallOption) (*manmanpb.UndrainServerResponse, error) {
	f.undrainCalls = append(f.undrainCalls, in.GetServerId())
	result := f.undrainResult
	if result == nil {
		result = &manmanpb.Server{ServerId: in.GetServerId(), DrainState: "schedulable"}
	}
	return &manmanpb.UndrainServerResponse{Server: result}, nil
}

func (f *fakeInfrastructureAPIClient) GetServer(ctx context.Context, in *manmanpb.GetServerRequest, opts ...grpc.CallOption) (*manmanpb.GetServerResponse, error) {
	for _, s := range f.servers {
		if s.GetServerId() == in.GetServerId() {
			return &manmanpb.GetServerResponse{Server: s}, nil
		}
	}
	return nil, http.ErrAbortHandler
}

func (f *fakeInfrastructureAPIClient) ListServerGameConfigs(ctx context.Context, in *manmanpb.ListServerGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error) {
	return &manmanpb.ListServerGameConfigsResponse{}, nil
}

func renderInfrastructureHTTP(t *testing.T, api *fakeInfrastructureAPIClient, target string) (int, string) {
	t.Helper()
	app := &App{grpc: &ControlClient{api: api}}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	app.handleInfrastructure(w, req)
	return w.Code, w.Body.String()
}

func postInfrastructureActionHTTP(t *testing.T, api *fakeInfrastructureAPIClient, target string) (int, string) {
	t.Helper()
	app := &App{grpc: &ControlClient{api: api}}
	req := httptest.NewRequest(http.MethodPost, target, nil)
	w := httptest.NewRecorder()
	app.handleInfrastructureAction(w, req)
	return w.Code, w.Body.String()
}

// TestHandleInfrastructure_ListsAllHostsWithDrainBadge covers the FR2 host
// list: /infrastructure renders 200 for every host in a faked ListServers,
// each with its drain-state badge (an empty drain_state renders as the
// "schedulable" floor per drainStateLabel).
func TestHandleInfrastructure_ListsAllHostsWithDrainBadge(t *testing.T) {
	api := newFakeInfrastructureAPIClient()
	api.servers = []*manmanpb.Server{
		{ServerId: 1, Name: "host-alpha", DrainState: ""},
		{ServerId: 2, Name: "host-beta", DrainState: "draining"},
		{ServerId: 3, Name: "host-gamma", DrainState: "drained"},
	}

	code, body := renderInfrastructureHTTP(t, api, "/infrastructure")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}

	for _, want := range []string{"host-alpha", "host-beta", "host-gamma"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered page missing host name %q", want)
		}
	}
	for _, badge := range []string{">schedulable<", ">draining<", ">drained<"} {
		if !strings.Contains(body, badge) {
			t.Errorf("rendered page missing drain-state badge %q", badge)
		}
	}
}

// TestHandleInfrastructure_NoCapabilityData_RendersFR2Floor covers a host
// with no capability/health data: it must render the FR2 floor (name +
// drain-state badge) without erroring, whether ListAllocatedPorts returns
// empty or fails outright (handleInfrastructure degrades that one host's
// row to "not reported" rather than failing the whole list).
func TestHandleInfrastructure_NoCapabilityData_RendersFR2Floor(t *testing.T) {
	t.Run("no allocated ports", func(t *testing.T) {
		api := newFakeInfrastructureAPIClient()
		api.servers = []*manmanpb.Server{{ServerId: 1, Name: "bare-host"}}

		code, body := renderInfrastructureHTTP(t, api, "/infrastructure")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", code, body)
		}
		if !strings.Contains(body, "bare-host") || !strings.Contains(body, ">schedulable<") {
			t.Errorf("rendered page missing FR2 floor for bare-host: %s", body)
		}
	})

	t.Run("ListAllocatedPorts fails", func(t *testing.T) {
		api := newFakeInfrastructureAPIClient()
		api.servers = []*manmanpb.Server{{ServerId: 1, Name: "erroring-host"}}
		api.listPortsErr = http.ErrAbortHandler

		code, body := renderInfrastructureHTTP(t, api, "/infrastructure")
		if code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (a per-host ports fetch failure must not fail the whole list); body: %s", code, body)
		}
		if !strings.Contains(body, "erroring-host") || !strings.Contains(body, ">schedulable<") {
			t.Errorf("rendered page missing FR2 floor for erroring-host: %s", body)
		}
	})
}

// TestHandleInfrastructureAction_Drain_PostsDrainServerForRightHost covers
// FR3: the drain control posts DrainServer for the exact host id in the
// URL path, not some other host.
func TestHandleInfrastructureAction_Drain_PostsDrainServerForRightHost(t *testing.T) {
	api := newFakeInfrastructureAPIClient()
	api.servers = []*manmanpb.Server{{ServerId: 42, Name: "target-host"}}

	code, _ := postInfrastructureActionHTTP(t, api, "/infrastructure/42/drain")
	if code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 redirect", code)
	}
	if len(api.drainCalls) != 1 || api.drainCalls[0] != 42 {
		t.Fatalf("drainCalls = %v, want exactly [42]", api.drainCalls)
	}
	if len(api.undrainCalls) != 0 {
		t.Errorf("undrainCalls = %v, want none (drain must not also call undrain)", api.undrainCalls)
	}
}

// TestHandleInfrastructureAction_Undrain_PostsUndrainServerForRightHost
// covers FR4: the undrain control posts UndrainServer for the exact host
// id in the URL path.
func TestHandleInfrastructureAction_Undrain_PostsUndrainServerForRightHost(t *testing.T) {
	api := newFakeInfrastructureAPIClient()
	api.servers = []*manmanpb.Server{{ServerId: 7, Name: "target-host", DrainState: "drained"}}

	code, _ := postInfrastructureActionHTTP(t, api, "/infrastructure/7/undrain")
	if code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 redirect", code)
	}
	if len(api.undrainCalls) != 1 || api.undrainCalls[0] != 7 {
		t.Fatalf("undrainCalls = %v, want exactly [7]", api.undrainCalls)
	}
	if len(api.drainCalls) != 0 {
		t.Errorf("drainCalls = %v, want none (undrain must not also call drain)", api.drainCalls)
	}
}

// TestHandleInfrastructure_DrainUndrainMutuallyExclusive covers the
// mutual-exclusivity requirement: a schedulable host offers only Drain, a
// drained host offers only Undrain -- never both, never neither.
func TestHandleInfrastructure_DrainUndrainMutuallyExclusive(t *testing.T) {
	api := newFakeInfrastructureAPIClient()
	api.servers = []*manmanpb.Server{
		{ServerId: 1, Name: "schedulable-host", DrainState: ""},
		{ServerId: 2, Name: "drained-host", DrainState: "drained"},
	}

	code, body := renderInfrastructureHTTP(t, api, "/infrastructure")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}

	schedulableRow := rowSection(t, body, "schedulable-host")
	if !strings.Contains(schedulableRow, ">Drain<") {
		t.Errorf("schedulable host row missing Drain control: %s", schedulableRow)
	}
	if strings.Contains(schedulableRow, ">Undrain<") {
		t.Errorf("schedulable host row must not offer Undrain: %s", schedulableRow)
	}

	drainedRow := rowSection(t, body, "drained-host")
	if !strings.Contains(drainedRow, ">Undrain<") {
		t.Errorf("drained host row missing Undrain control: %s", drainedRow)
	}
	if strings.Contains(drainedRow, ">Drain<") {
		t.Errorf("drained host row (already drained) must not offer Drain: %s", drainedRow)
	}
}

// TestHandleInfrastructure_DrainingDistinctFromDrained covers the FR2
// transient state: a "draining" host must render distinctly from a
// "drained" host, not collapse to the same badge/label.
func TestHandleInfrastructure_DrainingDistinctFromDrained(t *testing.T) {
	api := newFakeInfrastructureAPIClient()
	api.servers = []*manmanpb.Server{
		{ServerId: 1, Name: "draining-host", DrainState: "draining"},
		{ServerId: 2, Name: "drained-host", DrainState: "drained"},
	}

	code, body := renderInfrastructureHTTP(t, api, "/infrastructure")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}

	drainingRow := rowSection(t, body, "draining-host")
	if !strings.Contains(drainingRow, ">draining<") {
		t.Errorf("draining-host row missing 'draining' badge: %s", drainingRow)
	}
	if strings.Contains(drainingRow, ">drained<") {
		t.Errorf("draining-host row must not render as 'drained': %s", drainingRow)
	}

	drainedRow := rowSection(t, body, "drained-host")
	if !strings.Contains(drainedRow, ">drained<") {
		t.Errorf("drained-host row missing 'drained' badge: %s", drainedRow)
	}
	if strings.Contains(drainedRow, ">draining<") {
		t.Errorf("drained-host row must not render as 'draining': %s", drainedRow)
	}

	// draining also still offers Undrain (per canUndrain), but that is not
	// this test's concern -- the labels/badges above are.
}

// TestHandleInfrastructure_ConfirmCopy covers US2/FR4's required copy: the
// drain affordance must convey that running sessions are stopped, and the
// undrain affordance must convey that nothing is restarted.
func TestHandleInfrastructure_ConfirmCopy(t *testing.T) {
	api := newFakeInfrastructureAPIClient()
	api.servers = []*manmanpb.Server{
		{ServerId: 1, Name: "schedulable-host", DrainState: ""},
		{ServerId: 2, Name: "drained-host", DrainState: "drained"},
	}

	code, body := renderInfrastructureHTTP(t, api, "/infrastructure")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}

	lower := strings.ToLower(body)
	if !strings.Contains(lower, "stop") || !strings.Contains(lower, "session") {
		t.Errorf("rendered page's drain affordance does not convey that running sessions are stopped: %s", body)
	}
	if !strings.Contains(lower, "does not restart") && !strings.Contains(lower, "doesn't restart") {
		t.Errorf("rendered page's undrain affordance does not state that nothing is restarted: %s", body)
	}
}

// TestHandleInfrastructure_NoSGCTerminology guards the terminology
// constraint: no user-visible "SGC"/"server game config" string may
// appear in the rendered page.
func TestHandleInfrastructure_NoSGCTerminology(t *testing.T) {
	api := newFakeInfrastructureAPIClient()
	api.servers = []*manmanpb.Server{{ServerId: 1, Name: "host-alpha", DrainState: "draining"}}

	code, body := renderInfrastructureHTTP(t, api, "/infrastructure")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}

	lower := strings.ToLower(body)
	if strings.Contains(lower, "sgc") {
		t.Errorf("rendered Infrastructure page contains %q", "sgc")
	}
	if strings.Contains(lower, "server game config") {
		t.Errorf("rendered Infrastructure page contains %q", "server game config")
	}
}

// Note: /servers and /servers/<id> retired to redirects onto /infrastructure
// by task #2372 (M6 navigation/disposition, FR16) -- see
// handlers_redirects_test.go for that coverage. This file's own NFR6
// regression check against the old pages (previously
// TestServersAndServerDetail_StillReturn200) no longer applies now that
// this task deliberately changed that behavior.

// TestHandleInfrastructure_ManageQueryParam_OpensThatHostsPanel covers task
// #2372's "?manage=<id>" contract (FR16's /servers/<id> redirect target,
// preserving the identifier): the named host's row renders its Manage
// panel open (public address + allowed port ranges), and no other host's
// panel does.
func TestHandleInfrastructure_ManageQueryParam_OpensThatHostsPanel(t *testing.T) {
	api := newFakeInfrastructureAPIClient()
	api.servers = []*manmanpb.Server{
		{ServerId: 1, Name: "host-alpha", HostPublicAddress: "alpha.example.com:27015"},
		{ServerId: 2, Name: "host-beta"},
	}

	code, body := renderInfrastructureHTTP(t, api, "/infrastructure?manage=1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}

	if !strings.Contains(body, `id="host-manage-1"`) || !strings.Contains(body, `id="host-manage-2"`) {
		t.Fatalf("expected both hosts' Manage panels to render, got: %s", body)
	}
	openIdx := strings.Index(body, `id="host-manage-1"`)
	closedIdx := strings.Index(body, `id="host-manage-2"`)
	// host-manage-1's <details> must carry the "open" attribute; host-manage-2's must not.
	openTagEnd := strings.Index(body[openIdx:], ">")
	if !strings.Contains(body[openIdx:openIdx+openTagEnd], " open") {
		t.Errorf("expected host-manage-1's <details> to render open, got: %s", body[openIdx:openIdx+openTagEnd])
	}
	closedTagEnd := strings.Index(body[closedIdx:], ">")
	if strings.Contains(body[closedIdx:closedIdx+closedTagEnd], " open") {
		t.Errorf("expected host-manage-2's <details> to render closed, got: %s", body[closedIdx:closedIdx+closedTagEnd])
	}
	if !strings.Contains(body, "alpha.example.com:27015") {
		t.Errorf("expected host-alpha's public address to render in its Manage panel, got: %s", body)
	}
}

// TestHandleInfrastructureUpdateAddress_SendsFieldMask guards #1528's
// clear-via-field-mask contract (per #1527), now served from
// "/infrastructure/{id}/update-address" (moved here from the retired
// handleServerUpdateAddress by task #2372): both a non-empty and an empty
// submitted value must send update_paths == ["host_public_address"] on the
// outgoing UpdateServerRequest, so an empty submission clears the field via
// the mask rather than falling back to update-all semantics.
func TestHandleInfrastructureUpdateAddress_SendsFieldMask(t *testing.T) {
	cases := []struct {
		name        string
		submitted   string
		wantAddress string
	}{
		{name: "non-empty value", submitted: "game.example.com:27015", wantAddress: "game.example.com:27015"},
		{name: "empty value clears", submitted: "", wantAddress: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotReq *manmanpb.UpdateServerRequest
			api := &stubInfrastructureUpdateServerClient{
				updateServerFunc: func(ctx context.Context, in *manmanpb.UpdateServerRequest, opts ...grpc.CallOption) (*manmanpb.UpdateServerResponse, error) {
					gotReq = in
					return &manmanpb.UpdateServerResponse{}, nil
				},
			}
			app := &App{grpc: &ControlClient{api: api}}

			form := "host_public_address=" + tc.submitted
			req := httptest.NewRequest(http.MethodPost, "/infrastructure/6/update-address", strings.NewReader(form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()

			app.handleInfrastructureUpdateAddress(w, req, "6")

			if w.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusSeeOther, w.Body.String())
			}
			if got, want := w.Header().Get("Location"), "/infrastructure?manage=6#host-manage-6"; got != want {
				t.Errorf("Location = %q, want %q", got, want)
			}

			if gotReq == nil {
				t.Fatalf("expected UpdateServer to be called")
			}
			if gotReq.ServerId != 6 {
				t.Errorf("ServerId = %d, want 6", gotReq.ServerId)
			}
			if gotReq.HostPublicAddress != tc.wantAddress {
				t.Errorf("HostPublicAddress = %q, want %q", gotReq.HostPublicAddress, tc.wantAddress)
			}
			wantPaths := []string{"host_public_address"}
			if len(gotReq.UpdatePaths) != len(wantPaths) || gotReq.UpdatePaths[0] != wantPaths[0] {
				t.Errorf("UpdatePaths = %v, want %v", gotReq.UpdatePaths, wantPaths)
			}
		})
	}
}

// TestHandleInfrastructureUpdateAddress_RejectsNonPost guards NFR2/FR10-
// adjacent hygiene: this handler must only accept POST, not silently accept
// a GET that could be triggered by a prefetch or a stray link.
func TestHandleInfrastructureUpdateAddress_RejectsNonPost(t *testing.T) {
	called := false
	api := &stubInfrastructureUpdateServerClient{
		updateServerFunc: func(ctx context.Context, in *manmanpb.UpdateServerRequest, opts ...grpc.CallOption) (*manmanpb.UpdateServerResponse, error) {
			called = true
			return &manmanpb.UpdateServerResponse{}, nil
		},
	}
	app := &App{grpc: &ControlClient{api: api}}

	req := httptest.NewRequest(http.MethodGet, "/infrastructure/6/update-address", nil)
	w := httptest.NewRecorder()

	app.handleInfrastructureUpdateAddress(w, req, "6")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
	if called {
		t.Errorf("expected UpdateServer not to be called for a GET request")
	}
}

// stubInfrastructureUpdateServerClient embeds the (nil)
// manmanpb.ManManAPIClient interface so it satisfies the full interface
// without implementing every RPC -- only updateServerFunc is exercised by
// the tests above; any other method call would nil-panic, the desired
// failure mode for an unexpected call.
type stubInfrastructureUpdateServerClient struct {
	manmanpb.ManManAPIClient
	updateServerFunc func(ctx context.Context, in *manmanpb.UpdateServerRequest, opts ...grpc.CallOption) (*manmanpb.UpdateServerResponse, error)
}

func (s *stubInfrastructureUpdateServerClient) UpdateServer(ctx context.Context, in *manmanpb.UpdateServerRequest, opts ...grpc.CallOption) (*manmanpb.UpdateServerResponse, error) {
	return s.updateServerFunc(ctx, in, opts...)
}

// rowSection isolates the <tr>...</tr> block containing hostName's table
// cell, so assertions about a row's controls/badges don't accidentally
// match a different host's row in the same table -- or, importantly, the
// layout's server-switcher <select> in the nav, which also lists every
// host name (as an <option>) above the table.
func rowSection(t *testing.T, body, hostName string) string {
	t.Helper()
	marker := ">" + hostName + "</td>"
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("host-name table cell %q not found in body", marker)
	}
	rowStart := strings.LastIndex(body[:idx], "<tr")
	if rowStart < 0 {
		t.Fatalf("no <tr> found before marker %q", marker)
	}
	rowEndOffset := strings.Index(body[idx:], "</tr>")
	if rowEndOffset < 0 {
		t.Fatalf("no </tr> found after marker %q", marker)
	}
	return body[rowStart : idx+rowEndOffset+len("</tr>")]
}
