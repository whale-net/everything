package mcpauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// verifyCodeChallenge reports whether verifier — the PKCE code_verifier
// presented at POST /token (token.go) — matches challenge, the
// code_challenge stored on the AuthCode when GET /authorize
// (authorize.go) minted it. Per RFC 7636 §4.6 S256, challenge must equal
// base64url(sha256(verifier)) with no padding, compared in constant time
// (crypto/subtle) so a timing side channel cannot leak how many leading
// bytes matched.
func verifyCodeChallenge(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	// subtle.ConstantTimeCompare requires equal-length inputs to give a
	// constant-time guarantee; a length mismatch is itself not
	// security-sensitive information (challenge length is public — it is
	// echoed back in the /authorize redirect and observable by anyone
	// watching that traffic), so short-circuiting on it does not
	// reintroduce the timing side channel this function exists to avoid.
	if len(computed) != len(challenge) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}
