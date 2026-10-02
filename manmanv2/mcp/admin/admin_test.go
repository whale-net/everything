package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/manmanv2/mcp/server"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

type fakeAPI struct {
	API
	mu      sync.Mutex
	writes  int
	game    *manmanpb.Game
	drain   string
	deleted bool
}

func (f *fakeAPI) GetGame(context.Context, *manmanpb.GetGameRequest, ...grpc.CallOption) (*manmanpb.GetGameResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &manmanpb.GetGameResponse{Game: f.game}, nil
}
func (f *fakeAPI) CreateGame(_ context.Context, in *manmanpb.CreateGameRequest, _ ...grpc.CallOption) (*manmanpb.CreateGameResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	return &manmanpb.CreateGameResponse{Game: &manmanpb.Game{GameId: 9, Name: in.Name}}, nil
}
func (f *fakeAPI) UpdateGame(_ context.Context, in *manmanpb.UpdateGameRequest, _ ...grpc.CallOption) (*manmanpb.UpdateGameResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	f.game.Name = in.Name
	return &manmanpb.UpdateGameResponse{Game: f.game}, nil
}
func (f *fakeAPI) DeleteGame(context.Context, *manmanpb.DeleteGameRequest, ...grpc.CallOption) (*manmanpb.DeleteGameResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	f.deleted = true
	return &manmanpb.DeleteGameResponse{}, nil
}
func (f *fakeAPI) ListGameConfigs(context.Context, *manmanpb.ListGameConfigsRequest, ...grpc.CallOption) (*manmanpb.ListGameConfigsResponse, error) {
	return &manmanpb.ListGameConfigsResponse{Configs: []*manmanpb.GameConfig{{ConfigId: 5, Name: "vanilla"}}}, nil
}
func (f *fakeAPI) GetServer(context.Context, *manmanpb.GetServerRequest, ...grpc.CallOption) (*manmanpb.GetServerResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &manmanpb.GetServerResponse{Server: &manmanpb.Server{ServerId: 1, Name: "dev-host", DrainState: f.drain}}, nil
}
func (f *fakeAPI) ListSessions(context.Context, *manmanpb.ListSessionsRequest, ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	return &manmanpb.ListSessionsResponse{Sessions: []*manmanpb.Session{{SessionId: 3, ServerGameConfigId: 7, Status: "running"}}}, nil
}
func (f *fakeAPI) DrainServer(context.Context, *manmanpb.DrainServerRequest, ...grpc.CallOption) (*manmanpb.DrainServerResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	f.drain = "draining"
	return &manmanpb.DrainServerResponse{Server: &manmanpb.Server{DrainState: "draining"}}, nil
}
func (f *fakeAPI) UndrainServer(context.Context, *manmanpb.UndrainServerRequest, ...grpc.CallOption) (*manmanpb.UndrainServerResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	f.drain = "schedulable"
	return &manmanpb.UndrainServerResponse{Server: &manmanpb.Server{DrainState: "schedulable"}}, nil
}

type verifier map[string]*grpcauth.Claims

func (v verifier) Verify(_ context.Context, t string) (*grpcauth.Claims, error) {
	if c, ok := v[t]; ok {
		return c, nil
	}
	return nil, context.Canceled
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func setup(t *testing.T) (*fakeAPI, func(token string) *mcp.ClientSession) {
	t.Helper()
	api := &fakeAPI{game: &manmanpb.Game{GameId: 2, Name: "Old", Metadata: &manmanpb.GameMetadata{Genre: "rpg"}}, drain: "schedulable"}
	reg := server.NewRegistry(append([]server.Tool{server.WhoamiTool}, Tools(api)...)...)
	srv := server.NewServer(reg, server.LogAuditor{Logger: nopLogger()}, &server.MemIdempotencyStore{})
	Register(srv, api, &server.Gate{Store: &server.MemoryConfirmationStore{}})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(server.HTTPAuth(verifier{
		"adm": {Subject: "a", Roles: []string{"admin"}},
		"mgr": {Subject: "m", Roles: []string{"server-manager"}},
	}, "")(h))
	t.Cleanup(ts.Close)
	return api, func(token string) *mcp.ClientSession {
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, nil).Connect(context.Background(),
			&mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: &http.Client{Transport: bearer{token}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cs.Close() })
		return cs
	}
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (map[string]any, error) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, err
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if res.IsError {
		t.Fatalf("tool error: %s", text)
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("bad result %q: %v", text, err)
	}
	return out, nil
}

// confirm previews then confirms; returns the preview and applied outcomes.
func confirm(t *testing.T, api *fakeAPI, cs *mcp.ClientSession, tool string, args map[string]any) (prev, done map[string]any) {
	t.Helper()
	before := api.writes
	prev, _ = call(t, cs, tool, args)
	tok, _ := prev["confirmation_token"].(string)
	if tok == "" || prev["applied"] == true || api.writes != before {
		t.Fatalf("%s preview must not mutate and must issue a token: %v", tool, prev)
	}
	c := map[string]any{"confirmation_token": tok}
	for k, v := range args {
		c[k] = v
	}
	done, _ = call(t, cs, tool, c)
	if done["applied"] != true || api.writes != before+1 {
		t.Fatalf("%s confirm did not apply once: %v", tool, done)
	}
	return prev, done
}

func TestServerManagerRefusedAndHidden(t *testing.T) {
	api, connect := setup(t)
	cs := connect("mgr")
	lr, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range lr.Tools {
		if strings.HasSuffix(tl.Name, "_game") || strings.HasSuffix(tl.Name, "drain_server") {
			t.Errorf("tool %s visible to server-manager", tl.Name)
		}
	}
	if _, err := call(t, cs, DrainServer, map[string]any{"server_id": 1}); err == nil {
		t.Fatal("server-manager drain must be refused")
	}
	if api.writes != 0 || api.drain != "schedulable" {
		t.Fatal("refused call mutated")
	}
}

func TestAdminSeesTools(t *testing.T) {
	_, connect := setup(t)
	lr, err := connect("adm").ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(lr.Tools) != 6 { // whoami + five admin tools
		t.Fatalf("tools = %d", len(lr.Tools))
	}
}

func TestUpdateGamePreviewDiffAndConfirm(t *testing.T) {
	api, connect := setup(t)
	prev, _ := confirm(t, api, connect("adm"), UpdateGame, map[string]any{"game_id": 2, "name": "New", "genre": "rpg"})
	diffs := prev["preview"].(map[string]any)["diffs"].([]any)
	if len(diffs) != 1 || diffs[0].(map[string]any)["field"] != "name" {
		t.Fatalf("diffs = %v", diffs)
	}
	if api.game.Name != "New" {
		t.Fatal("not updated")
	}
}

func TestDeletePreviewNamesConfigs(t *testing.T) {
	api, connect := setup(t)
	prev, _ := confirm(t, api, connect("adm"), DeleteGame, map[string]any{"game_id": 2})
	if !strings.Contains(mustJSON(prev), "vanilla") || !api.deleted {
		t.Fatalf("preview %v deleted=%v", prev, api.deleted)
	}
}

func TestCreateGame(t *testing.T) {
	api, connect := setup(t)
	confirm(t, api, connect("adm"), CreateGame, map[string]any{"name": "Valheim"})
}

func TestDrainUndrainNamesHostAndDeployments(t *testing.T) {
	api, connect := setup(t)
	cs := connect("adm")
	prev, _ := confirm(t, api, cs, DrainServer, map[string]any{"server_id": 1})
	s := mustJSON(prev)
	if !strings.Contains(s, "dev-host") || !strings.Contains(s, `"deployment_id":7`) || api.drain != "draining" {
		t.Fatalf("preview %s drain=%s", s, api.drain)
	}
	confirm(t, api, cs, UndrainServer, map[string]any{"server_id": 1})
	if api.drain != "schedulable" {
		t.Fatal("not undrained")
	}
}

func TestIdempotentRetryAppliesOnce(t *testing.T) {
	api, connect := setup(t)
	cs := connect("adm")
	// The key rides only on the confirmed call: a keyed preview would be stored as that key's result.
	prev, _ := call(t, cs, DrainServer, map[string]any{"server_id": 1})
	c := map[string]any{"server_id": 1, "idempotency_key": "k1", "confirmation_token": prev["confirmation_token"]}
	call(t, cs, DrainServer, c)
	call(t, cs, DrainServer, c)
	if api.writes != 1 {
		t.Fatalf("writes = %d, want 1", api.writes)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
