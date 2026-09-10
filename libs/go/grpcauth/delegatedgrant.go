package grpcauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// defaultHTTPTimeout bounds discovery and (in later tasks) token/revocation
// calls when the caller does not supply its own HTTPClient.
const defaultHTTPTimeout = 15 * time.Second

// offlineAccessScope is always present in the resolved scope set (FR1):
// without it Keycloak does not issue a refresh token usable for
// non-interactive refresh (#2386).
const offlineAccessScope = "offline_access"

// Delegated-grant config validation errors. Each is distinct so a caller (or
// a table test) can tell which field was missing without string matching.
var (
	// ErrConfigIssuerRequired is returned when Issuer is empty and Endpoints
	// is not fully populated, so there is nothing to discover from.
	ErrConfigIssuerRequired = errors.New("grpcauth: DelegatedGrantConfig.Issuer is required when Endpoints is not fully populated")

	// ErrConfigClientIDRequired is returned when ClientID is empty.
	ErrConfigClientIDRequired = errors.New("grpcauth: DelegatedGrantConfig.ClientID is required")

	// ErrConfigClientSecretRequired is returned when ClientSecret is empty.
	// FR14 requires a confidential client; this package offers no
	// public-client (no-secret) mode.
	ErrConfigClientSecretRequired = errors.New("grpcauth: DelegatedGrantConfig.ClientSecret is required")

	// ErrConfigRedirectURIRequired is returned when RedirectURI is empty.
	ErrConfigRedirectURIRequired = errors.New("grpcauth: DelegatedGrantConfig.RedirectURI is required")

	// ErrConfigStoreRequired is returned when Store is nil.
	ErrConfigStoreRequired = errors.New("grpcauth: DelegatedGrantConfig.Store is required")

	// ErrConfigEncryptionKeySize is returned when EncryptionKey is set but is
	// not exactly GrantKeySize bytes. It reports only the expected size,
	// never any part of the supplied key (NFR1).
	ErrConfigEncryptionKeySize = fmt.Errorf("grpcauth: DelegatedGrantConfig.EncryptionKey must be %d bytes when set", GrantKeySize)
)

// Endpoints is the set of Keycloak URLs the delegated-grant flow talks to.
// Either supplied verbatim by the caller (this is how tests point the flow
// at internal/keycloakfake instead of a live Keycloak) or discovered once at
// construction from Issuer's OIDC discovery document.
type Endpoints struct {
	// Authorization is the browser-facing authorization endpoint the
	// interactive consent leg (#2384) redirects the grantor to.
	Authorization string

	// Token is the token endpoint used for the authorization-code exchange
	// (#2384) and for non-interactive refresh (#2386).
	Token string

	// Revocation is the RFC 7009 token-revocation endpoint used by
	// Store.Revoke's best-effort remote call (FR13). It may be empty: a
	// discovery document without a revocation_endpoint is not fatal at
	// construction (RFC 7009 support is best-effort per FR13) -- an empty
	// value here is how the revoke path later knows to log a WARNING and
	// skip the remote call instead of erroring.
	Revocation string
}

// fullyPopulated reports whether e has both an authorization and a token
// endpoint set -- the minimum NewDelegatedGrantSource needs to skip
// discovery entirely and use e verbatim. Revocation is not required here
// either, mirroring discovery's own leniency about a missing
// revocation_endpoint (see Endpoints.Revocation).
func (e Endpoints) fullyPopulated() bool {
	return e.Authorization != "" && e.Token != ""
}

// DelegatedGrantConfig configures a single confidential Keycloak client's
// delegated-grant flow (FR14). Every consuming domain supplies its own
// ClientID/ClientSecret/RedirectURI for its own registered Keycloak client --
// this package never hardcodes a shared client.
type DelegatedGrantConfig struct {
	// Issuer is the Keycloak realm issuer URL, used for OIDC discovery when
	// Endpoints is not fully populated.
	Issuer string

	// ClientID is the caller-supplied Keycloak client id, per consuming
	// domain (FR14).
	ClientID string

	// ClientSecret is the confidential client's secret (FR14). Never
	// logged, printed, or included in an error (NFR1) -- see
	// DelegatedGrantConfig's String()/LogValue().
	ClientSecret string

	// RedirectURI is the caller-supplied redirect URI, allow-listed on the
	// Keycloak client (NFR5). An attacker-supplied redirect_uri at callback
	// time must be rejected -- enforced by the interactive leg (#2384), not
	// here.
	RedirectURI string

	// Scopes are the OAuth2 scopes requested. offline_access is always
	// added if the caller omits it (FR1) -- see resolvedScopes, applied by
	// NewDelegatedGrantSource. This field itself holds only what the caller
	// supplied; use DelegatedGrantSource.Scopes() for the resolved set.
	Scopes []string

	// Endpoints, when fully populated (see Endpoints.fullyPopulated), is
	// used verbatim instead of discovering it from Issuer. This is the seam
	// tests use to point the flow at internal/keycloakfake.
	Endpoints Endpoints

	// HTTPClient is used for discovery and (in later tasks) token/
	// revocation calls. Defaults to a client with defaultHTTPTimeout when
	// nil.
	HTTPClient *http.Client

	// Store persists this flow's refresh-token material (see store.go).
	Store Store

	// EncryptionKey is the 32-byte AES-256-GCM key passed through to Store
	// implementations that need it (NFR2). Not every Store implementation
	// uses it directly -- FakeStore, for instance, does not encrypt --
	// but Validate() still enforces its size when set, since a
	// pgstore-backed Store always requires it.
	EncryptionKey []byte
}

// Validate reports whether cfg is usable, checking required fields ahead of
// any network call. Returned errors never embed ClientSecret or
// EncryptionKey (NFR1); see String()/LogValue() for the same guarantee on
// %v/log output.
func (c DelegatedGrantConfig) Validate() error {
	if !c.Endpoints.fullyPopulated() && c.Issuer == "" {
		return ErrConfigIssuerRequired
	}
	if c.ClientID == "" {
		return ErrConfigClientIDRequired
	}
	if c.ClientSecret == "" {
		return ErrConfigClientSecretRequired
	}
	if c.RedirectURI == "" {
		return ErrConfigRedirectURIRequired
	}
	if c.Store == nil {
		return ErrConfigStoreRequired
	}
	if c.EncryptionKey != nil && len(c.EncryptionKey) != GrantKeySize {
		return ErrConfigEncryptionKeySize
	}
	return nil
}

// String redacts ClientSecret and EncryptionKey so an accidental %v/%s of a
// DelegatedGrantConfig (in a log line or error) cannot leak them (NFR1).
func (c DelegatedGrantConfig) String() string {
	return fmt.Sprintf(
		"DelegatedGrantConfig{Issuer:%q ClientID:%q ClientSecret:%s RedirectURI:%q Scopes:%v Endpoints:%+v EncryptionKey:%s}",
		c.Issuer, c.ClientID, redactedPlaceholder(c.ClientSecret), c.RedirectURI, c.Scopes, c.Endpoints, redactedPlaceholder(string(c.EncryptionKey)),
	)
}

// LogValue implements slog.LogValuer so slog.Any("config", cfg) (or an
// accidental %v inside a structured log call) redacts ClientSecret and
// EncryptionKey the same way String() does (NFR1).
func (c DelegatedGrantConfig) LogValue() slog.Value {
	return slog.StringValue(c.String())
}

// redactedPlaceholder returns "(unset)" for an empty secret and "(redacted)"
// for a non-empty one, so log output can distinguish "not configured" from
// "configured, value withheld" without ever revealing the value itself.
func redactedPlaceholder(secret string) string {
	if secret == "" {
		return "(unset)"
	}
	return "(redacted)"
}

// resolvedScopes returns scopes with offline_access present exactly once
// (FR1): appended if the caller omitted it, left alone (not duplicated) if
// already present.
func resolvedScopes(scopes []string) []string {
	for _, s := range scopes {
		if s == offlineAccessScope {
			return scopes
		}
	}
	return append(append([]string{}, scopes...), offlineAccessScope)
}

// DelegatedGrantSource is the constructed, endpoint-resolved handle for one
// consuming domain's confidential-client delegated-grant flow (FR14). Later
// tasks in this plan hang the interactive consent leg
// (AuthCodeURL/Exchange, #2384) and the non-interactive refresh leg (Token,
// #2386) off this type; this task ships only the type, its constructor, and
// endpoint resolution.
type DelegatedGrantSource struct {
	cfg       DelegatedGrantConfig
	endpoints Endpoints
	oauth2Cfg oauth2.Config
}

// Endpoints returns the Keycloak endpoints this source resolved at
// construction -- either cfg.Endpoints verbatim or the result of OIDC
// discovery against cfg.Issuer.
func (s *DelegatedGrantSource) Endpoints() Endpoints {
	return s.endpoints
}

// Scopes returns the resolved OAuth2 scope set this source requests --
// cfg.Scopes plus offline_access (FR1), added if the caller omitted it and
// never duplicated if present.
func (s *DelegatedGrantSource) Scopes() []string {
	return s.oauth2Cfg.Scopes
}

// NewDelegatedGrantSource validates cfg, resolves its Keycloak endpoints
// (using cfg.Endpoints verbatim if fully populated, otherwise fetching
// <Issuer>/.well-known/openid-configuration once), and returns a
// DelegatedGrantSource ready for later tasks' flow methods.
//
// Endpoint discovery honours ctx and cfg.HTTPClient. A discovery failure
// returns a plain error: construction is not the non-interactive hot path
// FR9's TransientError classification exists for.
func NewDelegatedGrantSource(ctx context.Context, cfg DelegatedGrantConfig) (*DelegatedGrantSource, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}

	endpoints := cfg.Endpoints
	if !endpoints.fullyPopulated() {
		discovered, err := discoverEndpoints(ctx, httpClient, cfg.Issuer)
		if err != nil {
			return nil, fmt.Errorf("grpcauth: discover keycloak endpoints: %w", err)
		}
		endpoints = discovered
	}

	scopes := resolvedScopes(cfg.Scopes)

	return &DelegatedGrantSource{
		cfg:       cfg,
		endpoints: endpoints,
		oauth2Cfg: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURI,
			Scopes:       scopes,
			Endpoint: oauth2.Endpoint{
				AuthURL:  endpoints.Authorization,
				TokenURL: endpoints.Token,
			},
		},
	}, nil
}

// oidcDiscoveryDocument is the subset of a Keycloak realm's
// /.well-known/openid-configuration response this package reads.
type oidcDiscoveryDocument struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
}

// discoverEndpoints fetches issuer's OIDC discovery document and extracts
// the three endpoints this package cares about. A missing
// revocation_endpoint is not an error (see Endpoints.Revocation); a missing
// authorization_endpoint or token_endpoint is, since neither the consent nor
// the refresh leg can function without them.
func discoverEndpoints(ctx context.Context, httpClient *http.Client, issuer string) (Endpoints, error) {
	discoveryURL := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return Endpoints{}, fmt.Errorf("grpcauth: build discovery request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return Endpoints{}, fmt.Errorf("grpcauth: fetch discovery document: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Endpoints{}, fmt.Errorf("grpcauth: discovery document request returned status %d", resp.StatusCode)
	}

	var doc oidcDiscoveryDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return Endpoints{}, fmt.Errorf("grpcauth: decode discovery document: %w", err)
	}

	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" {
		return Endpoints{}, errors.New("grpcauth: discovery document missing authorization_endpoint or token_endpoint")
	}

	return Endpoints{
		Authorization: doc.AuthorizationEndpoint,
		Token:         doc.TokenEndpoint,
		Revocation:    doc.RevocationEndpoint,
	}, nil
}
