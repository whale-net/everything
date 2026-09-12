// Dispatch-time scope resolution and delegated-grant token acquisition
// (issue #2430, FR7/FR8): the sequence every tool's call() runs before
// ever forwarding a request to `api`, replacing RFC 8693 impersonation
// exchange (removed, FR19) with DelegatedGrantSource-backed acquisition.
//
// The two entry points below (resolveGrantTokenForAgent,
// resolveGrantTokenForSession) are gated on whether ctx carries a resolved
// Identity (whagent_net/mcpidentity.FromContext) -- placed there by
// ../server/auth.go's AuthMiddleware for the browser-OAuth2 path only.
// Absent (the manual-token path: a real bearer token is already on ctx,
// forwarded byte for byte) means both are a no-op -- scopeResolver and
// grant are never consulted, and ctx is returned unchanged. This is also
// why every pre-existing unit test in this package (constructed against a
// plain context.Background(), with scopeResolver/grant left nil) keeps
// passing unmodified: there is no Identity on that ctx, so nothing here
// ever touches the nil interfaces.
//
// A resolved-but-nil Scope (AgentDefinition.Scope unset) is also a no-op,
// distinct from the no-Identity case above: the operator IS authenticated,
// but the target agent definition carries no delegated-grant scoping at
// all, so there is nothing to derive a grant key from or acquire a token
// for -- ctx is returned unchanged and the call proceeds with whatever
// credential path it already had (the manual-token path's forwarded
// bearer token, if any). This is "no scoping", not "no permissions": the
// agent still runs with its own configured tool_set either way.
package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/grantkey"
	"github.com/whale-net/everything/whagent_net/mcpidentity"
)

// dispatchLoggerName is this package's structured-logger name
// (whagent_net/mcp/tools), used only by acquireGrantToken's
// ErrGrantNeedsReauth branch below -- issue #2431's mid-call reauth
// handling is the sole reason this package needs a logger at all today.
//
// Deliberately NOT a package-level `var dispatchLogger =
// logging.Get(...)`: logging.Get returns slog.Default().With(...), and a
// package-level var would freeze that Default() snapshot at package
// initialization time -- before any test's slog.SetDefault(...) call
// could ever take effect, making the WARNING-level/structured-field
// assertion issue #2431's Testing section calls for unwritable against
// this package's actual logger. Calling logging.Get(dispatchLoggerName)
// fresh at each log site instead (see below) costs one extra allocation
// on the (rare, human-intervention-needed) ErrGrantNeedsReauth path and
// keeps output identical in production, while letting a test observe
// slog.Default() as it stands at the moment the log line is actually
// emitted.
const dispatchLoggerName = "whagent_net/mcp/tools"

// resolveGrantTokenForAgent is start_session's dispatch-time sequence:
// resolve agentID's scope via ScopeForAgent (FR7), then acquire a
// working credential for it (FR8). See this file's doc comment for the
// manual-token-path and nil-scope no-op cases.
func resolveGrantTokenForAgent(ctx context.Context, scopeResolver ScopeResolver, grant GrantSource, agentID string) (context.Context, error) {
	identity, ok := mcpidentity.FromContext(ctx)
	if !ok {
		return ctx, nil
	}

	scope, err := scopeResolver.ScopeForAgent(ctx, agentID)
	if err != nil {
		return ctx, fmt.Errorf("resolve scope for agent %q: %w", agentID, err)
	}
	if scope == nil {
		return ctx, nil
	}
	return acquireGrantToken(ctx, grant, identity, *scope)
}

// resolveGrantTokenForSession is send_turn/stop_session/get_session/
// read_transcript's dispatch-time sequence: resolve sessionID's
// already-recorded agent-definition assignment's scope via
// ScopeForSession (FR7, never a scope re-derived from a fresh agent_id
// parsed off the request), then acquire a working credential for it
// (FR8). See this file's doc comment for the manual-token-path and
// nil-scope no-op cases.
func resolveGrantTokenForSession(ctx context.Context, scopeResolver ScopeResolver, grant GrantSource, sessionID string) (context.Context, error) {
	identity, ok := mcpidentity.FromContext(ctx)
	if !ok {
		return ctx, nil
	}

	scope, err := scopeResolver.ScopeForSession(ctx, sessionID)
	if err != nil {
		return ctx, fmt.Errorf("resolve scope for session %q: %w", sessionID, err)
	}
	if scope == nil {
		return ctx, nil
	}
	return acquireGrantToken(ctx, grant, identity, *scope)
}

// acquireGrantToken derives scope's grant key (whagent_net/grantkey.
// ForScope, FR4 -- the *only* permitted derivation) and acquires a
// working access token for (identity.Sub, grantKey) via
// grant.TokenSource(...).Token(ctx) -- never identity.Iss, per the
// subject-key convention whagent_net/ui/handlers_consent.go's
// authorizeConsentGate documents (issue #2428 comment
// https://github.com/whale-net/everything/issues/2428#issuecomment-5630520687):
// the delegated-grant Store is keyed on the operator's raw Keycloak `sub`
// claim, the same value grpcauth.CompleteAuthorization compares literally
// against, never the mcpidentity-encoded iss|sub composite (that encoding
// exists for a different, unrelated concern -- mcpauth.CredentialStore's
// Identity column, mcpidentity's own package doc).
//
// Only called once a scope has already been resolved as non-nil by the
// caller -- see resolveGrantTokenForAgent/resolveGrantTokenForSession's
// nil-scope no-op branches above.
//
// A scope with no active grant (grpcauth.ErrGrantNotFound), or one whose
// stored refresh token was revoked (ErrGrantRevoked), fails here rather
// than falling back to any other credential path (NFR2) -- and that
// sentinel's wrapped error text already names grantKey
// (delegatedgrant_token.go's own fmt.Errorf wrapping), which for
// whagent-net is scope itself (grantkey.ForScope's doc comment: "the
// grant key for scope s is s itself, once validated"), so an operator
// reading the failure knows exactly which scope to consent for.
//
// ErrGrantNeedsReauth is handled distinctly (issue #2431, FR18): rather
// than the plain scope-naming wrap above, it is translated into a
// reauthRequiredError (errors.go) -- its own type, still satisfying
// errors.Is(err, grpcauth.ErrGrantNeedsReauth) via Unwrap, so a caller
// that needs to distinguish "no grant at all"/"revoked" from "grant
// exists but a human must re-consent" can do so without string-matching
// Error() text. This branch fires identically whether the failing
// TokenSource call came from start_session's resolveGrantTokenForAgent or
// any existing-session handler's resolveGrantTokenForSession -- both
// funnel through this one function, so there is exactly one place this
// detection needs to live, at initial connect and mid-session alike. No
// retry is attempted (TokenSource is called exactly once here), no other
// grant is substituted, and no credential path other than the one
// resolved scope's delegated grant is ever consulted -- the function
// simply returns once TokenSource has answered, success or failure.
// Logged at WARNING (this package's own convention, AGENTS.md's logging
// levels table): the operation did not complete and needs a human, but
// the system itself is behaving correctly -- not an ERROR. The log line
// carries scope and the operator's subject in structured fields and
// nothing else off cause (no refresh token, no client secret ever
// reaches this function to log in the first place).
//
// No token is ever cached here or anywhere else in this package (NFR7):
// grant.TokenSource(...).Token(ctx) re-reads the underlying Store on
// every call, by contract (libs/go/grpcauth's GrantTokenSource doc
// comment) -- this function introduces no cache of its own on top of it.
func acquireGrantToken(ctx context.Context, grant GrantSource, identity mcpidentity.Identity, scope string) (context.Context, error) {
	grantKey, err := grantkey.ForScope(scope)
	if err != nil {
		return ctx, fmt.Errorf("derive grant key for scope %q: %w", scope, err)
	}

	tok, err := grant.TokenSource(identity.Sub, grantKey).Token(ctx)
	if err != nil {
		if errors.Is(err, grpcauth.ErrGrantNeedsReauth) {
			reauthErr := newReauthRequiredError(scope, err)
			if reauthedScope, ok := reauthScope(reauthErr); ok {
				logging.Get(dispatchLoggerName).WarnContext(ctx, "delegated grant needs re-consent; failing call, not retrying or substituting",
					"scope", reauthedScope, "subject", identity.Sub)
			}
			return ctx, reauthErr
		}
		return ctx, fmt.Errorf("acquire delegated-grant token for scope %q: %w", scope, err)
	}

	return grpcauth.WithUserToken(ctx, tok.AccessToken), nil
}
