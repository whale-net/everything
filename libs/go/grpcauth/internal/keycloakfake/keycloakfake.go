// Package keycloakfake is a reusable fake Keycloak HTTP server for grpcauth's
// delegated-grant tests (NFR4: no test in this plan ever contacts a live
// Keycloak instance). It lives under grpcauth/internal so it is importable
// from both grpcauth and grpcauth/pgstore while staying invisible to the
// rest of the repo.
//
// Server exposes the four endpoints a Keycloak realm serves that the
// delegated-grant flow needs: discovery, authorization, token, and RFC 7009
// revocation. Each endpoint's behaviour is scripted independently via
// SetTokenMode / SetRevokeMode, and every endpoint counts how many times it
// was called so tests can assert a call was never made (e.g. FR7's "revoked
// grants make zero Keycloak calls") -- an assertion a returned-error check
// alone cannot prove.
package keycloakfake

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Mode scripts how an endpoint responds to the next (and every subsequent,
// until changed) call.
type Mode int

const (
	// ModeSuccess returns a normal, successful response.
	ModeSuccess Mode = iota
	// ModeInvalidGrant returns Keycloak's {"error":"invalid_grant"} shape
	// with HTTP 400 -- the response FR8 treats as "the grant itself is no
	// longer valid".
	ModeInvalidGrant
	// ModeServerError returns a bare HTTP 500, simulating a Keycloak outage
	// -- the response FR9 treats as transient.
	ModeServerError
	// ModeConnectionFailure hijacks and closes the connection without
	// writing any response, simulating a network failure -- also treated
	// as transient (FR9).
	ModeConnectionFailure
)

// tokenResponse is the subset of a Keycloak token-endpoint response this
// fake produces and the delegated-grant flow (later tasks) consumes.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

// Server is a fake Keycloak realm backed by an httptest.Server. All methods
// are safe for concurrent use. Callers must Close it (e.g. via t.Cleanup).
type Server struct {
	*httptest.Server

	mu sync.Mutex

	tokenMode  Mode
	revokeMode Mode

	// omitRevocationEndpoint makes the discovery document leave out
	// revocation_endpoint, exercising the "best-effort RFC 7009" path
	// (Endpoints.Revocation ends up empty).
	omitRevocationEndpoint bool

	// nextRefreshToken is returned as the token endpoint's refresh_token,
	// letting tests assert refresh-token rotation write-back (FR13). A
	// fixed default is used until a test overrides it.
	nextRefreshToken string

	// authorizationCode is what /authorize redirects back with.
	authorizationCode string

	// subject/roles configure the claims MintAccessToken (and the token
	// endpoint's success response) bakes into access tokens, for FR4/NFR3
	// assertions.
	subject string
	roles   []string

	discoveryCalls int
	authorizeCalls int
	tokenCalls     int
	revokeCalls    int

	// lastAuthorizeQuery/lastTokenForm capture the most recent /authorize
	// query string and /token form body, so tests can assert PKCE
	// correlation (the code_verifier sent to /token hashes, S256, to the
	// code_challenge sent to /authorize) and that redirect_uri is always
	// the caller-configured value, never something request-supplied.
	lastAuthorizeQuery url.Values
	lastTokenForm      url.Values

	// lastRefreshToken is the refresh_token form value the /token endpoint
	// most recently received, letting tests assert which refresh token an
	// accessor actually sent (rotated vs. original, or the right subject's
	// vs. another's -- FR13/NFR3).
	lastRefreshToken string

	signingKey *ecdsa.PrivateKey
}

// New starts a fake Keycloak realm. Defaults: ModeSuccess for both token and
// revoke endpoints, a discovery document that includes revocation_endpoint,
// subject "fake-subject" with no roles, and a fixed rotated refresh token.
func New() (*Server, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("keycloakfake: generate signing key: %w", err)
	}

	s := &Server{
		nextRefreshToken:  "fake-rotated-refresh-token",
		authorizationCode: "fake-authorization-code",
		subject:           "fake-subject",
		signingKey:        key,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.route))
	return s, nil
}

// Close shuts down the underlying httptest.Server.
func (s *Server) Close() {
	s.Server.Close()
}

// --- configuration ---------------------------------------------------

// SetTokenMode scripts the /token endpoint's next (and subsequent) response.
func (s *Server) SetTokenMode(mode Mode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenMode = mode
}

// SetRevokeMode scripts the /revoke endpoint's next (and subsequent)
// response.
func (s *Server) SetRevokeMode(mode Mode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokeMode = mode
}

// SetNextRefreshToken sets the refresh_token value a successful /token
// response carries, so a test can assert a specific rotated value was
// persisted (FR13's refresh write-back).
func (s *Server) SetNextRefreshToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextRefreshToken = token
}

// SetAuthorizationCode sets the code value /authorize redirects back with.
func (s *Server) SetAuthorizationCode(code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authorizationCode = code
}

// SetSubject configures the sub and realm_access.roles claims a successful
// /token response's access_token (and MintAccessToken) carries, for FR4/
// NFR3 assertions.
func (s *Server) SetSubject(subject string, roles []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subject = subject
	s.roles = roles
}

// OmitRevocationEndpoint makes the discovery document leave out
// revocation_endpoint entirely, so a caller resolving endpoints against this
// fake exercises the "missing revocation_endpoint is not fatal" path.
func (s *Server) OmitRevocationEndpoint() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.omitRevocationEndpoint = true
}

// --- call counters -----------------------------------------------------

// DiscoveryCalls returns how many times /.well-known/openid-configuration
// was requested.
func (s *Server) DiscoveryCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discoveryCalls
}

// AuthorizeCalls returns how many times /authorize was requested.
func (s *Server) AuthorizeCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authorizeCalls
}

// TokenCalls returns how many times /token was requested. FR7's short-
// circuit tests assert this stays 0 for a revoked/needs_reauth grant.
func (s *Server) TokenCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokenCalls
}

// RevokeCalls returns how many times /revoke was requested.
func (s *Server) RevokeCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revokeCalls
}

// LastAuthorizeQuery returns the query string of the most recent /authorize
// request, or nil if /authorize has never been called. Used to assert PKCE's
// code_challenge/code_challenge_method and redirect_uri actually arrived as
// sent.
func (s *Server) LastAuthorizeQuery() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAuthorizeQuery
}

// LastTokenForm returns the parsed form body of the most recent /token
// request, or nil if /token has never been called. Used to assert the
// code_verifier sent on exchange correlates with the code_challenge sent on
// authorize, and that redirect_uri on exchange matches the configured value.
func (s *Server) LastTokenForm() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTokenForm
}

// LastRefreshToken returns the refresh_token form value the /token endpoint
// most recently received (empty if /token has never been called).
func (s *Server) LastRefreshToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRefreshToken
}

// --- token minting -------------------------------------------------------

// accessTokenClaims is the subset of Keycloak access-token claims this fake
// mints -- enough for FR4 (same subject/roles as a live session) and NFR3
// (identity isolation) assertions to decode.
type accessTokenClaims struct {
	Iss         string   `json:"iss"`
	Sub         string   `json:"sub"`
	Exp         int64    `json:"exp"`
	Iat         int64    `json:"iat"`
	RealmAccess struct { //nolint:govet // field ordering matches Keycloak's own claim shape
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// MintAccessToken signs and returns a JWT access token carrying sub and
// realm_access.roles, independent of any HTTP call -- for tests that need a
// token with specific claims without driving it through /token.
func (s *Server) MintAccessToken(sub string, roles []string) (string, error) {
	s.mu.Lock()
	baseURL := ""
	if s.Server != nil {
		baseURL = s.Server.URL
	}
	key := s.signingKey
	s.mu.Unlock()

	now := time.Now()
	claims := accessTokenClaims{
		Iss: baseURL,
		Sub: sub,
		Exp: now.Add(time.Hour).Unix(),
		Iat: now.Add(-time.Minute).Unix(),
	}
	claims.RealmAccess.Roles = roles

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("keycloakfake: marshal claims: %w", err)
	}

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, nil)
	if err != nil {
		return "", fmt.Errorf("keycloakfake: create signer: %w", err)
	}

	jws, err := signer.Sign(payload)
	if err != nil {
		return "", fmt.Errorf("keycloakfake: sign token: %w", err)
	}

	raw, err := jws.CompactSerialize()
	if err != nil {
		return "", fmt.Errorf("keycloakfake: serialize token: %w", err)
	}
	return raw, nil
}

// --- routing -------------------------------------------------------------

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		s.handleDiscovery(w, r)
	case "/authorize":
		s.handleAuthorize(w, r)
	case "/token":
		s.handleToken(w, r)
	case "/revoke":
		s.handleRevoke(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.discoveryCalls++
	baseURL := s.Server.URL
	omitRevocation := s.omitRevocationEndpoint
	s.mu.Unlock()

	doc := map[string]string{
		"issuer":                 baseURL,
		"authorization_endpoint": baseURL + "/authorize",
		"token_endpoint":         baseURL + "/token",
	}
	if !omitRevocation {
		doc["revocation_endpoint"] = baseURL + "/revoke"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.authorizeCalls++
	s.lastAuthorizeQuery = r.URL.Query()
	code := s.authorizationCode
	s.mu.Unlock()

	redirectURI := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	if redirectURI == "" {
		http.Error(w, "missing redirect_uri", http.StatusBadRequest)
		return
	}

	sep := "?"
	if strings.Contains(redirectURI, "?") {
		sep = "&"
	}
	location := fmt.Sprintf("%s%scode=%s&state=%s", redirectURI, sep, code, state)
	http.Redirect(w, r, location, http.StatusFound)
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	receivedRefreshToken := r.PostFormValue("refresh_token")

	s.mu.Lock()
	s.tokenCalls++
	s.lastTokenForm = r.Form
	s.lastRefreshToken = receivedRefreshToken
	mode := s.tokenMode
	refreshToken := s.nextRefreshToken
	subject := s.subject
	roles := s.roles
	s.mu.Unlock()

	if !writeModeResponse(w, mode) {
		return
	}

	accessToken, err := s.MintAccessToken(subject, roles)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tokenResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    300,
	})
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.revokeCalls++
	mode := s.revokeMode
	s.mu.Unlock()

	if !writeModeResponse(w, mode) {
		return
	}

	// RFC 7009: a successful revocation response is HTTP 200 with an empty
	// body, regardless of whether the token was valid to begin with.
	w.WriteHeader(http.StatusOK)
}

// writeModeResponse writes the response for a non-success Mode and reports
// whether the caller should still write its own success body (true only for
// ModeSuccess). ModeConnectionFailure hijacks and closes the connection
// itself rather than writing any HTTP response, simulating a network
// failure.
func writeModeResponse(w http.ResponseWriter, mode Mode) bool {
	switch mode {
	case ModeSuccess:
		return true
	case ModeInvalidGrant:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		return false
	case ModeServerError:
		w.WriteHeader(http.StatusInternalServerError)
		return false
	case ModeConnectionFailure:
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
				return false
			}
		}
		// Hijacking isn't always available (e.g. under HTTP/2 test
		// transports); fall back to a plain 500 so the call still fails.
		w.WriteHeader(http.StatusInternalServerError)
		return false
	default:
		w.WriteHeader(http.StatusInternalServerError)
		return false
	}
}
