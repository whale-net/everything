// Dispatch-time domain resolution and delegated-grant token acquisition
// (issue #2430, FR7/FR8): the sequence every tool's call() runs before
// ever forwarding a request to `api`, replacing RFC 8693 impersonation
// exchange (removed, FR19) with DelegatedGrantSource-backed acquisition.
//
// The two entry points below (resolveGrantTokenForAgent,
// resolveGrantTokenForSession) are gated on whether ctx carries a resolved
// Identity (whagent_net/mcpidentity.FromContext) -- placed there by
// ../server/auth.go's AuthMiddleware for the browser-OAuth2 path only.
// Absent (the manual-token path: a real bearer token is already on ctx,
// forwarded byte for byte) means both are a no-op -- domainResolver and
// grant are never consulted, and ctx is returned unchanged. This is also
// why every pre-existing unit test in this package (constructed against a
// plain context.Background(), with domainResolver/grant left nil) keeps
// passing unmodified: there is no Identity on that ctx, so nothing here
// ever touches the nil interfaces.
package tools

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/whagent_net/grantkey"
	"github.com/whale-net/everything/whagent_net/mcpidentity"
)

// resolveGrantTokenForAgent is start_session's dispatch-time sequence:
// resolve agentID's domain via DomainForAgent (FR7), then acquire a
// working credential for it (FR8). See this file's doc comment for the
// manual-token-path no-op case.
func resolveGrantTokenForAgent(ctx context.Context, domainResolver DomainResolver, grant GrantSource, agentID string) (context.Context, error) {
	identity, ok := mcpidentity.FromContext(ctx)
	if !ok {
		return ctx, nil
	}

	domain, err := domainResolver.DomainForAgent(ctx, agentID)
	if err != nil {
		return ctx, fmt.Errorf("resolve domain for agent %q: %w", agentID, err)
	}
	return acquireGrantToken(ctx, grant, identity, domain)
}

// resolveGrantTokenForSession is send_turn/stop_session/get_session/
// read_transcript's dispatch-time sequence: resolve sessionID's
// already-recorded agent-definition assignment's domain via
// DomainForSession (FR7, never a domain re-derived from a fresh agent_id
// parsed off the request), then acquire a working credential for it
// (FR8). See this file's doc comment for the manual-token-path no-op
// case.
func resolveGrantTokenForSession(ctx context.Context, domainResolver DomainResolver, grant GrantSource, sessionID string) (context.Context, error) {
	identity, ok := mcpidentity.FromContext(ctx)
	if !ok {
		return ctx, nil
	}

	domain, err := domainResolver.DomainForSession(ctx, sessionID)
	if err != nil {
		return ctx, fmt.Errorf("resolve domain for session %q: %w", sessionID, err)
	}
	return acquireGrantToken(ctx, grant, identity, domain)
}

// acquireGrantToken derives domain's grant key (whagent_net/grantkey.
// ForDomain, FR4 -- the *only* permitted derivation) and acquires a
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
// A domain with no active grant (grpcauth.ErrGrantNotFound), or one whose
// stored refresh token needs reauth or was revoked (ErrGrantNeedsReauth/
// ErrGrantRevoked), fails here rather than falling back to any other
// credential path (NFR2) -- and every one of those sentinels' wrapped
// error text already names grantKey (delegatedgrant_token.go's own
// fmt.Errorf wrapping), which for whagent-net is domain itself
// (grantkey.ForDomain's doc comment: "the grant key for domain d is d
// itself, once validated"), so an operator reading the failure knows
// exactly which domain to consent for. Mid-call ErrGrantNeedsReauth
// interrupt/redirect handling beyond that plain failure is a dependent
// task's scope (issue #2431), not this one's.
//
// No token is ever cached here or anywhere else in this package (NFR7):
// grant.TokenSource(...).Token(ctx) re-reads the underlying Store on
// every call, by contract (libs/go/grpcauth's GrantTokenSource doc
// comment) -- this function introduces no cache of its own on top of it.
func acquireGrantToken(ctx context.Context, grant GrantSource, identity mcpidentity.Identity, domain string) (context.Context, error) {
	grantKey, err := grantkey.ForDomain(domain)
	if err != nil {
		return ctx, fmt.Errorf("derive grant key for domain %q: %w", domain, err)
	}

	tok, err := grant.TokenSource(identity.Sub, grantKey).Token(ctx)
	if err != nil {
		return ctx, fmt.Errorf("acquire delegated-grant token for domain %q: %w", domain, err)
	}

	return grpcauth.WithUserToken(ctx, tok.AccessToken), nil
}
