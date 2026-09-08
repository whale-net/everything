package llm

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ErrModelNotServed is returned by Supports when Model is not present in
// OpenRouter's model catalogue. Typed so the API layer can map it to a
// precondition failure (FR5: an operator-specified model the provider
// does not serve fails at StartSession, with no session created, rather
// than surfacing on the first turn's LLM call).
type ErrModelNotServed struct {
	Model string
}

func (e *ErrModelNotServed) Error() string {
	return fmt.Sprintf("model %q is not served by the configured provider", e.Model)
}

// Catalog is OpenRouter's model catalogue (FR5): ListModels/Supports back
// StartSession's "does the provider serve this model" gate. Results are
// cached for TTL so the gate does not cost a round trip on every session
// start.
type Catalog struct {
	client *Client
	ttl    time.Duration

	mu      sync.Mutex
	models  map[string]struct{}
	fetched time.Time
}

// NewCatalog returns a Catalog backed by client, caching ListModels
// results for ttl.
func NewCatalog(client *Client, ttl time.Duration) *Catalog {
	return &Catalog{client: client, ttl: ttl}
}

// ListModels returns every model id OpenRouter currently serves,
// refreshing the cache when it has expired.
func (c *Catalog) ListModels(ctx context.Context) ([]string, error) {
	return nil, errNotImplemented
}

// Supports reports whether model is present in the catalogue. A
// catalogue fetch failure is returned as an error, never treated as
// "supported" -- FR5's gate must fail closed, not fail open.
func (c *Catalog) Supports(ctx context.Context, model string) (bool, error) {
	return false, errNotImplemented
}
