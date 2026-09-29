// Package apiclient is how a krill api caller presents a verifiable
// credential: a per-request user access token carried on the context (the
// signed-in operator's Keycloak token), or a Keycloak client_credentials
// token for machine callers with no operator behind them. Both are sent as
// `Authorization: Bearer`.
package apiclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

type userTokenKey struct{}

// WithUserToken returns ctx carrying the operator access token Transport
// forwards on any request built from it.
func WithUserToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, userTokenKey{}, token)
}

// UserTokenFromContext returns the token WithUserToken stored.
func UserTokenFromContext(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(userTokenKey{}).(string)
	return t, ok && t != ""
}

// ClientCredentialsConfig configures a machine caller's token source.
type ClientCredentialsConfig struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
}

// NewClientCredentialsSource returns a caching, auto-refreshing Keycloak
// client_credentials token source.
func NewClientCredentialsSource(ctx context.Context, cfg ClientCredentialsConfig) (oauth2.TokenSource, error) {
	if cfg.TokenURL == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("client credentials require token URL, client id and client secret")
	}
	cc := &clientcredentials.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, TokenURL: cfg.TokenURL}
	return cc.TokenSource(ctx), nil
}

// Transport attaches a Bearer token to every request: the context's user
// token when present, else Machine's token when configured. With neither,
// the request goes out unauthenticated (an api in flag-off mode accepts it).
type Transport struct {
	Base    http.RoundTripper
	Machine oauth2.TokenSource
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, ok := UserTokenFromContext(req.Context())
	if !ok && t.Machine != nil {
		tok, err := t.Machine.Token()
		if err != nil {
			return nil, fmt.Errorf("client credentials token: %w", err)
		}
		token, ok = tok.AccessToken, true
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if !ok {
		return base.RoundTrip(req)
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+token)
	return base.RoundTrip(clone)
}
