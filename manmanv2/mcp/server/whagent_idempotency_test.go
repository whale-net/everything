package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantflow"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// mintingRT presents a freshly minted whagent claim JWT on every request.
type mintingRT struct {
	t *testing.T
	f whagentFixture
	n atomic.Int32
}

func (m *mintingRT) RoundTrip(r *http.Request) (*http.Response, error) {
	m.n.Add(1)
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+m.f.mint(m.t, "human-1", testUserIssuer, testAudience))
	return http.DefaultTransport.RoundTrip(r)
}

type wgWriteEnv struct {
	s     *mcp.ClientSession
	life  *fakeLifecycleAPI
	acts  *fakeActionsAPI
	mints *mintingRT
}

// newWhagentWriteEnv mounts the real write tools behind the whagent auth path
// (static-key verifier, fake exchanger) with the production middleware order.
func newWhagentWriteEnv(t *testing.T) wgWriteEnv {
	t.Helper()
	f := newWhagentFixture(t)
	life := &fakeLifecycleAPI{live: &manmanpb.Session{SessionId: 7, ServerGameConfigId: 7, Status: "running"}}
	acts := &fakeActionsAPI{status: "running"}
	reg := NewRegistry(append(append([]Tool{}, LifecycleTools...), SessionActionTools...)...)
	srv := NewServer(reg, LogAuditor{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, &MemIdempotencyStore{})
	gate := &Gate{Store: &MemoryConfirmationStore{}}
	AddLifecycleTools(srv, life, fakeAllow{}, gate)
	AddSessionActionTools(srv, acts, fakeActionAllow{})
	ex := grantflow.Exchanger{
		Source:   grantSrc{"human-1": "user-token"},
		Grant:    "g",
		Verifier: fakeVerifier{"user-token": {Issuer: testUserIssuer, Subject: "human-1", Roles: []string{"server-manager"}}},
	}
	srv.AddReceivingMiddleware(WhagentMiddleware(ex))
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	cfg := WhagentAuthConfig{Verifier: f.verifier, Audience: testAudience, UserIssuer: testUserIssuer, WhagentIssuer: testWhagentIssuer}
	ts := httptest.NewServer(WhagentHTTPAuth(OIDCCallerVerifier(fakeVerifier{}), cfg, "")(h))
	t.Cleanup(ts.Close)
	rt := &mintingRT{t: t, f: f}
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: &http.Client{Transport: rt}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return wgWriteEnv{s, life, acts, rt}
}

func (e wgWriteEnv) call(t *testing.T, tool string, args map[string]any) (bool, string) {
	t.Helper()
	res, err := e.s.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return true, err.Error()
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return res.IsError, sb.String()
}

func TestWhagentStartRetryAppliesOnce(t *testing.T) {
	e := newWhagentWriteEnv(t)
	e.life.live = nil
	args := map[string]any{"deployment_id": 7, "idempotency_key": "wg-k1"}
	isErr, first := e.call(t, "start_deployment", args)
	if isErr {
		t.Fatal(first)
	}
	for i := 0; i < 2; i++ {
		if isErr, got := e.call(t, "start_deployment", args); isErr || got != first {
			t.Fatalf("retry %d: err=%v got %q want %q", i, isErr, got, first)
		}
	}
	if e.life.starts != 1 {
		t.Fatalf("StartSession called %d times, want 1", e.life.starts)
	}
	if isErr, _ := e.call(t, "start_deployment", map[string]any{"deployment_id": 8, "idempotency_key": "wg-k1"}); !isErr || e.life.starts != 1 {
		t.Fatalf("same key different args must conflict without a backend call (err=%v starts=%d)", isErr, e.life.starts)
	}
	if e.mints.n.Load() < 4 {
		t.Fatalf("expected a fresh claim JWT per request, minted %d", e.mints.n.Load())
	}
}

func TestWhagentExecuteActionRetryAndKeyRequired(t *testing.T) {
	e := newWhagentWriteEnv(t)
	args := map[string]any{"session_id": 11, "action_name": "save", "idempotency_key": "wg-x"}
	_, first := e.call(t, "execute_action", args)
	if _, again := e.call(t, "execute_action", args); again != first || len(e.acts.executed) != 1 {
		t.Fatalf("replay=%q first=%q executed=%d", again, first, len(e.acts.executed))
	}
	if isErr, _ := e.call(t, "execute_action", map[string]any{"session_id": 11, "action_name": "say", "idempotency_key": "wg-x"}); !isErr || len(e.acts.executed) != 1 {
		t.Fatalf("different args under same key must conflict (executed=%d)", len(e.acts.executed))
	}
	if isErr, _ := e.call(t, "execute_action", map[string]any{"session_id": 11, "action_name": "save"}); !isErr || len(e.acts.executed) != 1 {
		t.Fatalf("keyless execute_action must be rejected (executed=%d)", len(e.acts.executed))
	}
}

func TestWhagentPreviewThenConfirmMutatesOnce(t *testing.T) {
	for _, tc := range []struct {
		tool string
		n    func(*fakeLifecycleAPI) int
	}{
		{"stop_deployment", func(l *fakeLifecycleAPI) int { return l.stops }},
		{"restart_deployment", func(l *fakeLifecycleAPI) int { return l.restarts }},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			e := newWhagentWriteEnv(t)
			isErr, txt := e.call(t, tc.tool, map[string]any{"deployment_id": 7, "idempotency_key": "turn1-call0"})
			var out Outcome
			if err := json.Unmarshal([]byte(txt), &out); isErr || err != nil || out.ConfirmationToken == "" || out.Applied {
				t.Fatalf("preview: err=%v %q", isErr, txt)
			}
			if tc.n(e.life) != 0 {
				t.Fatal("preview mutated")
			}
			confirm := map[string]any{"deployment_id": 7, "idempotency_key": "turn2-call0", "confirmation_token": out.ConfirmationToken}
			isErr, txt = e.call(t, tc.tool, confirm)
			out = Outcome{}
			if err := json.Unmarshal([]byte(txt), &out); isErr || err != nil || !out.Applied {
				t.Fatalf("confirm: err=%v %q", isErr, txt)
			}
			if _, again := e.call(t, tc.tool, confirm); again != txt {
				t.Fatalf("replay %q != %q", again, txt)
			}
			if tc.n(e.life) != 1 {
				t.Fatalf("mutated %d times, want 1", tc.n(e.life))
			}
		})
	}
}

func TestLifecycleDescriptionsPointAtConfirmationToken(t *testing.T) {
	e := newWhagentWriteEnv(t)
	res, err := e.s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tl := range res.Tools {
		if tl.Name == "stop_deployment" || tl.Name == "restart_deployment" {
			seen++
			if !strings.Contains(tl.Description, "confirmation_token") {
				t.Errorf("%s description lacks confirmation_token: %q", tl.Name, tl.Description)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("saw %d gated tools", seen)
	}
}

var _ = grpcauth.Claims{}
