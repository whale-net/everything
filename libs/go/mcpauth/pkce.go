package mcpauth

// verifyCodeChallenge reports whether verifier — the PKCE code_verifier
// presented at POST /token (token.go) — matches challenge, the
// code_challenge stored on the AuthCode when GET /authorize
// (authorize.go) minted it. Per RFC 7636 §4.6 S256, challenge must equal
// base64url(sha256(verifier)) with no padding, compared in constant time
// (crypto/subtle) so a timing side channel cannot leak how many leading
// bytes matched.
//
// Scaffold note: stubbed to always return false pending the Implementation
// phase of #1642 — see pkce_test.go's RFC 7636 Appendix B test vector once
// Testing lands.
func verifyCodeChallenge(verifier, challenge string) bool {
	return false
}
