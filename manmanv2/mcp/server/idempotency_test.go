package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func idemChain(store IdempotencyStore, backend mcp.MethodHandler) mcp.MethodHandler {
	reg := NewRegistry(
		Tool{Name: "w", MinPersona: PersonaServerManager, Write: true},
		Tool{Name: "wreq", MinPersona: PersonaServerManager, Write: true, KeyRequired: true},
		Tool{Name: "r", MinPersona: PersonaGamer},
	)
	return Idempotency(reg, store)(backend)
}

func idemCall(h mcp.MethodHandler, c *Caller, tool, args string) (mcp.Result, error) {
	ctx := ContextWithCaller(context.Background(), c)
	req := &mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{Name: tool, Arguments: json.RawMessage(args)}}
	return h(ctx, "tools/call", req)
}

func countingBackend(n *int32, delay time.Duration, fail bool) mcp.MethodHandler {
	return func(context.Context, string, mcp.Request) (mcp.Result, error) {
		i := atomic.AddInt32(n, 1)
		time.Sleep(delay)
		if fail {
			return nil, errors.New("boom")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "call-" + string(rune('0'+i))}}}, nil
	}
}

func text(t *testing.T, r mcp.Result) string {
	t.Helper()
	cr, ok := r.(*mcp.CallToolResult)
	if !ok || len(cr.Content) == 0 {
		t.Fatalf("bad result %#v", r)
	}
	return cr.Content[0].(*mcp.TextContent).Text
}

var callerA = &Caller{Issuer: "iss", Subject: "a", Persona: PersonaServerManager}

func TestIdempotencyReplayReturnsStoredResult(t *testing.T) {
	var n int32
	h := idemChain(&MemIdempotencyStore{}, countingBackend(&n, 0, false))
	r1, err := idemCall(h, callerA, "w", `{"id":"1","idempotency_key":"k"}`)
	if err != nil {
		t.Fatal(err)
	}
	// Reordered keys still count as the same args.
	r2, err := idemCall(h, callerA, "w", `{"idempotency_key":"k","id":"1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || text(t, r1) != text(t, r2) {
		t.Fatalf("backend calls=%d, r1=%q r2=%q", n, text(t, r1), text(t, r2))
	}
}

func TestIdempotencyConflictOnDifferentArgs(t *testing.T) {
	var n int32
	h := idemChain(&MemIdempotencyStore{}, countingBackend(&n, 0, false))
	if _, err := idemCall(h, callerA, "w", `{"id":"1","idempotency_key":"k"}`); err != nil {
		t.Fatal(err)
	}
	_, err := idemCall(h, callerA, "w", `{"id":"2","idempotency_key":"k"}`)
	if !errors.Is(err, ErrIdempotencyConflict) || n != 1 {
		t.Fatalf("err=%v calls=%d", err, n)
	}
}

func TestIdempotencyConcurrentDuplicatesRunBackendOnce(t *testing.T) {
	var n int32
	h := idemChain(&MemIdempotencyStore{}, countingBackend(&n, 100*time.Millisecond, false))
	var wg sync.WaitGroup
	var inflight, ok int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := idemCall(h, callerA, "w", `{"id":"1","idempotency_key":"k"}`)
			switch {
			case err == nil:
				atomic.AddInt32(&ok, 1)
			case errors.Is(err, ErrIdempotencyInFlight):
				atomic.AddInt32(&inflight, 1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n != 1 || ok != 1 || inflight != 7 {
		t.Fatalf("calls=%d ok=%d inflight=%d", n, ok, inflight)
	}
}

func TestIdempotencyKeyIndependentPerCaller(t *testing.T) {
	var n int32
	h := idemChain(&MemIdempotencyStore{}, countingBackend(&n, 0, false))
	b := &Caller{Issuer: "iss", Subject: "b", Persona: PersonaServerManager}
	c := &Caller{Issuer: "other", Subject: "a", Persona: PersonaServerManager}
	for _, cl := range []*Caller{callerA, b, c} {
		if _, err := idemCall(h, cl, "w", `{"id":"1","idempotency_key":"k"}`); err != nil {
			t.Fatal(err)
		}
	}
	if n != 3 {
		t.Fatalf("calls=%d, want 3", n)
	}
}

func TestIdempotencyFailureNotRecorded(t *testing.T) {
	var n int32
	store := &MemIdempotencyStore{}
	if _, err := idemCall(idemChain(store, countingBackend(&n, 0, true)), callerA, "w", `{"idempotency_key":"k"}`); err == nil {
		t.Fatal("want error")
	}
	if _, err := idemCall(idemChain(store, countingBackend(&n, 0, false)), callerA, "w", `{"idempotency_key":"k"}`); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("calls=%d, want retry to re-run", n)
	}
}

func TestIdempotencyPassThroughAndRequired(t *testing.T) {
	var n int32
	h := idemChain(&MemIdempotencyStore{}, countingBackend(&n, 0, false))
	// No key on optional write, and any read: always hits backend.
	for _, tool := range []string{"w", "w", "r", "r"} {
		if _, err := idemCall(h, callerA, tool, `{"id":"1"}`); err != nil {
			t.Fatal(err)
		}
	}
	if n != 4 {
		t.Fatalf("calls=%d, want 4", n)
	}
	if _, err := idemCall(h, callerA, "wreq", `{"id":"1"}`); !errors.Is(err, ErrIdempotencyRequired) || n != 4 {
		t.Fatalf("err=%v calls=%d", err, n)
	}
}

// Retrying a write through the real NewServer + HTTP stack hits the backend once.
func TestNewServerIdempotencyReplaysOverHTTP(t *testing.T) {
	reg := NewRegistry(Tool{Name: "write", MinPersona: PersonaServerManager, Write: true})
	srv := NewServer(reg, LogAuditor{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, &MemIdempotencyStore{})
	var calls int32
	type in struct {
		ID  string `json:"id"`
		Key string `json:"idempotency_key,omitempty"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "write"}, func(context.Context, *mcp.CallToolRequest, in) (*mcp.CallToolResult, any, error) {
		n := atomic.AddInt32(&calls, 1)
		return nil, map[string]int32{"n": n}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(HTTPAuth(fakeVerifier{"mgr": claims("u2", "server-manager")}, "")(h))
	defer ts.Close()
	c := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: &http.Client{Transport: bearerRT{"mgr"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	args := map[string]any{"id": "x", "idempotency_key": "k1"}
	var first string
	for i := 0; i < 3; i++ {
		res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "write", Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("call %d: %v %+v %v", i, err, res, res.Content[0].(*mcp.TextContent).Text)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if i == 0 {
			first = string(b)
		} else if string(b) != first {
			t.Fatalf("call %d result %s, want %s", i, b, first)
		}
	}
	if calls != 1 {
		t.Fatalf("backend called %d times, want 1", calls)
	}
	if res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "write", Arguments: map[string]any{"id": "y", "idempotency_key": "k1"}}); err == nil && !res.IsError {
		t.Fatal("different args under same key should conflict")
	}
	if calls != 1 {
		t.Fatalf("conflict mutated: %d calls", calls)
	}
}

func TestIdempotencyComposesWithGate(t *testing.T) {
	f := newFixture()
	g := &Gate{Store: &MemoryConfirmationStore{}}
	gated := g.Handler(f.tool())
	backend := func(ctx context.Context, _ string, req mcp.Request) (mcp.Result, error) {
		return gated(ctx, &mcp.CallToolRequest{Params: req.GetParams().(*mcp.CallToolParamsRaw)})
	}
	h := idemChain(&MemIdempotencyStore{}, backend)
	var out Outcome
	call := func(a string) mcp.Result {
		t.Helper()
		r, err := idemCall(h, callerA, "w", a)
		if err != nil {
			t.Fatal(err)
		}
		if cr := r.(*mcp.CallToolResult); cr.IsError {
			t.Fatalf("error result: %s", text(t, r))
		}
		out = Outcome{}
		_ = json.Unmarshal([]byte(text(t, r)), &out)
		return r
	}
	call(`{"id":"1","idempotency_key":"k"}`)
	if out.ConfirmationToken == "" || out.Applied {
		t.Fatalf("expected preview, got %+v", out)
	}
	confirm := `{"id":"1","idempotency_key":"k","confirmation_token":"` + out.ConfirmationToken + `"}`
	r1 := call(confirm)
	if !out.Applied || f.applied.Load() != 1 {
		t.Fatalf("expected applied once, got %+v applied=%d", out, f.applied.Load())
	}
	r2 := call(confirm)
	if text(t, r1) != text(t, r2) || f.applied.Load() != 1 {
		t.Fatalf("replay differs or re-applied: %d", f.applied.Load())
	}
	_, err := idemCall(h, callerA, "w", `{"id":"2","idempotency_key":"k"}`)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}
