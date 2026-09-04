package mcpauth

import (
	"net/http"
	"net/url"
	"time"
)

// handleAuthorize serves the OAuth2 authorization-code + PKCE GET
// /authorize endpoint: resolves the already-signed-in caller via
// ProviderConfig.Resolver (FR2 — never a login form, never a password
// prompt), validates response_type/client_id/redirect_uri/code_challenge/
// code_challenge_method, and on success mints a single-use authorization
// code and 302s back to the client's redirect_uri with `code` and the
// echoed `state`.
//
// Error-handling contract (OAuth 2.0 §4.1.2.1): an invalid or unregistered
// client_id/redirect_uri is rendered directly to the user agent (never
// redirected — that would be an open redirect). Every other error is only
// reachable once client_id and redirect_uri have both been validated, and
// redirects to that validated redirect_uri with `error=` + `state=`.
//
// Never logs the raw authorization code (NFR1) — nothing in this function
// or its callees logs at all.
func (p *Provider) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Step 1 (#1642): resolve the already-signed-in caller. This must run
	// before any parameter validation below — FR2 requires /authorize to
	// never render a login form or collect credentials itself, so an
	// unresolved caller is sent to SignInURL (or 401'd) regardless of
	// whether the request's other parameters are even well-formed.
	identity, ok := p.cfg.Resolver.ResolveCaller(r)
	if !ok {
		if p.cfg.SignInURL == "" {
			http.Error(w, "mcpauth: no established session and no SignInURL configured", http.StatusUnauthorized)
			return
		}

		signIn, err := url.Parse(p.cfg.SignInURL)
		if err != nil {
			http.Error(w, "mcpauth: server misconfiguration (invalid SignInURL)", http.StatusInternalServerError)
			return
		}
		// The return-to target is this exact /authorize request — query
		// string intact — reconstructed from the validated Issuer (not
		// r.Host/r.TLS, which a request can spoof) plus the raw
		// request-target the client actually sent.
		returnTo := p.cfg.Issuer + r.URL.RequestURI()
		values := signIn.Query()
		values.Set(p.cfg.SignInReturnParam, returnTo)
		signIn.RawQuery = values.Encode()

		http.Redirect(w, r, signIn.String(), http.StatusFound)
		return
	}

	// Step 2a (#1642): client_id and redirect_uri validate first, and
	// unregistered/mismatched values render directly rather than
	// redirecting — until both are known-good, there is no
	// attacker-controlled URL that is safe to redirect to (open-redirect
	// prevention, OAuth 2.0 §4.1.2.1).
	clientID := q.Get("client_id")
	client, err := p.cfg.Clients.Get(r.Context(), clientID)
	if err != nil {
		http.Error(w, "mcpauth: unknown or unregistered client_id", http.StatusBadRequest)
		return
	}

	redirectURI := q.Get("redirect_uri")
	if !redirectURIRegistered(client, redirectURI) {
		http.Error(w, "mcpauth: redirect_uri is not registered for this client_id", http.StatusBadRequest)
		return
	}

	// Step 2b (#1642): every remaining validation failure is safe to
	// redirect to the now-validated redirectURI, per §4.1.2.1.
	state := q.Get("state")

	if q.Get("response_type") != "code" {
		writeAuthorizeRedirectError(w, r, redirectURI, state, "unsupported_response_type")
		return
	}

	codeChallenge := q.Get("code_challenge")
	if codeChallenge == "" {
		writeAuthorizeRedirectError(w, r, redirectURI, state, "invalid_request")
		return
	}

	// Only PKCE S256 is supported — "plain" and any other value are
	// rejected outright (#1642's scope: authorization_code + PKCE S256
	// only, no device-code, no client-credentials).
	if q.Get("code_challenge_method") != "S256" {
		writeAuthorizeRedirectError(w, r, redirectURI, state, "invalid_request")
		return
	}

	// Step 3 (#1642): mint a single-use authorization code. The raw code
	// is generated here and handed to the client in the redirect below;
	// only its SHA-256 hash (NFR1) is ever persisted via AuthCodeStore.Save
	// — see AuthCode.Code's doc. Never logged (NFR1).
	rawCode, err := generateAuthCode()
	if err != nil {
		writeAuthorizeRedirectError(w, r, redirectURI, state, "server_error")
		return
	}

	authCode := AuthCode{
		Code:                hashToken(rawCode),
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		Identity:            identity,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: "S256",
		ExpiresAt:           time.Now().Add(p.cfg.AuthCodeTTL),
	}
	if err := p.cfg.AuthCodes.Save(r.Context(), authCode); err != nil {
		writeAuthorizeRedirectError(w, r, redirectURI, state, "server_error")
		return
	}

	dest, err := url.Parse(redirectURI)
	if err != nil {
		// redirectURI was already validated against the client's
		// registered, previously-validated (validateRedirectURI,
		// clients.go) redirect_uris, so this is unreachable in practice —
		// guarded defensively rather than assumed.
		http.Error(w, "mcpauth: invalid redirect_uri", http.StatusBadRequest)
		return
	}
	values := dest.Query()
	values.Set("code", rawCode)
	if state != "" {
		values.Set("state", state)
	}
	dest.RawQuery = values.Encode()

	http.Redirect(w, r, dest.String(), http.StatusFound)
}

// redirectURIRegistered reports whether redirectURI exactly matches one of
// client's registered RedirectURIs. Exact string match only (no
// prefix/substring matching) — RFC 6749 §3.1.2.3.
func redirectURIRegistered(client OAuthClient, redirectURI string) bool {
	if redirectURI == "" {
		return false
	}
	for _, registered := range client.RedirectURIs {
		if registered == redirectURI {
			return true
		}
	}
	return false
}

// writeAuthorizeRedirectError 302s to redirectURI with the OAuth 2.0
// §4.1.2.1 error query parameters: `error` always, `state` only if the
// original request carried one (state is echoed back untouched — an empty
// state is treated the same as an absent one, mirroring the success path).
// Only reachable once redirectURI has already been validated against the
// client's registration (see handleAuthorize's Step 2a) — this function
// itself performs no validation and must never be called with an
// unvalidated redirectURI.
func writeAuthorizeRedirectError(w http.ResponseWriter, r *http.Request, redirectURI, state, errorCode string) {
	dest, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "mcpauth: invalid redirect_uri", http.StatusBadRequest)
		return
	}
	values := dest.Query()
	values.Set("error", errorCode)
	if state != "" {
		values.Set("state", state)
	}
	dest.RawQuery = values.Encode()

	http.Redirect(w, r, dest.String(), http.StatusFound)
}
