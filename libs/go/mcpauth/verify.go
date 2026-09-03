package mcpauth

import (
	"context"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// errInvalidToken is the single, fixed error TokenVerifier returns for
// every verification failure (FR6, NFR1): its Error() string never varies
// between an unrecognized, malformed, revoked, or store-error case, and
// never includes the presented token or any derived hash. It wraps
// sdkauth.ErrInvalidToken so sdkauth.RequireBearerToken's own
// errors.Is(err, sdkauth.ErrInvalidToken) branch (see the SDK's verify
// helper in auth/auth.go) treats it as a 401, not a 500 — and this error's
// own Error() string is what sdkauth.RequireBearerToken writes directly to
// the HTTP response body, so it must be safe to expose to the caller as-is.
var errInvalidToken = errInvalidTokenWrap{}

// errInvalidTokenWrap is a distinct type (rather than a plain
// fmt.Errorf-wrapped sentinel) so its Error() string is a compile-time
// constant with no possibility of a future edit accidentally interpolating
// per-call detail (a token, a hash, a store error) into the message.
type errInvalidTokenWrap struct{}

func (errInvalidTokenWrap) Error() string { return "mcpauth: invalid or revoked credential" }

func (errInvalidTokenWrap) Unwrap() error { return sdkauth.ErrInvalidToken }

var _ error = errInvalidTokenWrap{}

// TokenVerifier adapts a CredentialStore to the MCP Go SDK's resource-server
// verifier (github.com/modelcontextprotocol/go-sdk/auth.TokenVerifier): it
// resolves a presented bearer token to its owning identity via
// store.Verify, reported as TokenInfo.UserID.
//
// Any verification failure — an unrecognized, malformed, or revoked token,
// or a store-level error — is reported as the single fixed errInvalidToken,
// which wraps sdkauth.ErrInvalidToken and never varies its message or
// reveals which failure mode occurred (FR6, NFR1). It never echoes the
// presented token or any derived hash — errInvalidToken's message is a
// compile-time constant, so there is no code path that could interpolate
// either into it.
//
// The returned TokenInfo always carries a zero Expiration: mcpauth
// credentials are revocable, not time-boxed (see mcpauth.go's package doc,
// "What this library deliberately is not"), so there is no per-token `exp`
// to report. Callers must pair TokenVerifier with RequireBearerToken (or
// otherwise set AllowMissingExpiration: true) — see that function's doc.
func TokenVerifier(store CredentialStore) sdkauth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		identity, _, err := store.Verify(ctx, token)
		if err != nil {
			return nil, errInvalidToken
		}
		return &sdkauth.TokenInfo{UserID: identity}, nil
	}
}

// RequireBearerToken wraps sdkauth.RequireBearerToken with the defaults an
// mcpauth credential needs: mcpauth credentials are revocable, not
// time-boxed (see mcpauth.go's package doc, "What this library deliberately
// is not") — there is no per-token `exp` claim, so TokenVerifier always
// returns a TokenInfo with a zero Expiration, which sdkauth.RequireBearerToken
// rejects outright unless AllowMissingExpiration is set.
//
// AllowMissingExpiration is therefore always forced true: if opts is nil, a
// zero-value RequireBearerTokenOptions with AllowMissingExpiration: true is
// used; if opts is non-nil, its AllowMissingExpiration field is overwritten
// to true regardless of what the caller set (a caller-supplied false would
// otherwise reject every single mcpauth credential outright, since none of
// them ever carry an expiration — there is no legitimate reason to pass
// false here). ResourceMetadataURL and Scopes, if set, are passed straight
// through unmodified.
func RequireBearerToken(store CredentialStore, opts *sdkauth.RequireBearerTokenOptions) func(http.Handler) http.Handler {
	if opts == nil {
		opts = &sdkauth.RequireBearerTokenOptions{}
	} else {
		o := *opts
		opts = &o
	}
	opts.AllowMissingExpiration = true

	return sdkauth.RequireBearerToken(TokenVerifier(store), opts)
}
