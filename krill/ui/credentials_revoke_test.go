package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
	"github.com/whale-net/everything/libs/go/auth"
)

// ---------------------------------------------------------------------------
// revoke: ask, dismiss, confirm
// ---------------------------------------------------------------------------

// credentialsRevokeApp builds an app whose store holds one live credential
// named "ci runner", and returns the app, the mux and that credential's id.
func credentialsRevokeApp(t *testing.T) (*App, *http.ServeMux, *fakeCredentials, uuid.UUID) {
	t.Helper()
	store := &fakeCredentials{now: credentialsNow, listed: []auth.Credential{{
		ID:        uuid.New(),
		Name:      "ci runner",
		CreatedAt: credentialsNow.Add(-30 * 24 * time.Hour),
	}}}
	app, mux := newCredentialsApp(t, store)
	return app, mux, store, store.listed[0].ID
}

// credentialsRevokeGet issues a GET against the row's own routes.
func credentialsRevokeGet(t *testing.T, mux *http.ServeMux, path string, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// credentialsRevokeConfirm answers the confirmation: hx=true is the swap,
// false the no-JS path. Nothing is posted with it -- the id in the URL is
// the whole request, exactly as the row's form sends it.
func credentialsRevokeConfirm(t *testing.T, mux *http.ServeMux, id string, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, credentialRevokePath(id), nil)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Activating Revoke posts nothing and asks inline: the row's confirm URL
// answers with the question in the row, naming the credential and the
// consequence, rooted at the row's own region so the swap replaces it and
// leaves a target behind (FR ab55875e).
func TestRevoke_AskingPostsNothingAndConfirmsInTheRow(t *testing.T) {
	_, mux, store, id := credentialsRevokeApp(t)

	rec := credentialsRevokeGet(t, mux, credentialRevokePath(id.String()), true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	assert.Empty(t, store.revokedIDs, "asking a question is not a revocation")
	assert.Contains(t, body, "Revoke ci runner? Clients using it lose access immediately.")
	assert.Contains(t, body, `data-krill="credential-revoke-form"`)
	assert.Contains(t, body, `action="`+credentialRevokePath(id.String())+`"`,
		"the confirming form posts to the revoke route")
	assert.Equal(t, []string{"div"}, topLevelElements(t, body),
		"the fragment must be exactly the row region it replaces")
	assert.Contains(t, body, `id="`+pages.CredentialRevokeRegionID(id.String())+`"`)
	assert.NotContains(t, body, "<html", "an htmx request gets the bare region")
}

// A no-JS click follows the row's real href and lands on the page with
// that one row confirming -- the same view, the whole page around it.
func TestRevoke_ConfirmURLRenderedDirectlyShowsTheQuestionInShell(t *testing.T) {
	_, mux, store, id := credentialsRevokeApp(t)

	rec := credentialsRevokeGet(t, mux, credentialRevokePath(id.String()), false)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Empty(t, store.revokedIDs)
	assert.Contains(t, rec.Body.String(), "<html")
	assert.Contains(t, rec.Body.String(), "Revoke ci runner? Clients using it lose access immediately.")
	assert.Contains(t, rec.Body.String(), `data-krill="credentials-table"`,
		"the list the operator was reading is what re-renders, not a bare fragment")
}

// Dismissing asks for the row's own URL and posts nothing: the row comes
// back at rest and no credential was revoked.
func TestRevoke_DismissReturnsTheRowAndPostsNothing(t *testing.T) {
	_, mux, store, id := credentialsRevokeApp(t)
	idStr := id.String()

	rec := credentialsRevokeGet(t, mux, credentialRowPath(idStr), true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	assert.Empty(t, store.revokedIDs, "dismissal must not reach the store at all")
	assert.Contains(t, body, `data-krill="credential-revoke"`, "the row is back at rest")
	assert.NotContains(t, body, `data-krill="credential-revoke-form"`)
	assert.NotContains(t, body, "Clients using it lose access immediately.")
	assert.Contains(t, body, `id="`+pages.CredentialRevokeRegionID(idStr)+`"`)
}

// The htmx confirm answers 200 with the list re-read from fresh state --
// the revoked row has left the table -- plus EXACTLY one out-of-band
// toast. One, because a second copy swapped into the same host would
// replace nothing and read as a confirmation nobody received.
func TestRevoke_HtmxConfirmRemovesTheRowAndToastsOnce(t *testing.T) {
	app, mux, store, id := credentialsRevokeApp(t)

	rec := credentialsRevokeConfirm(t, mux, id.String(), true)
	require.Equal(t, http.StatusOK, rec.Code, "a swap target never sees a status code")
	body := rec.Body.String()

	assert.Equal(t, []uuid.UUID{id}, store.revokedIDs)
	assert.Equal(t, []string{credentialsCallerIdentity(t, app)}, store.revokedIdentities,
		"the revoke is filed under the caller's own identity, resolved from their session")

	// The row is gone from the LIST. Its name survives only in the toast,
	// which is the confirmation -- so the list is asserted by its rows, not
	// by the absence of a string the toast legitimately carries.
	assert.NotContains(t, body, `data-krill="credential-row"`)
	assert.NotContains(t, body, `data-krill="credential-name"`)
	assert.Contains(t, body, `id="credentials-results"`, "the list re-rendered in place")
	assert.Contains(t, body, `data-krill="credentials-empty"`,
		"the last row leaving shows the empty state, not an empty table")

	assert.Equal(t, 1, strings.Count(body, "hx-swap-oob"), "exactly one out-of-band toast")
	assert.Equal(t, 1, strings.Count(body, `data-krill="toast"`))
	assert.Contains(t, body, "Revoked ci runner.", "the confirmation names what was removed")
	assert.Contains(t, body, `id="`+components.ToastHostID+`"`)
}

// The no-JS path is the Post/Redirect/Get: a 303 has no body to carry the
// confirmation in, so the message rides the one-shot cookie the landing
// page renders as a success alert.
func TestRevoke_ConfirmWithoutJavaScriptRedirectsWithTheFlash(t *testing.T) {
	_, mux, store, id := credentialsRevokeApp(t)

	rec := credentialsRevokeConfirm(t, mux, id.String(), false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, credentialsPath, rec.Header().Get("Location"))
	assert.Equal(t, []uuid.UUID{id}, store.revokedIDs)

	var flashed string
	for _, c := range rec.Result().Cookies() {
		if c.Name == toastCookieName {
			flashed = c.Value
		}
	}
	require.NotEmpty(t, flashed, "the confirmation has to reach the no-JS operator somehow")
	assert.NotContains(t, rec.Body.String(), "ci runner", "the redirect carries no body")
}

// A store refusal is not a success and not a bare error page: the list
// re-renders at 200 with the reason inline, and no toast claims it worked.
func TestRevoke_StoreRefusalRendersTheListWithTheReasonInline(t *testing.T) {
	_, mux, store, id := credentialsRevokeApp(t)
	store.revokeErr = errors.New("store refused")

	rec := credentialsRevokeConfirm(t, mux, id.String(), true)
	require.Equal(t, http.StatusOK, rec.Code, "htmx does not swap on an error status")
	body := rec.Body.String()

	assert.Contains(t, body, "Could not revoke the credential.")
	assert.Contains(t, body, `data-krill="credentials-alert"`)
	assert.Contains(t, body, `id="credentials-results"`, "the swap target survives a refusal")
	assert.Contains(t, body, ">ci runner<", "the credential is still live and still listed")
	assert.NotContains(t, body, "hx-swap-oob", "a refusal is not a toast")
	assert.NotContains(t, body, "Revoked ci runner.")
	assert.NotContains(t, body, "store refused", "the store's own text never reaches the page")
}

// A store refusal on the no-JS path is the same 200 with the reason
// inline, not a redirect: there is nothing to have succeeded at.
func TestRevoke_StoreRefusalWithoutJavaScriptStaysAt200(t *testing.T) {
	_, mux, store, id := credentialsRevokeApp(t)
	store.revokeErr = errors.New("store refused")

	rec := credentialsRevokeConfirm(t, mux, id.String(), false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"), "a refusal must not redirect away from the reason")
	assert.Contains(t, rec.Body.String(), "Could not revoke the credential.")
	assert.Contains(t, rec.Body.String(), "<html")
}

// A read failure behind the revocation still answers with the reason
// inline. The write may well have landed; what the page must not do is
// show a confident, wrong list.
func TestRevoke_ReadFailureAfterTheWriteRendersInline(t *testing.T) {
	_, mux, store, id := credentialsRevokeApp(t)
	store.listErr = errors.New("list unavailable")

	rec := credentialsRevokeConfirm(t, mux, id.String(), true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	assert.Contains(t, body, "Could not load credentials.")
	assert.Contains(t, body, `data-krill="credentials-alert"`)
	assert.Contains(t, body, `id="credentials-results"`)
	assert.NotContains(t, body, `data-krill="credentials-table"`,
		"a read failure must not render as an empty list")
	assert.NotContains(t, body, "list unavailable")
}

// The non-disclosure acceptance condition. A credential that is not the
// caller's is the store's silent no-op, and the page's answer is the same
// answer the list itself gives: everything the caller already had, nothing
// more. No alert, no toast, no name, no row -- and no way to tell a foreign
// id from a stale one.
func TestRevoke_ForeignOrStaleIDDisclosesNothingAndLeavesTheListIntact(t *testing.T) {
	// The stale arm is built by actually revoking first, so it is the same
	// request arriving twice -- which is what "stale" means -- rather than
	// a fixture that pretends a row is gone.
	_, mux, store, id := credentialsRevokeApp(t)
	require.Equal(t, http.StatusOK, credentialsRevokeConfirm(t, mux, id.String(), true).Code)

	for _, tc := range []struct {
		name string
		id   string
	}{
		{"an id the caller has never held", uuid.New().String()},
		{"an id that is already revoked", id.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := credentialsGet(t, mux, credentialsPath, false).Body.String()

			rec := credentialsRevokeConfirm(t, mux, tc.id, true)
			require.Equal(t, http.StatusOK, rec.Code)
			body := rec.Body.String()

			// Nothing is said about whether that credential exists.
			assert.NotContains(t, body, "hx-swap-oob", "no toast: there is nothing to have confirmed")
			assert.NotContains(t, body, `data-krill="credentials-alert"`, "no alert either")
			assert.NotContains(t, body, "Revoked ", "nothing is named as revoked")
			assert.NotContains(t, body, tc.id, "the id is not echoed back in any message")
			assert.NotContains(t, body, "not found")
			assert.NotContains(t, body, "belongs to")

			// The list the caller could already see is unchanged.
			after := credentialsGet(t, mux, credentialsPath, false).Body.String()
			assert.Equal(t, before, after, "the caller's own list must be untouched")
		})
	}

	// The store was reached, and idempotently so: asking twice is not an
	// error and does not un-revoke anything.
	assert.Len(t, store.revokedIDs, 3, "one real revoke plus the two no-ops")
}

// The store was reached with the URL's id and the CALLER's identity -- and
// with nothing else. Nothing about the operator is posted or rendered
// (LB4, FR 6d8c70d2): the identity comes from their session, so a posted
// one is ignored and none of it is echoed back.
func TestRevoke_FilesTheRevocationUnderTheCallersOwnIdentity(t *testing.T) {
	app, mux, store, id := credentialsRevokeApp(t)

	req := httptest.NewRequest(http.MethodPost, credentialRevokePath(id.String()),
		strings.NewReader(url.Values{
			"identity": {"someone-else"},
			"subject":  {"someone-else"},
		}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, []string{credentialsCallerIdentity(t, app)}, store.revokedIdentities,
		"a posted identity is never taken; the caller's own comes from their session")
	assert.NotContains(t, rec.Body.String(), "someone-else", "and none of it is echoed back")
}

// The revoke step's own responses are pure server-rendered markup: every
// state an htmx browser swaps in carries no script and no scripted state
// flip. (The whole-page renders do load htmx and Alpine from the shell --
// what is asserted here is that the STEP adds nothing to them.)
func TestRevoke_SwappedResponsesNeedNoScript(t *testing.T) {
	_, mux, _, id := credentialsRevokeApp(t)

	for _, tc := range []struct {
		name string
		body string
	}{
		{"the question", credentialsRevokeGet(t, mux, credentialRevokePath(id.String()), true).Body.String()},
		{"the dismissal", credentialsRevokeGet(t, mux, credentialRowPath(id.String()), true).Body.String()},
		{"the row in the list", credentialsGet(t, mux, credentialsPath, true).Body.String()},
		{"the confirmation", credentialsRevokeConfirm(t, mux, id.String(), true).Body.String()},
	} {
		assert.NotContains(t, tc.body, "<script", tc.name+" must not need JavaScript")
		assert.NotContains(t, tc.body, "hx-confirm",
			tc.name+": the confirmation is a rendered region, not a browser dialog")
	}
}

// The route the FR names is the route that is mounted, and the row's own
// URL answers too -- a revoke step whose no-JS branch 404s is a revoke step
// that only works in one browser.
func TestRevoke_RoutesAreMounted(t *testing.T) {
	_, mux, _, id := credentialsRevokeApp(t)

	for _, path := range []string{
		credentialRevokePath(id.String()),
		credentialRowPath(id.String()),
	} {
		assert.Equal(t, http.StatusOK, credentialsRevokeGet(t, mux, path, false).Code, path)
		assert.Equal(t, http.StatusOK, credentialsRevokeGet(t, mux, path, true).Code, path+" as htmx")
	}
}

// topLevelElements returns the tag name of every top-level element in a
// served fragment, parsed with a real HTML parser rather than a scanner:
// "is this fragment exactly its swap target" is the invariant a tag scanner
// gets wrong, and the repo's other swap-target tests parse for the same
// reason.
func topLevelElements(t *testing.T, body string) []string {
	t.Helper()
	nodes, err := html.ParseFragment(strings.NewReader(body), &html.Node{
		Type: html.ElementNode, Data: "body", DataAtom: atom.Body,
	})
	require.NoError(t, err)
	var tags []string
	for _, n := range nodes {
		if n.Type == html.ElementNode {
			tags = append(tags, n.Data)
		}
	}
	return tags
}
