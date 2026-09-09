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
// and `mcp` (a dependent task's verification middleware, which decodes an
// already-verified credential's identity back) cannot drift apart on the
// format.
package mcpidentity

import (
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
