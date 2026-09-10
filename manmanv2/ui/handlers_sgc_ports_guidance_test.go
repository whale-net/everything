package main

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/pages"
	"google.golang.org/grpc"
)

// This file guards task #2098's FR13/FR14 guidance contract for the ports
// editor: random generation (FR13) picks a host port inside the server's
// configured ranges that is neither currently allocated nor saved in
// another deployment's configuration on that server, and inline warnings
// (FR14) render in-use/out-of-range guidance at edit time WITHOUT ever
// rejecting a save (Decision 7: the API keeps today's save-time
// semantics; session-start allocation remains the backstop). All data
// comes through the public API (NFR3): GetServer's allowed_port_ranges,
// ListAllocatedPorts, and sibling SGCs' port_bindings via
// ListServerGameConfigs.
//
// mutation-tested (verified red, by hand, then reverted): disabling the
// sibling-bindings loop in buildSGCPortContext made
// TestBuildSGCPortContext_IncludesAllocatedAndSiblingSaved fail; dropping
// the used-port filter from pickRandomAvailablePort's candidate scan made
// TestPickRandomAvailablePort_AvoidsUsedAndStaysInRange and
// TestPickRandomAvailablePort_AllTaken_Errors fail; reverting restored
// green in both cases.

type fakeGuidanceManManAPIClient struct {
	manmanpb.ManManAPIClient

	server       *manmanpb.Server
	allocated    []*manmanpb.AllocatedPort
	sgcs         []*manmanpb.ServerGameConfig
	sgcByID      map[int64]*manmanpb.ServerGameConfig
	listAllocReq []*manmanpb.ListAllocatedPortsRequest

	// updateSGCErr, updateSGCReqs: task #2275's deployment-settings ports
	// Save tests (handlers_deployment_settings_test.go) reuse this same
	// fixture rather than standing up a second one -- see that file's doc
	// comment on why. updateSGCErr forces the failure branch (a backend
	// save-time rejection); nil (the zero value) leaves the shipped
	// UpdateServerGameConfig write path succeeding, mirroring
	// fakePortsManManAPIClient (handlers_sgc_update_ports_test.go).
	updateSGCErr  error
	updateSGCReqs []*manmanpb.UpdateServerGameConfigRequest
}

func (f *fakeGuidanceManManAPIClient) GetServer(ctx context.Context, in *manmanpb.GetServerRequest, opts ...grpc.CallOption) (*manmanpb.GetServerResponse, error) {
	if f.server == nil || f.server.GetServerId() != in.GetServerId() {
		return nil, errors.New("not found")
	}
	return &manmanpb.GetServerResponse{Server: f.server}, nil
}

func (f *fakeGuidanceManManAPIClient) ListAllocatedPorts(ctx context.Context, in *manmanpb.ListAllocatedPortsRequest, opts ...grpc.CallOption) (*manmanpb.ListAllocatedPortsResponse, error) {
	f.listAllocReq = append(f.listAllocReq, in)
	return &manmanpb.ListAllocatedPortsResponse{Ports: f.allocated}, nil
}

func (f *fakeGuidanceManManAPIClient) ListServerGameConfigs(ctx context.Context, in *manmanpb.ListServerGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error) {
	var out []*manmanpb.ServerGameConfig
	for _, sgc := range f.sgcs {
		if in.GetServerId() == 0 || sgc.GetServerId() == in.GetServerId() {
			out = append(out, sgc)
		}
	}
	return &manmanpb.ListServerGameConfigsResponse{Configs: out}, nil
}

func (f *fakeGuidanceManManAPIClient) GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	if sgc, ok := f.sgcByID[in.GetServerGameConfigId()]; ok {
		return &manmanpb.GetServerGameConfigResponse{Config: sgc}, nil
	}
	return nil, errors.New("not found")
}

// UpdateServerGameConfig records the request (so tests can assert on the
// shipped write path's exact shape -- update_paths, port_bindings) and,
// absent updateSGCErr, mirrors fakePortsManManAPIClient's success
// behavior: echo back a config carrying whatever bindings were submitted.
func (f *fakeGuidanceManManAPIClient) UpdateServerGameConfig(ctx context.Context, in *manmanpb.UpdateServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.UpdateServerGameConfigResponse, error) {
	f.updateSGCReqs = append(f.updateSGCReqs, in)
	if f.updateSGCErr != nil {
		return nil, f.updateSGCErr
	}
	return &manmanpb.UpdateServerGameConfigResponse{
		Config: &manmanpb.ServerGameConfig{
			ServerGameConfigId: in.GetServerGameConfigId(),
			PortBindings:       in.GetPortBindings(),
		},
	}, nil
}

func guidanceServer() *manmanpb.Server {
	return &manmanpb.Server{
		ServerId: 7,
		Name:     "srv-7",
		AllowedPortRanges: []*manmanpb.PortRange{
			{Start: 25565, End: 25570, Protocol: "TCP"},
			{Start: 27015, End: 27020, Protocol: "UDP"},
		},
	}
}

func newGuidanceAPI() *fakeGuidanceManManAPIClient {
	return &fakeGuidanceManManAPIClient{
		server: guidanceServer(),
		sgcByID: map[int64]*manmanpb.ServerGameConfig{
			7: {ServerGameConfigId: 7, ServerId: 7, PortBindings: []*manmanpb.PortBinding{
				{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
			}},
			8: {ServerGameConfigId: 8, ServerId: 7, PortBindings: []*manmanpb.PortBinding{
				{ContainerPort: 30000, HostPort: 30000, Protocol: "TCP"},
			}},
			9: {ServerGameConfigId: 9, ServerId: 99, PortBindings: []*manmanpb.PortBinding{
				{ContainerPort: 1, HostPort: 31000, Protocol: "TCP"},
			}},
		},
		sgcs: []*manmanpb.ServerGameConfig{
			{ServerGameConfigId: 7, ServerId: 7, PortBindings: []*manmanpb.PortBinding{
				{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
			}},
			{ServerGameConfigId: 8, ServerId: 7, PortBindings: []*manmanpb.PortBinding{
				{ContainerPort: 30000, HostPort: 30000, Protocol: "TCP"},
			}},
			// Different server entirely: must never leak into server 7's context.
			{ServerGameConfigId: 9, ServerId: 99, PortBindings: []*manmanpb.PortBinding{
				{ContainerPort: 1, HostPort: 31000, Protocol: "TCP"},
			}},
		},
		allocated: []*manmanpb.AllocatedPort{
			{ServerId: 7, Port: 25566, Protocol: "TCP", SessionId: 101},
			{ServerId: 7, Port: 27016, Protocol: "UDP"},
		},
	}
}

// --- FR14: inline warnings ---

func TestPortWarning_AllocatedPort_InUse(t *testing.T) {
	ctx := &pages.SGCPortContext{
		Ranges: map[string][]*manmanpb.PortRange{"TCP": {{Start: 25565, End: 25570, Protocol: "TCP"}}},
		InUse:  map[string][]int32{"TCP": {25566}},
	}
	got := portWarning(ctx, "TCP", 25566)
	if !strings.Contains(got, "already in use") {
		t.Errorf("portWarning(allocated) = %q, want in-use warning", got)
	}
}

func TestPortWarning_OutsideRanges_Warns(t *testing.T) {
	ctx := &pages.SGCPortContext{
		Ranges: map[string][]*manmanpb.PortRange{"TCP": {{Start: 25565, End: 25570, Protocol: "TCP"}}},
		InUse:  map[string][]int32{"TCP": {}},
	}
	got := portWarning(ctx, "TCP", 30000)
	if !strings.Contains(got, "outside the configured allowed ranges") {
		t.Errorf("portWarning(out of range) = %q, want range warning", got)
	}
	if got := portWarning(ctx, "TCP", 25568); got != "" {
		t.Errorf("portWarning(in range, unused) = %q, want no warning", got)
	}
}

// SB-1.2: with no ranges configured for the protocol, only the in-use
// warning applies.
func TestPortWarning_NoRanges_OnlyInUseApplies(t *testing.T) {
	ctx := &pages.SGCPortContext{
		Ranges: map[string][]*manmanpb.PortRange{},
		InUse:  map[string][]int32{"TCP": {}},
	}
	if got := portWarning(ctx, "TCP", 30000); got != "" {
		t.Errorf("portWarning(rangeless server) = %q, want no warning", got)
	}
}

// --- FR13 context builder ---

func TestBuildSGCPortContext_IncludesAllocatedAndSiblingSaved(t *testing.T) {
	api := newGuidanceAPI()
	ctx := buildSGCPortContext(t.Context(), api, 7, 7)

	// Ranges ride through from GetServer.
	if len(ctx.Ranges["TCP"]) != 1 || ctx.Ranges["TCP"][0].GetStart() != 25565 {
		t.Errorf("TCP ranges = %+v, want [{25565 25570 TCP}]", ctx.Ranges["TCP"])
	}

	// Allocated: 25566/TCP and 27016/UDP.
	hasPort := func(proto string, port int32) bool {
		for _, p := range ctx.InUse[proto] {
			if p == port {
				return true
			}
		}
		return false
	}
	if !hasPort("TCP", 25566) || !hasPort("UDP", 27016) {
		t.Errorf("InUse missing allocated ports: %+v", ctx.InUse)
	}

	// Sibling-saved: deployment 8's 30000/TCP on the same server.
	if !hasPort("TCP", 30000) {
		t.Errorf("InUse missing sibling-saved port 30000/TCP: %+v", ctx.InUse)
	}

	// The edited SGC's OWN saved binding (25565) must be excluded, and a
	// different server's saved port (31000) must never leak in.
	if hasPort("TCP", 25565) {
		t.Errorf("InUse includes the edited SGC's own saved binding: %+v", ctx.InUse)
	}
	if hasPort("TCP", 31000) {
		t.Errorf("InUse includes another server's saved binding: %+v", ctx.InUse)
	}

	// Allocations were requested for the right server.
	if len(api.listAllocReq) != 1 || api.listAllocReq[0].GetServerId() != 7 {
		t.Errorf("ListAllocatedPorts requests = %+v, want one for server 7", api.listAllocReq)
	}
}

// --- FR13: random pick ---

func TestPickRandomAvailablePort_AvoidsUsedAndStaysInRange(t *testing.T) {
	ctx := &pages.SGCPortContext{
		Ranges: map[string][]*manmanpb.PortRange{"TCP": {{Start: 25565, End: 25570, Protocol: "TCP"}}},
		InUse:  map[string][]int32{"TCP": {25565, 25566, 25567, 25568}},
	}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 50; i++ {
		got, err := pickRandomAvailablePort(rng, ctx, "TCP")
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if got != 25569 && got != 25570 {
			t.Fatalf("pick = %d, want one of the only free ports (25569/25570)", got)
		}
	}
}

func TestPickRandomAvailablePort_Deterministic(t *testing.T) {
	ctx := &pages.SGCPortContext{
		Ranges: map[string][]*manmanpb.PortRange{"TCP": {{Start: 1000, End: 1100, Protocol: "TCP"}}},
		InUse:  map[string][]int32{"TCP": {}},
	}
	a, err := pickRandomAvailablePort(rand.New(rand.NewSource(7)), ctx, "TCP")
	if err != nil {
		t.Fatal(err)
	}
	b, err := pickRandomAvailablePort(rand.New(rand.NewSource(7)), ctx, "TCP")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("same seed gave %d and %d; pick must be deterministic under an injected rng", a, b)
	}
}

func TestPickRandomAvailablePort_NoRanges_IsNoOp(t *testing.T) {
	ctx := &pages.SGCPortContext{Ranges: map[string][]*manmanpb.PortRange{}, InUse: map[string][]int32{}}
	if _, err := pickRandomAvailablePort(rand.New(rand.NewSource(1)), ctx, "TCP"); err == nil {
		t.Error("random pick on a range-less server must be a no-op (error), not an unconstrained pick")
	}
}

func TestPickRandomAvailablePort_AllTaken_Errors(t *testing.T) {
	ctx := &pages.SGCPortContext{
		Ranges: map[string][]*manmanpb.PortRange{"TCP": {{Start: 25565, End: 25566, Protocol: "TCP"}}},
		InUse:  map[string][]int32{"TCP": {25565, 25566}},
	}
	if _, err := pickRandomAvailablePort(rand.New(rand.NewSource(1)), ctx, "TCP"); err == nil {
		t.Error("pick with every in-range port taken must error legibly")
	}
}

// --- handler: GET /sgc/{id}/random-port ---

func get(t *testing.T, app *App, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	app.handleSGCRandomPort(w, req, strconv.Itoa(7))
	return w
}

func TestHandleSGCRandomPort_ReturnsFreeInRangePort(t *testing.T) {
	api := newGuidanceAPI()
	app := &App{grpc: &ControlClient{api: api}}

	w := get(t, app, "/sgc/7/random-port?protocol=TCP")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var body struct {
		Port int32 `json:"port"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON {port}: %v (%s)", err, w.Body.String())
	}
	// Contract: the pick lands inside the server's configured TCP range
	// (25565-25570), avoids ports currently allocated on the server
	// (25566), and avoids sibling deployments' saved bindings (30000).
	// The edited SGC's own saved binding (25565) is intentionally
	// eligible: the build excludes it from the in-use set (editing your
	// own bindings must not flag them as taken), so re-drawing your own
	// port is a valid no-op pick.
	if body.Port < 25565 || body.Port > 25570 {
		t.Errorf("port = %d, want one inside the configured TCP range", body.Port)
	}
	if body.Port == 25566 || body.Port == 30000 {
		t.Errorf("port = %d, want a port not allocated (25566) and not sibling-saved (30000)", body.Port)
	}
}

func TestHandleSGCRandomPort_NoRanges_Conflict(t *testing.T) {
	api := newGuidanceAPI()
	api.server.AllowedPortRanges = nil
	app := &App{grpc: &ControlClient{api: api}}

	w := get(t, app, "/sgc/7/random-port?protocol=TCP")
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (guidance-grade, never a save blocker); body: %s", w.Code, http.StatusConflict, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || !strings.Contains(body["error"], "no allowed port ranges") {
		t.Errorf("body = %s, want legible no-ranges error", w.Body.String())
	}
}

func TestHandleSGCRandomPort_BadProtocol_BadRequest(t *testing.T) {
	app := &App{grpc: &ControlClient{api: newGuidanceAPI()}}
	req := httptest.NewRequest(http.MethodGet, "/sgc/7/random-port?protocol=ICMP", nil)
	w := httptest.NewRecorder()
	app.handleSGCRandomPort(w, req, "7")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleSGCRandomPort_UnknownSGC_NotFound(t *testing.T) {
	app := &App{grpc: &ControlClient{api: newGuidanceAPI()}}
	req := httptest.NewRequest(http.MethodGet, "/sgc/999/random-port?protocol=TCP", nil)
	w := httptest.NewRecorder()
	app.handleSGCRandomPort(w, req, "999")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}
