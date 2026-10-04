package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/whagentlink"
	"github.com/whale-net/everything/manmanv2/identitylink"
)

const (
	linkTestWhagent = "https://whagent.example.test"
	linkTestReturn  = linkTestWhagent + "/link/manmanv2/result"
)

type fakeLinkVerifier struct {
	assertion *whagentlink.Assertion
	err       error
}

func (f fakeLinkVerifier) Verify(context.Context, string) (*whagentlink.Assertion, error) {
	return f.assertion, f.err
}

type fakeLinkStore struct {
	consumed map[string]bool
	links    map[string]string // "iss|sub" -> user sub
}

func newFakeLinkStore() *fakeLinkStore {
	return &fakeLinkStore{consumed: map[string]bool{}, links: map[string]string{}}
}

func (f *fakeLinkStore) IsConsumed(_ context.Context, jti string) (bool, error) {
	return f.consumed[jti], nil
}

func (f *fakeLinkStore) Consume(_ context.Context, jti string, _ time.Time) error {
	if f.consumed[jti] {
		return identitylink.ErrAssertionConsumed
	}
	f.consumed[jti] = true
	return nil
}

func (f *fakeLinkStore) Link(_ context.Context, iss, sub, userSub string) (identitylink.Outcome, error) {
	k := iss + "|" + sub
	if existing, ok := f.links[k]; ok {
		if existing == userSub {
			return identitylink.AlreadyLinked, nil
		}
		return 0, identitylink.ErrLinkedToOtherUser
	}
	f.links[k] = userSub
	return identitylink.Created, nil
}

type fakeGrants struct {
	start    bool
	err      error
	returnTo string
}

func (f *fakeGrants) Begin(w http.ResponseWriter, r *http.Request, returnTo string) (bool, error) {
	f.returnTo = returnTo
	if f.start {
		http.Redirect(w, r, "https://kc.example.test/consent", http.StatusFound)
	}
	return f.start, f.err
}

func validAssertion() *whagentlink.Assertion {
	return &whagentlink.Assertion{
		Issuer: linkTestWhagent, Subject: "op-1", SubjectIssuer: "https://kc/realms/x",
		ID: "jti-1", Expiry: time.Now().Add(time.Minute), ReturnURL: linkTestReturn,
	}
}

func newLinkHarness(t *testing.T, v fakeLinkVerifier) (*whagentLinkHandlers, *fakeLinkStore, *fakeGrants, *http.ServeMux) {
	t.Helper()
	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "link-test-secret-at-least-32-bytes-long!",
		SessionName:   "manmanv2_ui_link_test_session",
	})
	require.NoError(t, err)
	store := newFakeLinkStore()
	grants := &fakeGrants{}
	h := &whagentLinkHandlers{verifier: v, store: store, grants: grants, whagentURL: linkTestWhagent}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /link/whagent", auth.RequireAuthFunc(h.handleShow))
	mux.HandleFunc("POST /link/whagent/confirm", auth.RequireAuthFunc(h.handleConfirm))
	mux.HandleFunc("GET "+linkCompletePath, auth.RequireAuthFunc(h.handleComplete))
	return h, store, grants, mux
}

func do(mux *http.ServeMux, method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func outcomeOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusSeeOther, w.Code)
	loc, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, linkTestWhagent, loc.Scheme+"://"+loc.Host)
	return loc.Query().Get("outcome")
}

func confirmBody() string { return url.Values{"token": {"t"}}.Encode() }

func TestLinkShow_ValidAssertionRendersConfirmation(t *testing.T) {
	_, _, _, mux := newLinkHarness(t, fakeLinkVerifier{assertion: validAssertion()})
	w := do(mux, "GET", "/link/whagent?token=t", "", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Confirm link")
	assert.Contains(t, w.Body.String(), "op-1")
}

func TestLinkShow_InvalidTokenIsRejectedPage(t *testing.T) {
	_, _, _, mux := newLinkHarness(t, fakeLinkVerifier{err: whagentlink.ErrInvalidSignature})
	w := do(mux, "GET", "/link/whagent?token=garbage", "", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Link request rejected")
	w = do(mux, "GET", "/link/whagent", "", nil)
	assert.Contains(t, w.Body.String(), "Link request rejected")
}

func TestLinkShow_ReplayedAssertionRedirectsRejected(t *testing.T) {
	_, store, _, mux := newLinkHarness(t, fakeLinkVerifier{assertion: validAssertion()})
	store.consumed["jti-1"] = true
	assert.Equal(t, linkOutcomeRejected, outcomeOf(t, do(mux, "GET", "/link/whagent?token=t", "", nil)))
}

func TestLinkShow_ReturnOriginMismatchIsRejectedPage(t *testing.T) {
	a := validAssertion()
	a.ReturnURL = "https://evil.example.test/steal"
	_, _, _, mux := newLinkHarness(t, fakeLinkVerifier{assertion: a})
	w := do(mux, "GET", "/link/whagent?token=t", "", nil)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Link request rejected")
}

func TestLinkConfirm_LinksSignedInUserAndReplayIsRejected(t *testing.T) {
	_, store, _, mux := newLinkHarness(t, fakeLinkVerifier{assertion: validAssertion()})
	w := do(mux, "POST", "/link/whagent/confirm", confirmBody(), nil)
	assert.Equal(t, linkOutcomeLinked, outcomeOf(t, w))
	assert.Equal(t, "dev-user", store.links["https://kc/realms/x|op-1"], "mapping must target the signed-in manmanv2 user")
	assert.True(t, store.consumed["jti-1"])

	w = do(mux, "POST", "/link/whagent/confirm", confirmBody(), nil)
	assert.Equal(t, linkOutcomeRejected, outcomeOf(t, w))
}

func TestLinkConfirm_AlreadyLinkedAndConflict(t *testing.T) {
	_, store, _, mux := newLinkHarness(t, fakeLinkVerifier{assertion: validAssertion()})
	store.links["https://kc/realms/x|op-1"] = "dev-user"
	assert.Equal(t, linkOutcomeAlreadyLinked, outcomeOf(t, do(mux, "POST", "/link/whagent/confirm", confirmBody(), nil)))

	_, store, _, mux = newLinkHarness(t, fakeLinkVerifier{assertion: validAssertion()})
	store.links["https://kc/realms/x|op-1"] = "someone-else"
	assert.Equal(t, linkOutcomeConflict, outcomeOf(t, do(mux, "POST", "/link/whagent/confirm", confirmBody(), nil)))
}

func TestLinkConfirm_RefusesCrossSitePost(t *testing.T) {
	_, store, _, mux := newLinkHarness(t, fakeLinkVerifier{assertion: validAssertion()})
	w := do(mux, "POST", "/link/whagent/confirm", confirmBody(), map[string]string{"Sec-Fetch-Site": "cross-site"})
	assert.Equal(t, http.StatusForbidden, w.Code)
	w = do(mux, "POST", "/link/whagent/confirm", confirmBody(), map[string]string{"Origin": "https://evil.example.test"})
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Empty(t, store.links)
	assert.Empty(t, store.consumed)
}

func TestLinkConfirm_StartsConsentWhenGrantMissing(t *testing.T) {
	_, store, grants, mux := newLinkHarness(t, fakeLinkVerifier{assertion: validAssertion()})
	grants.start = true
	w := do(mux, "POST", "/link/whagent/confirm", confirmBody(), nil)
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "kc.example.test/consent")
	assert.NotEmpty(t, store.links, "mapping is written before consent")

	resume, err := url.Parse(grants.returnTo)
	require.NoError(t, err)
	assert.Equal(t, linkCompletePath, resume.Path)
	assert.Equal(t, linkTestReturn, resume.Query().Get("return"))
	assert.Equal(t, linkOutcomeLinked, resume.Query().Get("outcome"))
}

func TestLinkConfirm_GrantErrorIs500(t *testing.T) {
	_, _, grants, mux := newLinkHarness(t, fakeLinkVerifier{assertion: validAssertion()})
	grants.err = errors.New("boom")
	assert.Equal(t, http.StatusInternalServerError, do(mux, "POST", "/link/whagent/confirm", confirmBody(), nil).Code)
}

func TestLinkComplete(t *testing.T) {
	_, _, _, mux := newLinkHarness(t, fakeLinkVerifier{})
	q := url.Values{"return": {linkTestReturn}, "outcome": {linkOutcomeLinked}}
	assert.Equal(t, linkOutcomeLinked, outcomeOf(t, do(mux, "GET", linkCompletePath+"?"+q.Encode(), "", nil)))

	// Open-redirect and forged outcomes are refused.
	q.Set("return", "https://evil.example.test/x")
	assert.Contains(t, do(mux, "GET", linkCompletePath+"?"+q.Encode(), "", nil).Body.String(), "Link request rejected")
	q = url.Values{"return": {linkTestReturn}, "outcome": {linkOutcomeConflict}}
	assert.Contains(t, do(mux, "GET", linkCompletePath+"?"+q.Encode(), "", nil).Body.String(), "Link request rejected")
}
