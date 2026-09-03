package mcpauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// errVerifyNotImplemented is returned by TokenVerifier's stub body and by
// the middleware RequireBearerToken constructs until the Implementation
// phase of issue #1640 lands the real store.Verify wiring. Scaffold exists
// to settle this package's public shape (the sdkauth.TokenVerifier and
// middleware signatures), not the method bodies.
var errVerifyNotImplemented = errors.New("mcpauth: not implemented yet (scaffold phase, see issue #1640)")

// TokenVerifier adapts a CredentialStore to the MCP Go SDK's resource-server
// verifier (github.com/modelcontextprotocol/go-sdk/auth.TokenVerifier): it
// resolves a presented bearer token to its owning identity via
// store.Verify, reported as TokenInfo.UserID.
//
// Any verification failure — an unrecognized, malformed, or revoked token,
// or a store-level error — must be reported as a single fixed error that
// wraps sdkauth.ErrInvalidToken and never varies its message or reveals
// which failure mode occurred (FR6, NFR1). It must not echo the presented
// token or any derived hash.
//
// Scaffold note: this stub does not yet call store.Verify; it always
// returns errVerifyNotImplemented wrapping sdkauth.ErrInvalidToken. The
// real store.Verify wiring lands in the Implementation phase of issue
// #1640 — see that issue's "Implementation" section for the exact
// behavior.
func TokenVerifier(store CredentialStore) sdkauth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		_ = store
		return nil, fmt.Errorf("%w: %w", sdkauth.ErrInvalidToken, errVerifyNotImplemented)
	}
}

// RequireBearerToken wraps sdkauth.RequireBearerToken with the defaults an
// mcpauth credential needs: mcpauth credentials are revocable, not
// time-boxed (see mcpauth.go's package doc, "What this library deliberately
// is not") — there is no per-token `exp` claim, so TokenVerifier always
// returns a TokenInfo with a zero Expiration, which sdkauth.RequireBearerToken
// rejects outright unless AllowMissingExpiration is set. ResourceMetadataURL
// and Scopes, if set, are passed straight through unmodified.
//
// Scaffold note: this stub wires TokenVerifier(store) into
// sdkauth.RequireBearerToken as-is, without yet forcing
// AllowMissingExpiration to true — every request will 401 until both the
// TokenVerifier stub above and the AllowMissingExpiration default land in
// the Implementation phase of issue #1640.
func RequireBearerToken(store CredentialStore, opts *sdkauth.RequireBearerTokenOptions) func(http.Handler) http.Handler {
	return sdkauth.RequireBearerToken(TokenVerifier(store), opts)
}
