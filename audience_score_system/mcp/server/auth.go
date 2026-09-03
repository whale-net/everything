// Caller authentication -- the resolved design decision from this task's
// (#1575) Scaffold phase. See ../../ARCHITECTURE.md "MCP server: caller
// authentication" for the full obtain/revoke/NFR3 rationale; in short: an
// MCP client authenticates with a bearer credential minted by an
// authenticated endpoint on `web` (sign-in machinery, not a new C4-C10
// capability surface, so NFR3 is not violated), stored here only as a
// SHA-256 hash (store.CredentialStore, migration 005), and resolved to a
// Person in two steps:
//
//  1. TokenVerifier (HTTP layer, wraps every request via
//     auth.RequireBearerToken in transport.go) hashes the raw token and
//     looks it up via store.CredentialStore.VerifyTokenHash, producing an
//     auth.TokenInfo carrying the Person's ID as UserID.
//  2. PersonMiddleware (MCP-protocol layer, wraps every tools/call via
//     mcp.Server.AddReceivingMiddleware in server.go) reads that
//     TokenInfo off the request's RequestExtra, resolves the full
//     store.Person, and places it on ctx for handlers to read via
//     PersonFromContext.
//
// Both steps currently call into store methods that are Scaffold-only
// stubs (store.CredentialStore.VerifyTokenHash returns "not implemented"
// until issue #1575's Implementation phase lands the real
// mcp_credential lookup) -- the wiring is real, the backing data access is
// not yet.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/store"
)

// TokenVerifier returns the auth.TokenVerifier the HTTP layer's
// auth.RequireBearerToken middleware (transport.go) uses: it hashes the
// raw bearer token (never storing or logging the raw value) and resolves
// it to a Person via credentials.VerifyTokenHash, reporting the resolved
// PersonID as auth.TokenInfo.UserID. A failed verification is logged at
// Warn (never the raw token or its hash -- only that verification
// failed) so a spike in invalid/revoked-credential attempts is visible
// without needing a tool-level span, since a rejection here never reaches
// instrumentToolCall (observability.go).
func TokenVerifier(credentials store.CredentialStore) sdkauth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		sum := sha256.Sum256([]byte(token))
		personID, err := credentials.VerifyTokenHash(ctx, hex.EncodeToString(sum[:]))
		if err != nil {
			logger.WarnContext(ctx, "mcp bearer token verification failed", "error", err.Error())
			return nil, fmt.Errorf("%w: %v", sdkauth.ErrInvalidToken, err)
		}
		return &sdkauth.TokenInfo{UserID: personID.String()}, nil
	}
}

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
func PersonMiddleware(persons store.PersonStore) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
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

			return next(withPerson(ctx, person), method, req)
		}
	}
}
