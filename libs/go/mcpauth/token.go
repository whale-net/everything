package mcpauth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// tokenErrorBody is the RFC 6749 §5.2 JSON error body every /token failure
// mode renders. error_description is intentionally omitted everywhere in
// this file — every failure (expired code, mismatched client_id/
// redirect_uri, wrong code_verifier, unknown/already-consumed code) must
// produce a byte-identical invalid_grant response (#1642), and the surest
// way to guarantee that is to never populate a field that could vary
// between call sites.
type tokenErrorBody struct {
	Error string `json:"error"`
}

// writeTokenError writes a fixed RFC 6749 §5.2 error body at the given
// status, with Cache-Control: no-store — a /token response, success or
// failure, must never be cached (it can carry a bearer credential).
func writeTokenError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(tokenErrorBody{Error: code})
}

// writeInvalidGrant is the single call site every /token failure mode that
// must be indistinguishable from every other routes through: expired code,
// mismatched client_id, mismatched redirect_uri, wrong code_verifier, and
// unknown/already-consumed code all render this exact body (#1642) so a
// client (or an attacker probing the endpoint) cannot learn which check
// failed.
func writeInvalidGrant(w http.ResponseWriter) {
	writeTokenError(w, http.StatusBadRequest, "invalid_grant")
}

// tokenResponse is the exact, minimal success body #1642 (FR4) requires:
// access_token + token_type only. No expires_in, no refresh_token, no
// scope — mcpauth's credential has no refresh lifecycle (see mcpauth.go's
// package doc, "What this library deliberately is not"), and fabricating
// any of those fields here would silently promise a lifecycle this package
// does not implement.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// handleToken serves the OAuth2 authorization-code + PKCE POST /token
// endpoint: requires grant_type=authorization_code (any other grant_type,
// including refresh_token and client_credentials, is rejected —
// unsupported_grant_type; FR4 — this package never introduces a refresh
// lifecycle), atomically consumes the single-use authorization code,
// verifies it against code_verifier (verifyCodeChallenge, pkce.go) plus
// client_id/redirect_uri/expiry, and on success mints the long-lived
// bearer credential via ProviderConfig.Credentials.Mint and responds with
// exactly {"access_token", "token_type": "Bearer"}.
//
// Never logs the raw code, code_verifier, or minted token (NFR1) — nothing
// in this function or its callees logs at all.
func (p *Provider) handleToken(w http.ResponseWriter, r *http.Request) {
	contentType := r.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		writeTokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	if err := r.ParseForm(); err != nil {
		writeTokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	// grant_type is checked before any other field: an unsupported
	// grant_type is rejected on its own terms, regardless of whether the
	// request also happens to carry an authorization_code-shaped payload
	// (#1642 — device-code and client-credentials are explicitly out of
	// scope and must not be reachable at all).
	if r.PostForm.Get("grant_type") != "authorization_code" {
		writeTokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}

	code := r.PostForm.Get("code")
	codeVerifier := r.PostForm.Get("code_verifier")
	clientID := r.PostForm.Get("client_id")
	redirectURI := r.PostForm.Get("redirect_uri")
	if code == "" || codeVerifier == "" || clientID == "" || redirectURI == "" {
		writeTokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}

	// Consume is atomic and single-use: a second exchange of the same code
	// — concurrent or sequential — always misses here, whether the store is
	// the in-process map (memoryAuthCodeStore) or Postgres (pgxAuthCodeStore,
	// whose DELETE ... RETURNING makes this Postgres's own atomicity
	// guarantee, not an in-process lock).
	authCode, err := p.cfg.AuthCodes.Consume(r.Context(), code)
	if err != nil {
		// Unknown, never-issued, and already-consumed codes all land here
		// and all render the same invalid_grant body as every other
		// failure mode below.
		writeInvalidGrant(w)
		return
	}

	if time.Now().After(authCode.ExpiresAt) {
		writeInvalidGrant(w)
		return
	}
	if authCode.ClientID != clientID {
		writeInvalidGrant(w)
		return
	}
	if authCode.RedirectURI != redirectURI {
		writeInvalidGrant(w)
		return
	}
	if !verifyCodeChallenge(codeVerifier, authCode.CodeChallenge) {
		writeInvalidGrant(w)
		return
	}

	rawToken, _, err := p.cfg.Credentials.Mint(r.Context(), authCode.Identity)
	if err != nil {
		writeTokenError(w, http.StatusInternalServerError, "server_error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(tokenResponse{
		AccessToken: rawToken,
		TokenType:   "Bearer",
	})
}
