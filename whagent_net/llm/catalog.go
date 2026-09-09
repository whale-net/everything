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
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.refreshLocked(ctx); err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(c.models))
	for id := range c.models {
		ids = append(ids, id)
	}
	return ids, nil
}

// Supports reports whether model is present in the catalogue. A
// catalogue fetch failure is returned as an error, never treated as
// "supported" -- FR5's gate must fail closed, not fail open.
func (c *Catalog) Supports(ctx context.Context, model string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.refreshLocked(ctx); err != nil {
		return false, err
	}

	_, ok := c.models[model]
	return ok, nil
}

// refreshLocked refetches the catalogue from OpenRouter's models
// endpoint when the cache is empty or has exceeded ttl. Callers must
// hold c.mu. A fetch failure leaves any existing (stale) cache
// untouched and is returned to the caller rather than papered over --
// FR5's gate fails closed on a catalogue it can't refresh, it does not
// silently serve a possibly-stale "supported".
func (c *Catalog) refreshLocked(ctx context.Context) error {
	if c.models != nil && time.Since(c.fetched) < c.ttl {
		return nil
	}

	page, err := c.client.oa.Models.List(ctx)
	if err != nil {
		return fmt.Errorf("llm: list models: %w", err)
	}

	models := make(map[string]struct{}, len(page.Data))
	for _, m := range page.Data {
		models[m.ID] = struct{}{}
	}

	c.models = models
	c.fetched = time.Now()
	return nil
}
