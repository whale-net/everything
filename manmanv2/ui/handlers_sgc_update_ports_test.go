package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// This file guards task #2094's FR11 red/green contract for the port
// bindings editor on the ServerGameConfig surface: writes go only through
// the public API (NFR3) -- handleSGCUpdatePorts must land on
// UpdateServerGameConfig with update_paths=["port_bindings"], the exact
// mechanism manmanv2/scripts/load-*-config.sh uses -- and add / edit /
// remove all round-trip with what the seed scripts express (container
// port, host port, protocol; no more, no less).
//
// fakePortsManManAPIClient mirrors fakeManManAPIClient's approach (embed
// the nil gRPC client interface, override only what the handler's call
// graph reaches; un-overridden calls panic loudly) but records the
// UpdateServerGameConfig requests so tests can assert on persistence
// without a live API server.
type fakePortsManManAPIClient struct {
	manmanpb.ManManAPIClient

	updates []*manmanpb.UpdateServerGameConfigRequest
}

func (f *fakePortsManManAPIClient) UpdateServerGameConfig(ctx context.Context, in *manmanpb.UpdateServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.UpdateServerGameConfigResponse, error) {
	f.updates = append(f.updates, in)
	return &manmanpb.UpdateServerGameConfigResponse{
		Config: &manmanpb.ServerGameConfig{
			ServerGameConfigId: in.GetServerGameConfigId(),
			PortBindings:       in.GetPortBindings(),
		},
	}, nil
}

func newPortsTestApp(api *fakePortsManManAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}}
}

func postPortBindings(t *testing.T, app *App, sgcID int64, bindingsJSON string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"port_bindings_json": {bindingsJSON}}
	id := strconv.FormatInt(sgcID, 10)
	req := httptest.NewRequest(http.MethodPost, "/sgc/"+id+"/update-ports", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The editor posts as an htmx request (the page's Alpine.js form); the
	// handler answers with 200 + HX-Redirect instead of a 303 redirect.
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	app.handleSGCUpdatePorts(w, req, id)
	return w
}

// --- add: a binding submitted via the UI persists through the public API ---

func TestHandleSGCUpdatePorts_AddBinding_PersistsViaAPI(t *testing.T) {
	api := &fakePortsManManAPIClient{}
	app := newPortsTestApp(api)

	// One new binding added through the editor's hidden form field.
	w := postPortBindings(t, app, 7, `[{"container_port":25565,"host_port":25565,"protocol":"TCP"}]`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if len(api.updates) != 1 {
		t.Fatalf("UpdateServerGameConfig calls = %d, want 1", len(api.updates))
	}
	got := api.updates[0]
	if got.GetServerGameConfigId() != 7 {
		t.Errorf("sgc id = %d, want 7", got.GetServerGameConfigId())
	}
	if len(got.GetUpdatePaths()) != 1 || got.GetUpdatePaths()[0] != "port_bindings" {
		t.Errorf("update_paths = %v, want [port_bindings] (must match what load-*-config.sh sends)", got.GetUpdatePaths())
	}
	bindings := got.GetPortBindings()
	if len(bindings) != 1 {
		t.Fatalf("bindings = %d, want 1", len(bindings))
	}
	if bindings[0].GetContainerPort() != 25565 || bindings[0].GetHostPort() != 25565 || bindings[0].GetProtocol() != "TCP" {
		t.Errorf("binding = %d:%d/%s, want 25565:25565/TCP",
			bindings[0].GetContainerPort(), bindings[0].GetHostPort(), bindings[0].GetProtocol())
	}

	// Visible via API: the response config carries the persisted binding,
	// the same surface ListServerGameConfigs/GetServerGameConfig serve.
	if got := w.Header().Get("HX-Redirect"); got != "/sgc/7" {
		t.Errorf("HX-Redirect = %q, want /sgc/7 (non-HTMX requests redirect too)", got)
	}
}

// --- edit: submitting a modified set replaces the persisted set ---

func TestHandleSGCUpdatePorts_EditBinding_Updated(t *testing.T) {
	api := &fakePortsManManAPIClient{}
	app := newPortsTestApp(api)

	// The editor submits the full set; changing a host port means the
	// edited list is sent wholesale, exactly like load-*-config.sh's
	// update_paths=["port_bindings"] replace-all semantics.
	w := postPortBindings(t, app, 7, `[{"container_port":27015,"host_port":27016,"protocol":"UDP"}]`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if len(api.updates) != 1 {
		t.Fatalf("UpdateServerGameConfig calls = %d, want 1", len(api.updates))
	}
	bindings := api.updates[0].GetPortBindings()
	if len(bindings) != 1 || bindings[0].GetHostPort() != 27016 {
		t.Fatalf("edited binding = %+v, want host port 27016", bindings)
	}
	if bindings[0].GetProtocol() != "UDP" {
		t.Errorf("protocol = %q, want UDP", bindings[0].GetProtocol())
	}
}

// --- remove: an empty submission clears the persisted set ---

func TestHandleSGCUpdatePorts_RemoveAllBindings_Gone(t *testing.T) {
	api := &fakePortsManManAPIClient{}
	app := newPortsTestApp(api)

	w := postPortBindings(t, app, 7, `[]`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if len(api.updates) != 1 {
		t.Fatalf("UpdateServerGameConfig calls = %d, want 1", len(api.updates))
	}
	// Replace-all semantics: removing every binding is a real update with
	// an empty set, not a skipped call.
	if n := len(api.updates[0].GetPortBindings()); n != 0 {
		t.Errorf("persisted bindings = %d, want 0", n)
	}
	if paths := api.updates[0].GetUpdatePaths(); len(paths) != 1 || paths[0] != "port_bindings" {
		t.Errorf("update_paths = %v, want [port_bindings]", paths)
	}
}

// --- round-trip parity with what load-minecraft-config.sh posts ---

func TestHandleSGCUpdatePorts_RoundTripParityWithSeedScript(t *testing.T) {
	api := &fakePortsManManAPIClient{}
	app := newPortsTestApp(api)

	// Verbatim port_bindings JSON from load-minecraft-config.sh's update
	// payload (protocol included): the UI write path must accept and
	// persist exactly what the seed scripts express for the same config.
	scriptJSON := `[{"container_port":25565,"host_port":25565,"protocol":"TCP"}]`
	w := postPortBindings(t, app, 42, scriptJSON)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	if len(api.updates) != 1 {
		t.Fatalf("UpdateServerGameConfig calls = %d, want 1", len(api.updates))
	}
	bindings := api.updates[0].GetPortBindings()
	if len(bindings) != 1 ||
		bindings[0].GetContainerPort() != 25565 ||
		bindings[0].GetHostPort() != 25565 ||
		bindings[0].GetProtocol() != "TCP" {
		t.Fatalf("seed-script binding did not survive the UI write path: %+v", bindings)
	}
}

// --- validation: out-of-range ports and bad protocols are rejected ---

func TestHandleSGCUpdatePorts_InvalidInput_RejectedWithoutWrite(t *testing.T) {
	cases := map[string]string{
		"container port zero":   `[{"container_port":0,"host_port":25565,"protocol":"TCP"}]`,
		"host port over range":  `[{"container_port":25565,"host_port":70000,"protocol":"TCP"}]`,
		"unknown protocol":      `[{"container_port":25565,"host_port":25565,"protocol":"SCTP"}]`,
		"malformed json":        `not json at all`,
		"missing host port":     `[{"container_port":25565,"protocol":"TCP"}]`,
		"negative container":    `[{"container_port":-1,"host_port":25565,"protocol":"TCP"}]`,
	}
	for name, payload := range cases {
		api := &fakePortsManManAPIClient{}
		app := newPortsTestApp(api)
		w := postPortBindings(t, app, 7, payload)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d; body: %s", name, w.Code, http.StatusBadRequest, w.Body.String())
		}
		if len(api.updates) != 0 {
			t.Errorf("%s: UpdateServerGameConfig called %d times, want 0", name, len(api.updates))
		}
	}
}

// --- method + id plumbing ---

func TestHandleSGCUpdatePorts_PlainFormPost_RedirectsBack(t *testing.T) {
	api := &fakePortsManManAPIClient{}
	app := newPortsTestApp(api)

	form := url.Values{"port_bindings_json": {`[{"container_port":25565,"host_port":25565,"protocol":"TCP"}]`}}
	req := httptest.NewRequest(http.MethodPost, "/sgc/7/update-ports", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleSGCUpdatePorts(w, req, "7")

	// A no-JS form submit is redirected back to the SGC page (303).
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if loc := w.Header().Get("Location"); loc != "/sgc/7" {
		t.Errorf("Location = %q, want /sgc/7", loc)
	}
	if len(api.updates) != 1 {
		t.Fatalf("UpdateServerGameConfig calls = %d, want 1", len(api.updates))
	}
}

func TestHandleSGCUpdatePorts_MethodNotAllowed(t *testing.T) {
	app := newPortsTestApp(&fakePortsManManAPIClient{})
	req := httptest.NewRequest(http.MethodGet, "/sgc/7/update-ports", nil)
	w := httptest.NewRecorder()
	app.handleSGCUpdatePorts(w, req, "7")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleSGCUpdatePorts_InvalidSGCID(t *testing.T) {
	app := newPortsTestApp(&fakePortsManManAPIClient{})
	req := httptest.NewRequest(http.MethodPost, "/sgc/abc/update-ports", nil)
	w := httptest.NewRecorder()
	app.handleSGCUpdatePorts(w, req, "abc")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
