package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/mcp/server"
)

// readRoutes is one representative path per read route family.
var readRoutes = []string{
	"/",
	opsPath,
	opsClaimedPath,
	opsEscalatedPath,
	opsCancelledPath,
	opsNotesPath,
	designPath,
	"/design/products/11111111-2222-3333-4444-555555555555/design-sessions",
	"/design/design-sessions/11111111-2222-3333-4444-555555555555",
	designGoPath,
	credentialsPath,
	specPath,
	specProductsPath,
	"/spec/products/11111111-2222-3333-4444-555555555555",
	"/spec/products/11111111-2222-3333-4444-555555555555/decisions",
	"/spec/products/11111111-2222-3333-4444-555555555555/personas",
	"/spec/products/11111111-2222-3333-4444-555555555555/non-goals",
	"/spec/products/11111111-2222-3333-4444-555555555555/delivery",
}

// statusThroughGate serves path and reports the status; a handler that
// panics on this test's nil stores has passed the gate, reported as 200.
func statusThroughGate(mux *http.ServeMux, path string, cookie *http.Cookie) (status int) {
	defer func() {
		if recover() != nil {
			status = http.StatusOK
		}
	}()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

func readGateMux(t *testing.T, idpRoles []string) (*http.ServeMux, *http.Cookie) {
	t.Helper()
	idp := newFakeIDP(t, testOperatorSub)
	authenticator, cookie := newSignedInOperator(t, idp)
	app := newSignedInApp(t, authenticator, idp.server.URL, "http://api.invalid")
	app.sessionRoles = func(*http.Request) ([]string, error) { return idpRoles, nil }
	app.roles = server.RoleConfig{OperatorRole: "krill-operator", ReaderRole: "krill-reader"}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux, cookie
}

func TestReadRoutes_RoleGate(t *testing.T) {
	cases := []struct {
		name  string
		roles []string
		deny  bool
	}{
		{"no role", []string{"unrelated"}, true},
		{"no roles claim", nil, true},
		{"reader", []string{"krill-reader"}, false},
		{"operator", []string{"krill-operator"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, cookie := readGateMux(t, tc.roles)
			for _, path := range readRoutes {
				got := statusThroughGate(mux, path, cookie)
				if tc.deny {
					assert.Equal(t, http.StatusForbidden, got, path)
				} else {
					assert.NotEqual(t, http.StatusForbidden, got, path)
					assert.NotEqual(t, http.StatusUnauthorized, got, path)
				}
			}
		})
	}
}
