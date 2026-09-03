package mcpauth

import "net/http"

// handleToken serves the OAuth2 authorization-code + PKCE POST /token
// endpoint: requires grant_type=authorization_code (any other grant_type,
// including refresh_token and client_credentials, is rejected —
// unsupported_grant_type; FR4 — this package never introduces a refresh
// lifecycle), atomically consumes the single-use authorization code,
// verifies it against code_verifier (verifyCodeChallenge, pkce.go) plus
// client_id/redirect_uri/expiry, and on success mints the long-lived
// bearer credential via ProviderConfig.Credentials.Mint and responds with
// exactly {"access_token", "token_type": "Bearer"} — no expires_in, no
// refresh_token, no fabricated scope (FR4). See #1642's "Implementation"
// section for the exact RFC 6749 §5.2 error-body contract: every failure
// mode (expired code, mismatched client_id/redirect_uri, wrong
// code_verifier, unknown code) must render a byte-identical invalid_grant
// body.
//
// Scaffold note: stubbed to 501 pending Implementation.
func (p *Provider) handleToken(w http.ResponseWriter, r *http.Request) {
	notImplementedHandler(w, r)
}
