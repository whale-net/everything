package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/ui/linkassert"
)

// This file guards issue #2596's Testing section: FR13's browser-session-
// only gating, FR2's mint + redirect (and its "not configured" degrade
// path), FR10's four distinct outcome messages, and FR1's "never silently
// automatic" invariant on linkassert.Key.Mint's only call site.

// newTestLinkAssertKey returns a *linkassert.Key backed by a fresh
// Ed25519 keypair, plus that keypair's public half, so a test can
// independently verify a minted assertion's signature without reaching
// into linkassert's own unexported Key.signer field. linkassert_test.go's
// generateSigningKeyPEM/mustLoadTestKey do the same thing but are
// package-private to linkassert, so this is a deliberate local copy of
// their shape rather than a shared export.
func newTestLinkAssertKey(t *testing.T) (*linkassert.Key, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	key, err := linkassert.LoadKey(pemStr, "test-link-key")
	require.NoError(t, err)
	return key, pub
}

// linkAssertionClaims mirrors linkassert.go's own unexported
// assertionClaims JSON shape (sub_iss/return_url alongside the standard
// registered JWT claims) so this package's tests can decode a minted
// assertion's claims without reaching into linkassert's unexported types.
type linkAssertionClaims struct {
	jwt.Claims
	SubjectIssuer string `json:"sub_iss"`
	ReturnURL     string `json:"return_url"`
}

// TestLinkASSRoutes_UnauthenticatedRedirectToSignIn asserts an
// unauthenticated POST /link/ass and GET /link/ass/result both redirect to
// sign-in and neither mints nor renders anything (FR13).
func TestLinkASSRoutes_UnauthenticatedRedirectToSignIn(t *testing.T) {
	linkKey, _ := newTestLinkAssertKey(t)
	app := &App{
		auth:          newTestOIDCAuthenticator(t),
		oidcIssuer:    testIssuer,
		publicURL:     "https://ui.example",
		assLinkURL:    "https://ass.example",
		linkAssertKey: linkKey,
	}

	startReq := httptest.NewRequest(http.MethodPost, "/link/ass", nil)
	startW := httptest.NewRecorder()
	app.auth.RequireAuthFunc(app.handleLinkASSStart)(startW, startReq)
	assert.True(t, requestWasAuthBlocked(startW), "unauthenticated POST /link/ass must redirect to sign-in, got status %d", startW.Code)
	assert.NotContains(t, startW.Header().Get("Location"), "ass.example", "an unauthenticated request must never reach the ASS redirect at all")

	resultReq := httptest.NewRequest(http.MethodGet, "/link/ass/result?outcome=linked", nil)
	resultW := httptest.NewRecorder()
	app.auth.RequireAuthFunc(app.handleLinkASSResult)(resultW, resultReq)
	assert.True(t, requestWasAuthBlocked(resultW), "unauthenticated GET /link/ass/result must redirect to sign-in, got status %d", resultW.Code)
	assert.NotContains(t, resultW.Body.String(), "Linked", "an unauthenticated request must never render the outcome page")
}

// TestHandleLinkASSStart_RedirectsWithAssertion asserts that, with the ASS
// link URL configured, POST /link/ass returns 303 with a Location pointing
// at the configured base URL, carrying an assertion that parses and whose
// sub/subject-issuer match the test session's Keycloak identity.
func TestHandleLinkASSStart_RedirectsWithAssertion(t *testing.T) {
	linkKey, pub := newTestLinkAssertKey(t)
	app := &App{
		auth:          devModeAuthenticator(t),
		oidcIssuer:    testIssuer,
		publicURL:     "https://ui.example",
		assLinkURL:    "https://ass.example",
		linkAssertKey: linkKey,
	}
	wrapped := app.auth.RequireAuthFunc(app.handleLinkASSStart)

	req := httptest.NewRequest(http.MethodPost, "/link/ass", nil)
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusSeeOther, w.Code)
	loc := w.Header().Get("Location")
	require.True(t, strings.HasPrefix(loc, "https://ass.example/link/whagent?"), "Location = %q, want it to start with the configured ASS base URL's /link/whagent path", loc)

	parsedLoc, err := url.Parse(loc)
	require.NoError(t, err)
	token := parsedLoc.Query().Get("token")
	require.NotEmpty(t, token, "Location must carry a token query parameter")

	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.EdDSA})
	require.NoError(t, err)
	var claims linkAssertionClaims
	require.NoError(t, parsed.Claims(pub, &claims), "token must verify against the configured signing key")

	assert.Equal(t, "dev-user", claims.Subject, "sub must be the signed-in Operator's own Keycloak sub")
	assert.Equal(t, testIssuer, claims.SubjectIssuer, "sub_iss must be the signed-in Operator's own Keycloak issuer")
	assert.Equal(t, "https://ui.example/link/ass/result", claims.ReturnURL)
}

// TestHandleLinkASSStart_NotConfigured asserts that, with the ASS link URL
// unset, POST /link/ass returns the not-configured response -- never a 500
// and never a redirect to a bare/empty host -- and that /grants does not
// render FR1's action either.
func TestHandleLinkASSStart_NotConfigured(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t), oidcIssuer: testIssuer}

	wrapped := app.auth.RequireAuthFunc(app.handleLinkASSStart)
	req := httptest.NewRequest(http.MethodPost, "/link/ass", nil)
	w := httptest.NewRecorder()
	wrapped(w, req)

	assert.NotEqual(t, http.StatusInternalServerError, w.Code, "not-configured response must never be a 500")
	assert.Empty(t, w.Header().Get("Location"), "not-configured response must never redirect to a bare/empty host")
	assert.Contains(t, strings.ToLower(w.Body.String()), "not configured", "not-configured response must say so explicitly")

	grantsWrapped := app.auth.RequireAuthFunc(app.handleGrants)
	grantsReq := httptest.NewRequest(http.MethodGet, "/grants", nil)
	grantsW := httptest.NewRecorder()
	grantsWrapped(grantsW, grantsReq)
	assert.NotContains(t, grantsW.Body.String(), "Link ASS identity", "with ASS link URL unset, /grants must not render FR1's action")
}

// TestHandleGrants_ShowsLinkASSActionWhenConfigured is the mirror of
// TestHandleLinkASSStart_NotConfigured's negative check: with the ASS
// link URL configured, /grants does render FR1's action.
func TestHandleGrants_ShowsLinkASSActionWhenConfigured(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t), oidcIssuer: testIssuer, assLinkURL: "https://ass.example"}

	wrapped := app.auth.RequireAuthFunc(app.handleGrants)
	req := httptest.NewRequest(http.MethodGet, "/grants", nil)
	w := httptest.NewRecorder()
	wrapped(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Link ASS identity")
}

// TestHandleLinkASSResult_FourDistinctOutcomes asserts each of FR10's four
// outcomes (linked, already linked, conflict, rejected assertion) renders
// its own distinct message, and an unrecognized or absent outcome value
// renders the generic failure, never a success.
func TestHandleLinkASSResult_FourDistinctOutcomes(t *testing.T) {
	app := &App{auth: devModeAuthenticator(t)}
	wrapped := app.auth.RequireAuthFunc(app.handleLinkASSResult)

	render := func(t *testing.T, outcome string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/link/ass/result?outcome="+url.QueryEscape(outcome), nil)
		w := httptest.NewRecorder()
		wrapped(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}

	outcomes := []string{"linked", "already_linked", "conflict", "rejected"}
	bodies := make(map[string]string, len(outcomes))
	for _, outcome := range outcomes {
		bodies[outcome] = render(t, outcome)
	}

	assert.Contains(t, bodies["linked"], "alert-success")
	assert.Contains(t, bodies["already_linked"], "alert-success")
	assert.Contains(t, bodies["conflict"], "alert-error")
	assert.Contains(t, bodies["rejected"], "alert-error")

	for i, a := range outcomes {
		for _, b := range outcomes[i+1:] {
			assert.NotEqual(t, bodies[a], bodies[b], "outcome %q and %q must render distinct messages", a, b)
		}
	}

	genericUnknown := render(t, "some-garbage-value")
	genericAbsent := render(t, "")
	for _, outcome := range outcomes {
		assert.NotEqual(t, bodies[outcome], genericUnknown, "an unrecognized outcome must never render the same message as %q", outcome)
		assert.NotEqual(t, bodies[outcome], genericAbsent, "an absent outcome must never render the same message as %q", outcome)
	}
	assert.Contains(t, genericUnknown, "alert-error", "unrecognized outcome must never render as success")
	assert.Contains(t, genericAbsent, "alert-error", "absent outcome must never render as success")
}

// TestMint_OnlyCalledFromHandleLinkASSStart asserts no code path in this
// binary calls linkassert.Key.Mint other than handleLinkASSStart (FR1
// "never silently automatic"). Parses every non-test .go source file in
// this package via go/ast (same technique as
// manmanv2/ui/nfr5_bulk_env_write_guard_test.go's guard) and fails if a
// ".Mint(...)" call expression appears inside any function other than
// handleLinkASSStart.
func TestMint_OnlyCalledFromHandleLinkASSStart(t *testing.T) {
	anchor, err := runfiles.Rlocation("_main/whagent_net/ui/main.go")
	require.NoError(t, err, "is whagent_net/ui's ui_test data glob still present?")
	dir := filepath.Dir(anchor)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var srcFiles []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		srcFiles = append(srcFiles, name)
	}
	require.NotEmpty(t, srcFiles, "no non-test .go sources discovered in %s -- guard is not checking anything", dir)

	mintCallSites := 0
	for _, srcFile := range srcFiles {
		resolved := filepath.Join(dir, srcFile)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, resolved, nil, 0)
		require.NoError(t, err)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Mint" {
					return true
				}
				mintCallSites++
				if fn.Name.Name != "handleLinkASSStart" {
					t.Errorf("%s: %s calls .Mint(...) -- FR1 requires linkassert.Key.Mint's only call site to be handleLinkASSStart", srcFile, fn.Name.Name)
				}
				return true
			})
		}
	}
	require.Positive(t, mintCallSites, "no .Mint(...) call site found at all -- guard is not checking anything (has handleLinkASSStart stopped calling Mint?)")
}
