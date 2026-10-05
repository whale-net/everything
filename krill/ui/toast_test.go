// Coverage for the shell's mutation feedback: one toast host, and the two
// paths a success message reaches the operator through (FR c97a5018).
//
// Each case drives the real form POST through operatorRoute ->
// requireOperator -> withKrillSession -> the real write client against the
// fake api in harness_test.go, so what is asserted is the bytes a browser
// would receive, not the mechanism's internals. The four bullets the
// requirement states are each one test:
//
//  1. a success naming a message drops exactly one toast into the host,
//     carrying the auto-dismiss timer and a dismiss control;
//  2. a success naming no message shows nothing at all;
//  3. a refusal (the P0 escalate-of-a-Done-lane 409) is an inline alert and
//     NO toast, so a toast is never the only record of an outcome; and
//  4. with JavaScript disabled the same success lands on a page showing the
//     same message as a success-severity alert.
//
// The no-JS path is driven through a cookie jar, not by calling
// takeFlashSuccess twice on one request. The one-shot guarantee lives in the
// response's Set-Cookie, so the jar is the only thing that can honestly model
// it: a browser re-sends a cookie it was given and stops sending one the
// server expired.
package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// toastJar is the minimum of a browser's cookie store the no-JS path needs:
// it stores the cookies a response Set-Cookied and replays them on the next
// request, and drops a cookie the server expired. Modelling the jar rather
// than calling takeFlashSuccess directly is deliberate -- the one-shot rule
// is carried by the response header, so only a store that honours an expiring
// Set-Cookie can show it holds.
type toastJar struct{ cookies map[string]*http.Cookie }

func newToastJar() *toastJar { return &toastJar{cookies: map[string]*http.Cookie{}} }

// absorb records every Set-Cookie on a response, deleting the ones the
// response expired (an empty value or a negative MaxAge) so a later request
// does not carry them -- what the browser's own store does.
func (j *toastJar) absorb(rec *httptest.ResponseRecorder) {
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(j.cookies, c.Name)
			continue
		}
		j.cookies[c.Name] = c
	}
}

// attach adds every stored cookie to a request, the way the browser would.
func (j *toastJar) attach(req *http.Request) {
	for _, c := range j.cookies {
		req.AddCookie(c)
	}
}

// get issues a plain page load with the jar's cookies attached.
func (j *toastJar) get(mux *http.ServeMux, target string, extra ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	j.attach(req)
	for _, c := range extra {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// toastMux registers the intervention routes plus the console view a
// no-JS post redirects back to, both behind the same gates setupRoutes
// mounts. Paths are literals so a test cannot pass by tracking a constant
// that changed with the implementation.
func toastMux(app *App) *http.ServeMux {
	mux := newInterventionMux(app)
	mux.HandleFunc("GET /ops/claimed", app.readerRoute(app.handleClaimedTasks))
	mux.HandleFunc("GET /ops/escalated", app.readerRoute(app.handleEscalatedTasks))
	return mux
}

// newToastApp wires a signed-in operator to a fake api and to the
// console-query surface, with the reader persona the landing pages' gate
// requires (the cookie-backed test session does not persist roles), and
// returns the mux a browser would talk to.
func newToastApp(t *testing.T, api *fakeAPI) (*http.Cookie, *http.ServeMux) {
	t.Helper()
	app, sessionCookie, _ := newHtmxInterventionApp(t, api, uuid.NewString())
	app.sessionRoles = func(*http.Request) ([]string, error) { return []string{"krill-operator"}, nil }
	app.roles = server.RoleConfig{OperatorRole: "krill-operator", ReaderRole: "krill-reader"}
	return sessionCookie, toastMux(app)
}

// countToasts is how many toasts a response carries. Scoped to the
// data-krill="toast" marker with its closing quote so the toast HOST
// (data-krill="toast-host") is not counted as one.
func countToasts(body string) int {
	return strings.Count(body, `data-krill="toast"`)
}

// alertMessageOf extracts the EXACT text an alert rendered under marker (the
// htmx toast or the no-JS flash). htmxui.Alert renders its message as the
// content of a <span class="text-sm">, so reading that span is what makes
// "exactly this string" assertable: a Contains check would pass on
// "Claim released." or "Task requeued; it is claimable again." just as well,
// and the FR names its three confirmations as exact strings.
func alertMessageOf(t *testing.T, body, marker string) string {
	t.Helper()
	i := strings.Index(body, marker)
	require.GreaterOrEqual(t, i, 0, "no element carries %s", marker)
	rest := body[i:]
	const open = `<span class="text-sm">`
	j := strings.Index(rest, open)
	require.GreaterOrEqual(t, j, 0, "the alert carries no message span")
	rest = rest[j+len(open):]
	k := strings.Index(rest, `</span>`)
	require.GreaterOrEqual(t, k, 0, "the message span is not closed")
	return rest[:k]
}

// ---------------------------------------------------------------------------
// 1. a success naming a message shows exactly one toast
// ---------------------------------------------------------------------------

// TestSuccessfulMutationShowsOneToastInTheHost requires a successful htmx
// mutation to answer 200 with a single out-of-band toast addressed to the
// host, carrying the message, a self-dismiss timer, and a dismiss control.
// "One" is the load-bearing word: a burst of mutations must not stack two
// confirmations for one action, and a success must not also leave an inline
// copy behind.
func TestSuccessfulMutationShowsOneToastInTheHost(t *testing.T) {
	for _, tc := range []struct{ verb, message string }{
		{"escalate", "Task escalated"},
		{"release", "Claim released"},
		{"requeue", "Task requeued"},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			api := newFakeAPI(t)
			sessionCookie, mux := newToastApp(t, api)

			rec := hxFormPost(mux, "/ops/tasks/"+uuid.NewString()+"/"+tc.verb,
				url.Values{"reason": {"x"}, "return_to": {"/ops/claimed"}}, sessionCookie)

			require.Equal(t, http.StatusOK, rec.Code)
			got := rec.Body.String()
			assert.Equal(t, 1, countToasts(got), "exactly one toast per successful mutation")
			assert.Equal(t, tc.message, alertMessageOf(t, got, `data-krill="toast"`),
				"the toast states the verb's own confirmation, exactly")
			assert.Contains(t, got, `hx-swap-oob="beforeend"`,
				"the toast is appended to the host, not swapped in as the host")
			assert.Contains(t, got, `id="`+components.ToastHostID+`"`,
				"the OOB swap addresses the host by the id the component declares")

			// Dismissal, both ways: the toast removes itself after a few
			// seconds, and the operator can dismiss it now.
			assert.Contains(t, got, "x-init=", "the toast schedules its own removal")
			assert.Contains(t, got, "setTimeout(")
			assert.Contains(t, got, "$el.remove()", "removing the toast must not disturb the host or its siblings")
			assert.Contains(t, got, "alert-success", "a confirmation is success severity, not an error")
			assert.Contains(t, got, `data-krill="toast-dismiss"`, "the toast carries a dismiss control")
			assert.Contains(t, got, "Dismiss this message")
		})
	}
}

// TestToastHostIsAnAriaLiveRegionOutsideTheSwappedRegion states the two
// properties that make a toast readable at all: the host is an aria-live
// status region (a live region only announces nodes inserted after it was
// rendered), and the OOB toast is appended to it rather than replacing it
// (an outerHTML swap would take the live region and its wiring with it).
func TestToastHostIsAnAriaLiveRegionOutsideTheSwappedRegion(t *testing.T) {
	host := mustRenderComponent(components.ToastHost())
	assert.Contains(t, host, `aria-live="status"`, "the host must announce toasts it did not render")
	assert.Contains(t, host, `id="`+components.ToastHostID+`"`, "the host's id is the OOB swap's address")
	assert.NotContains(t, host, "alert-", "the host itself carries no message; toasts arrive by swap")

	oob := mustRenderComponent(components.ToastOOB("Claim released"))
	assert.Contains(t, oob, `hx-swap-oob="beforeend"`)
	assert.NotContains(t, oob, `hx-swap-oob="outerHTML"`,
		"an outerHTML swap would replace the host, so the second toast of a session would announce nothing")
}

// ---------------------------------------------------------------------------
// 2. a success naming no message shows nothing
// ---------------------------------------------------------------------------

// TestMutationNamingNoMessageShowsNoToast requires the empty-means-nothing
// rule on both halves of the mechanism: an empty toast appends nothing to
// the fragment, and an empty flash arms no cookie. A mutation that names no
// message must show no confirmation rather than an empty box the operator
// has to interpret.
func TestMutationNamingNoMessageShowsNoToast(t *testing.T) {
	page := pages.EscalatedData{Href: "/ops/escalated"}
	for _, message := range []string{"", "   ", "\n\t"} {
		t.Run("message="+strings.ReplaceAll(message, "\n", "\\n"), func(t *testing.T) {
			// The htmx half: an empty message leaves the fragment byte-for-byte
			// what it would have been without the mechanism.
			want := mustRenderComponent(pages.EscalatedResults(page))
			assert.Equal(t, want, mustRenderComponent(withToast(message, pages.EscalatedResults(page))),
				"an empty message appends nothing")

			// The no-JS half: nothing is armed, so nothing can be replayed.
			rec := httptest.NewRecorder()
			flashSuccess(rec, message)
			assert.Empty(t, rec.Result().Cookies(), "an empty message arms no flash cookie")
		})
	}
}

// TestHandlerSuccessWithNoMessageRendersNoToast is the same rule stated at
// the handler seam rather than at the helper: a 200 results fragment
// re-derived for a mutation that named no message carries no toast markup,
// no OOB wrapper, and nothing for a swap to act on.
func TestHandlerSuccessWithNoMessageRendersNoToast(t *testing.T) {
	api := newFakeAPI(t)
	app, _, _ := newHtmxInterventionApp(t, api, uuid.NewString())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ops/tasks/x/escalate", nil)
	req.Header.Set("HX-Request", "true")
	// return_to names a console tab, so the acted-on task id is never
	// consulted; any id does.
	app.renderInterventionResults(rec, req, uuid.Nil, "/ops/escalated", "", "")

	got := rec.Body.String()
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, countToasts(got), "a mutation naming no message shows no toast")
	assert.NotContains(t, got, "hx-swap-oob", "an empty message emits no out-of-band markup at all")
	assert.Contains(t, got, "a still-escalated task", "the view itself still renders")
}

// ---------------------------------------------------------------------------
// 3. a refusal is an inline alert and no toast
// ---------------------------------------------------------------------------

// TestRefusedMutationShowsInlineAlertAndNoToast requires the P0 refusal --
// a manual escalate of a task already in the terminal Done lane, which the
// store answers 409 -- to reach the operator as an inline alert in the
// results block, with no toast anywhere in the response. A toast is
// transient by design, so an outcome recorded only there would be gone
// before a slow operator looked.
func TestRefusedMutationShowsInlineAlertAndNoToast(t *testing.T) {
	api := newFakeAPI(t)
	api.rejectWrite(http.StatusConflict,
		`{"error":"krill/store: task is already in the terminal Done lane"}`)
	sessionCookie, mux := newToastApp(t, api)

	rec := hxFormPost(mux, "/ops/tasks/"+uuid.NewString()+"/escalate",
		url.Values{"reason": {"force"}, "return_to": {"/ops/escalated"}}, sessionCookie)

	require.Equal(t, http.StatusOK, rec.Code, "a refusal is presented, not status-coded")
	got := rec.Body.String()
	assert.Contains(t, got, "task is already in the terminal Done lane",
		"the api's own message reaches the operator")
	assert.Contains(t, got, `role="alert"`, "the refusal rides inline in the fragment")
	assert.Contains(t, got, "alert-error", "a refusal is error severity")
	assert.NotContains(t, got, `{"error"`, "the raw api JSON must not leak to the browser")

	assert.Equal(t, 0, countToasts(got), "a refused mutation raises no toast")
	assert.NotContains(t, got, "hx-swap-oob", "nothing is appended to the host for a refusal")
	assert.NotContains(t, got, "krill_toast", "no flash cookie is armed for a refusal")
}

// TestRefusedNoJSMutationShowsNoToast is the same rule on the no-JS path: a
// refused form post answers api's own status in the shell and arms no
// flash, so the landing page has nothing to announce.
func TestRefusedNoJSMutationShowsNoToast(t *testing.T) {
	api := newFakeAPI(t)
	api.rejectWrite(http.StatusConflict,
		`{"error":"krill/store: task is already in the terminal Done lane"}`)
	sessionCookie, mux := newToastApp(t, api)

	rec := serveFormPost(mux, "/ops/tasks/"+uuid.NewString()+"/escalate",
		url.Values{"reason": {"force"}, "return_to": {"/ops/escalated"}}, sessionCookie)

	assert.Equal(t, http.StatusConflict, rec.Code, "api's status is relayed verbatim")
	assert.Contains(t, rec.Body.String(), "task is already in the terminal Done lane")
	assert.Empty(t, toastCookie(rec), "a refusal arms no flash cookie")
	assert.Equal(t, 0, countToasts(rec.Body.String()), "a refused post shows no toast")
}

// toastCookie is the flash cookie a response armed, or nil when it armed
// none. Read from Set-Cookie rather than from a re-read of the request, so
// a test cannot accidentally assert the cookie the request carried in.
func toastCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == toastCookieName && c.MaxAge > 0 {
			return c
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 4. no JavaScript: the same message on the landing page
// ---------------------------------------------------------------------------

// TestNoJSFormPostLandsOnSuccessAlert requires the no-JS path end to end:
// a successful form post redirects (303) with a one-shot flash cookie armed,
// and following that redirect with a cookie jar lands on the console view
// showing the SAME message the toast would have shown, as a
// success-severity htmxui.Alert. The jar is what makes this honest -- the
// message is read from the cookie the previous response set, and the
// landing response's expiring Set-Cookie is what stops it repeating.
func TestNoJSFormPostLandsOnSuccessAlert(t *testing.T) {
	api := newFakeAPI(t)
	sessionCookie, mux := newToastApp(t, api)
	jar := newToastJar()

	post := serveFormPost(mux, "/ops/tasks/"+uuid.NewString()+"/escalate",
		url.Values{"reason": {"needs a human"}, "return_to": {"/ops/claimed"}}, sessionCookie)

	require.Equal(t, http.StatusSeeOther, post.Code, "a no-JS success is Post/Redirect/Get")
	require.Equal(t, "/ops/claimed", post.Header().Get("Location"))
	armed := toastCookie(post)
	require.NotNil(t, armed, "the confirmation rides a flash cookie across the redirect")
	assert.True(t, armed.HttpOnly, "the message is for the operator to read, never for a script")

	jar.absorb(post)

	landing := jar.get(mux, "/ops/claimed", sessionCookie)
	require.Equal(t, http.StatusOK, landing.Code)
	body := landing.Body.String()
	assert.Equal(t, "Task escalated", alertMessageOf(t, body, `data-krill="toast-flash"`),
		"the landing page states the same message the toast would have shown, exactly")
	assert.Contains(t, body, `data-krill="toast-flash"`)
	assert.Contains(t, body, "alert-success", "the no-JS rendering is success severity")
	assert.Contains(t, body, `role="status"`, "htmxui.Alert derives the role from the variant")
	assert.Equal(t, 0, countToasts(body), "the no-JS page shows an alert, not a toast")

	// One shot: the landing response expires the cookie, so the operator's
	// next page load -- which the browser now makes without it -- is silent.
	jar.absorb(landing)
	_, stillArmed := jar.cookies[toastCookieName]
	assert.False(t, stillArmed, "the flash cookie must be expired by the response that shows it")

	next := jar.get(mux, "/ops/claimed", sessionCookie)
	assert.NotContains(t, next.Body.String(), "Task escalated",
		"a confirmation is not repeated on the next page load")
}

// TestNoJSFragmentRequestDoesNotConsumeTheFlash requires the page-side half
// of the no-JS path to leave the cookie alone when it cannot display it. A
// fragment request renders no document, so an alert prepended to it would be
// swapped into the middle of whatever target asked for it -- and the flash
// would be consumed, leaving a full browser nothing to show.
func TestNoJSFragmentRequestDoesNotConsumeTheFlash(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ops/claimed", nil)
	req.Header.Set("HX-Request", "true")
	req.AddCookie(&http.Cookie{Name: toastCookieName, Value: "Q2xhaW0gcmVsZWFzZWQ"})
	rec := httptest.NewRecorder()

	body := withFlashSuccess(req, rec, pages.ClaimedResults(pages.ClaimedData{Href: "/ops/claimed"}))

	assert.NotContains(t, mustRenderComponent(body), "Claim released",
		"a fragment must not render a page-level flash")
	assert.Nil(t, toastCookie(rec), "the cookie is left armed for the page load that can show it")
}

// ---------------------------------------------------------------------------
// escaping
// ---------------------------------------------------------------------------

// TestToastTextIsEscaped requires a message carrying markup to render as
// text on both paths. The message is built by templ, not interpolated into
// an attribute or handed to a JavaScript template, so the escaped form is
// what reaches the browser -- which is exactly why the mechanism uses an
// out-of-band swap rather than HX-Trigger.
func TestToastTextIsEscaped(t *testing.T) {
	const markup = `<script>alert("pwned")</script> & "quoted" 'text'`
	const escaped = `&lt;script&gt;alert(&#34;pwned&#34;)&lt;/script&gt; &amp; &#34;quoted&#34; &#39;text&#39;`

	t.Run("htmx toast", func(t *testing.T) {
		got := mustRenderComponent(withToast(markup, pages.EscalatedResults(pages.EscalatedData{Href: "/ops/escalated"})))
		assert.Contains(t, got, escaped, "the message reaches the page as text")
		assert.NotContains(t, got, markup, "the markup must never be live in the response")
		assert.NotContains(t, got, "<script>alert(")
	})

	t.Run("no-JS flash", func(t *testing.T) {
		// Round-trip the message through the cookie the redirect arms, so
		// the escaping is proved on the bytes a browser would actually
		// replay rather than on a string passed straight to the component.
		armed := httptest.NewRecorder()
		flashSuccess(armed, markup)
		cookie := toastCookie(armed)
		require.NotNil(t, cookie)

		req := httptest.NewRequest(http.MethodGet, "/ops/claimed", nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()

		body := withFlashSuccess(req, rec, pages.ClaimedResults(pages.ClaimedData{Href: "/ops/claimed"}))
		got := mustRenderComponent(body)
		assert.Contains(t, got, escaped)
		assert.NotContains(t, got, markup)
		assert.NotContains(t, got, "<script>alert(")
	})
}

// TestFlashMessageIsBoundedAndSaysSo requires an over-long message to be cut
// with a visible ellipsis rather than silently: a cookie has a hard size
// limit, and an operator reading a complete-looking sentence that is not
// one is worse off than one told it was trimmed.
func TestFlashMessageIsBoundedAndSaysSo(t *testing.T) {
	long := strings.Repeat("x", maxToastMessageLen*2)
	got := truncateToastMessage(long)

	assert.True(t, len([]rune(got)) <= maxToastMessageLen+1, "the trimmed message stays within the bound")
	assert.True(t, strings.HasSuffix(got, "…"), "the operator is told the message was cut")
}
