package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

type fakeActionsAPI struct {
	mu       sync.Mutex
	status   string
	executed []*manmanpb.ExecuteActionRequest
	fail     bool
}

func (f *fakeActionsAPI) GetSession(_ context.Context, in *manmanpb.GetSessionRequest, _ ...grpc.CallOption) (*manmanpb.GetSessionResponse, error) {
	return &manmanpb.GetSessionResponse{Session: &manmanpb.Session{SessionId: in.SessionId, ServerGameConfigId: 7, Status: f.status}}, nil
}

func (f *fakeActionsAPI) ListSessions(_ context.Context, _ *manmanpb.ListSessionsRequest, _ ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	return &manmanpb.ListSessionsResponse{Sessions: []*manmanpb.Session{{SessionId: 11, ServerGameConfigId: 7, Status: f.status}}}, nil
}

func (f *fakeActionsAPI) GetSessionActions(context.Context, *manmanpb.GetSessionActionsRequest, ...grpc.CallOption) (*manmanpb.GetSessionActionsResponse, error) {
	return &manmanpb.GetSessionActionsResponse{Actions: []*manmanpb.ActionDefinition{
		{ActionId: 5, Name: "say", InputFields: []*manmanpb.ActionInputField{{Name: "msg", Required: true}}},
		{ActionId: 6, Name: "save"},
	}}, nil
}

func (f *fakeActionsAPI) ExecuteAction(_ context.Context, in *manmanpb.ExecuteActionRequest, _ ...grpc.CallOption) (*manmanpb.ExecuteActionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.executed = append(f.executed, in)
	return &manmanpb.ExecuteActionResponse{Success: true, ExecutionId: int64(100 + len(f.executed)), RenderedCommand: "say hi"}, nil
}

type fakeAllow map[string]bool

func (a fakeAllow) Allowed(_ context.Context, dep int64, name string) (bool, error) {
	return a[name], nil
}

func actionsSession(t *testing.T, api *fakeActionsAPI, allow ActionAllowlist, token string) *mcp.ClientSession {
	t.Helper()
	reg := NewRegistry(SessionActionTools...)
	srv := NewServer(reg, LogAuditor{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, &MemIdempotencyStore{})
	AddSessionActionTools(srv, api, allow)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(HTTPAuth(fakeVerifier{
		"gamer": claims("g", "gamer"),
		"mgr":   claims("m", "server-manager"),
	}, "")(h))
	t.Cleanup(ts.Close)
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: &http.Client{Transport: bearerRT{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func execAction(s *mcp.ClientSession, args map[string]any) (*mcp.CallToolResult, error) {
	return s.CallTool(context.Background(), &mcp.CallToolParams{Name: "execute_action", Arguments: args})
}

// failed reports whether the call errored, returning the error text.
func failed(res *mcp.CallToolResult, err error) (string, bool) {
	if err != nil {
		return err.Error(), true
	}
	if res.IsError {
		return res.Content[0].(*mcp.TextContent).Text, true
	}
	return "", false
}

func TestGetSessionActionsListsNamesAndParams(t *testing.T) {
	s := actionsSession(t, &fakeActionsAPI{status: "running"}, fakeAllow{}, "gamer")
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_session_actions", Arguments: map[string]any{"session_id": 11}})
	if msg, bad := failed(res, err); bad {
		t.Fatal(msg)
	}
	m := res.StructuredContent.(map[string]any)["actions"].([]any)
	if len(m) != 2 || m[0].(map[string]any)["name"] != "say" || len(m[0].(map[string]any)["parameters"].([]any)) != 1 {
		t.Fatalf("unexpected actions %#v", m)
	}
}

func TestExecuteActionAllowlist(t *testing.T) {
	api := &fakeActionsAPI{status: "running"}
	s := actionsSession(t, api, fakeAllow{"say": true}, "gamer")
	if msg, bad := failed(execAction(s, map[string]any{"session_id": 11, "action_name": "say", "params": map[string]string{"msg": "hi"}, "idempotency_key": "k1"})); bad {
		t.Fatalf("allowlisted action rejected: %s", msg)
	}
	if len(api.executed) != 1 || api.executed[0].ActionId != 5 || api.executed[0].InputValues["msg"] != "hi" {
		t.Fatalf("executed %+v", api.executed)
	}
	msg, bad := failed(execAction(s, map[string]any{"session_id": 11, "action_name": "save", "idempotency_key": "k2"}))
	if !bad || !strings.Contains(msg, "allowlist") || len(api.executed) != 1 {
		t.Fatalf("non-allowlisted: bad=%v msg=%q executed=%d", bad, msg, len(api.executed))
	}
}

// A grant whose valid_to is set no longer appears as allowed.
func TestExecuteActionRevokedGrant(t *testing.T) {
	api := &fakeActionsAPI{status: "running"}
	allow := fakeAllow{"say": true}
	s := actionsSession(t, api, allow, "gamer")
	if _, bad := failed(execAction(s, map[string]any{"session_id": 11, "action_name": "say", "idempotency_key": "a"})); bad {
		t.Fatal("granted call should pass")
	}
	delete(allow, "say")
	if _, bad := failed(execAction(s, map[string]any{"session_id": 11, "action_name": "say", "idempotency_key": "b"})); !bad || len(api.executed) != 1 {
		t.Fatalf("revoked grant still dispatched: %d", len(api.executed))
	}
}

func TestExecuteActionServerManagerBypassesAllowlist(t *testing.T) {
	api := &fakeActionsAPI{status: "running"}
	s := actionsSession(t, api, fakeAllow{}, "mgr")
	if msg, bad := failed(execAction(s, map[string]any{"deployment_id": 7, "action_name": "save", "idempotency_key": "k"})); bad {
		t.Fatal(msg)
	}
	if len(api.executed) != 1 || api.executed[0].SessionId != 11 {
		t.Fatalf("executed %+v", api.executed)
	}
}

func TestExecuteActionNonRunningSession(t *testing.T) {
	api := &fakeActionsAPI{status: "stopped"}
	s := actionsSession(t, api, fakeAllow{"say": true}, "gamer")
	msg, bad := failed(execAction(s, map[string]any{"session_id": 11, "action_name": "say", "idempotency_key": "k"}))
	if !bad || !strings.Contains(msg, "not running") || len(api.executed) != 0 {
		t.Fatalf("bad=%v msg=%q executed=%d", bad, msg, len(api.executed))
	}
}

func TestExecuteActionMissingKeyRejected(t *testing.T) {
	api := &fakeActionsAPI{status: "running"}
	s := actionsSession(t, api, fakeAllow{"say": true}, "gamer")
	if _, bad := failed(execAction(s, map[string]any{"session_id": 11, "action_name": "say"})); !bad || len(api.executed) != 0 {
		t.Fatalf("missing key: bad=%v executed=%d", bad, len(api.executed))
	}
}

func TestExecuteActionReplayReturnsOriginalDispatch(t *testing.T) {
	api := &fakeActionsAPI{status: "running"}
	s := actionsSession(t, api, fakeAllow{"say": true}, "gamer")
	args := map[string]any{"session_id": 11, "action_name": "say", "idempotency_key": "same"}
	r1, err := execAction(s, args)
	if msg, bad := failed(r1, err); bad {
		t.Fatal(msg)
	}
	r2, err := execAction(s, args)
	if msg, bad := failed(r2, err); bad {
		t.Fatal(msg)
	}
	id1 := r1.StructuredContent.(map[string]any)["execution_id"]
	id2 := r2.StructuredContent.(map[string]any)["execution_id"]
	if len(api.executed) != 1 || id1 != id2 {
		t.Fatalf("executed=%d ids %v %v", len(api.executed), id1, id2)
	}
}
