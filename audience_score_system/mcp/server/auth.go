// Caller authentication -- migrated (issue #1643) from the bespoke
// store.CredentialStore (migration 005, issue #1575's Scaffold-phase
// design decision) onto the shared libs/go/mcpauth.CredentialStore. See
// ../../ARCHITECTURE.md "MCP server: caller authentication" for the full
// obtain/revoke/NFR3 rationale; in short: an MCP client authenticates
// with a bearer credential minted by an authenticated endpoint on `web`
// (sign-in machinery, not a new C4-C10 capability surface, so NFR3 is not
// violated), stored as a SHA-256 hash (mcpauth.CredentialStore, migration
// 006), and resolved to a Person in two steps:
//
//  1. HTTP layer (transport.go): mcpauth.RequireBearerToken wraps every
//     request, calling mcpauth.TokenVerifier under the hood to hash the
//     raw token, resolve it via mcpauth.CredentialStore.Verify, and
//     produce an auth.TokenInfo carrying the Person's UUID (rendered as a
//     string) as UserID. That verifier now lives in libs/go/mcpauth --
//     this package no longer defines its own TokenVerifier.
//  2. PersonMiddleware (MCP-protocol layer, wraps every tools/call via
//     mcp.Server.AddReceivingMiddleware in server.go) reads that
//     TokenInfo off the request's RequestExtra, parses UserID back into a
//     uuid.UUID, resolves the full store.Person via store.PersonStore,
//     and places it on ctx for handlers to read via PersonFromContext.
//     This step is unchanged by the mcpauth migration (FR11) -- mcpauth
//     only replaces the credential storage/verification layer, not how a
//     resolved identity becomes a Person.
package server

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/store"
)

// PersonMiddleware is the mcp.Middleware every request passes through
// (wired in server.New): it reads the auth.TokenInfo the HTTP layer
// already verified (req.GetExtra().TokenInfo, populated by the streamable
// transport from auth.RequireBearerToken's context value), resolves it to
// a store.Person, and places that Person on ctx (PersonFromContext) before
// calling next. A call with no resolved TokenInfo, an unparseable UserID,
// or a UserID that doesn't resolve to a real Person is rejected here and
// next is never invoked -- the handler cannot be entered unauthenticated.
// Every rejection is logged at Warn with the JSON-RPC method (e.g.
// "tools/call") -- these calls never reach instrumentToolCall
// (observability.go), since that only wraps a tool once RegisterRead/
// RegisterWrite's mcp.AddTool handler is actually entered.
//
// Coexistence with the whagent-net path (issue #2116, FR12(a)): if a
// Person is already on ctx when this middleware runs, some earlier
// receiving middleware -- WhagentPersonMiddleware, whagent_auth.go -- has
// already authenticated and resolved this call via a different path, and
// this middleware must not re-interpret its (non-mcp_credential-shaped)
// TokenInfo as an ASS credential and reject it. This check is a no-op for
// every call that predates #2116 (nothing else ever placed a Person on
// ctx before PersonMiddleware ran), so mcp_credential's own resolution
// below is unchanged for those callers.
func PersonMiddleware(persons store.PersonStore) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if PersonFromContext(ctx) != nil {
				return next(ctx, method, req)
			}

			extra := req.GetExtra()
			if extra == nil || extra.TokenInfo == nil || extra.TokenInfo.UserID == "" {
				logger.WarnContext(ctx, "mcp call rejected: no caller credential resolved", "method", method)
				return nil, fmt.Errorf("unauthenticated: no caller credential resolved")
			}

			personID, err := uuid.Parse(extra.TokenInfo.UserID)
			if err != nil {
				logger.WarnContext(ctx, "mcp call rejected: invalid caller identity", "method", method)
				return nil, fmt.Errorf("unauthenticated: invalid caller identity")
			}

			person, err := persons.GetByID(ctx, personID)
			if err != nil {
				logger.WarnContext(ctx, "mcp call rejected: caller identity not found", "method", method, "person_id", personID)
				return nil, fmt.Errorf("unauthenticated: caller identity not found")
			}

			return next(withPerson(ctx, person, AuthPathMCPCredential), method, req)
		}
	}
}
