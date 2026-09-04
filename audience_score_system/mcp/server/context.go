package server

import (
	"context"

	"github.com/whale-net/everything/audience_score_system/store"
)

// contextKey namespaces this package's context values so they can't
// collide with keys any other package sets on the same request context --
// mirrors web/auth's contextKey convention.
type contextKey string

const personContextKey contextKey = "audience_score_system/mcp/server.person"

// withPerson returns a copy of ctx carrying p, retrievable via
// PersonFromContext.
func withPerson(ctx context.Context, p store.Person) context.Context {
	return context.WithValue(ctx, personContextKey, &p)
}

// PersonFromContext returns the store.Person PersonMiddleware (auth.go)
// resolved onto ctx for the current tool call, or nil if no Person has
// been resolved (e.g. called outside a request PersonMiddleware handled).
func PersonFromContext(ctx context.Context) *store.Person {
	p, _ := ctx.Value(personContextKey).(*store.Person)
	return p
}
