package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/identity"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/ui/pages"
	"github.com/whale-net/everything/libs/go/auth"
)

// credentialsNow is the instant the credentials page renders its relative
// ages from. Held still, so a row's "21 days ago" is a fact about the test
// rather than a coincidence with the wall clock.
var credentialsNow = time.Date(2026, 10, 4, 21, 0, 0, 0, time.UTC)

// credentialsIssuer is a configured OIDC issuer. operatorIdentity refuses
// to encode an empty issuer (a deployed UI has no identity to attribute
// with when it is unset), so every test that gets as far as a mint needs
// one.
const credentialsIssuer = "https://krill.test/realms/krill"

// newCredentialsApp builds the app the credentials handlers run on: the
// real gate and the real routes over a fake store, with the caller's realm
// roles resolving to the reader -- a reader may manage their own
// credentials, so the mint path is reachable at all.
func newCredentialsApp(t *testing.T, store *fakeCredentials) (*App, *http.ServeMux) {
	t.Helper()
	app := newTestApp(t)
	app.credentials = store
	app.oidcIssuer = credentialsIssuer
	app.roles = server.RoleConfig{ReaderRole: "krill-reader"}
	app.sessionRoles = func(*http.Request) ([]string, error) { return []string{"krill-reader"}, nil }
	app.now = func() time.Time { return credentialsNow }
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return app, mux
}

// credentialsGet issues one GET, optionally as an htmx fragment request.
func credentialsGet(t *testing.T, mux *http.ServeMux, path string, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// credentialsPostForm submits the create form: hx=true is the swap, false
// the no-JS path -- the same form posting to the same path.
func credentialsPostForm(t *testing.T, mux *http.ServeMux, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, credentialsMintPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// credentialsCallerIdentity is the identity the request under test resolves
// to, read from the authenticator rather than restated: a test that
// hardcoded the pair would still pass if the handler stopped resolving it.
func credentialsCallerIdentity(t *testing.T, app *App) string {
	t.Helper()
	user, err := app.auth.CurrentUser(httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	encoded, err := identity.Encode(credentialsIssuer, user.Sub)
	require.NoError(t, err)
	return encoded
}

// The register is the caller's LIVE credentials with the four columns the
// FR names. A revoked one is absent: this table has no status column, so a
// revoked row left in it would read as usable (FR 5e1af175).
func TestCredentialsPage_ListsOnlyLiveCredentials(t *testing.T) {
	created := credentialsNow.Add(-21 * 24 * time.Hour)
	used := credentialsNow.Add(-2 * time.Minute)
	never := credentialsNow.Add(-90 * 24 * time.Hour)
	revokedAt := credentialsNow

	store := &fakeCredentials{listed: []auth.Credential{
		{ID: uuid.New(), Name: "claude-code laptop", CreatedAt: created, LastUsedAt: &used},
		{ID: uuid.New(), Name: "ci runner", CreatedAt: never},
		{ID: uuid.New(), Name: "retired laptop", CreatedAt: created, RevokedAt: &revokedAt},
	}}
	_, mux := newCredentialsApp(t, store)

	rec := credentialsGet(t, mux, credentialsPath, false)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	assert.Contains(t, body, `data-krill="credentials-table"`)
	assert.Contains(t, body, ">claude-code laptop<")
	assert.Contains(t, body, ">ci runner<")
	assert.NotContains(t, body, "retired laptop", "a revoked credential must not linger looking live")

	// The relative age is what renders server-side; the exact instant rides
	// the title, so a hover answers "exactly when" either way.
	assert.Contains(t, body, ">21 days ago<")
	assert.Contains(t, body, `title="`+created.Format(time.RFC3339)+`"`)
	assert.Contains(t, body, ">2 min ago<")
	assert.Contains(t, body, `title="`+used.Format(time.RFC3339)+`"`)
	assert.Contains(t, body, ">never<")

	assert.Equal(t, 2, strings.Count(body, `data-krill="credential-revoke"`))
	assert.NotContains(t, body, "Generate a token", "the unbladed mint button is gone")
	assert.NotContains(t, body, "<th>Status</th>")
}

// No credentials is htmxui's empty state, and it points at the blade the
// header's action opens -- one way to create a credential, not two.
func TestCredentialsPage_EmptyStateWhenNoCredentials(t *testing.T) {
	_, mux := newCredentialsApp(t, &fakeCredentials{})

	body := credentialsGet(t, mux, credentialsPath, false).Body.String()

	assert.Contains(t, body, `data-krill="credentials-empty"`)
	assert.Contains(t, body, "No credentials yet.")
	assert.Contains(t, body, `href="`+credentialsNewPath+`"`)
	assert.NotContains(t, body, `data-krill="credentials-table"`)
}

// The blade URL is one view in two modes: an htmx request gets the bare
// fragment whose ROOT is the region it replaces, and a plain request gets
// the whole page in-shell with the blade open -- so a copied link, a
// bookmark and a reload all land on the blade.
func TestCredentialsBlade_ServesBothModes(t *testing.T) {
	_, mux := newCredentialsApp(t, &fakeCredentials{})

	frag := credentialsGet(t, mux, credentialsNewPath, true)
	require.Equal(t, http.StatusOK, frag.Code)
	fragment := frag.Body.String()
	assert.NotContains(t, fragment, "<html", "an htmx request must get the bare fragment, not the page")
	assert.True(t, strings.HasPrefix(fragment, `<div id="`+pages.CredentialsBladeAnchor+`"`),
		"the fragment's root must be the swap target or htmx deletes the region: %s", fragment)
	assert.Contains(t, fragment, `data-krill="credentials-blade-body"`)
	assert.Contains(t, fragment, `name="name"`)

	page := credentialsGet(t, mux, credentialsNewPath, false)
	require.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), "<html")
	assert.Contains(t, page.Body.String(), `data-krill="credentials-blade-body"`)
	assert.Contains(t, page.Body.String(), `data-krill="page-title"`)
}

// A refused name answers 200 with the blade re-rendered and the operator's
// typing intact. htmx does not swap on an error status, so an error status
// would drop the typing on the floor.
func TestMint_RefusesAnEmptyNameKeepingTheBlade(t *testing.T) {
	for _, mode := range []struct {
		name  string
		plain bool
	}{{"htmx", false}, {"no-js", true}} {
		t.Run(mode.name, func(t *testing.T) {
			store := &fakeCredentials{}
			_, mux := newCredentialsApp(t, store)

			rec := credentialsPostPlain(t, mux, url.Values{"name": {"   "}}, mode.plain)
			require.Equal(t, http.StatusOK, rec.Code, "a refusal is 200, never an error status")
			body := rec.Body.String()

			assert.Zero(t, store.minted, "an empty name must never reach the store")
			assert.Contains(t, body, "Enter a name for this credential.")
			assert.Contains(t, body, `data-krill="credentials-blade-body"`)
			assert.Contains(t, body, `id="credentials-results"`)
			assert.NotContains(t, body, "raw-token")
			if mode.plain {
				assert.Contains(t, body, "<html", "the no-JS refusal renders the blade in the shell at 200")
			}
		})
	}
}

// A name already live on another of the caller's credentials is the store's
// own refusal, and the page states it in its own words -- never a driver
// message or a SQLSTATE (FR 5e1af175).
func TestMint_RefusesADuplicateNameKeepingTheTypedName(t *testing.T) {
	store := &fakeCredentials{live: map[string]bool{"ci runner": true}}
	_, mux := newCredentialsApp(t, store)

	rec := credentialsPostForm(t, mux, url.Values{"name": {"ci runner"}})
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	assert.Equal(t, 1, store.minted, "the refusal came from the store, so the mint was attempted")
	assert.Contains(t, body, "Revoke it first, or pick another name.")
	assert.Contains(t, body, `value="ci runner"`, "the typed name survives the refusal")
	assert.Contains(t, body, `data-krill="credentials-blade-body"`)
	assert.NotContains(t, body, "SQLSTATE")
	assert.NotContains(t, body, "unique")
}

// Each of the store's two named refusals reaches the operator in this
// page's own words. The empty-name arm is refused before the store is
// reached; this pins the translation itself.
func TestMint_TranslatesTheStoresNamedRefusals(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{auth.ErrCredentialNameRequired, "Enter a name for this credential."},
		{auth.ErrCredentialNameTaken, "already named"},
	} {
		store := &fakeCredentials{mintErr: tc.err}
		_, mux := newCredentialsApp(t, store)

		rec := credentialsPostForm(t, mux, url.Values{"name": {"laptop"}})
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), tc.want)
		assert.NotContains(t, rec.Body.String(), "raw-token")
	}
}

// Success answers 200 and not a redirect: a 303 has no body to carry the
// token, and this is the only response that will ever have one.
func TestMint_ShowsTheTokenExactlyOnceAndNeverAgain(t *testing.T) {
	store := &fakeCredentials{token: "krill-secret-token"}
	_, mux := newCredentialsApp(t, store)

	rec := credentialsPostForm(t, mux, url.Values{"name": {"claude-code laptop"}})
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Equal(t, 1, strings.Count(body, "krill-secret-token"),
		"the raw token must appear exactly once in the response that carries it")
	assert.Contains(t, body, `data-krill="credentials-new-token-value"`)
	assert.Contains(t, body, "Copy this token now")
	// The blade closed and the new row is in the table underneath it.
	assert.NotContains(t, body, `data-krill="credentials-blade-body"`)
	assert.Contains(t, body, ">claude-code laptop<")

	// A later load has nothing to show: the token is not recoverable.
	later := credentialsGet(t, mux, credentialsPath, false).Body.String()
	assert.NotContains(t, later, "krill-secret-token")
	assert.NotContains(t, later, "it will not be shown again")
	assert.Contains(t, later, ">claude-code laptop<")
}

// A credential is filed under the caller's OWN encoded identity, resolved
// from their session, under the name they typed. Nothing about the caller
// is posted -- an identity in the form is ignored, and none of it is
// rendered back (LB4, FR 6d8c70b2).
func TestMint_FilesTheCredentialUnderTheCallersOwnIdentity(t *testing.T) {
	store := &fakeCredentials{}
	app, mux := newCredentialsApp(t, store)

	rec := credentialsPostForm(t, mux, url.Values{
		"name":     {"  ci runner  "},
		"identity": {"someone-else"},
		"subject":  {"someone-else"},
	})
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, []string{credentialsCallerIdentity(t, app)}, store.mintedIdentities)
	assert.Equal(t, []string{"ci runner"}, store.mintedNames, "the typed name, trimmed")
	assert.NotContains(t, rec.Body.String(), "someone-else", "a posted identity is never taken and never echoed")
}

// credentialsPostPlain is the no-JS path: the same form, posted without
// the HX-Request header, so the handler answers the whole page at 200.
func credentialsPostPlain(t *testing.T, mux *http.ServeMux, form url.Values, plain bool) *httptest.ResponseRecorder {
	t.Helper()
	if !plain {
		return credentialsPostForm(t, mux, form)
	}
	req := httptest.NewRequest(http.MethodPost, credentialsMintPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}