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
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/ui/pages"
)

// assertInterventionAttribution asserts the recorded init minted the krill
// session under the fake-Keycloak operator's REAL (iss, sub) as both
// subjects. operatorSub is minted fresh per test (not the shared constant),
// so an assertion here cannot pass by matching a hardcoded identity: only the
// signed-in operator's own subject, resolved through the genuine htmxauth
// sign-in callback, produces it.
func assertInterventionAttribution(t *testing.T, api *fakeAPI, issuer, _ string) {
	t.Helper()
	assertOperatorAttribution(t, api, issuer)
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
		// The percent-encoded spelling of the same traversal. The ".."
		// check has to run on the DECODED path; testing the raw string
		// would pass this straight through and the browser would then
		// normalise it to a path outside /ops.
		"/ops/%2e%2e/%2e%2e/etc/passwd",
		"/ops/%2E%2E/secret",
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

// taskTitle is the title the control tests render a row with, so the
// destructive verb's confirmation copy can be asserted against the task it
// names rather than against the task id.
const taskTitle = "Paginate ListClaimedTasks"

// TestCancelRendersAsAConfirmingControlNotAForm requires the destructive verb
// to be reachable without posting anything: with JavaScript its htmx half
// confirms (hx-confirm, naming the task) before it posts, and without
// JavaScript its no-JS half is a GET to the confirmation page. Neither half
// may be a plain form that POSTs the cancel route.
func TestCancelRendersAsAConfirmingControlNotAForm(t *testing.T) {
	taskID := uuid.NewString()
	title := "Paginate ListClaimedTasks"
	html := mustRenderComponent(renderTaskActions(taskID, title, "/ops/claimed", "cancel"))

	assert.Contains(t, html, fmt.Sprintf(`<form method="get" action="/ops/tasks/%s/cancel/confirm"`, taskID),
		"the no-JS half opens the confirm page")
	assert.Contains(t, html, fmt.Sprintf(`hx-post="/ops/tasks/%s/cancel"`, taskID),
		"the htmx half posts the cancel route")
	assert.Contains(t, html, `hx-confirm="`+cancelConfirmMessage(title)+`"`,
		"the confirmation is the FR's own copy, naming the task")
	assert.Equal(t, `Cancel `+title+`? It moves to Cancelled and cannot be claimed again.`,
		cancelConfirmMessage(title), "the copy is exactly the FR's")
	assert.Contains(t, html, ">Cancel</button>")
	assert.NotContains(t, html, fmt.Sprintf(`action="/ops/tasks/%s/cancel"`, taskID),
		"cancel must not render a form whose no-JS half posts the cancel route directly")
}

// TestNonDestructiveVerbsRenderInlineForms requires release/requeue/escalate
// to each render an inline form posting to its own route, carrying a return_to
// and a reason field, each with a distinct per-verb reason placeholder.
func TestNonDestructiveVerbsRenderInlineForms(t *testing.T) {
	taskID := uuid.NewString()
	placeholders := map[string]string{}
	for _, verb := range []string{"release", "requeue", "escalate"} {
		t.Run(verb, func(t *testing.T) {
			html := mustRenderComponent(renderTaskActions(taskID, taskTitle, "/ops/claimed", verb))
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
	html := mustRenderComponent(renderTaskActions(taskID, taskTitle, "/ops/claimed", "release"))

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

// TestCancelConfirmPageRequiresReasonAndPostsToCancel walks the full no-JS
// cancel affordance: the row's GET (the doubled control's no-JS half) opens
// the confirm page carrying the guard the row observed, that page renders a
// form requiring a reason and posting the cancel route with the guard still on
// it, and submitting therefore reaches the api's cancel endpoint with the same
// observed-state guard the htmx half posts -- so a claim or escalation that
// changed since the row was read is refused on both paths, not just one.
func TestCancelConfirmPageRequiresReasonAndPostsToCancel(t *testing.T) {
	operatorSub := uuid.NewString()
	idp := newFakeIDP(t, operatorSub)
	authenticator, sessionCookie := newSignedInOperator(t, idp)
	api := newFakeAPI(t)
	app := newTestApp(t, authenticator, idp.server.URL, api.server.URL)
	mux := newInterventionMux(app)

	taskID := uuid.NewString()
	observed := uuid.NewString()
	// The row's own GET: the no-JS half of the doubled control, carrying the
	// guard the row observed as a query parameter.
	confirmURL := "/ops/tasks/" + taskID + "/cancel/confirm?" + url.Values{
		"return_to":          {"/ops/claimed"},
		expectedClaimIDParam: {observed},
	}.Encode()
	getRec := serveWithCookie(mux, http.MethodGet, confirmURL, "", sessionCookie)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())
	page := getRec.Body.String()
	// It names the task, restates the consequence, requires a reason, and its
	// form action is the cancel route.
	assert.Contains(t, page, "Cancel task "+taskID)
	assert.Contains(t, page, "dead-letters")
	assert.Contains(t, page, `name="reason"`)
	assert.Contains(t, page, "required")
	assert.Contains(t, page, fmt.Sprintf(`action="/ops/tasks/%s/cancel"`, taskID))
	// The guard the row observed survives the round trip onto the page's own
	// form: without it the confirm would post an unguarded cancel.
	assert.Equal(t, observed, hiddenInputValue(t, page, expectedClaimIDParam),
		"the confirm page hands the row's observed guard on to its own form")
	assert.Equal(t, "/ops/claimed", hiddenInputValue(t, page, "return_to"))

	// Confirming posts the guard the page rendered, and reaches the api.
	postRec := serveFormPost(mux, "/ops/tasks/"+taskID+"/cancel",
		url.Values{
			"reason":             {"dead-lettered after confirmation"},
			"return_to":          {hiddenInputValue(t, page, "return_to")},
			expectedClaimIDParam: {hiddenInputValue(t, page, expectedClaimIDParam)},
		},
		sessionCookie)
	require.Equal(t, http.StatusSeeOther, postRec.Code, postRec.Body.String())
	assertInterventionAttribution(t, api, idp.server.URL, operatorSub)
	recorded := api.recorded()
	require.Len(t, recorded, 2)
	assert.Equal(t, "/tasks/"+taskID+"/cancel", recorded[1].Path)
	assert.JSONEq(t, `{"reason":"dead-lettered after confirmation","expected_claim_id":"`+observed+`"}`,
		string(recorded[1].Body),
		"the no-JS path's cancel is guarded by the same observed id the row rendered")
}

// ---------------------------------------------------------------------------
// 7. the htmx branch: one route, two modes
// ---------------------------------------------------------------------------

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
