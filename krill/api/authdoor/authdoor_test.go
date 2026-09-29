package authdoor

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

const issuer = "https://kc.example/realms/x"

type fakeStore struct{ auth.CredentialStore }

func (fakeStore) Verify(_ context.Context, raw string) (string, auth.Credential, error) {
	switch raw {
	case "opaque-op":
		return "https://kc.example/realms/x|u1", auth.Credential{Persona: "swarm_operator"}, nil
	case "opaque-nopersona":
		return "https://kc.example/realms/x|u1", auth.Credential{}, nil
	}
	return "", auth.Credential{}, auth.ErrInvalidCredential
}

type fakeOIDC struct{ claims *grpcauth.Claims }

func (f fakeOIDC) Verify(context.Context, string) (*grpcauth.Claims, error) {
	if f.claims == nil {
		return nil, errors.New("bad")
	}
	return f.claims, nil
}

func jwt(iss string) string {
	e := base64.RawURLEncoding.EncodeToString
	payload := `{"sub":"u1"}`
	if iss != "" {
		payload = `{"sub":"u1","iss":"` + iss + `"}`
	}
	return e([]byte(`{"alg":"ES256"}`)) + "." + e([]byte(payload)) + ".sig"
}

func do(cfg Config, token string) (int, Caller, bool) {
	var got Caller
	var ok bool
	h := Middleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok = FromContext(r.Context())
	}))
	req := httptest.NewRequest("GET", "/products", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, got, ok
}

func baseCfg() Config {
	return Config{
		Credentials: fakeStore{},
		OIDC:        fakeOIDC{claims: &grpcauth.Claims{Issuer: issuer, Subject: "u1", Roles: []string{"krill-op"}}},
		OIDCIssuer:  issuer,
		Roles:       server.RoleConfig{OperatorRole: "krill-op"},
	}
}

func TestOIDC_ValidResolvesIdentityAndPersona(t *testing.T) {
	code, c, ok := do(baseCfg(), jwt(issuer))
	require.Equal(t, 200, code)
	require.True(t, ok)
	require.Equal(t, server.PersonaSwarmOperator, c.Persona)
	require.Equal(t, "u1", c.Identity.Acting.Sub)
}

func TestOIDC_WrongOrMissingIssRejected(t *testing.T) {
	for _, iss := range []string{"", "https://whagent.example", issuer + "/"} {
		code, _, ok := do(baseCfg(), jwt(iss))
		require.Equal(t, 401, code, iss)
		require.False(t, ok)
	}
	code, _, _ := do(baseCfg(), "a.b.c")
	require.Equal(t, 401, code)
}

func TestOIDC_NoRoleForbidden(t *testing.T) {
	cfg := baseCfg()
	cfg.OIDC = fakeOIDC{claims: &grpcauth.Claims{Issuer: issuer, Subject: "u1"}}
	code, _, _ := do(cfg, jwt(issuer))
	require.Equal(t, 403, code)
}

func TestOpaque_ResolvesViaStoreWithPersona(t *testing.T) {
	code, c, ok := do(baseCfg(), "opaque-op")
	require.Equal(t, 200, code)
	require.True(t, ok)
	require.Equal(t, server.PersonaSwarmOperator, c.Persona)
	code, _, _ = do(baseCfg(), "opaque-nopersona")
	require.Equal(t, 403, code)
	code, _, _ = do(baseCfg(), "nope")
	require.Equal(t, 401, code)
}

func doReq(cfg Config, method, path, token string) int {
	h := Middleware(cfg)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func readerCfg() Config {
	cfg := baseCfg()
	cfg.OIDC = fakeOIDC{claims: &grpcauth.Claims{Issuer: issuer, Subject: "r1", Roles: []string{"krill-ro"}}}
	cfg.Roles.ReaderRole = "krill-ro"
	return cfg
}

var routeTable = []struct{ method, path string }{
	{"GET", "/products"}, {"GET", "/scope"}, {"GET", "/tasks/x"}, {"GET", "/milestones/x/tasks"},
	{"GET", "/console/claimed"}, {"GET", "/design-sessions/x"}, {"GET", "/nope"},
	{"POST", "/sessions/init"}, {"POST", "/products"}, {"POST", "/tasks/x/claim"},
	{"POST", "/features/x/amend"}, {"POST", "/nope"}, {"PUT", "/products"}, {"DELETE", "/products"},
}

func TestUnauthenticated401OnEveryRoute(t *testing.T) {
	for _, rt := range routeTable {
		require.Equal(t, 401, doReq(baseCfg(), rt.method, rt.path, ""), rt.method+" "+rt.path)
		require.Equal(t, 401, doReq(baseCfg(), rt.method, rt.path, "nope"), rt.method+" "+rt.path)
	}
}

func TestReaderReadsOKWritesForbidden(t *testing.T) {
	for _, rt := range routeTable {
		want := 403
		if rt.method == "GET" {
			want = 200
		}
		require.Equal(t, want, doReq(readerCfg(), rt.method, rt.path, jwt(issuer)), rt.method+" "+rt.path)
	}
}

func TestOperatorAllowedEverywhere(t *testing.T) {
	for _, rt := range routeTable {
		require.Equal(t, 200, doReq(baseCfg(), rt.method, rt.path, "opaque-op"), rt.method+" "+rt.path)
	}
}

func TestProbesNeedNoToken(t *testing.T) {
	for _, p := range []string{"/healthz", "/readyz"} {
		require.Equal(t, 200, doReq(baseCfg(), "GET", p, ""), p)
	}
}

func TestDevToken(t *testing.T) {
	cfg := baseCfg()
	cfg.DevToken = "dev-secret"
	code, c, ok := do(cfg, "dev-secret")
	require.Equal(t, 200, code)
	require.True(t, ok)
	require.Equal(t, server.PersonaSwarmOperator, c.Persona)
	require.Equal(t, DevSubject, c.Identity.Acting.Sub)

	code, _, _ = do(cfg, "wrong")
	require.Equal(t, 401, code)

	// Unset DevToken: the same bearer is not special.
	code, _, _ = do(baseCfg(), "dev-secret")
	require.Equal(t, 401, code)
}
