package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	applied atomic.Int32
	fp      atomic.Value
}

func newFixture() *fixture {
	f := &fixture{}
	f.fp.Store("v1")
	return f
}

func (f *fixture) tool() GatedTool {
	return GatedTool{
		Name: "fixture_stop",
		Preview: func(context.Context, json.RawMessage) (any, string, error) {
			return map[string]string{"effect": "stops it"}, f.fp.Load().(string), nil
		},
		Apply: func(context.Context, json.RawMessage) (any, error) {
			f.applied.Add(1)
			return "done", nil
		},
	}
}

type fakeElicit struct {
	supported, accept bool
	asked             int
}

func (e *fakeElicit) SupportsElicitation() bool { return e.supported }
func (e *fakeElicit) Elicit(context.Context, string) (bool, error) {
	e.asked++
	return e.accept, nil
}

var alice = &Caller{Issuer: "iss", Subject: "alice"}

func args(s string) json.RawMessage { return json.RawMessage(s) }

func withToken(a, tok string) json.RawMessage {
	m := map[string]any{}
	_ = json.Unmarshal([]byte(a), &m)
	m["confirmation_token"] = tok
	b, _ := json.Marshal(m)
	return b
}

func preview(t *testing.T, g *Gate, c *Caller, tool GatedTool, a string) string {
	t.Helper()
	out, err := g.Call(context.Background(), c, nil, tool, args(a))
	if err != nil || out.Applied || out.ConfirmationToken == "" || out.Preview == nil {
		t.Fatalf("expected preview+token, got %+v err=%v", out, err)
	}
	return out.ConfirmationToken
}

func TestPreviewThenConfirm(t *testing.T) {
	f := newFixture()
	g := &Gate{Store: &MemoryConfirmationStore{}}
	tok := preview(t, g, alice, f.tool(), `{"id":1}`)
	if f.applied.Load() != 0 {
		t.Fatal("preview mutated")
	}
	out, err := g.Call(context.Background(), alice, nil, f.tool(), withToken(`{"id":1,"idempotency_key":"k"}`, tok))
	if err != nil || !out.Applied || f.applied.Load() != 1 {
		t.Fatalf("confirm failed: %+v %v", out, err)
	}
}

func TestExpiry(t *testing.T) {
	f := newFixture()
	now := time.Now()
	clock := func() time.Time { return now }
	g := &Gate{Store: &MemoryConfirmationStore{Now: clock}, Now: clock}
	tok := preview(t, g, alice, f.tool(), `{"id":1}`)
	now = now.Add(5*time.Minute + time.Second)
	if _, err := g.Call(context.Background(), alice, nil, f.tool(), withToken(`{"id":1}`, tok)); !errors.Is(err, ErrTokenNotConsumable) {
		t.Fatalf("want not consumable, got %v", err)
	}
	if f.applied.Load() != 0 {
		t.Fatal("mutated on expired token")
	}
}

func TestWrongCallerToolAndArgs(t *testing.T) {
	f := newFixture()
	g := &Gate{Store: &MemoryConfirmationStore{}}
	tok := preview(t, g, alice, f.tool(), `{"id":1}`)
	ctx := context.Background()

	bob := &Caller{Issuer: "iss", Subject: "bob"}
	if _, err := g.Call(ctx, bob, nil, f.tool(), withToken(`{"id":1}`, tok)); !errors.Is(err, ErrTokenNotConsumable) {
		t.Fatalf("wrong caller: %v", err)
	}
	other := f.tool()
	other.Name = "other_tool"
	if _, err := g.Call(ctx, alice, nil, other, withToken(`{"id":1}`, tok)); !errors.Is(err, ErrTokenNotConsumable) {
		t.Fatalf("wrong tool: %v", err)
	}
	if _, err := g.Call(ctx, alice, nil, f.tool(), withToken(`{"id":2}`, tok)); !errors.Is(err, ErrArgsChanged) {
		t.Fatalf("changed args: %v", err)
	}
	if f.applied.Load() != 0 {
		t.Fatal("mutated on rejected call")
	}
	// Rejected attempts must not burn the legitimate token.
	if out, err := g.Call(ctx, alice, nil, f.tool(), withToken(`{"id":1}`, tok)); err != nil || !out.Applied {
		t.Fatalf("legit confirm after rejections: %+v %v", out, err)
	}
}

func TestDoubleUseConcurrentTwoGates(t *testing.T) {
	f := newFixture()
	store := &MemoryConfirmationStore{}
	g1, g2 := &Gate{Store: store}, &Gate{Store: store}
	tok := preview(t, g1, alice, f.tool(), `{"id":1}`)
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < 20; i++ {
		g := g1
		if i%2 == 1 {
			g = g2
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.Call(context.Background(), alice, nil, f.tool(), withToken(`{"id":1}`, tok)); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || f.applied.Load() != 1 {
		t.Fatalf("want exactly one apply, got ok=%d applied=%d", ok.Load(), f.applied.Load())
	}
}

func TestEntityChangedSincePreview(t *testing.T) {
	f := newFixture()
	g := &Gate{Store: &MemoryConfirmationStore{}}
	tok := preview(t, g, alice, f.tool(), `{"id":1}`)
	f.fp.Store("v2")
	if _, err := g.Call(context.Background(), alice, nil, f.tool(), withToken(`{"id":1}`, tok)); !errors.Is(err, ErrEntityChanged) {
		t.Fatalf("want entity changed, got %v", err)
	}
	if f.applied.Load() != 0 {
		t.Fatal("mutated despite changed entity")
	}
}

func TestElicitation(t *testing.T) {
	ctx := context.Background()
	t.Run("accept", func(t *testing.T) {
		f := newFixture()
		g := &Gate{Store: &MemoryConfirmationStore{}}
		e := &fakeElicit{supported: true, accept: true}
		out, err := g.Call(ctx, alice, e, f.tool(), args(`{"id":1}`))
		if err != nil || !out.Applied || e.asked != 1 || f.applied.Load() != 1 || out.ConfirmationToken != "" {
			t.Fatalf("%+v %v asked=%d", out, err, e.asked)
		}
	})
	t.Run("decline", func(t *testing.T) {
		f := newFixture()
		g := &Gate{Store: &MemoryConfirmationStore{}}
		e := &fakeElicit{supported: true}
		out, err := g.Call(ctx, alice, e, f.tool(), args(`{"id":1}`))
		if err != nil || out.Applied || !out.Declined || f.applied.Load() != 0 {
			t.Fatalf("%+v %v", out, err)
		}
	})
	t.Run("unsupported falls back to two-call", func(t *testing.T) {
		f := newFixture()
		g := &Gate{Store: &MemoryConfirmationStore{}}
		e := &fakeElicit{supported: false, accept: true}
		out, err := g.Call(ctx, alice, e, f.tool(), args(`{"id":1}`))
		if err != nil || out.Applied || out.ConfirmationToken == "" || e.asked != 0 {
			t.Fatalf("%+v %v", out, err)
		}
	})
}

func TestHashArgsExcludesReservedKeys(t *testing.T) {
	a, _ := HashArgs(args(`{"b":2,"a":1}`))
	b, _ := HashArgs(args(`{"a":1,"b":2,"idempotency_key":"x","confirmation_token":"y"}`))
	c, _ := HashArgs(args(`{"a":1,"b":3}`))
	if a != b || a == c {
		t.Fatalf("hash mismatch a=%s b=%s c=%s", a, b, c)
	}
}

func TestUnauthenticatedGate(t *testing.T) {
	g := &Gate{Store: &MemoryConfirmationStore{}}
	if _, err := g.Call(context.Background(), nil, nil, newFixture().tool(), args(`{}`)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("got %v", err)
	}
}
