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
	html := string(renderTaskActions(taskID, "/ops/claimed", "cancel"))
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
			html := string(renderTaskActions(taskID, "/ops/claimed", verb))
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
