// Package identity encodes and decodes the (iss, sub) identity pair
// krill/ui's mcpauth OAuth2 authorization-server front door (the
// mcpauth.CallerResolver in krill/ui/mcpauth.go) resolves a signed-in
// operator to.
//
// mcpauth.CredentialStore/mcpauth.AuthCodeStore's Identity column is a
// plain opaque string (libs/go/mcpauth's package doc, "zero
// domain-specific types") -- krill has no person/user table to key it to
// (NFR1 authorizes by persona, never by individual identity), so this
// package is where the signed-in operator's (iss, sub) pair gets packed
// into that string, mirroring whagent_net/mcpidentity's own encoding
// exactly (same separator, same validation) so a future cross-domain
// comparison of the two never has to reconcile two different formats.
package identity

import (
	"fmt"
	"strings"
)

// separator joins the encoded (iss, sub) pair. A literal "|" cannot
// appear unescaped in a Keycloak issuer URL (RFC 3986 forbids a raw pipe
// in a URI) or in a Keycloak subject (a UUID), so splitting on it is
// unambiguous for every value either side of this package actually
// produces.
const separator = "|"

// Encode packs iss and sub into the single opaque string
// mcpauth.CredentialStore and mcpauth.AuthCodeStore store as Identity. It
// fails loudly -- rather than silently producing a string Decode could
// misparse -- if either part is empty or already contains separator.
func Encode(iss, sub string) (string, error) {
	if iss == "" {
		return "", fmt.Errorf("identity: iss must not be empty")
	}
	if sub == "" {
		return "", fmt.Errorf("identity: sub must not be empty")
	}
	if strings.Contains(iss, separator) {
		return "", fmt.Errorf("identity: iss must not contain %q: %q", separator, iss)
	}
	if strings.Contains(sub, separator) {
		return "", fmt.Errorf("identity: sub must not contain %q: %q", separator, sub)
	}
	return iss + separator + sub, nil
}

// Decode reverses Encode. It rejects anything that isn't exactly two
// non-empty parts joined by separator -- including a malformed or
// tampered identity string -- rather than guessing which part is which.
func Decode(encoded string) (iss, sub string, err error) {
	parts := strings.Split(encoded, separator)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("identity: malformed encoded identity (expected exactly one %q separator): %q", separator, encoded)
	}
	iss, sub = parts[0], parts[1]
	if iss == "" || sub == "" {
		return "", "", fmt.Errorf("identity: malformed encoded identity (empty iss or sub): %q", encoded)
	}
	return iss, sub, nil
}
