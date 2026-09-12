package tools

// GrantSource is the FR8 token-acquisition seam a tool handler needs at
// dispatch time (issue #2430, depending on #2426): producing a
// grpcauth.GrantTokenSource -- the "TokenSource-shaped" (see
// libs/go/grpcauth/delegatedgrant_token.go's own doc comment) accessor
// bound to one (subject, grant) pair. A handler asks GrantSource for one
// immediately before forwarding a call, and calls Token(ctx) on it right
// then -- it never mints, caches, or otherwise holds a token itself
// (NFR7: TokenSource.Token re-reads the underlying Store on every call,
// so this package introduces no replacement cache of its own).
//
// *grpcauth.DelegatedGrantSource -- constructed only at
// whagent_net/mcp/main.go's composition root, from
// whagent_net/delegatedgrant.Components.Source (issue #2426) -- satisfies
// this interface exactly as declared here, structurally, with no adapter:
// TokenSource's return type below is grpcauth's own GrantTokenSource, not
// a package-independent duplicate the way ../server/scope.go's
// ScopeResolver return types are plain strings and error only. Depending
// on //libs/go/grpcauth directly is fine here -- unlike
// whagent_net/delegatedgrant (the composition-root-only package that
// pulls in pgstore/pgx) or whagent_net/mcpscope (which pulls in
// whagent_net/session/pgx), grpcauth itself carries none of
// deps_test.go's forbidden dependencies (TestBUILD_NoStoreOrTemporalDependency)
// -- ../server already depends on it directly today (auth.go's
// grpcauth.WithUserToken).
//
// dispatch.go's resolveGrantTokenForAgent/resolveGrantTokenForSession run
// the actual dispatch-time call sequence (ScopeForAgent/ScopeForSession
// -> grantkey.ForScope -> TokenSource(subject, grant).Token(ctx)) --
// subject is always identity.Sub, the operator's raw Keycloak `sub` claim
// resolved by ../server/auth.go's AuthMiddleware, never grant, agentID,
// sessionID, or anything else the request itself carries (NFR2).
import "github.com/whale-net/everything/libs/go/grpcauth"

type GrantSource interface {
	// TokenSource returns a non-interactive GrantTokenSource bound to
	// (subject, grant) -- see grpcauth.DelegatedGrantSource.TokenSource's
	// own doc comment. Cheap to call repeatedly; holds no state of its
	// own beyond the (subject, grant) key.
	TokenSource(subject, grant string) grpcauth.GrantTokenSource
}
