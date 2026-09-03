package mcpauth

import "net/http"

// handleAuthorize serves the OAuth2 authorization-code + PKCE GET
// /authorize endpoint: resolves the already-signed-in caller via
// ProviderConfig.Resolver (FR2 — never a login form, never a password
// prompt), validates response_type/client_id/redirect_uri/code_challenge/
// code_challenge_method, and on success mints a single-use authorization
// code and 302s back to the client's redirect_uri with `code` and the
// echoed `state`. See #1642's "Implementation" section for the exact OAuth
// 2.0 §4.1.2.1 error-handling contract this must follow: an invalid or
// unregistered client_id/redirect_uri is rendered directly to the user
// agent (never redirected — that would be an open redirect), while every
// other error redirects to the validated redirect_uri with
// error=&state=.
//
// Scaffold note: stubbed to 501 pending Implementation.
func (p *Provider) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	notImplementedHandler(w, r)
}
