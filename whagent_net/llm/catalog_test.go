package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

const modelsFixture = `{
  "object": "list",
  "data": [
    {"id": "openai/gpt-4o", "created": 1700000000, "object": "model", "owned_by": "openai"},
    {"id": "anthropic/claude-3.5-sonnet", "created": 1700000000, "object": "model", "owned_by": "anthropic"}
  ]
}`

func TestCatalog_Supports(t *testing.T) {
	stub := &stubTransport{body: modelsFixture}
	catalog := NewCatalog(newStubClient(stub), time.Minute)

	ok, err := catalog.Supports(context.Background(), "openai/gpt-4o")
	if err != nil {
		t.Fatalf("Supports(present): %v", err)
	}
	if !ok {
		t.Errorf("Supports(present) = false, want true")
	}

	ok, err = catalog.Supports(context.Background(), "openai/gpt-4-absent")
	if err != nil {
		t.Fatalf("Supports(absent): %v", err)
	}
	if ok {
		t.Errorf("Supports(absent) = true, want false")
	}
}

func TestCatalog_FetchFailureIsSurfacedNotSupported(t *testing.T) {
	// FR5: the gate fails closed. A catalogue fetch failure must be
	// returned as an error, never silently treated as "supported".
	stub := &stubTransport{status: http.StatusInternalServerError, body: `{"error": {"message": "boom"}}`}
	catalog := NewCatalog(newStubClient(stub), time.Minute)

	ok, err := catalog.Supports(context.Background(), "openai/gpt-4o")
	if err == nil {
		t.Fatalf("Supports: got nil error on a catalogue fetch failure")
	}
	if ok {
		t.Errorf("Supports: got ok=true alongside a fetch error, want false")
	}

	var notServed *ErrModelNotServed
	if errors.As(err, &notServed) {
		t.Errorf("Supports: a fetch failure must not be reported as ErrModelNotServed (that is for a healthy catalogue that just lacks the model)")
	}
}

func TestCatalog_ListModels(t *testing.T) {
	stub := &stubTransport{body: modelsFixture}
	catalog := NewCatalog(newStubClient(stub), time.Minute)

	ids, err := catalog.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := map[string]bool{"openai/gpt-4o": true, "anthropic/claude-3.5-sonnet": true}
	if len(ids) != len(want) {
		t.Fatalf("ListModels = %v, want keys of %v", ids, want)
	}
	for _, id := range ids {
		if !want[id] {
			t.Errorf("ListModels returned unexpected id %q", id)
		}
	}
}

func TestCatalog_CachesWithinTTL(t *testing.T) {
	stub := &stubTransport{body: modelsFixture}
	catalog := NewCatalog(newStubClient(stub), time.Hour)

	if _, err := catalog.Supports(context.Background(), "openai/gpt-4o"); err != nil {
		t.Fatalf("Supports (first call): %v", err)
	}
	first := stub.lastRequest

	// Change the fixture underneath the stub -- if Supports refetched, it
	// would see this and Supports would flip to false. Within the TTL it
	// must not refetch, so the cached "true" from the first call stands.
	stub.body = `{"object": "list", "data": []}`

	ok, err := catalog.Supports(context.Background(), "openai/gpt-4o")
	if err != nil {
		t.Fatalf("Supports (second call): %v", err)
	}
	if !ok {
		t.Errorf("Supports (second call, within TTL) = false, want true (cache should not have refreshed)")
	}
	if stub.lastRequest != first {
		t.Errorf("a request was made on the second call within the TTL window")
	}
}
