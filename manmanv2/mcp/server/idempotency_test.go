package server

import (
	"context"
	"encoding/json"
	"errors"
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
