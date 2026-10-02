package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

type fakeActionAPI struct {
	mu      sync.Mutex
	actions map[int64]*manmanpb.ActionDefinition
	next    int64
	writes  int
}

func (f *fakeActionAPI) ListActionDefinitions(_ context.Context, _ *manmanpb.ListActionDefinitionsRequest, _ ...grpc.CallOption) (*manmanpb.ListActionDefinitionsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := &manmanpb.ListActionDefinitionsResponse{}
	for _, a := range f.actions {
		r.Actions = append(r.Actions, a)
	}
	return r, nil
}

func (f *fakeActionAPI) GetActionDefinition(_ context.Context, in *manmanpb.GetActionDefinitionRequest, _ ...grpc.CallOption) (*manmanpb.GetActionDefinitionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.actions[in.ActionId]
	if !ok {
		return nil, status.Error(codes.NotFound, "nope")
	}
	return &manmanpb.GetActionDefinitionResponse{Action: a}, nil
}

func (f *fakeActionAPI) CreateActionDefinition(_ context.Context, in *manmanpb.CreateActionDefinitionRequest, _ ...grpc.CallOption) (*manmanpb.CreateActionDefinitionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	f.next++
	a := in.Action
	a.ActionId = f.next
	f.actions[a.ActionId] = a
	return &manmanpb.CreateActionDefinitionResponse{ActionId: a.ActionId}, nil
}

func (f *fakeActionAPI) UpdateActionDefinition(_ context.Context, in *manmanpb.UpdateActionDefinitionRequest, _ ...grpc.CallOption) (*manmanpb.UpdateActionDefinitionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	f.actions[in.Action.ActionId] = in.Action
	return &manmanpb.UpdateActionDefinitionResponse{}, nil
}

func (f *fakeActionAPI) DeleteActionDefinition(_ context.Context, in *manmanpb.DeleteActionDefinitionRequest, _ ...grpc.CallOption) (*manmanpb.DeleteActionDefinitionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	delete(f.actions, in.ActionId)
	return &manmanpb.DeleteActionDefinitionResponse{}, nil
}

func actionSession(t *testing.T, api ActionDefinitionAPI, role string) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "v0"}, nil)
	srv.AddReceivingMiddleware(Middleware(NewRegistry(ActionDefinitionTools(api)...), &recordingAuditor{}))
	AddActionDefinitionTools(srv, api, &Gate{Store: &MemoryConfirmationStore{}})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(HTTPAuth(fakeVerifier{"t": claims("u", role)}, "")(h))
	t.Cleanup(ts.Close)
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: &http.Client{Transport: bearerRT{"t"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type recordingAuditor struct {
	mu   sync.Mutex
	recs []AuditRecord
}

func (r *recordingAuditor) Record(_ context.Context, rec AuditRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, rec)
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s tool error: %+v", name, res.Content)
	}
	var out map[string]any
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		_ = json.Unmarshal([]byte(tc.Text), &out)
	}
	if out == nil {
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func newActionAPI() *fakeActionAPI {
	return &fakeActionAPI{actions: map[int64]*manmanpb.ActionDefinition{
		7: {ActionId: 7, DefinitionLevel: "game", EntityId: 1, Name: "save", Label: "Save", CommandTemplate: "save", Enabled: true},
	}}
}

func TestActionUpdateDiffPreviewAndConfirm(t *testing.T) {
	api := newActionAPI()
	s := actionSession(t, api, "server-manager")
	prev := callTool(t, s, "update_action_definition", map[string]any{"action_id": 7, "label": "Save Now", "command_template": "save-all"})
	if api.writes != 0 || api.actions[7].Label != "Save" {
		t.Fatal("preview mutated")
	}
	ch := prev["preview"].(map[string]any)["changes"].(map[string]any)
	if len(ch) != 2 {
		t.Fatalf("want 2 changed fields, got %v", ch)
	}
	lbl := ch["label"].(map[string]any)
	if lbl["current"] != "Save" || lbl["proposed"] != "Save Now" {
		t.Fatalf("bad diff %v", lbl)
	}
	out := callTool(t, s, "update_action_definition", map[string]any{"action_id": 7, "label": "Save Now", "command_template": "save-all", "confirmation_token": prev["confirmation_token"]})
	if out["applied"] != true || api.actions[7].Label != "Save Now" || api.actions[7].CommandTemplate != "save-all" {
		t.Fatalf("not applied: %v %+v", out, api.actions[7])
	}
}

func TestActionCreateAndDelete(t *testing.T) {
	api := newActionAPI()
	s := actionSession(t, api, "server-manager")
	ca := map[string]any{"definition_level": "game", "entity_id": 1, "name": "kick", "label": "Kick", "command_template": "kick {{.p}}",
		"parameters": []any{map[string]any{"name": "p", "label": "Player", "field_type": "text"}}}
	prev := callTool(t, s, "create_action_definition", ca)
	if api.writes != 0 || len(api.actions) != 1 {
		t.Fatal("unconfirmed create mutated")
	}
	ca["confirmation_token"] = prev["confirmation_token"]
	callTool(t, s, "create_action_definition", ca)
	if api.writes != 1 || len(api.actions) != 2 || api.actions[1].Name != "kick" {
		t.Fatalf("create not applied: %+v", api.actions)
	}

	prev = callTool(t, s, "delete_action_definition", map[string]any{"action_id": 7})
	if len(api.actions) != 2 {
		t.Fatal("unconfirmed delete mutated")
	}
	callTool(t, s, "delete_action_definition", map[string]any{"action_id": 7, "confirmation_token": prev["confirmation_token"]})
	if _, ok := api.actions[7]; ok {
		t.Fatal("delete not applied")
	}
}

func TestActionUnknownIDAndPersona(t *testing.T) {
	api := newActionAPI()
	s := actionSession(t, api, "server-manager")
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_action_definition", Arguments: map[string]any{"action_id": 99}})
	if err == nil && !res.IsError {
		t.Fatal("unknown id should error")
	}
	g := actionSession(t, api, "gamer")
	if res, err := g.CallTool(context.Background(), &mcp.CallToolParams{Name: "delete_action_definition", Arguments: map[string]any{"action_id": 7}}); err == nil && !res.IsError {
		t.Fatal("gamer must be refused")
	}
	if api.writes != 0 {
		t.Fatal("mutated")
	}
}
