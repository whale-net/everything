// Round-trip coverage for the console's four task interventions -- release,
// requeue, escalate, and cancel (FR 1f44e461). Each case drives the real
// form POST through operatorRoute -> requireOperator -> withKrillSession ->
// the real write client against the fake api, and asserts the four things
// that make a browser intervention "exactly as its MCP tool does, attributed
// to the real signed-in operator":
//
//  1. the write reaches the *api* path POST /tasks/{id}/{verb} (derived here
//     from the api routes, not from ui's own constants) with the body
//     {"reason": ...} -- the MCP tool's sole argument;
//  2. the krill session is minted under the signed-in Keycloak operator's real
//     (iss, sub) as BOTH acting and on-behalf-of, carried on the write;
//  3. a success is Post/Redirect/Get (303) back to the originating console
//     view; and
//  4. return_to cannot be used as an open redirect off this binary.
//
// It also covers the api's rejection surfacing as an in-shell HTML error page
// carrying the api's own status and {"error"} message, and cancel's
// confirm-before-destructive-post affordance.
//
// The sign-in / fake-Keycloak / fake-api harness is reused from writes_test.go
// so attribution is asserted through the genuine htmxauth sign-in callback,
// not a stub. Expectations are written out literally (paths, bodies, an
// operator sub minted fresh per test) so they are independent of the
// production constants under test.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// newInterventionMux registers exactly the intervention routes setupRoutes
// mounts, each behind the same operatorRoute, so a form submission traverses
// the production auth -> operator -> write path. Route paths are written as
// literals here (not built from opsTaskActionBase) to keep the test
// independent of the constant it exercises.
func newInterventionMux(app *App) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ops/tasks/{id}/release", app.operatorRoute(app.handleTaskIntervention("release")))
	mux.HandleFunc("POST /ops/tasks/{id}/requeue", app.operatorRoute(app.handleTaskIntervention("requeue")))
	mux.HandleFunc("POST /ops/tasks/{id}/escalate", app.operatorRoute(app.handleTaskIntervention("escalate")))
	mux.HandleFunc("POST /ops/tasks/{id}/cancel", app.operatorRoute(app.handleTaskIntervention("cancel")))
	mux.HandleFunc("GET /ops/tasks/{id}/cancel/confirm", app.operatorRoute(app.handleCancelConfirm))
	return mux
}

// serveFormPost issues a urlencoded form POST -- what the browser's inline
// action form and the cancel-confirm form actually send -- through mux with
// the operator's session cookie attached.
func serveFormPost(mux *http.ServeMux, target string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// assertInterventionAttribution asserts the recorded init minted the krill
// session under the fake-Keycloak operator's REAL (iss, sub) as both
// subjects. operatorSub is minted fresh per test (not the shared constant),
// so an assertion here cannot pass by matching a hardcoded identity: only the
// signed-in operator's own subject, resolved through the genuine htmxauth
// sign-in callback, produces it.
func assertInterventionAttribution(t *testing.T, api *fakeAPI, issuer, operatorSub string) {
	t.Helper()
	init := api.initRequest(t)
	assert.Equal(t, issuer, init.Acting.Iss, "acting issuer is the configured Keycloak realm")
	assert.Equal(t, operatorSub, init.Acting.Sub, "acting sub is the signed-in operator's own subject")
	assert.Equal(t, string(store.SubjectKindHuman), init.Acting.Kind)
	assert.Equal(t, init.Acting, init.OnBehalfOf, "a signed-in operator acts for themselves")
}

// ---------------------------------------------------------------------------
// 1 + 2 + 3. round-trip: correct api path/body, real identity, 303 back
// ---------------------------------------------------------------------------

// TestInterventionRoundTripMatchesMCPContract drives a form POST for each of
// the four verbs and requires that it lands on the same api endpoint the
// corresponding MCP tool drives, with the same {"reason"} body, attributed to
// the real signed-in operator, and redirected back (303) to the view the
// operator acted from.
func TestInterventionRoundTripMatchesMCPContract(t *testing.T) {
	// One genuine sign-in, reused across the four verbs; each verb gets a
	// fresh fake api so "exactly one init and one write" is per-case.
	operatorSub := uuid.NewString()
	idp := newFakeIDP(t, operatorSub)
	authenticator, sessionCookie := newSignedInOperator(t, idp)
	issuer := idp.server.URL

	verbs := []struct{ verb, reason string }{
		{"release", "claim is stale, reclaiming elsewhere"},
		{"requeue", "flaky upstream, safe to retry"},
		{"escalate", "needs a human decision"},
		{"cancel", "unrecoverable, dead-lettering"},
	}

	for _, v := range verbs {
		t.Run(v.verb, func(t *testing.T) {
			api := newFakeAPI(t)
			app := newTestApp(t, authenticator, issuer, api.server.URL)
			mux := newInterventionMux(app)

			taskID := uuid.NewString()
			// return_to names the console view the operator acted from.
			form := url.Values{"reason": {v.reason}, "return_to": {"/ops/claimed"}}
			rec := serveFormPost(mux, "/ops/tasks/"+taskID+"/"+v.verb, form, sessionCookie)

			// 3. Post/Redirect/Get back to the originating console view.
			require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
			assert.Equal(t, "/ops/claimed", rec.Header().Get("Location"), "303 returns to the originating view")

			// 2. the write is attributed to the real signed-in operator.
			assertInterventionAttribution(t, api, issuer, operatorSub)

			recorded := api.recorded()
			require.Len(t, recorded, 2, "exactly one init and one write: %+v", recorded)

			// 1. the write reaches the *api* path POST /tasks/{id}/{verb} (the
			// endpoint the MCP tool drives), not the console's /ops/... path.
			write := recorded[1]
			assert.Equal(t, http.MethodPost, write.Method)
			assert.Equal(t, "/tasks/"+taskID+"/"+v.verb, write.Path)
			assert.Equal(t, api.sessionID, write.Header.Get(sessionHeader),
				"the write carries the session init minted under the operator's identity")
			// ...with the MCP tool's exact {"reason"} body -- no identity, no
			// scope, no extra note field.
			assert.JSONEq(t, `{"reason":`+strconv.Quote(v.reason)+`}`, string(write.Body))
		})
	}
}

// TestInterventionForwardsEmptyReasonAsNull covers the optional-reason case:
// an omitted/blank reason is sent as a null, which the api's *string Reason
// accepts exactly as an omitted reason would -- the same shape the MCP tool
// sends when its optional reason is absent.
func TestInterventionForwardsEmptyReasonAsNull(t *testing.T) {
	operatorSub := uuid.NewString()
	idp := newFakeIDP(t, operatorSub)
	authenticator, sessionCookie := newSignedInOperator(t, idp)
	api := newFakeAPI(t)
	app := newTestApp(t, authenticator, idp.server.URL, api.server.URL)
	mux := newInterventionMux(app)

	taskID := uuid.NewString()
	// No reason field at all -- the operator just clicks the button.
	rec := serveFormPost(mux, "/ops/tasks/"+taskID+"/release",
		url.Values{"return_to": {"/ops/claimed"}}, sessionCookie)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	recorded := api.recorded()
	require.Len(t, recorded, 2)
	assert.JSONEq(t, `{"reason":null}`, string(recorded[1].Body))
}

// ---------------------------------------------------------------------------
// 4. return_to open-redirect safety
// ---------------------------------------------------------------------------

// TestInterventionReturnToRejectsOpenRedirect requires that a hostile
// return_to -- an absolute off-site URL or a scheme-relative "//" URL -- is
// refused and the operator is sent to the ops root instead, while a genuine
// console view is honored. Only paths under this binary's own /ops are
// followed, so a crafted return_to can never bounce the operator off-site.
func TestInterventionReturnToRejectsOpenRedirect(t *testing.T) {
	operatorSub := uuid.NewString()
	idp := newFakeIDP(t, operatorSub)
	authenticator, sessionCookie := newSignedInOperator(t, idp)
	api := newFakeAPI(t)
	app := newTestApp(t, authenticator, idp.server.URL, api.server.URL)
	mux := newInterventionMux(app)

	hostile := []string{
		"https://evil.example.com/steal",
		"http://evil.example.com",
		"//evil.example.com/steal",
		"javascript:alert(1)",
		// Same-origin, but not a view this binary serves. The guard
		// matches at path-segment boundaries, so a raw-prefix check would
		// admit this and 404 the operator instead of falling back to the
		// console root.
		"/opsarchive",
		"/ops/../../etc/passwd",
	}
	for _, to := range hostile {
		t.Run(to, func(t *testing.T) {
			taskID := uuid.NewString()
			rec := serveFormPost(mux, "/ops/tasks/"+taskID+"/release",
				url.Values{"reason": {"x"}, "return_to": {to}}, sessionCookie)
			require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
			assert.Equal(t, "/ops", rec.Header().Get("Location"),
				"a hostile return_to must fall back to the ops root, never be followed")
		})
	}

	// Positive control: a real console view is still honored, so the guard is
	// specific to off-site values, not to return_to in general.
	rec := serveFormPost(mux, "/ops/tasks/"+uuid.NewString()+"/release",
		url.Values{"reason": {"x"}, "return_to": {"/ops/escalated"}}, sessionCookie)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, "/ops/escalated", rec.Header().Get("Location"))
}

// ---------------------------------------------------------------------------
// 5. rejected write -> in-shell HTML error page
// ---------------------------------------------------------------------------

// TestInterventionRejectionRendersInShellErrorPage drives a refused write
// (api answers 409 with its {"error"} body) and requires the console to
// present it as a real page carrying the api's own status and message -- not
// as a raw JSON dump -- with a link back to the console.
func TestInterventionRejectionRendersInShellErrorPage(t *testing.T) {
	operatorSub := uuid.NewString()
	idp := newFakeIDP(t, operatorSub)
	authenticator, sessionCookie := newSignedInOperator(t, idp)
	api := newFakeAPI(t)
	api.rejectWrite(http.StatusConflict, `{"error":"task is already cancelled"}`)
	app := newTestApp(t, authenticator, idp.server.URL, api.server.URL)
	mux := newInterventionMux(app)

	taskID := uuid.NewString()
	rec := serveFormPost(mux, "/ops/tasks/"+taskID+"/cancel",
		url.Values{"reason": {"force"}, "return_to": {"/ops/claimed"}}, sessionCookie)

	// The api's own status is preserved as the page's status.
	assert.Equal(t, http.StatusConflict, rec.Code)
	body := rec.Body.String()
	// The api's {"error"} message is presented as readable text...
	assert.Contains(t, body, "task is already cancelled")
	// ...never as the raw JSON body.
	assert.NotContains(t, body, `{"error"`, "the raw api JSON must not leak to the browser")
	// ...and there is a way back to the console.
	assert.Contains(t, body, "Back to the console")
	assert.Contains(t, body, `href="/ops/claimed"`)
}

// ---------------------------------------------------------------------------
// 6. cancel's confirm step + the non-destructive inline forms
// ---------------------------------------------------------------------------

// TestCancelRendersAsConfirmLinkNotForm requires the destructive verb to
// render as a link to its confirmation page (nothing posts until the operator
// confirms), never as a form that posts to the cancel route directly.
func TestCancelRendersAsConfirmLinkNotForm(t *testing.T) {
	taskID := uuid.NewString()
	html := mustRenderComponent(renderTaskActions(taskID, "/ops/claimed", "cancel"))
	assert.Contains(t, html, fmt.Sprintf(`<a href="/ops/tasks/%s/cancel/confirm?`, taskID),
		"cancel renders as a link to its confirm page")
	assert.Contains(t, html, "Cancel")
	assert.NotContains(t, html, fmt.Sprintf(`action="/ops/tasks/%s/cancel"`, taskID),
		"cancel must not render a form that posts to the cancel route directly")
}

// TestNonDestructiveVerbsRenderInlineForms requires release/requeue/escalate
// to each render an inline form posting to its own route, carrying a return_to
// and a reason field, each with a distinct per-verb reason placeholder.
func TestNonDestructiveVerbsRenderInlineForms(t *testing.T) {
	taskID := uuid.NewString()
	placeholders := map[string]string{}
	for _, verb := range []string{"release", "requeue", "escalate"} {
		t.Run(verb, func(t *testing.T) {
			html := mustRenderComponent(renderTaskActions(taskID, "/ops/claimed", verb))
			assert.Contains(t, html, "<form method=\"post\"")
			assert.Contains(t, html, fmt.Sprintf(`action="/ops/tasks/%s/%s"`, taskID, verb))
			assert.Contains(t, html, `name="return_to" value="/ops/claimed"`)
			placeholders[verb] = placeholderOf(html)
			assert.NotEmpty(t, placeholders[verb], "the form must prompt for a reason")
		})
	}
	// The reason prompt is per-verb, not one shared string.
	assert.NotEqual(t, placeholders["release"], placeholders["requeue"])
	assert.NotEqual(t, placeholders["requeue"], placeholders["escalate"])
}

// TestNonDestructiveFormsAreDoubled requires each inline row form to carry
// BOTH halves of the doubled-form rule: the no-JS branch (method="post" +
// action=) and the htmx branch (hx-post + hx-target + hx-swap). A form
// missing either half would silently break one of the two browsers.
func TestNonDestructiveFormsAreDoubled(t *testing.T) {
	taskID := uuid.NewString()
	action := "/ops/tasks/" + taskID + "/release"
	html := mustRenderComponent(renderTaskActions(taskID, "/ops/claimed", "release"))

	assert.Contains(t, html, `<form method="post" action="`+action+`"`,
		"the no-JS half posts to the same route it always did")
	assert.Contains(t, html, `hx-post="`+action+`"`,
		"the htmx half posts to the same route, not a second one")
	assert.Contains(t, html, `hx-target="#ops-results"`,
		"the htmx half swaps the view's whole results block")
	assert.Contains(t, html, `hx-swap="outerHTML"`)
}

// TestCancelConfirmFormIsDoubled requires the confirm card's form to be
// doubled the same way, targeting the card itself so a refusal can be
// re-rendered into it.
func TestCancelConfirmFormIsDoubled(t *testing.T) {
	taskID := uuid.NewString()
	action := "/ops/tasks/" + taskID + "/cancel"
	html := mustRenderComponent(pages.CancelConfirmCard(pages.CancelConfirmData{
		TaskID:   taskID,
		Action:   action,
		ReturnTo: "/ops/claimed",
	}))

	assert.Contains(t, html, `id="cancel-confirm"`, "the card is the fragment's own swap target")
	assert.Contains(t, html, `<form method="post" action="`+action+`"`)
	assert.Contains(t, html, `hx-post="`+action+`"`)
	assert.Contains(t, html, `hx-target="#cancel-confirm"`)
	assert.Contains(t, html, `hx-swap="outerHTML"`)
}

// placeholderOf extracts a rendered input's placeholder attribute value.
func placeholderOf(html string) string {
	const key = `placeholder="`
	i := strings.Index(html, key)
	if i < 0 {
		return ""
	}
	rest := html[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// TestCancelConfirmPageRequiresReasonAndPostsToCancel walks the full cancel
// affordance: the confirm page the Cancel link points at renders a form that
// requires a reason and posts to the cancel route, and submitting that route
// reaches the api's cancel endpoint -- so the destructive verb posts only
// after an explicit confirmation, and then exactly as the MCP tool would.
func TestCancelConfirmPageRequiresReasonAndPostsToCancel(t *testing.T) {
	operatorSub := uuid.NewString()
	idp := newFakeIDP(t, operatorSub)
	authenticator, sessionCookie := newSignedInOperator(t, idp)
	api := newFakeAPI(t)
	app := newTestApp(t, authenticator, idp.server.URL, api.server.URL)
	mux := newInterventionMux(app)

	taskID := uuid.NewString()
	// GET the confirmation page the Cancel link points at.
	getRec := serveWithCookie(mux, http.MethodGet,
		"/ops/tasks/"+taskID+"/cancel/confirm?return_to="+url.QueryEscape("/ops/claimed"),
		"", sessionCookie)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())
	page := getRec.Body.String()
	// It names the task, restates the consequence, requires a reason, and its
	// form action is the cancel route.
	assert.Contains(t, page, "Cancel task "+taskID)
	assert.Contains(t, page, "dead-letters")
	assert.Contains(t, page, `name="reason"`)
	assert.Contains(t, page, "required")
	assert.Contains(t, page, fmt.Sprintf(`action="/ops/tasks/%s/cancel"`, taskID))

	// Confirming posts to that cancel route and reaches the api endpoint.
	postRec := serveFormPost(mux, "/ops/tasks/"+taskID+"/cancel",
		url.Values{"reason": {"dead-lettered after confirmation"}, "return_to": {"/ops/claimed"}},
		sessionCookie)
	require.Equal(t, http.StatusSeeOther, postRec.Code, postRec.Body.String())
	assertInterventionAttribution(t, api, idp.server.URL, operatorSub)
	recorded := api.recorded()
	require.Len(t, recorded, 2)
	assert.Equal(t, "/tasks/"+taskID+"/cancel", recorded[1].Path)
	assert.JSONEq(t, `{"reason":"dead-lettered after confirmation"}`, string(recorded[1].Body))
}

// ---------------------------------------------------------------------------
// 7. the htmx branch: one route, two modes
// ---------------------------------------------------------------------------

// fakeFragmentTasks is the minimum console-query surface the htmx branch
// of an intervention needs: after a write, the handler re-derives the whole
// results block of the view the operator acted from, which means re-reading
// it. It embeds store.TaskStore so any other List method the re-derivation
// ever grew would nil-panic rather than pass unnoticed -- these tests must
// not depend on store surface the console does not use.
type fakeFragmentTasks struct {
	store.TaskStore

	claimed []store.ClaimedTaskRow
	// escalated is distinct from claimed so a test can tell which view the
	// re-derivation actually read.
	escalated []store.EscalatedTaskRow
}

func (f *fakeFragmentTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{Items: f.claimed}, nil
}

func (f *fakeFragmentTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{Items: f.escalated}, nil
}

// newHtmxInterventionApp wires a signed-in operator to a fake api AND to the
// console-query surface, so an intervention can be driven all the way
// through to the fragment its htmx response renders. It returns the fake
// IdP's issuer so attribution can be asserted the same fresh way the
// no-HX cases assert it.
func newHtmxInterventionApp(t *testing.T, api *fakeAPI, operatorSub string) (*App, *http.Cookie, string) {
	t.Helper()
	idp := newFakeIDP(t, operatorSub)
	authenticator, sessionCookie := newSignedInOperator(t, idp)
	app := newTestApp(t, authenticator, idp.server.URL, api.server.URL)
	app.tasks = &fakeFragmentTasks{
		claimed:   []store.ClaimedTaskRow{{TaskID: uuid.New(), Title: "a still-claimed task"}},
		escalated: []store.EscalatedTaskRow{{TaskID: uuid.New(), Title: "a still-escalated task"}},
	}
	return app, sessionCookie, idp.server.URL
}

// hxFormPost issues the same form POST with the HX-Request header htmx
// sets -- the one difference between the route's two branches.
func hxFormPost(mux *http.ServeMux, target string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestInterventionHTMXAnswersResultsFragmentNotRedirect requires that an
// htmx intervention answers 200 with the whole results block of the view
// the operator acted from, re-derived from freshly observed state. Never a
// 303, never a 4xx, never an assumed "success" render: a swap target's
// HTTP status is not surfaced to the operator, so anything that matters
// has to ride inside the fragment.
func TestInterventionHTMXAnswersResultsFragmentNotRedirect(t *testing.T) {
	for _, tc := range []struct {
		verb     string
		returnTo string
		wantRow  string
	}{
		{"release", "/ops/claimed", "a still-claimed task"},
		{"escalate", "/ops/claimed", "a still-claimed task"},
		{"requeue", "/ops/escalated", "a still-escalated task"},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			operatorSub := uuid.NewString()
			api := newFakeAPI(t)
			app, sessionCookie, _ := newHtmxInterventionApp(t, api, operatorSub)
			mux := newInterventionMux(app)

			taskID := uuid.NewString()
			rec := hxFormPost(mux, "/ops/tasks/"+taskID+"/"+tc.verb,
				url.Values{"reason": {"x"}, "return_to": {tc.returnTo}}, sessionCookie)

			assert.Equal(t, http.StatusOK, rec.Code, "an htmx intervention never answers non-200")
			assert.Empty(t, rec.Header().Get("Location"), "an htmx intervention never redirects")
			got := rec.Body.String()
			assert.Contains(t, got, `id="ops-results"`, "the response is the view's results block")
			assert.Contains(t, got, tc.wantRow, "the block is re-derived from the view return_to names")
			assert.NotContains(t, got, "<html", "the fragment carries no shell chrome")
		})
	}
}

// TestInterventionHTMXReDerivesFromTheViewReturnToNames is the other half
// of the same rule, stated as its own case: the re-derivation follows
// return_to rather than always landing on the claimed view, so a requeue
// out of /ops/escalated refreshes the escalated table the operator was
// looking at instead of swapping another view's rows into it.
func TestInterventionHTMXReDerivesFromTheViewReturnToNames(t *testing.T) {
	operatorSub := uuid.NewString()
	api := newFakeAPI(t)
	app, sessionCookie, _ := newHtmxInterventionApp(t, api, operatorSub)
	mux := newInterventionMux(app)

	rec := hxFormPost(mux, "/ops/tasks/"+uuid.NewString()+"/requeue",
		url.Values{"reason": {"x"}, "return_to": {"/ops/escalated"}}, sessionCookie)

	got := rec.Body.String()
	assert.Contains(t, got, "a still-escalated task")
	assert.NotContains(t, got, "a still-claimed task",
		"the claimed view's rows must not be swapped into the escalated view")
}

// TestInterventionHTMXRefusalRendersInlineAlert requires a refused write
// to reach the operator as a readable message inside the 200 fragment --
// never as a status code the swap would discard, and never as the raw api
// JSON.
func TestInterventionHTMXRefusalRendersInlineAlert(t *testing.T) {
	for _, verb := range []string{"release", "escalate", "requeue"} {
		t.Run(verb, func(t *testing.T) {
			operatorSub := uuid.NewString()
			api := newFakeAPI(t)
			api.rejectWrite(http.StatusConflict, `{"error":"task is already cancelled"}`)
			app, sessionCookie, _ := newHtmxInterventionApp(t, api, operatorSub)
			mux := newInterventionMux(app)

			rec := hxFormPost(mux, "/ops/tasks/"+uuid.NewString()+"/"+verb,
				url.Values{"reason": {"force"}, "return_to": {"/ops/claimed"}}, sessionCookie)

			assert.Equal(t, http.StatusOK, rec.Code, "a refusal is presented, not status-coded")
			got := rec.Body.String()
			assert.Contains(t, got, "task is already cancelled", "the api's own message reaches the operator")
			assert.Contains(t, got, `role="alert"`, "the refusal rides inline in the fragment")
			assert.Contains(t, got, "alert-error")
			assert.NotContains(t, got, `{"error"`, "the raw api JSON must not leak to the browser")
			assert.Contains(t, got, `id="ops-results"`, "the results block is still re-derived under the refusal")
		})
	}
}

// TestCancelConfirmHTMXSuccessSetsRedirectHeader requires the one legitimate
// redirect on an htmx path: a confirmed cancel navigates back to the console
// view it came from. It is a navigation, not a swap, so it is carried by
// HX-Redirect rather than by a status code.
func TestCancelConfirmHTMXSuccessSetsRedirectHeader(t *testing.T) {
	operatorSub := uuid.NewString()
	api := newFakeAPI(t)
	app, sessionCookie, issuer := newHtmxInterventionApp(t, api, operatorSub)
	mux := newInterventionMux(app)

	taskID := uuid.NewString()
	rec := hxFormPost(mux, "/ops/tasks/"+taskID+"/cancel",
		url.Values{"reason": {"dead-lettered"}, "return_to": {"/ops/claimed"}}, sessionCookie)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/ops/claimed", rec.Header().Get("HX-Redirect"),
		"a confirmed cancel navigates back to the view it came from")
	assert.Empty(t, rec.Header().Get("Location"),
		"only an htmx request gets HX-Redirect; a full browser still gets the 303")

	// The write is still exactly the MCP tool's, attributed to the real
	// signed-in operator: doubling the form changed the response, not the write.
	assertInterventionAttribution(t, api, issuer, operatorSub)
	recorded := api.recorded()
	require.Len(t, recorded, 2)
	assert.Equal(t, "/tasks/"+taskID+"/cancel", recorded[1].Path)
	assert.JSONEq(t, `{"reason":"dead-lettered"}`, string(recorded[1].Body))
}

// TestCancelConfirmHTMXRefusalReRendersTheCard requires a refused confirm
// to re-render the confirm card at 200 with the reason inline: the operator
// keeps their card and learns why, rather than the card vanishing behind a
// 4xx the swap never surfaces.
func TestCancelConfirmHTMXRefusalReRendersTheCard(t *testing.T) {
	operatorSub := uuid.NewString()
	api := newFakeAPI(t)
	api.rejectWrite(http.StatusConflict, `{"error":"task is already cancelled"}`)
	app, sessionCookie, _ := newHtmxInterventionApp(t, api, operatorSub)
	mux := newInterventionMux(app)

	rec := hxFormPost(mux, "/ops/tasks/"+uuid.NewString()+"/cancel",
		url.Values{"reason": {"force"}, "return_to": {"/ops/claimed"}}, sessionCookie)

	assert.Equal(t, http.StatusOK, rec.Code)
	got := rec.Body.String()
	assert.Contains(t, got, `id="cancel-confirm"`, "the card is re-rendered into its own target")
	assert.Contains(t, got, "task is already cancelled", "the refusal is explained inline")
	assert.Contains(t, got, `role="alert"`)
	assert.NotContains(t, got, `{"error"`, "the raw api JSON must not leak to the browser")
	assert.Empty(t, rec.Header().Get("HX-Redirect"), "a refused cancel must not navigate away")
}

// TestInterventionHTMXKeepsTheOpenRedirectGuard requires the guard to run
// before the htmx branch picks a view, exactly as it runs before the no-JS
// branch picks a redirect: a hostile return_to must never re-derive a view
// the operator was not on, and never navigate off-site.
func TestInterventionHTMXKeepsTheOpenRedirectGuard(t *testing.T) {
	operatorSub := uuid.NewString()
	api := newFakeAPI(t)
	app, sessionCookie, _ := newHtmxInterventionApp(t, api, operatorSub)
	mux := newInterventionMux(app)

	rec := hxFormPost(mux, "/ops/tasks/"+uuid.NewString()+"/release",
		url.Values{"reason": {"x"}, "return_to": {"https://evil.example.com/steal"}}, sessionCookie)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"), "the htmx path never navigates at all")
	got := rec.Body.String()
	assert.NotContains(t, got, "evil.example.com", "a hostile return_to never reaches the response")
	assert.Contains(t, got, `id="ops-results"`, "the guard falls back to the ops root, which still renders a view")
}
