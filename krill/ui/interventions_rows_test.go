// The three in-place interventions driven from the Needs attention tab the
// operator is actually reading (FR 43e39aae): Requeue from the Escalated tab,
// Release and Escalate from the Claimed tab.
//
// Each case renders the tab's own results region through the tab's own
// loader, takes the control's action, its return_to and its observed-state
// guard OFF THAT MARKUP -- never spelling any of the three itself -- posts it
// through the production intervention route, and asserts:
//
//   - the write reaches the api with the verb's own path and the guard id the
//     row observed, so a changed claim or escalation is refused against what
//     the operator actually saw (store.ErrObservedStateMismatch);
//   - the response is 200 with the acting tab's results region re-derived from
//     a fresh read, so a row that left its tab is gone from the table it was
//     in; and
//   - the confirmation is the FR's exact wording.
//
// The store is a fake whose rows the test mutates between the render and the
// post, which is the honest model of the transition: the re-derivation reads
// state as it is AFTER the write, not as it was when the page was built.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// ---------------------------------------------------------------------------
// the fixture: a signed-in operator on the page's own store
// ---------------------------------------------------------------------------

// tabActionTasks is the two list reads the tab's loaders make, plus the
// fixture rows a test mutates to stand in for what the write did. It embeds
// store.TaskStore so any other read the re-derivation grows nil-panics rather
// than quietly answering from nowhere.
type tabActionTasks struct {
	store.TaskStore

	claimed   []store.ClaimedTaskRow
	escalated []store.EscalatedTaskRow
}

func (s *tabActionTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{Items: s.claimed}, nil
}

func (s *tabActionTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{Items: s.escalated}, nil
}

// tabInterventionFixture is the world these tests walk: the harness's real
// signed-in operator and real write client against a fake api, with the Needs
// attention page's own task store and product wired in.
type tabInterventionFixture struct {
	app     *App
	mux     *http.ServeMux
	api     *fakeAPI
	session *http.Cookie
	pid     uuid.UUID
	tasks   *tabActionTasks
}

func newTabInterventionFixture(t *testing.T) *tabInterventionFixture {
	t.Helper()

	api := newFakeAPI(t)
	idp := newFakeIDP(t, uuid.NewString())
	authenticator, sessionCookie := newSignedInOperator(t, idp)

	app := newSignedInApp(t, authenticator, idp.server.URL, api.server.URL)
	tasks := &tabActionTasks{}
	app.tasks = tasks

	// The intervention routes production mounts (main.go's setupRoutes), so a
	// submission takes the real auth -> operator -> write path.
	mux := newInterventionMux(app)
	return &tabInterventionFixture{
		app: app, mux: mux, api: api, session: sessionCookie, pid: testScopeID, tasks: tasks,
	}
}

// tabPath is the tab's own URL, built by the production href builder so the
// selfPath the region is rendered with is the address the page itself spells.
func (f *tabInterventionFixture) tabPath(tab string) string {
	return needsAttentionTabHref(f.pid, tab)
}

// region renders one tab's results region through the tab's own loader, the
// same function the tab's GET and the post-write re-derivation both call.
func (f *tabInterventionFixture) region(t *testing.T, tab string) string {
	t.Helper()
	view, err := f.app.needsAttentionResults(context.Background(), f.pid,
		needsAttentionFilter{}, "", tab, store.PageParams{}, f.tabPath(tab), f.app.clock(), "")
	require.NoError(t, err)
	return mustRenderComponent(view.Results)
}

// tabClaimedRow is one claimed task whose guard the row must carry.
func tabClaimedRow() store.ClaimedTaskRow {
	return store.ClaimedTaskRow{
		TaskID:      uuid.MustParse("aaaaaaaa-1111-2222-3333-444444444444"),
		Title:       "a claimed task on the Claimed tab",
		CurrentLane: store.LaneImplementation,
		ClaimID:     uuid.MustParse("bbbbbbbb-1111-2222-3333-444444444444"),
	}
}

// tabEscalatedRow is one escalated task whose guard the row must carry.
func tabEscalatedRow() store.EscalatedTaskRow {
	return store.EscalatedTaskRow{
		TaskID:       uuid.MustParse("cccccccc-1111-2222-3333-444444444444"),
		Title:        "an escalated task on the Escalated tab",
		Reason:       store.EscalationReasonThrashCap,
		Lane:         store.LaneImplementation,
		EscalationID: uuid.MustParse("dddddddd-1111-2222-3333-444444444444"),
	}
}

// ---------------------------------------------------------------------------
// reading a control off the markup, so the POST is the row's own
// ---------------------------------------------------------------------------

// formActions returns every action= URL in the markup, in document order.
func formActions(markup string) []string {
	var out []string
	rest := markup
	for {
		i := strings.Index(rest, `action="`)
		if i < 0 {
			return out
		}
		rest = rest[i+len(`action="`):]
		j := strings.Index(rest, `"`)
		if j < 0 {
			return out
		}
		out = append(out, rest[:j])
		rest = rest[j:]
	}
}

// formActionFor is the action of the region's inline form for one verb, read
// from the markup so the POST is built from the control the row rendered
// rather than from a URL the test spelled.
func formActionFor(t *testing.T, markup, verb string) string {
	t.Helper()
	for _, action := range formActions(markup) {
		if strings.HasSuffix(action, "/"+verb) {
			return action
		}
	}
	t.Fatalf("the region renders no form for %q; actions: %v", verb, formActions(markup))
	return ""
}

// hiddenInputValue is the value the region submits under name. It requires the
// input to be hidden, which is the FR's "taken from the row and never typed":
// the operator has nothing to type into.
func hiddenInputValue(t *testing.T, markup, name string) string {
	t.Helper()
	const open = `type="hidden" name="`
	i := strings.Index(markup, open+name+`" value="`)
	require.GreaterOrEqual(t, i, 0, "%s must be rendered as a hidden input", name)
	rest := markup[i+len(open)+len(name)+len(`" value="`):]
	j := strings.Index(rest, `"`)
	require.GreaterOrEqual(t, j, 0)
	return rest[:j]
}

// ---------------------------------------------------------------------------
// 1. each verb posts from its own tab, carrying the id the row observed
// ---------------------------------------------------------------------------

// TestEachVerbsPostsFromItsOwnTab is the FR's first sentence, one case per
// verb: activating the row's control posts the api intervention with the verb's
// own path and the observed-state id the row carried, and the response is 200
// with the ACTING tab's region re-derived from a fresh read -- the acted-on row
// gone, a surviving row of the same tab still there, and the other tab's table
// never swapped in.
func TestEachVerbsPostsFromItsOwnTab(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tab       string
		verb      string
		guard     string
		wantToast string
	}{
		{
			name: "Release from the Claimed tab",
			tab:  needsAttentionTabClaimed, verb: actionRelease,
			guard: expectedClaimIDParam, wantToast: "Claim released",
		},
		{
			name: "Escalate from the Claimed tab",
			tab:  needsAttentionTabClaimed, verb: actionEscalate,
			guard: expectedClaimIDParam, wantToast: "Task escalated",
		},
		{
			name: "Requeue from the Escalated tab",
			tab:  needsAttentionTabEscalated, verb: actionRequeue,
			guard: escalatedGuardField, wantToast: "Task requeued",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTabInterventionFixture(t)

			// Two rows per tab: the one acted on, and a survivor that must
			// still be rendered. The survivor (with no claim or escalation id,
			// so it renders no guard field of its own) is what proves the
			// region is the acting tab's table re-read rather than an empty
			// block, and the other tab's survivor is what proves the region
			// was not re-derived from the wrong tab.
			claimed, claimedSurvivor := tabClaimedRow(), tabClaimedRow()
			claimedSurvivor.TaskID = uuid.New()
			claimedSurvivor.Title = "a surviving claimed task"
			claimedSurvivor.ClaimID = uuid.Nil
			escalated, escalatedSurvivor := tabEscalatedRow(), tabEscalatedRow()
			escalatedSurvivor.TaskID = uuid.New()
			escalatedSurvivor.Title = "a surviving escalated task"
			escalatedSurvivor.EscalationID = uuid.Nil
			f.tasks.claimed = []store.ClaimedTaskRow{claimed, claimedSurvivor}
			f.tasks.escalated = []store.EscalatedTaskRow{escalated, escalatedSurvivor}

			rowTitle, survivorTitle, taskID := claimed.Title, claimedSurvivor.Title, claimed.TaskID
			otherTabTitle, wantID := escalatedSurvivor.Title, claimed.ClaimID
			if tc.tab == needsAttentionTabEscalated {
				rowTitle, survivorTitle, taskID = escalated.Title, escalatedSurvivor.Title, escalated.TaskID
				otherTabTitle, wantID = claimedSurvivor.Title, escalated.EscalationID
			}

			markup := f.region(t, tc.tab)
			action := formActionFor(t, markup, tc.verb)
			returnTo := hiddenInputValue(t, markup, "return_to")
			observed := hiddenInputValue(t, markup, tc.guard)

			// The guard is the id the ROW observed, and the row's control is a
			// hidden input: there is nothing for the operator to type.
			assert.Equal(t, wantID.String(), observed, "the control carries the id its own row was read with")

			// The write's own transition: only the acted-on row leaves.
			if tc.tab == needsAttentionTabClaimed {
				f.tasks.claimed = []store.ClaimedTaskRow{claimedSurvivor}
			} else {
				f.tasks.escalated = []store.EscalatedTaskRow{escalatedSurvivor}
			}
			// The POST is the row's own control, values included.
			form := url.Values{
				"reason":    {"posted from the row"},
				"return_to": {returnTo},
				tc.guard:    {observed},
			}
			rec := hxFormPost(f.mux, action, form, f.session)

			require.Equal(t, http.StatusOK, rec.Code, "an htmx intervention answers 200: %s", rec.Body.String())
			got := rec.Body.String()
			assert.Contains(t, got, `id="ops-results"`, "the acting tab's results region is the swap target")
			assert.Contains(t, got, survivorTitle, "the acting tab's table is re-read, survivor and all")
			assert.NotContains(t, got, rowTitle,
				"the acted-on row left its tab: the region is re-derived from a fresh read")
			assert.NotContains(t, got, otherTabTitle,
				"the region is the acting tab's table, never the other tab's")
			assert.Contains(t, got, `<span class="text-sm">`+tc.wantToast+`</span>`,
				"the toast states the FR's exact wording")

			// The write: the verb's own api path, the row's observed id under
			// the api's own field name, and no identity or scope the browser
			// could have supplied.
			write := f.api.writeRequest(t)
			assert.Equal(t, http.MethodPost, write.Method)
			assert.Equal(t, "/tasks/"+taskID.String()+"/"+tc.verb, write.Path)
			var body map[string]any
			require.NoError(t, json.Unmarshal(write.Body, &body))
			assert.Equal(t, wantID.String(), body[tc.guard],
				"the observed id the row rendered is the guard the api is checked against")
			assert.Equal(t, "posted from the row", body["reason"])
			for _, forbidden := range []string{"acting", "on_behalf_of", "scope_id"} {
				assert.NotContains(t, body, forbidden, "no operator identity or scope is posted")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. the action is the row's own, and its guard is never typed
// ---------------------------------------------------------------------------

// TestRowGuardIsAHiddenFieldTheRowOwns is the FR's "taken from the row and
// never typed": the guard rides a hidden input (there is nothing for the
// operator to type) and its value is exactly the id the read returned.
func TestRowGuardIsAHiddenFieldTheRowOwns(t *testing.T) {
	f := newTabInterventionFixture(t)
	claimed, escalated := tabClaimedRow(), tabEscalatedRow()
	f.tasks.claimed, f.tasks.escalated = []store.ClaimedTaskRow{claimed}, []store.EscalatedTaskRow{escalated}

	for _, tc := range []struct {
		tab, field, want string
	}{
		{needsAttentionTabClaimed, expectedClaimIDParam, claimed.ClaimID.String()},
		{needsAttentionTabEscalated, escalatedGuardField, escalated.EscalationID.String()},
	} {
		t.Run(tc.tab, func(t *testing.T) {
			markup := f.region(t, tc.tab)
			assert.Contains(t, markup, `type="hidden" name="`+tc.field+`" value="`+tc.want+`"`,
				"the guard is a hidden input carrying the observed id")
			assert.NotContains(t, markup, `type="text" name="`+tc.field+`"`,
				"the operator is never asked to type the guard")
		})
	}
}

// ---------------------------------------------------------------------------
// 3. the shared legality predicate, at the rows an operator acts from
// ---------------------------------------------------------------------------

// TestTabRowsOfferOnlyTheLegalVerbs pins the FR's legality clause on the rows
// themselves: an escalated row offers no Release (it holds no claim to
// force-close), and a Done-lane claimed row offers no Escalate (there is
// nothing left to flag for attention), while Release -- the claim it still
// holds -- stays.
func TestTabRowsOfferOnlyTheLegalVerbs(t *testing.T) {
	t.Run("an escalated row offers no Release", func(t *testing.T) {
		for _, lane := range []store.Lane{store.LaneScaffold, store.LaneImplementation, store.LaneDone} {
			f := newTabInterventionFixture(t)
			row := tabEscalatedRow()
			row.Lane = lane
			f.tasks.escalated = []store.EscalatedTaskRow{row}

			markup := f.region(t, needsAttentionTabEscalated)

			assert.NotContains(t, markup, "/"+actionRelease,
				"an escalated task holds no claim, so there is nothing to release")
			assert.NotContains(t, markup, ">Release<")
			assert.Contains(t, markup, ">Requeue<", "the recovery is still offered")
		}
	})

	t.Run("a Done-lane claimed row offers no Escalate", func(t *testing.T) {
		f := newTabInterventionFixture(t)
		row := tabClaimedRow()
		row.CurrentLane = store.LaneDone
		f.tasks.claimed = []store.ClaimedTaskRow{row}

		markup := f.region(t, needsAttentionTabClaimed)

		assert.NotContains(t, markup, "/"+actionEscalate,
			"a finished task offers no Escalate")
		assert.NotContains(t, markup, ">Escalate<")
		assert.Contains(t, markup, ">Release<", "the claim it still holds is releasable")
		assert.NotContains(t, markup, cancelConfirmSuffix, "and a finished task offers no Cancel")
	})
}

// ---------------------------------------------------------------------------
// 4. the guard: the observed id reaches the api, and its refusal stays inline
// ---------------------------------------------------------------------------

// TestObservedStateGuardRefusalStaysInline raises the FR's guard-trigger
// clause at this binary's edge: the api's observed-state guard
// (store.ErrObservedStateMismatch, 409) is modelled here as the rule "the id
// in the write must be the id that is CURRENT". The success cases above prove
// the UI carries the row's id; this case proves that when that id is no longer
// current the refusal comes back as an inline alert in the tab's region rather
// than as a crash, an empty table or an assumed success.
//
// The rest of the observer-facing refusal RESPONSE contract (the fresh ids,
// the no-JS in-shell page, the transport path) is FR c69a42b4's; this asserts
// the presented refusal, including that it is krill's own wording rather than
// the store's.
func TestObservedStateGuardRefusalStaysInline(t *testing.T) {
	for _, tc := range []struct {
		name, tab, verb, field string
	}{
		{"release", needsAttentionTabClaimed, actionRelease, expectedClaimIDParam},
		{"requeue", needsAttentionTabEscalated, actionRequeue, escalatedGuardField},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTabInterventionFixture(t)
			claimed, escalated := tabClaimedRow(), tabEscalatedRow()
			f.tasks.claimed, f.tasks.escalated = []store.ClaimedTaskRow{claimed}, []store.EscalatedTaskRow{escalated}

			markup := f.region(t, tc.tab)
			action := formActionFor(t, markup, tc.verb)
			observed := hiddenInputValue(t, markup, tc.field)

			// The current state has moved on since the page was read, so the
			// id the row carries is stale and the guard refuses.
			f.api.onRequest(guardLikeStore(map[string]string{tc.field: uuid.NewString()}))

			rec := hxFormPost(f.mux, action, url.Values{
				"return_to": {hiddenInputValue(t, markup, "return_to")},
				tc.field:    {observed},
			}, f.session)

			require.Equal(t, http.StatusOK, rec.Code, "a guard refusal is presented, not status-coded")
			got := rec.Body.String()
			assert.Contains(t, got, "changed since the page was loaded",
				"the guard's refusal reaches the operator in krill's own words (FR c69a42b4)")
			assert.NotContains(t, got, store.ErrObservedStateMismatch.Error(),
				"and never as the store's own package-qualified text")
			assert.Contains(t, got, `role="alert"`, "and it rides inline in the fragment")
			assert.NotContains(t, got, `data-krill="toast"`,
				"a refusal is not a success: no toast is raised")
			assert.Contains(t, got, `id="ops-results"`, "the results region survives the refusal")
		})
	}
}

// guardLikeStore answers the fake api the way krill's observed-state guard
// does: a write whose guard id is not the CURRENT one is refused 409 with the
// guard's own error, exactly as api/handlers maps store.ErrObservedStateMismatch.
func guardLikeStore(current map[string]string) apiResponder {
	return func(req recordedRequest) (int, string) {
		if req.Path == "/sessions/init" {
			return 0, ""
		}
		var body map[string]any
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return 0, ""
		}
		for field, want := range current {
			if got, _ := body[field].(string); got != want {
				return http.StatusConflict,
					`{"error":` + strconv.Quote(store.ErrObservedStateMismatch.Error()) + `}`
			}
		}
		return 0, ""
	}
}

// ---------------------------------------------------------------------------
// 5. the no-JavaScript branch posts the same write and lands on the tab
// ---------------------------------------------------------------------------

// TestNoJSReleaseFromTheClaimedTabReturnsToTheTab requires the doubled form's
// no-JS half to post the same guarded write and answer the console's
// Post/Redirect/Get, back to the tab the row was read from -- so a browser
// with JavaScript off takes one route and one write, not two.
func TestNoJSReleaseFromTheClaimedTabReturnsToTheTab(t *testing.T) {
	f := newTabInterventionFixture(t)
	row := tabClaimedRow()
	f.tasks.claimed = []store.ClaimedTaskRow{row}

	markup := f.region(t, needsAttentionTabClaimed)
	action := formActionFor(t, markup, actionRelease)
	returnTo := hiddenInputValue(t, markup, "return_to")
	observed := hiddenInputValue(t, markup, expectedClaimIDParam)
	f.tasks.claimed = nil

	rec := serveFormPost(f.mux, action, url.Values{
		"reason":             {"out of band"},
		"return_to":          {returnTo},
		expectedClaimIDParam: {observed},
	}, f.session)

	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, f.tabPath(needsAttentionTabClaimed), rec.Header().Get("Location"),
		"the operator lands back on the tab they acted from")

	write := f.api.writeRequest(t)
	assert.Equal(t, "/tasks/"+row.TaskID.String()+"/"+actionRelease, write.Path)
	var body map[string]any
	require.NoError(t, json.Unmarshal(write.Body, &body))
	assert.Equal(t, observed, body[expectedClaimIDParam],
		"the no-JS branch carries the same observed guard as the htmx one")
}

// ---------------------------------------------------------------------------
// 6. the reason popover, not a text input in every row (FR 0cf360c5)
// ---------------------------------------------------------------------------

// tableSectionOf is the markup from the region's first <table to its close --
// what "no row contains a text input" is about.
func tableSectionOf(t *testing.T, markup string) string {
	t.Helper()
	i := strings.Index(markup, "<table")
	require.GreaterOrEqual(t, i, 0, "the region renders a table")
	j := strings.Index(markup[i:], "</table>")
	require.GreaterOrEqual(t, j, 0, "the table is closed")
	return markup[i : i+j+len("</table>")]
}

// tableRowMarkup returns every <tr>...</tr> run in markup.
func tableRowMarkup(markup string) []string {
	var out []string
	rest := markup
	for {
		i := strings.Index(rest, "<tr")
		if i < 0 {
			return out
		}
		rest = rest[i:]
		j := strings.Index(rest, "</tr>")
		if j < 0 {
			return out
		}
		out = append(out, rest[:j+len("</tr>")])
		rest = rest[j+len("</tr>"):]
	}
}

// popoverTargets returns every popovertarget attribute value in markup, the
// popovers the row's action controls open.
func popoverTargets(markup string) []string {
	var out []string
	const open = `popovertarget="`
	rest := markup
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return out
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, `"`)
		if j < 0 {
			return out
		}
		out = append(out, rest[:j])
		rest = rest[j:]
	}
}

// TestTabRowsCarryNoTextFieldAndTheirReasonPopoversSitOutsideTheTable is FR
// 0cf360c5's shape at the region an operator actually reads: the rows of both
// action tabs hold no text input, each action control's trigger opens a
// popover the region renders, and every reason field sits OUTSIDE the table
// (so a table of N rows adds no N inputs) while still naming a form that is in
// a row.
func TestTabRowsCarryNoTextFieldAndTheirReasonPopoversSitOutsideTheTable(t *testing.T) {
	for _, tab := range []string{needsAttentionTabClaimed, needsAttentionTabEscalated} {
		t.Run(tab, func(t *testing.T) {
			f := newTabInterventionFixture(t)
			f.tasks.claimed = []store.ClaimedTaskRow{tabClaimedRow()}
			f.tasks.escalated = []store.EscalatedTaskRow{tabEscalatedRow()}

			markup := f.region(t, tab)
			table := tableSectionOf(t, markup)
			assert.NotContains(t, table, `type="text"`, "no row contains a text input")
			for _, row := range tableRowMarkup(table) {
				assert.NotContains(t, row, `type="text"`,
					"a row holds its hidden guard and return_to fields, never a text field")
			}

			// Every row trigger opens a popover the region renders OUTSIDE the
			// table: the trigger's popovertarget names an element that appears
			// after </table>, so the popover is the table's sibling, not a row's.
			targets := popoverTargets(markup)
			require.NotEmpty(t, targets, "each action control's trigger opens a reason popover")
			closeTable := strings.Index(markup, "</table>")
			for _, target := range targets {
				at := strings.Index(markup, `id="`+target+`"`)
				require.GreaterOrEqual(t, at, 0, "the popover %q the row's trigger names is rendered", target)
				assert.Greater(t, at, closeTable, "the popover %q renders outside the table", target)
			}

			// The reason field itself is outside the table, and names a form
			// that is inside one -- so the popover submits the row's own action.
			reasonAt := strings.Index(markup, `type="text" name="reason"`)
			require.GreaterOrEqual(t, reasonAt, 0, "the region renders the reason popover's field")
			assert.Greater(t, reasonAt, closeTable, "the reason field renders after the table, never in a row")
			formID := formAttr(t, markup[reasonAt:], "form")
			assert.Contains(t, table, `id="`+formID+`"`,
				"the popover's reason field names the form the row rendered")
		})
	}
}
