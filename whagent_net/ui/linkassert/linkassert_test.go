package linkassert

import "testing"

// TestLoadKey_Rejects asserts LoadKey returns an error -- never a usable
// Key -- for an empty string, non-PEM garbage, a PEM block that isn't
// PKCS8, and a symmetric/HMAC secret.
func TestLoadKey_Rejects(t *testing.T) {
	// TODO: Implement test.
}

// TestMint_ClaimShape asserts a minted token's header kid equals the
// loaded key's kid, its iss equals the issuer argument, its exp - iat
// equals whagent.DefaultTTL exactly, and its jti differs across two
// successive calls with identical arguments.
func TestMint_ClaimShape(t *testing.T) {
	// TODO: Implement test.
}

// TestMint_NoActorOrSessionFields asserts Mint's output carries none of
// whagent.Claim's Actor/session fields.
func TestMint_NoActorOrSessionFields(t *testing.T) {
	// TODO: Implement test.
}

// TestJWKSHandler_PublicKeyOnly asserts the JWKS document contains the
// active key, kid matches, and no private JWK parameter (d, p, q, ...)
// appears anywhere in the serialized output -- mirrors persona's own jwks
// test assertion.
func TestJWKSHandler_PublicKeyOnly(t *testing.T) {
	// TODO: Implement test.
}

// TestJWKSHandler_UnauthenticatedAndMethodGated asserts the JWKS route is
// reachable without an authenticated session (no redirect to /login) and
// rejects non-GET/HEAD with 405.
func TestJWKSHandler_UnauthenticatedAndMethodGated(t *testing.T) {
	// TODO: Implement test.
}
