package server

import (
	"context"

	"github.com/whale-net/everything/libs/go/grpcauth"
)

// Caller is the verified identity behind one MCP request.
type Caller struct {
	// Issuer is the verified token's iss claim; with Subject it identifies the caller.
	Issuer  string
	Subject string
	Roles   []string
	Persona Persona
	// Token is the caller's own access token, forwarded on every backend call.
	Token string
}

// CallContext is what a tool Snapshot hook receives: the request context
// (already carrying the caller's token) and the caller.
type CallContext struct {
	context.Context
	Caller *Caller
}

type callerKey struct{}

// ContextWithCaller returns ctx carrying c, with c's token set for gRPC
// forwarding so every backend call made with the returned ctx authenticates
// as the caller.
func ContextWithCaller(ctx context.Context, c *Caller) context.Context {
	ctx = context.WithValue(ctx, callerKey{}, c)
	return grpcauth.WithUserToken(ctx, c.Token)
}

// CallerFromContext returns the caller on ctx, or nil.
func CallerFromContext(ctx context.Context) *Caller {
	c, _ := ctx.Value(callerKey{}).(*Caller)
	return c
}
