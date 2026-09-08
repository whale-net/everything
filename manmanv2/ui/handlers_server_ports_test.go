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

// Guards task #2095's per-server allowed host-port range UI (FR12):
// adds, edits, and removals are all funneled through the public
// UpdateServerAllowedPortRanges replace-all RPC (NFR3), the zero-ranges
// state stays unconstrained (SB-1.2), and duplicate adds are rejected
// client-side (Decision 7: guidance only, no save-time API validation).

type fakePortRangeAPIClient struct {
	manmanpb.ManManAPIClient

	server  *manmanpb.Server
	ranges  []*manmanpb.PortRange
	updates []*manmanpb.UpdateServerAllowedPortRangesRequest
	gets    int
}

func (f *fakePortRangeAPIClient) GetServer(ctx context.Context, in *manmanpb.GetServerRequest, opts ...grpc.CallOption) (*manmanpb.GetServerResponse, error) {
	f.gets++
	return &manmanpb.GetServerResponse{Server: f.server}, nil
}

func (f *fakePortRangeAPIClient) UpdateServerAllowedPortRanges(ctx context.Context, in *manmanpb.UpdateServerAllowedPortRangesRequest, opts ...grpc.CallOption) (*manmanpb.UpdateServerAllowedPortRangesResponse, error) {
	f.updates = append(f.updates, in)
	f.ranges = in.Ranges
	return &manmanpb.UpdateServerAllowedPortRangesResponse{Ranges: in.Ranges}, nil
}

func portRangeTestApp(api *fakePortRangeAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}}
}

func postPortRange(t *testing.T, app *App, handler func(http.ResponseWriter, *http.Request, string), serverID, action string, form map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/servers/"+serverID+"/ports/"+action, strings.NewReader(formEncode(form)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler(w, req, serverID)
	return w
}

func newPortRangeFixtures() *fakePortRangeAPIClient {
	return &fakePortRangeAPIClient{
		server: &manmanpb.Server{ServerId: 2, Name: "srv-2", Status: "online"},
		ranges: []*manmanpb.PortRange{},
	}
}

func TestHandleServerPortRangeSet_AddsRangeViaReplaceAll(t *testing.T) {
	api := newPortRangeFixtures()
	api.server.AllowedPortRanges = []*manmanpb.PortRange{{Start: 27015, End: 27020, Protocol: "UDP"}}
	api.ranges = api.server.AllowedPortRanges
	app := portRangeTestApp(api)

	w := postPortRange(t, app, app.handleServerPortRangeSet, "2", "set", map[string]string{"start": "25565", "end": "25570", "protocol": "TCP"})

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if len(api.updates) != 1 {
		t.Fatalf("UpdateServerAllowedPortRanges calls = %d, want 1", len(api.updates))
	}
	req := api.updates[0]
	if req.ServerId != 2 {
		t.Errorf("server_id = %d, want 2", req.ServerId)
	}
	if len(req.Ranges) != 2 {
		t.Fatalf("ranges = %d, want 2", len(req.Ranges))
	}
	// Original range preserved, new one appended (replace-all set semantics).
	if req.Ranges[0].Start != 27015 || req.Ranges[0].End != 27020 || req.Ranges[0].Protocol != "UDP" {
		t.Errorf("existing range not preserved: %+v", req.Ranges[0])
	}
	if req.Ranges[1].Start != 25565 || req.Ranges[1].End != 25570 || req.Ranges[1].Protocol != "TCP" {
		t.Errorf("new range wrong: %+v", req.Ranges[1])
	}
}

func TestHandleServerPortRangeSet_RejectsDuplicate(t *testing.T) {
	api := newPortRangeFixtures()
	api.server.AllowedPortRanges = []*manmanpb.PortRange{{Start: 25565, End: 25570, Protocol: "TCP"}}
	api.ranges = api.server.AllowedPortRanges
	app := portRangeTestApp(api)

	w := postPortRange(t, app, app.handleServerPortRangeSet, "2", "set", map[string]string{"start": "25565", "end": "25570", "protocol": "tcp"})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if len(api.updates) != 0 {
		t.Errorf("duplicate add reached the API; recorded writes = %d", len(api.updates))
	}
}

func TestHandleServerPortRangeSet_RejectsInvalidInput(t *testing.T) {
	api := newPortRangeFixtures()
	app := portRangeTestApp(api)

	for _, form := range []map[string]string{
		{"start": "0", "end": "100", "protocol": "TCP"},
		{"start": "65536", "end": "65536", "protocol": "TCP"},
		{"start": "100", "end": "50", "protocol": "TCP"},
		{"start": "10", "end": "20", "protocol": "ICMP"},
		{"start": "abc", "end": "20", "protocol": "TCP"},
	} {
		w := postPortRange(t, app, app.handleServerPortRangeSet, "2", "set", form)
		if w.Code != http.StatusBadRequest {
			t.Errorf("form %v: status = %d, want 400", form, w.Code)
		}
	}
	if len(api.updates) != 0 {
		t.Errorf("invalid input reached the API; recorded writes = %d", len(api.updates))
	}
}

func TestHandleServerPortRangeSet_EditsRangeInPlace(t *testing.T) {
	api := newPortRangeFixtures()
	api.server.AllowedPortRanges = []*manmanpb.PortRange{
		{Start: 25565, End: 25570, Protocol: "TCP"},
		{Start: 27015, End: 27020, Protocol: "UDP"},
	}
	api.ranges = api.server.AllowedPortRanges
	app := portRangeTestApp(api)

	w := postPortRange(t, app, app.handleServerPortRangeSet, "2", "set", map[string]string{
		"editing":    "true",
		"orig_start": "25565", "orig_end": "25570", "orig_protocol": "TCP",
		"start": "25565", "end": "25575", "protocol": "TCP",
	})

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if len(api.updates) != 1 {
		t.Fatalf("UpdateServerAllowedPortRanges calls = %d, want 1", len(api.updates))
	}
	req := api.updates[0]
	if len(req.Ranges) != 2 {
		t.Fatalf("ranges = %d, want 2", len(req.Ranges))
	}
	if req.Ranges[0].End != 25575 {
		t.Errorf("edited range end = %d, want 25575", req.Ranges[0].End)
	}
	if req.Ranges[1].Protocol != "UDP" {
		t.Errorf("untouched range changed: %+v", req.Ranges[1])
	}
}

func TestHandleServerPortRangeRemove_DropsOnlyTargetRange(t *testing.T) {
	api := newPortRangeFixtures()
	api.server.AllowedPortRanges = []*manmanpb.PortRange{
		{Start: 25565, End: 25570, Protocol: "TCP"},
		{Start: 27015, End: 27020, Protocol: "UDP"},
	}
	api.ranges = api.server.AllowedPortRanges
	app := portRangeTestApp(api)

	w := postPortRange(t, app, app.handleServerPortRangeRemove, "2", "remove", map[string]string{"start": "27015", "end": "27020", "protocol": "UDP"})

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if len(api.updates) != 1 {
		t.Fatalf("UpdateServerAllowedPortRanges calls = %d, want 1", len(api.updates))
	}
	req := api.updates[0]
	if len(req.Ranges) != 1 || req.Ranges[0].Protocol != "TCP" {
		t.Errorf("remaining ranges wrong: %+v", req.Ranges)
	}
}

func TestHandleServerPortRangeRemove_ToEmptySet_ClearsAll(t *testing.T) {
	api := newPortRangeFixtures()
	api.server.AllowedPortRanges = []*manmanpb.PortRange{{Start: 25565, End: 25570, Protocol: "TCP"}}
	api.ranges = api.server.AllowedPortRanges
	app := portRangeTestApp(api)

	w := postPortRange(t, app, app.handleServerPortRangeRemove, "2", "remove", map[string]string{"start": "25565", "end": "25570", "protocol": "TCP"})

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	req := api.updates[0]
	// Empty set is first-class: unconstrained assignment (SB-1.2).
	if len(req.Ranges) != 0 {
		t.Errorf("expected empty replacement set, got %+v", req.Ranges)
	}
}

func TestHandleServerPortRangeRemove_RejectsInvalidIdentity(t *testing.T) {
	api := newPortRangeFixtures()
	app := portRangeTestApp(api)

	w := postPortRange(t, app, app.handleServerPortRangeRemove, "2", "remove", map[string]string{"start": "99999", "end": "0", "protocol": "TCP"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if api.gets != 0 {
		t.Errorf("invalid remove fetched the server (%d gets)", api.gets)
	}
}
