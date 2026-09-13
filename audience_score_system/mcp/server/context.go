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
const authPathContextKey contextKey = "audience_score_system/mcp/server.authPath"

// AuthPath records which of the two auth middlewares resolved the Person
// on a request's context -- an explicit signal set by the resolving
// middleware itself, not inferred from the token shape or claim contents.
type AuthPath int

const (
	// AuthPathUnknown means no middleware has resolved a Person onto ctx.
	AuthPathUnknown AuthPath = iota
	// AuthPathWhagent means WhagentPersonMiddleware (whagent_auth.go)
	// resolved the Person from a verified whagent-net Claim.
	AuthPathWhagent
	// AuthPathMCPCredential means PersonMiddleware (auth.go) resolved the
	// Person from an ASS-native mcp_credential.
	AuthPathMCPCredential
)

// withPerson returns a copy of ctx carrying p and the auth path that
// resolved it, retrievable via PersonFromContext and AuthPathFromContext.
func withPerson(ctx context.Context, p store.Person, path AuthPath) context.Context {
	ctx = context.WithValue(ctx, personContextKey, &p)
	return context.WithValue(ctx, authPathContextKey, path)
}

// PersonFromContext returns the store.Person PersonMiddleware (auth.go) or
// WhagentPersonMiddleware (whagent_auth.go) resolved onto ctx for the
// current tool call, or nil if no Person has been resolved (e.g. called
// outside a request either middleware handled).
func PersonFromContext(ctx context.Context) *store.Person {
	p, _ := ctx.Value(personContextKey).(*store.Person)
	return p
}

// AuthPathFromContext returns which middleware resolved the Person on ctx,
// or AuthPathUnknown if no Person has been resolved.
func AuthPathFromContext(ctx context.Context) AuthPath {
	path, _ := ctx.Value(authPathContextKey).(AuthPath)
	return path
}
