package whagentlink

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testIssuer = "https://ui.example.test"
	testReturn = "https://ui.example.test/link/manmanv2/result"
)

func newKey(t *testing.T) (ed25519.PrivateKey, jose.JSONWebKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return priv, jose.JSONWebKey{Key: pub, KeyID: "k1", Algorithm: string(jose.EdDSA), Use: "sig"}
}

func sign(t *testing.T, priv ed25519.PrivateKey, claims map[string]any) string {
	t.Helper()
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	require.NoError(t, err)
	tok, err := jwt.Signed(s).Claims(claims).Serialize()
	require.NoError(t, err)
	return tok
}

func validClaims() map[string]any {
	return map[string]any{
		"iss": testIssuer, "sub": "u1", "sub_iss": "https://kc/realms/x", "jti": "j1",
		"exp": time.Now().Add(5 * time.Minute).Unix(), "return_url": testReturn,
	}
}

func setup(t *testing.T) (*Verifier, ed25519.PrivateKey) {
	t.Helper()
	priv, jwk := newKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	}))
	t.Cleanup(srv.Close)
	v, err := NewVerifier(context.Background(), srv.URL, testIssuer)
	require.NoError(t, err)
	return v, priv
}

func TestVerify_HappyPath(t *testing.T) {
	v, priv := setup(t)
	a, err := v.Verify(context.Background(), sign(t, priv, validClaims()))
	require.NoError(t, err)
	assert.Equal(t, "u1", a.Subject)
	assert.Equal(t, "https://kc/realms/x", a.SubjectIssuer)
	assert.Equal(t, "j1", a.ID)
	assert.Equal(t, testReturn, a.ReturnURL)
}

func TestVerify_Rejections(t *testing.T) {
	v, priv := setup(t)
	otherPriv, _ := newKey(t)

	tamper := func() string {
		parts := strings.Split(sign(t, priv, validClaims()), ".")
		p, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var m map[string]any
		require.NoError(t, json.Unmarshal(p, &m))
		m["sub"] = "attacker"
		p, _ = json.Marshal(m)
		parts[1] = base64.RawURLEncoding.EncodeToString(p)
		return strings.Join(parts, ".")
	}
	mut := func(f func(map[string]any)) string {
		c := validClaims()
		f(c)
		return sign(t, priv, c)
	}

	cases := []struct {
		name  string
		token string
		want  error
	}{
		{"tampered", tamper(), ErrInvalidSignature},
		{"unknown key", sign(t, otherPriv, validClaims()), ErrInvalidSignature},
		{"garbage", "not-a-jwt", ErrInvalidSignature},
		{"expired", mut(func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }), ErrExpired},
		{"wrong issuer", mut(func(c map[string]any) { c["iss"] = "https://evil" }), ErrUnknownIssuer},
		{"missing jti", mut(func(c map[string]any) { delete(c, "jti") }), ErrMissingField},
		{"missing sub_iss", mut(func(c map[string]any) { delete(c, "sub_iss") }), ErrMissingField},
		{"missing return_url", mut(func(c map[string]any) { delete(c, "return_url") }), ErrMissingField},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := v.Verify(context.Background(), tc.token)
			assert.Nil(t, a)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestNewVerifier_RequiresArgs(t *testing.T) {
	_, err := NewVerifier(context.Background(), "", "i")
	assert.Error(t, err)
	_, err = NewVerifier(context.Background(), "u", "")
	assert.Error(t, err)
}

func TestReturnURLOriginMatches(t *testing.T) {
	assert.True(t, ReturnURLOriginMatches(testReturn, testIssuer))
	assert.False(t, ReturnURLOriginMatches("https://evil.test/x", testIssuer))
	assert.False(t, ReturnURLOriginMatches("http://ui.example.test/x", testIssuer))
	assert.False(t, ReturnURLOriginMatches("/relative", testIssuer))
}

func TestUnverifiedReturnURL(t *testing.T) {
	_, priv := setup(t)
	u, ok := UnverifiedReturnURL(sign(t, priv, validClaims()))
	assert.True(t, ok)
	assert.Equal(t, testReturn, u)
	_, ok = UnverifiedReturnURL("a.b")
	assert.False(t, ok)
}
