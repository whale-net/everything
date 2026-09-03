package mcpauth

import "net/http"

// CallerResolver resolves the already-authenticated caller behind an HTTP
// request to a stable identity key.
//
// This is NOT a token verifier. By the time a request reaches a
// CallerResolver, the consuming domain's own sign-in flow (Keycloak via
// libs/go/htmxauth, Google OIDC via audience_score_system/web/auth, or any
// other obtain-step mechanism) has already established who the caller is —
// a session cookie, a trusted proxy header, whatever that domain's sign-in
// machinery leaves on the request. ResolveCaller's only job is to read that
// already-established identity off r and report it as a stable string key.
//
// A CallerResolver implementation must never perform a fresh token/claims
// verification, and must never talk to an identity provider (no OIDC
// discovery, no token introspection, no round trip to Keycloak/Google/etc.)
// — that work belongs entirely to the domain's sign-in flow, which runs
// before this interface is ever consulted. An implementation that does
// either of those things is a misuse of this interface: it duplicates work
// the domain's sign-in flow already did, and it reintroduces the very IdP
// dependency mcpauth is designed to stay free of (see mcpauth.go's package
// doc, "What this library deliberately is not").
type CallerResolver interface {
	// ResolveCaller returns the stable identity key of the caller already
	// authenticated by the consuming domain's sign-in flow, and whether
	// resolution succeeded. ok is false when r carries no recognizable
	// established identity (e.g. no session, an expired session cookie the
	// domain's own session store already rejected) — in that case identity
	// must be ignored by the caller.
	ResolveCaller(r *http.Request) (identity string, ok bool)
}

// CallerResolverFunc adapts a plain function to CallerResolver, mirroring
// the http.HandlerFunc pattern.
//
// Example — a resolver backed by an existing session cookie (the
// domain's sign-in flow has already validated the session; this merely
// reads the identity it left behind):
//
//	resolver := mcpauth.CallerResolverFunc(func(r *http.Request) (string, bool) {
//		sess, ok := sessions.FromRequest(r)
//		if !ok {
//			return "", false
//		}
//		return sess.UserID, true
//	})
//
// A resolver that instead validates a freshly presented bearer/ID token
// against an identity provider (an OIDC round trip, a JWKS fetch, etc.) is
// a misuse of CallerResolver — see the interface doc.
type CallerResolverFunc func(r *http.Request) (identity string, ok bool)

// ResolveCaller calls f.
func (f CallerResolverFunc) ResolveCaller(r *http.Request) (identity string, ok bool) {
	return f(r)
}
