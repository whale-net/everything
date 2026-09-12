package grpcauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// GrantTokenSource is a non-interactive, TokenSource-shaped accessor bound to
// one (subject, grant) pair. "TokenSource-shaped" means an interface shaped
// like oauth2.TokenSource, deliberately context-threaded to match grpcauth's
// existing surface (ClaimsFromContext / ContextWithClaims / WithUserToken) --
// it does NOT implement oauth2.TokenSource literally (FR3): that interface's
// Token() takes no context, and every other call in this package threads one.
type GrantTokenSource interface {
	// Token returns a live access token for the bound (subject, grant),
	// refreshing against Keycloak as needed. It requires no user and no
	// browser present (FR3) -- safe to call from a Temporal Activity at
	// schedule-fire time.
	Token(ctx context.Context) (*oauth2.Token, error)
}

// TokenSource returns a non-interactive GrantTokenSource bound to
// (subject, grant). The returned value is cheap to construct and holds no
// state of its own beyond the (subject, grant) key -- every call to Token
// re-reads the Store.
func (s *DelegatedGrantSource) TokenSource(subject, grant string) GrantTokenSource {
	return &delegatedGrantTokenSource{
		source:  s,
		subject: subject,
		grant:   grant,
	}
}

// Status is a pass-through to Store.Status so a caller can check a grant's
// persisted lifecycle state without attempting a token fetch (FR10): no
// decrypt, no Keycloak call.
func (s *DelegatedGrantSource) Status(ctx context.Context, subject, grant string) (GrantStatus, error) {
	status, err := s.cfg.Store.Status(ctx, subject, grant)
	if err != nil {
		return "", fmt.Errorf("grpcauth: grant status: %w", err)
	}
	return status, nil
}

// delegatedGrantTokenSource is the concrete GrantTokenSource returned by
// DelegatedGrantSource.TokenSource. The subject used for every Store and
// Keycloak call is always the subject captured here at construction --
// nothing derived from an incoming token can redirect the lookup to another
// subject's material (NFR3).
type delegatedGrantTokenSource struct {
	source  *DelegatedGrantSource
	subject string
	grant   string
}

// keycloakTokenResponse is the subset of a Keycloak token-endpoint response
// this package reads on a successful refresh.
type keycloakTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

// keycloakErrorResponse is RFC 6749 section 5.2's error response shape.
type keycloakErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Token implements GrantTokenSource. See delegatedgrant_token_test.go and
// issue #2386 for the exact sequence this follows.
func (t *delegatedGrantTokenSource) Token(ctx context.Context) (*oauth2.Token, error) {
	s := t.source

	// Step 1: status-first short-circuit (FR7). Store.TokenMaterial checks
	// persisted status before decrypting anything and before any Keycloak
	// call, returning ErrGrantRevoked / ErrGrantNeedsReauth directly when
	// the grant is not active.
	material, err := s.cfg.Store.TokenMaterial(ctx, t.subject, t.grant)
	if err != nil {
		return nil, fmt.Errorf("grpcauth: token material for grant %q: %w", t.grant, err)
	}

	// Step 2: refresh at the token endpoint (confidential client, FR14).
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {material.RefreshToken},
		"client_id":     {s.cfg.ClientID},
		"client_secret": {s.cfg.ClientSecret},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoints.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, NewTransientError("build refresh request", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		// Network error / timeout: transient, no state change (FR9, NFR4).
		return nil, NewTransientError("refresh token request", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, NewTransientError("read refresh response", err)
	}

	// Step 3: classify the response.
	if resp.StatusCode != http.StatusOK {
		var errResp keycloakErrorResponse
		if jsonErr := json.Unmarshal(body, &errResp); jsonErr == nil && errResp.Error == "invalid_grant" {
			if markErr := s.cfg.Store.MarkNeedsReauth(ctx, t.subject, t.grant); markErr != nil {
				// The grant is unusable either way; surface the mark
				// failure as transient so a caller retries the whole
				// operation rather than silently losing the transition.
				return nil, NewTransientError("mark needs_reauth", markErr)
			}
			return nil, fmt.Errorf("grpcauth: grant %q rejected by keycloak: %w", t.grant, ErrGrantNeedsReauth)
		}
		// Any other non-200 (5xx, unrecognised error code, unparseable
		// body) is transient: it says nothing about the grant itself
		// (FR9, NFR4), so persisted status is left untouched.
		return nil, NewTransientError("refresh token request", fmt.Errorf("unexpected status %d", resp.StatusCode))
	}

	var tokenResp keycloakTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		// An unparseable 200 response says nothing about the grant --
		// treat it the same as any other transient failure (FR9).
		return nil, NewTransientError("decode refresh response", err)
	}

	// Step 4: refresh write-back (FR13) -- persist the rotated refresh
	// token before returning the access token. Refresh-token rotation is
	// assumed enabled for this client; without this write-back the next
	// refresh would fail with invalid_grant and spuriously mark a healthy
	// grant needs_reauth (NFR4 violation).
	if tokenResp.RefreshToken != "" {
		newMaterial := TokenMaterial{
			RefreshToken: tokenResp.RefreshToken,
			ObtainedAt:   time.Now(),
		}
		if err := s.cfg.Store.Persist(ctx, t.subject, t.grant, newMaterial); err != nil {
			// Do not return the access token as if nothing happened: the
			// store no longer matches what Keycloak issued, so the caller
			// should retry rather than proceed.
			return nil, NewTransientError("persist rotated refresh token", err)
		}
	}

	// Step 5: return the access token. It carries the grantor's subject
	// and their current realm roles as issued by Keycloak -- no scope
	// narrowing, no claim filtering by this library (FR4).
	token := &oauth2.Token{
		AccessToken:  tokenResp.AccessToken,
		TokenType:    tokenResp.TokenType,
		RefreshToken: tokenResp.RefreshToken,
	}
	if tokenResp.ExpiresIn > 0 {
		token.Expiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	}
	return token, nil
}
