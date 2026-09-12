// Package mcpidentity encodes and decodes the (iss, sub) identity pair
// whagent-net's MCP OAuth2 authorization server (FR9/C27, issue #2245)
// resolves a signed-in operator to.
//
// NFR7 is the reason this package exists at all: the identity a
// mcpauth.CredentialStore/mcpauth.AuthCodeStore row holds must be exactly
// the LB2 (iss, sub) pair whagent_net/session.Subject and
// sessions.subject_iss/subject_sub already use -- never a new
// whagent-net-only user/person id, keyed on the credential or otherwise.
// mcpauth.CredentialStore.Identity is a plain opaque string (see
// libs/go/mcpauth's package doc, "zero domain-specific types"), so that
// pair has to be packed into one string somehow; this package is the one
// place that packing happens, so `ui` (whagent_net/ui/mcpauth.go's
// CallerResolver, which encodes the signed-in operator's own (iss, sub))
// and `mcp` (whagent_net/mcp/server/auth.go's NewVerifier, which decodes an
// already-verified credential's identity back) cannot drift apart on the
// format.
//
// It also carries that same (iss, sub) pair across the one other package
// boundary within `mcp` that needs it unpacked (Identity/ContextWithIdentity/
// FromContext below, issue #2430) -- see Identity's own doc comment for why
// that lives here rather than in mcp/server or mcp/tools directly.
package mcpidentity

import (
	"context"
	"fmt"
	"strings"
)

// separator joins the encoded (iss, sub) pair. A literal "|" cannot
// appear unescaped in a Keycloak issuer URL -- RFC 3986 doesn't permit a
// raw pipe character in a URI, it would have to be percent-encoded -- or
// in a Keycloak subject (a UUID), so splitting on it is unambiguous for
// every value either side of this package actually produces. Encode and
// Decode both validate this assumption explicitly rather than silently
// trusting it (see their doc comments) so a future issuer/subject shape
// that violates it fails loudly instead of corrupting the pair.
const separator = "|"

// Encode packs iss and sub into the single opaque string
// mcpauth.CredentialStore and mcpauth.AuthCodeStore store as Identity.
// It fails loudly -- rather than silently producing a string Decode could
// misparse -- if either part is empty or already contains separator.
func Encode(iss, sub string) (string, error) {
	if iss == "" {
		return "", fmt.Errorf("mcpidentity: iss must not be empty")
	}
	if sub == "" {
		return "", fmt.Errorf("mcpidentity: sub must not be empty")
	}
	if strings.Contains(iss, separator) {
		return "", fmt.Errorf("mcpidentity: iss must not contain %q: %q", separator, iss)
	}
	if strings.Contains(sub, separator) {
		return "", fmt.Errorf("mcpidentity: sub must not contain %q: %q", separator, sub)
	}
	return iss + separator + sub, nil
}

// Decode reverses Encode. It rejects anything that isn't exactly two
// non-empty parts joined by separator -- including a malformed or
// tampered identity string -- rather than guessing which part is which.
func Decode(encoded string) (iss, sub string, err error) {
	parts := strings.Split(encoded, separator)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("mcpidentity: malformed encoded identity (expected exactly one %q separator): %q", separator, encoded)
	}
	iss, sub = parts[0], parts[1]
	if iss == "" || sub == "" {
		return "", "", fmt.Errorf("mcpidentity: malformed encoded identity (empty iss or sub): %q", encoded)
	}
	return iss, sub, nil
}

// Identity is a resolved (iss, sub) pair carried on a context.Context
// between whagent_net/mcp/server's AuthMiddleware (the producer, for the
// browser-OAuth2 credential path only -- issue #2430, FR7/FR8) and
// whagent_net/mcp/tools' dispatch-time handlers (the consumer). It is
// deliberately not this package's packed single-string encoding
// (Encode/Decode above): that format exists solely for
// mcpauth.CredentialStore/mcpauth.AuthCodeStore's Identity column (NFR7),
// an unrelated persistence concern, whereas this type never leaves process
// memory.
//
// mcp/server and mcp/tools are otherwise deliberately decoupled -- neither
// imports the other (see mcp/tools/scope.go and mcp/tools/grant.go's own
// doc comments for why the ScopeResolver/GrantSource seams are duplicated
// interface literals rather than shared types) -- so this context-carrying
// pair lives here, in the one small dependency-free package both already
// import, mirroring libs/go/grpcauth's own
// ClaimsFromContext/ContextWithClaims/WithUserToken pattern for crossing
// an analogous package boundary.
type Identity struct {
	Iss string
	Sub string
}

// identityContextKey is the unexported context key ContextWithIdentity/
// FromContext use.
type identityContextKey struct{}

// ContextWithIdentity returns a context carrying id. AuthMiddleware calls
// this exactly once, for the browser-OAuth2 path only -- the manual-token
// path (a real bearer token forwarded byte for byte) never carries an
// Identity, by design (see FromContext's doc comment).
func ContextWithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, id)
}

// FromContext retrieves the Identity ContextWithIdentity placed on ctx, if
// any. A tool handler's absence check (the second return value) is exactly
// how it distinguishes the two auth paths at dispatch time (issue #2430's
// FR7/FR8 sequence): present means the browser-OAuth2 path, so the handler
// must resolve a scope and acquire a token via GrantSource before
// forwarding; absent means the manual-token path, where AuthMiddleware
// already placed a real, usable bearer token on ctx directly (grpcauth.
// WithUserToken) and no further resolution is needed or possible (there is
// no caller identity here for a GrantSource lookup to key off of).
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityContextKey{}).(Identity)
	return id, ok
}
