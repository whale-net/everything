package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	upstreamCallbackPath = "/oauth/callback"
	sealPurposeState     = "state"
	sealPurposeCode      = "code"

	defaultUpstreamStateTTL = 10 * time.Minute
)

// UpstreamConfig configures an OAuth authorization server for an MCP server
// whose callers already hold identities in an upstream OIDC provider
// (Keycloak). MCP clients register with this server (RFC 7591) and run the
// authorization-code + PKCE flow against it; it delegates sign-in to the
// upstream with one confidential server-side client and hands the upstream's
// own access/refresh tokens back to the MCP client. The MCP resource server
// therefore keeps verifying upstream JWTs (audience, roles) unchanged, and
// the upstream's dynamic-registration policy never applies to MCP clients.
type UpstreamConfig struct {
	// Issuer is this authorization server's public URL (the MCP server's own
	// origin), no trailing slash. Required.
	Issuer string
	// Resource is the MCP resource identifier; defaults to Issuer.
	Resource     string
	ResourceName string

	// Upstream endpoints and the confidential client registered there. The
	// client's redirect URI must be Issuer + "/oauth/callback".
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string // defaults to ["openid"]

	// Sealer carries pending-authorization state, dynamic client
	// registrations and authorization codes between requests. Replicas must
	// share its secret.
	Sealer *Sealer

	// CodeTTL bounds an issued authorization code (default 60s).
	CodeTTL    time.Duration
	HTTPClient *http.Client
}

// UpstreamProvider serves /register, /authorize, /oauth/callback, /token and
// RFC 8414 metadata on behalf of an upstream OIDC provider.
type UpstreamProvider struct {
	cfg  UpstreamConfig
	base *Provider
}

// NewUpstreamProvider validates cfg and builds the provider.
func NewUpstreamProvider(cfg UpstreamConfig) (*UpstreamProvider, error) {
	if cfg.Sealer == nil {
		return nil, errors.New("auth: UpstreamConfig.Sealer is required")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.AuthorizeURL == "" || cfg.TokenURL == "" {
		return nil, errors.New("auth: UpstreamConfig needs AuthorizeURL, TokenURL, ClientID and ClientSecret")
	}
	if cfg.Resource == "" {
		cfg.Resource = cfg.Issuer
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid"}
	}
	if cfg.CodeTTL == 0 {
		cfg.CodeTTL = defaultAuthCodeTTL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	pc := ProviderConfig{
		Issuer:              cfg.Issuer,
		Resource:            cfg.Resource,
		ResourceName:        cfg.ResourceName,
		Clients:             NewSealedClientRegistry(cfg.Sealer),
		GrantTypesSupported: []string{"authorization_code", "refresh_token"},
	}
	for _, u := range []string{pc.Issuer, pc.Resource} {
		if err := validateAbsoluteURL(u); err != nil {
			return nil, fmt.Errorf("auth: %q is invalid: %w", u, err)
		}
	}
	if strings.HasSuffix(pc.Issuer, "/") {
		return nil, fmt.Errorf("auth: Issuer %q must not have a trailing slash", pc.Issuer)
	}
	return &UpstreamProvider{cfg: cfg, base: &Provider{cfg: pc}}, nil
}

// Mount registers the authorization-server endpoints on mux. Protected-
// resource metadata is the resource server's concern (MountProtectedResourceMetadata).
func (u *UpstreamProvider) Mount(mux *http.ServeMux) {
	mux.Handle(authServerMetadataPath, u.base.authServerMetadataHandler())
	mux.HandleFunc("POST "+registerPath, u.base.handleRegister)
	mux.HandleFunc("GET "+authorizePath, u.handleAuthorize)
	mux.HandleFunc("GET "+upstreamCallbackPath, u.handleCallback)
	mux.HandleFunc("POST "+tokenPath, u.handleToken)
}

type upstreamState struct {
	ClientID      string    `json:"c"`
	RedirectURI   string    `json:"r"`
	State         string    `json:"s"`
	CodeChallenge string    `json:"cc"`
	Verifier      string    `json:"v"` // PKCE verifier for the upstream leg
	Expires       time.Time `json:"e"`
}

// upstreamTokens is the upstream token endpoint's success body, relayed
// verbatim to the MCP client.
type upstreamTokens struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

type upstreamCode struct {
	ClientID      string         `json:"c"`
	RedirectURI   string         `json:"r"`
	CodeChallenge string         `json:"cc"`
	Tokens        upstreamTokens `json:"t"`
	Expires       time.Time      `json:"e"`
}

func (u *UpstreamProvider) callbackURL() string { return u.cfg.Issuer + upstreamCallbackPath }

func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (u *UpstreamProvider) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	client, err := u.base.cfg.Clients.Get(r.Context(), q.Get("client_id"))
	if err != nil {
		http.Error(w, "auth: unknown or unregistered client_id", http.StatusBadRequest)
		return
	}
	redirectURI := q.Get("redirect_uri")
	if !redirectURIRegistered(client, redirectURI) {
		http.Error(w, "auth: redirect_uri is not registered for this client_id", http.StatusBadRequest)
		return
	}
	state := q.Get("state")
	if q.Get("response_type") != "code" {
		writeAuthorizeRedirectError(w, r, redirectURI, state, "unsupported_response_type")
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		writeAuthorizeRedirectError(w, r, redirectURI, state, "invalid_request")
		return
	}

	verifier, err := generateAuthCode()
	if err != nil {
		writeAuthorizeRedirectError(w, r, redirectURI, state, "server_error")
		return
	}
	sealed, err := u.cfg.Sealer.Seal(sealPurposeState, upstreamState{
		ClientID: client.ClientID, RedirectURI: redirectURI, State: state,
		CodeChallenge: q.Get("code_challenge"), Verifier: verifier,
		Expires: time.Now().Add(defaultUpstreamStateTTL),
	})
	if err != nil {
		writeAuthorizeRedirectError(w, r, redirectURI, state, "server_error")
		return
	}

	dest, err := url.Parse(u.cfg.AuthorizeURL)
	if err != nil {
		http.Error(w, "auth: server misconfiguration (invalid upstream authorize URL)", http.StatusInternalServerError)
		return
	}
	v := dest.Query()
	v.Set("response_type", "code")
	v.Set("client_id", u.cfg.ClientID)
	v.Set("redirect_uri", u.callbackURL())
	v.Set("scope", strings.Join(u.cfg.Scopes, " "))
	v.Set("state", sealed)
	v.Set("code_challenge", s256(verifier))
	v.Set("code_challenge_method", "S256")
	dest.RawQuery = v.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

func (u *UpstreamProvider) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var st upstreamState
	if err := u.cfg.Sealer.Open(sealPurposeState, q.Get("state"), &st); err != nil || time.Now().After(st.Expires) {
		http.Error(w, "auth: invalid or expired authorization state; restart sign-in", http.StatusBadRequest)
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		writeAuthorizeRedirectError(w, r, st.RedirectURI, st.State, "access_denied")
		return
	}
	tokens, err := u.upstreamToken(r.Context(), url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {q.Get("code")},
		"redirect_uri":  {u.callbackURL()},
		"code_verifier": {st.Verifier},
	})
	if err != nil {
		writeAuthorizeRedirectError(w, r, st.RedirectURI, st.State, "server_error")
		return
	}
	code, err := u.cfg.Sealer.Seal(sealPurposeCode, upstreamCode{
		ClientID: st.ClientID, RedirectURI: st.RedirectURI, CodeChallenge: st.CodeChallenge,
		Tokens: tokens, Expires: time.Now().Add(u.cfg.CodeTTL),
	})
	if err != nil {
		writeAuthorizeRedirectError(w, r, st.RedirectURI, st.State, "server_error")
		return
	}
	dest, err := url.Parse(st.RedirectURI)
	if err != nil {
		http.Error(w, "auth: invalid redirect_uri", http.StatusBadRequest)
		return
	}
	v := dest.Query()
	v.Set("code", code)
	if st.State != "" {
		v.Set("state", st.State)
	}
	dest.RawQuery = v.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

func (u *UpstreamProvider) handleToken(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || r.ParseForm() != nil {
		writeTokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	f := r.PostForm
	var tokens upstreamTokens
	switch f.Get("grant_type") {
	case "authorization_code":
		var c upstreamCode
		if err := u.cfg.Sealer.Open(sealPurposeCode, f.Get("code"), &c); err != nil ||
			time.Now().After(c.Expires) ||
			c.ClientID != f.Get("client_id") ||
			c.RedirectURI != f.Get("redirect_uri") ||
			f.Get("code_verifier") == "" ||
			!verifyCodeChallenge(f.Get("code_verifier"), c.CodeChallenge) {
			writeInvalidGrant(w)
			return
		}
		tokens = c.Tokens
	case "refresh_token":
		if _, err := u.base.cfg.Clients.Get(r.Context(), f.Get("client_id")); err != nil || f.Get("refresh_token") == "" {
			writeTokenError(w, http.StatusBadRequest, "invalid_client")
			return
		}
		t, err := u.upstreamToken(r.Context(), url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {f.Get("refresh_token")},
		})
		if err != nil {
			writeInvalidGrant(w)
			return
		}
		tokens = t
	default:
		writeTokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(tokens)
}

// upstreamToken calls the upstream token endpoint as the confidential client.
func (u *UpstreamProvider) upstreamToken(ctx context.Context, form url.Values) (upstreamTokens, error) {
	form.Set("client_id", u.cfg.ClientID)
	form.Set("client_secret", u.cfg.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return upstreamTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := u.cfg.HTTPClient.Do(req)
	if err != nil {
		return upstreamTokens{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return upstreamTokens{}, fmt.Errorf("auth: upstream token endpoint returned %d", resp.StatusCode)
	}
	var t upstreamTokens
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil || t.AccessToken == "" {
		return upstreamTokens{}, errors.New("auth: upstream token response invalid")
	}
	if t.TokenType == "" {
		t.TokenType = "Bearer"
	}
	return t, nil
}

// DiscoverUpstreamEndpoints reads authorize/token URLs from an OIDC issuer's
// discovery document.
func DiscoverUpstreamEndpoints(ctx context.Context, client *http.Client, issuer string) (authorizeURL, tokenURL string, err error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var doc struct {
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&doc) != nil || doc.Authorization == "" || doc.Token == "" {
		return "", "", fmt.Errorf("auth: OIDC discovery for %q failed", issuer)
	}
	return doc.Authorization, doc.Token, nil
}


// Env vars read by NewUpstreamProviderFromEnv; shared by every MCP server so
// they are configured identically.
const (
	EnvUpstreamClientID     = "MCP_OAUTH_CLIENT_ID"
	EnvUpstreamClientSecret = "MCP_OAUTH_CLIENT_SECRET"
	EnvUpstreamStateKey     = "MCP_OAUTH_STATE_KEY"
)

// NewUpstreamProviderFromEnv builds an UpstreamProvider for an MCP server
// served at publicURL, delegating sign-in to the OIDC provider at
// upstreamIssuer. It returns (nil, nil) when EnvUpstreamClientID is unset,
// so a server can keep its previous behaviour until the client exists.
func NewUpstreamProviderFromEnv(ctx context.Context, getenv func(string) string, upstreamIssuer, publicURL, resourceName string) (*UpstreamProvider, error) {
	clientID := getenv(EnvUpstreamClientID)
	if clientID == "" {
		return nil, nil
	}
	sealer, err := NewSealer(getenv(EnvUpstreamStateKey))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvUpstreamStateKey, err)
	}
	authorizeURL, tokenURL, err := DiscoverUpstreamEndpoints(ctx, nil, upstreamIssuer)
	if err != nil {
		return nil, err
	}
	return NewUpstreamProvider(UpstreamConfig{
		Issuer:       strings.TrimRight(publicURL, "/"),
		ResourceName: resourceName,
		AuthorizeURL: authorizeURL,
		TokenURL:     tokenURL,
		ClientID:     clientID,
		ClientSecret: getenv(EnvUpstreamClientSecret),
		Sealer:       sealer,
	})
}
