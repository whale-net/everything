// Testing-lane coverage for FR c69a42b4: a refused or stale intervention
// answers 200 with the results region re-derived from freshly read state and
// an inline alert naming the refusal, carrying the ids that are current NOW;
// no store text reaches the browser; a read failure is never an empty table
// or an assumed success; and without JavaScript the refusal renders inside
// the shell with a link back to the tab.
//
// The tests take the same route the browser does: the row is rendered through
// the tab's OWN loader, the control's action, its return_to and its
// observed-state guard are read OFF THAT MARKUP, and the POST is the row's
// own. What the store refuses is modelled at the api (the same 409 with the
// store's own sentinel text krill api returns verbatim), and what the
// re-derivation then reads is the fixture's state AFTER the competing action
// -- so the assertions discriminate between "re-read fresh" and "replayed the
// refused request's own ids", which is the difference the requirement is
// about.
package main

import (
	"context"
	"errors"
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

// ---------------------------------------------------------------------------
// fixture
// ---------------------------------------------------------------------------

// refusalFixture is the harness's real signed-in operator and real write
// client against a fake api, with the Needs attention page's own task store
// carrying both the row the operator read and the state a competing action
// left behind. It reuses the cancel fixture's wiring rather than copying it,
// so both lanes walk one world.
type refusalFixture = cancelRowFixture

func newRefusalFixture(t *testing.T) *refusalFixture {
	t.Helper()
	return newCancelRowFixture(t)
}

// The wording every refusal here must produce, in krill's own words.
const (
	refusalChanged  = "changed since the page was loaded"
	refusalNotLegal = "no longer in a state this action applies to"
	// storeTextMarker is the package-qualified prefix of every store
	// sentinel (store/errors.go) -- what must never appear in a response.
	storeTextMarker = "krill/store:"
)

// refusalToasts is how many toasts a response carries. Scoped to the
// data-krill="toast" marker with its closing quote so the toast HOST
// (data-krill="toast-host") is not counted as one.
func refusalToasts(body string) int {
	return strings.Count(body, `data-krill="toast"`)
}

// assertRefusalIsKrillOwnWords is the requirement's own promise, asserted on
// whatever bytes a response carried: the refusal is named, it rides in the
// shared alert primitive, and the store's own package-qualified text is not
// among them.
func assertRefusalIsKrillOwnWords(t *testing.T, body, want string) {
	t.Helper()
	assert.NotContains(t, body, storeTextMarker,
		"no package-qualified store text reaches the operator, on either path")
	assert.Contains(t, body, want, "the operator still gets a refusal they can act on")
	assert.Contains(t, body, `role="alert"`, "the refusal rides in the shared alert primitive")
	assert.Contains(t, body, "alert-error", "a refusal is error severity, not a note")
}

// assertNotAnAssumedSuccess is the other half of "never an assumed success":
// a refused intervention raises no toast (transient, and gone before a slow
// operator read it) and states the refused verb's own confirmation -- or the
// success path's reload wording -- nowhere.
func assertNotAnAssumedSuccess(t *testing.T, body string) {
	t.Helper()
	assert.Equal(t, 0, refusalToasts(body), "a refused intervention raises no toast")
	assert.NotContains(t, body, "hx-swap-oob", "nothing is appended to the toast host for a refusal")
	assert.NotContains(t, body, interventionReloadFailure,
		"a refusal must never borrow the success path's \"the intervention was applied\"")
}

// ---------------------------------------------------------------------------
// 1. a stale observed id: refused, and the re-offered row carries the FRESH id
// ---------------------------------------------------------------------------

// TestStaleGuardRefusalReOffersTheFreshId is the FR's fresh-ids bullet, raised
// on the verb whose guard is the claim (Release) and the one whose guard is
// the escalation (Requeue). The row is read, the guard is taken off its markup,
// the state moves on (a different claim / escalation is now current) and the
// POST carries the id the row observed -- which the store refuses exactly as
// krill api does (store.ErrObservedStateMismatch over the wire).
//
// The response must re-derive the tab from the fresh read, so the row it
// re-offers carries the id that is current NOW. The two ids differ, so an
// implementation that replayed the refused request's guard -- or dropped the
// guard entirely -- fails rather than passing by accident.
func TestStaleGuardRefusalReOffersTheFreshId(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tab      string
		verb     string
		field    string
		seed     func(f *refusalFixture) string
		reRender func(f *refusalFixture, fresh string)
	}{
		{
			name:  "Release from the Claimed tab with a stale claim id",
			tab:   needsAttentionTabClaimed,
			verb:  actionRelease,
			field: expectedClaimIDParam,
			seed: func(f *refusalFixture) string {
				row := f.claimedRow(store.LaneImplementation)
				f.tasks.claimed = []store.ClaimedTaskRow{row}
				return row.ClaimID.String()
			},
			reRender: func(f *refusalFixture, fresh string) {
				row := f.claimedRow(store.LaneImplementation)
				row.ClaimID = uuid.MustParse(fresh)
				f.tasks.claimed = []store.ClaimedTaskRow{row}
			},
		},
		{
			name:  "Requeue from the Escalated tab with a stale escalation id",
			tab:   needsAttentionTabEscalated,
			verb:  actionRequeue,
			field: escalatedGuardField,
			seed: func(f *refusalFixture) string {
				row := f.escalatedRow(store.LaneImplementation)
				f.tasks.escalated = []store.EscalatedTaskRow{row}
				return row.EscalationID.String()
			},
			reRender: func(f *refusalFixture, fresh string) {
				row := f.escalatedRow(store.LaneImplementation)
				row.EscalationID = uuid.MustParse(fresh)
				f.tasks.escalated = []store.EscalatedTaskRow{row}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefusalFixture(t)
			stale := tc.seed(f)

			region := f.region(t, tc.tab)
			action := formActionFor(t, region, tc.verb)
			observed := hiddenInputValue(t, region, tc.field)
			require.Equal(t, stale, observed,
				"the row's control carries the id the row observed, so a stale-id case is meaningful")

			// The state moved on: this is the id that is current NOW, and the
			// fresh read below is what a re-derivation must carry.
			fresh := uuid.NewString()
			require.NotEqual(t, stale, fresh)
			tc.reRender(f, fresh)
			f.api.onRequest(guardLikeStore(map[string]string{tc.field: fresh}))

			rec := hxFormPost(f.mux, action, url.Values{
				"return_to": {hiddenInputValue(t, region, "return_to")},
				tc.field:    {observed},
			}, f.session)

			require.Equal(t, http.StatusOK, rec.Code,
				"a refusal is presented, not status-coded: htmx does not swap a non-2xx")
			body := rec.Body.String()

			assertRefusalIsKrillOwnWords(t, body, refusalChanged)
			assertNotAnAssumedSuccess(t, body)
			assert.Contains(t, body, `id="`+pages.OpsResultsAnchor+`"`,
				"the results region survives the refusal")

			// The re-offered row carries the guard that is current now...
			assert.Contains(t, body, `name="`+tc.field+`" value="`+fresh+`"`,
				"the re-render carries the FRESH observed id, so a retry is guarded against what the operator now sees")
			// ...and never the one the store just refused.
			assert.NotContains(t, body, stale,
				"the stale guard the store refused must not be re-offered")
		})
	}
}

// ---------------------------------------------------------------------------
// 2. a task a competing action already moved
// ---------------------------------------------------------------------------

// TestCompetingActionRefusalShowsTheFreshTableWithoutAssumedSuccess covers the
// task that left the tab between the read and the write: a competing action
// requeued the escalation, so the Requeue the operator posted is refused as
// illegal rather than merely stale (the store's ErrTaskNotEscalated, which the
// api maps to the same 409).
//
// The response is the fresh tab -- the task is genuinely gone from it -- with
// the refusal stated above it. That is what makes this empty table honest: the
// operator is told the action did not apply, rather than being left to read an
// empty queue as "it worked".
func TestCompetingActionRefusalShowsTheFreshTableWithoutAssumedSuccess(t *testing.T) {
	f := newRefusalFixture(t)
	row := f.escalatedRow(store.LaneImplementation)
	f.tasks.escalated = []store.EscalatedTaskRow{row}

	region := f.region(t, needsAttentionTabEscalated)
	action := formActionFor(t, region, actionRequeue)
	observed := hiddenInputValue(t, region, escalatedGuardField)

	// A competing action already moved the task out of the queue the operator
	// was reading, and the store answers with its own named legality refusal.
	f.tasks.escalated = nil
	f.api.rejectWrite(http.StatusConflict, `{"error":`+strconv.Quote(store.ErrTaskNotEscalated.Error())+`}`)

	rec := hxFormPost(f.mux, action, url.Values{
		"return_to":         {hiddenInputValue(t, region, "return_to")},
		escalatedGuardField: {observed},
	}, f.session)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	assertRefusalIsKrillOwnWords(t, body, refusalNotLegal)
	assertNotAnAssumedSuccess(t, body)
	assert.NotContains(t, body, interventionSuccessMessage(actionRequeue),
		"the refused verb's confirmation is never stated")
	assert.NotContains(t, body, row.TaskID.String(),
		"the task left the tab, so the fresh table does not list it")
	assert.Contains(t, body, "No escalated tasks.",
		"the tab is empty because the fresh read says so, with the refusal above it")
	assert.Contains(t, body, `id="`+pages.OpsResultsAnchor+`"`)
}

// ---------------------------------------------------------------------------
// 3. store failures: the write's own, the re-read's, and the success control
// ---------------------------------------------------------------------------

// failingRefusalTasks is fakeFragmentTasks whose tab reads fail, standing in
// for the store going away between the write and the re-derivation. Its
// GetTaskByID still answers, so the refusal and the re-read are separately
// fail-able.
type failingRefusalTasks struct {
	*fakeFragmentTasks
	err error
}

func (f failingRefusalTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, f.err
}

func (f failingRefusalTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, f.err
}

func (f failingRefusalTasks) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, f.err
}

// TestStoreFailureRefusalIsNeverAnEmptyTableOrAnAssumedSuccess drives api's
// own 5xx, the re-read failing on top of a refusal, and -- as the control that
// makes the other two mean something -- a SUCCESS whose re-read fails, which
// may still say the write landed because it did.
//
// The middle case is the one the requirement names explicitly: the region
// could not be rebuilt at all, so it must say so rather than render "No
// claimed tasks." (indistinguishable from a successful read that came back
// empty) or the success path's "the intervention was applied" (an assumed
// success about a write krill refused).
func TestStoreFailureRefusalIsNeverAnEmptyTableOrAnAssumedSuccess(t *testing.T) {
	// releasePostFrom drives one Release from the Claimed tab using the region
	// the caller read BEFORE it broke anything, so a fixture that fails the
	// store can still submit the row's own form.
	releasePostFrom := func(t *testing.T, f *refusalFixture, region string) *httptest.ResponseRecorder {
		t.Helper()
		return hxFormPost(f.mux, formActionFor(t, region, actionRelease), url.Values{
			"return_to":          {hiddenInputValue(t, region, "return_to")},
			expectedClaimIDParam: {hiddenInputValue(t, region, expectedClaimIDParam)},
		}, f.session)
	}

	t.Run("api fails the write outright", func(t *testing.T) {
		f := newRefusalFixture(t)
		f.tasks.claimed = []store.ClaimedTaskRow{f.claimedRow(store.LaneImplementation)}
		region := f.region(t, needsAttentionTabClaimed)
		f.api.rejectWrite(http.StatusInternalServerError, `{"error":"failed to release task"}`)

		rec := releasePostFrom(t, f, region)

		require.Equal(t, http.StatusOK, rec.Code, "api's status is never what an htmx caller sees")
		body := rec.Body.String()
		assertRefusalIsKrillOwnWords(t, body, "failed to release task")
		assert.Contains(t, body, "krill rejected the Release.",
			"the refusal names the verb it refused")
		assertNotAnAssumedSuccess(t, body)
	})

	t.Run("the refusal stands but the re-read fails", func(t *testing.T) {
		f := newRefusalFixture(t)
		row := f.claimedRow(store.LaneImplementation)
		f.tasks.claimed = []store.ClaimedTaskRow{row}

		region := f.region(t, needsAttentionTabClaimed)
		action := formActionFor(t, region, actionRelease)
		observed := hiddenInputValue(t, region, expectedClaimIDParam)

		// The store is gone by the time the region is re-derived, and the
		// guard the row carried is no longer current either.
		f.app.tasks = failingRefusalTasks{fakeFragmentTasks: f.tasks, err: errors.New("dial tcp: connection refused")}
		f.api.onRequest(guardLikeStore(map[string]string{expectedClaimIDParam: uuid.NewString()}))

		rec := hxFormPost(f.mux, action, url.Values{
			"return_to":          {hiddenInputValue(t, region, "return_to")},
			expectedClaimIDParam: {observed},
		}, f.session)

		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		assertRefusalIsKrillOwnWords(t, body, refusalChanged)
		assert.Contains(t, body, "could not be reloaded",
			"the region says it could not be rebuilt rather than rendering an empty one")
		assert.NotContains(t, body, "No claimed tasks.",
			"a read failure must never render as an empty table")
		assertNotAnAssumedSuccess(t, body)
		assert.NotContains(t, body, "connection refused",
			"nor does the store's own failure leak its internal address")
	})

	t.Run("a success whose re-read fails still says the write landed", func(t *testing.T) {
		f := newRefusalFixture(t)
		f.tasks.claimed = []store.ClaimedTaskRow{f.claimedRow(store.LaneImplementation)}
		region := f.region(t, needsAttentionTabClaimed)
		// The write is accepted; only the re-derivation fails.
		f.app.tasks = failingRefusalTasks{fakeFragmentTasks: f.tasks, err: errors.New("boom")}

		rec := releasePostFrom(t, f, region)

		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		assert.Contains(t, body, interventionReloadFailure,
			"the success path's failed re-read states what actually happened: the write landed")
		assert.NotContains(t, body, "No claimed tasks.",
			"and still does not render an empty table")
	})
}

// ---------------------------------------------------------------------------
// 4. no JavaScript: the refusal renders inside the shell, with a link back
// ---------------------------------------------------------------------------

// TestNoJSRefusalRendersInTheShellWithALinkBackToTheTab is the FR's third
// bullet. A plain form post (no HX-Request) does not swap a fragment, so the
// refusal has to be a real page: api's status, the nav still there, the same
// sentence inline, and a link back to the tab the row was read from rather
// than a text/plain body with no way out.
func TestNoJSRefusalRendersInTheShellWithALinkBackToTheTab(t *testing.T) {
	f := newRefusalFixture(t)
	row := f.escalatedRow(store.LaneImplementation)
	f.tasks.escalated = []store.EscalatedTaskRow{row}

	returnTo := needsAttentionTabHref(f.pid, needsAttentionTabEscalated)
	region := f.region(t, needsAttentionTabEscalated)
	action := formActionFor(t, region, actionRequeue)
	observed := hiddenInputValue(t, region, escalatedGuardField)

	f.api.rejectWrite(http.StatusConflict,
		`{"error":`+strconv.Quote(store.ErrObservedStateMismatch.Error())+`}`)

	rec := serveFormPost(f.mux, action, url.Values{
		"return_to":         {returnTo},
		escalatedGuardField: {observed},
	}, f.session)

	assert.Equal(t, http.StatusConflict, rec.Code,
		"a browser gets the status the error earns, not the 200 the fragment needs")
	require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"),
		"a rendered page, not http.Error's text/plain")
	body := rec.Body.String()

	assertRefusalIsKrillOwnWords(t, body, refusalChanged)
	assert.Contains(t, body, `data-krill="primary-nav"`,
		"the response renders through the shell, so the operator keeps the nav")
	assert.NotContains(t, body, `id="`+pages.OpsResultsAnchor+`"`,
		"a full page is not the results region; the refusal rides the page instead")

	// The way back is the tab the operator acted from, and says so.
	assert.Contains(t, body, `href="`+returnTo+`"`,
		"the link returns to the tab the row was read from")
	assert.Contains(t, body, "Back to the tab",
		"rather than naming the console area the retired /ops URLs belong to")
}

// ---------------------------------------------------------------------------
// 5. the promise itself: no response body ever carries store text
// ---------------------------------------------------------------------------

// TestNoRefusalResponseEverCarriesStoreText is the requirement's first bullet,
// asserted as the promise it is: whatever krill's store refused with, and
// whichever path the operator took, the bytes that reach the browser never
// carry the store's own package-qualified error. It sweeps all four verbs on
// both the htmx and the no-JS path with the texts krill api really returns --
// including the observed-state guard's WRAPPED form, which is how
// store/task_observed_state.go builds it ("...: expected claim X, current
// claim Y: task id Z").
//
// Without the mapping this passes for none of them: the sentinel text is
// exactly what krill api puts in its {"error"} body, and the wrapped form
// carries two internal ids besides.
func TestNoRefusalResponseEverCarriesStoreText(t *testing.T) {
	const sentinelTaskID = "22222222-2222-2222-2222-222222222222"
	observedStateSentinel := store.ErrObservedStateMismatch.Error() +
		": expected claim 11111111-1111-1111-1111-111111111111, current claim none: task id " + sentinelTaskID
	legalitySentinel := store.ErrTaskAlreadyCancelled.Error()

	for _, refusal := range []struct {
		name    string
		message string
		want    string
	}{
		{"observed state changed", observedStateSentinel, refusalChanged},
		{"no longer legal", legalitySentinel, refusalNotLegal},
	} {
		for _, verb := range []string{actionRelease, actionRequeue, actionEscalate, actionCancel} {
			for _, mode := range []struct {
				name  string
				serve func(*http.ServeMux, string, url.Values, ...*http.Cookie) *httptest.ResponseRecorder
			}{
				{"htmx", hxFormPost},
				{"no-JS", serveFormPost},
			} {
				t.Run(refusal.name+"/"+verb+"/"+mode.name, func(t *testing.T) {
					f := newRefusalFixture(t)
					f.tasks.claimed = []store.ClaimedTaskRow{f.claimedRow(store.LaneImplementation)}
					f.tasks.escalated = []store.EscalatedTaskRow{f.escalatedRow(store.LaneImplementation)}
					f.api.rejectWrite(http.StatusConflict,
						`{"error":`+strconv.Quote(refusal.message)+`}`)

					rec := mode.serve(f.mux, "/ops/tasks/"+uuid.NewString()+"/"+verb, url.Values{
						"reason":    {"force"},
						"return_to": {needsAttentionTabHref(f.pid, needsAttentionTabClaimed)},
					}, f.session)

					body := rec.Body.String()
					assert.NotContains(t, body, storeTextMarker,
						"the store's own package-qualified text must never reach the browser")
					assert.NotContains(t, body, sentinelTaskID,
						"nor the ids the store's own message was carrying")
					assert.NotContains(t, body, `{"error"`,
						"nor the raw api JSON the text arrived in")
					assert.Contains(t, body, refusal.want,
						"the operator still gets krill's own wording for the refusal")
					assert.Contains(t, body, `role="alert"`)
				})
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 6. the mapping itself, and the transport half
// ---------------------------------------------------------------------------

// TestInterventionRefusalOfClassifiesAndNeverLeaks pins the one mapping the
// four verbs share, so the routes' wording is not the only thing holding the
// contract up: a sentinel the table has never enumerated still cannot leak,
// because the package prefix is what the last line of defence matches.
func TestInterventionRefusalOfClassifiesAndNeverLeaks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		message string
		want    interventionRefusalKind
		says    string
	}{
		{
			name:    "the observed-state guard, named",
			status:  http.StatusConflict,
			message: store.ErrObservedStateMismatch.Error(),
			want:    refusalStateChanged,
			says:    refusalChanged,
		},
		{
			name:    "a sentinel added after this table was written",
			status:  http.StatusConflict,
			message: "krill/store: a refusal nobody enumerated",
			want:    refusalNotApplicable,
			says:    refusalNotLegal,
		},
		{
			name:    "a not-found sentinel",
			status:  http.StatusBadRequest,
			message: store.ErrNotFound.Error(),
			want:    refusalNotApplicable,
			says:    refusalNotLegal,
		},
		{
			name:    "api's own wording, carrying no store text",
			status:  http.StatusBadRequest,
			message: "invalid id: must be a UUID",
			want:    refusalReported,
			says:    "invalid id: must be a UUID",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := interventionRefusalOf(tc.status, tc.message)
			assert.Equal(t, tc.want, got.Kind)

			body := got.message(actionCancel)
			assert.Contains(t, body, tc.says)
			assert.NotContains(t, body, storeTextMarker,
				"the mapped wording never carries the store's own prefix")
			// The sentence the region falls back to when the re-read also
			// fails composes the same refusal rather than replacing it.
			assert.Contains(t, got.reloadFailure(actionCancel), tc.says,
				"a failed re-read inherits the refusal, not the success path's wording")
		})
	}
}

// TestInterventionNotIssuedStatusKeepsTheEarnedStatus is the transport half:
// a write that never reached krill takes the same route as a refusal, and the
// status it earns is not flattened to one number -- an unresolvable operator
// is still a 401 and api's own rejection of the session is still api's.
func TestInterventionNotIssuedStatusKeepsTheEarnedStatus(t *testing.T) {
	assert.Equal(t, http.StatusUnauthorized, interventionNotIssuedStatus(errNoOperator))
	assert.Equal(t, http.StatusForbidden,
		interventionNotIssuedStatus(&writeRejection{status: http.StatusForbidden, message: "reader role"}))
	assert.Equal(t, http.StatusBadGateway, interventionNotIssuedStatus(errors.New("api unreachable")))
}

// TestRefusalBackLabelNamesTheTab keeps the in-shell link's text honest about
// where it goes, which is the part of "a link back to the tab" a reader
// actually sees.
func TestRefusalBackLabelNamesTheTab(t *testing.T) {
	assert.Equal(t, "Back to the tab",
		refusalBackLabel(needsAttentionTabHref(testScopeID, needsAttentionTabClaimed)))
	assert.Equal(t, "Back to the console", refusalBackLabel(opsClaimedPath),
		"a form whose return_to is one of the retired /ops paths keeps the console's own label")
	assert.Equal(t, "Back to the console", refusalBackLabel("://not a url"))
}
