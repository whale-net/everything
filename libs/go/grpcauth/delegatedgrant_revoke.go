package grpcauth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// RevokeRefreshToken best-effort revokes refreshToken against
// s.Endpoints().Revocation per RFC 7009 (FR13), authenticated as this
// source's confidential client via HTTP Basic auth (client_id +
// client_secret, FR14) -- the same client authentication style
// golang.org/x/oauth2 uses for this package's token-endpoint calls.
//
// This method has the exact shape of pgstore.Revoker
// (RevokeRefreshToken(ctx, string) error), so a consuming domain wires
// pgstore.StoreConfig.Revoker = source directly. Keeping this
// implementation in core grpcauth (rather than pgstore) is what keeps
// pgstore free of any OIDC/Keycloak import (see pgstore/revoke.go's
// Revoker doc).
//
// Returns nil both when the remote call genuinely succeeds (2xx) and when
// there is deliberately nothing to do -- s.Endpoints().Revocation is empty
// because Keycloak's discovery document did not advertise a
// revocation_endpoint (allowed; see Endpoints.Revocation) -- logging that
// second case at WARNING exactly once per source rather than once per call,
// since it would otherwise storm the log during a revoke-heavy workload.
// Returns a non-nil error for a non-2xx response or a transport failure;
// the error names only the HTTP status (or the transport failure), never
// refreshToken or ClientSecret (NFR1) -- pgstore.Revoke is what decides to
// swallow this error and log its own WARNING alongside the (subject, grant)
// key.
func (s *DelegatedGrantSource) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	if s.endpoints.Revocation == "" {
		s.missingRevocationWarnOnce.Do(func() {
			slog.Warn("grpcauth: Keycloak discovery did not advertise a revocation_endpoint; best-effort RFC 7009 remote revocation is disabled for this source",
				"client_id", s.cfg.ClientID)
		})
		return nil
	}

	httpClient := s.cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}

	form := url.Values{
		"token":           {refreshToken},
		"token_type_hint": {"refresh_token"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoints.Revocation, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("grpcauth: build revocation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(s.cfg.ClientID, s.cfg.ClientSecret)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("grpcauth: revocation request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("grpcauth: revocation endpoint returned status %d", resp.StatusCode)
	}
	return nil
}
