package main

// The task detail's intervention controls at the page the operator reads
// them from (FR af61631d): which verbs each state offers, what each control
// carries as its observed-state guard, the confirmation Cancel is reached
// through, and the swap region every control names.
//
// The offer set is asserted against the ONE shared predicate
// (legalInterventions), never against a second table restating it, so this
// file would fail if the detail grew a reading of its own.

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

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// ---------------------------------------------------------------------------
// reading the callout and its controls off the rendered markup
// ---------------------------------------------------------------------------

// detailActionsOf is the detail's actions callout: from its own marker to
// the frame that follows it, so an assertion about the offered verbs is
// about the callout and not about whatever else the page carries.
func detailActionsOf(t *testing.T, html string) string {
	t.Helper()
	return regionBetween(t, html, `data-krill="task-actions"`, `data-krill="task-detail-frame"`)
}

// offeredVerbs is the verbs a region's controls post, in document order.
// It reads the hx-post each control carries -- the htmx half of the same
// route the form's action names -- so a control that renders but posts
// somewhere else is not counted as an offer of its verb.
func offeredVerbs(t *testing.T, region string) []string {
	t.Helper()
	const open = `hx-post="/ops/tasks/`
	var verbs []string
	rest := region
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return verbs
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, `"`)
		require.GreaterOrEqual(t, j, 0, "an hx-post attribute is never closed:\n%s", region)
		path := rest[:j]
		verbs = append(verbs, path[strings.LastIndex(path, "/")+1:])
		rest = rest[j:]
	}
}

// controlTargets is the hx-target of every control in a region, in document
// order.
func controlTargets(t *testing.T, region string) []string {
	t.Helper()
	const open = `hx-target="`
	var targets []string
	rest := region
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return targets
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, `"`)
		require.GreaterOrEqual(t, j, 0, "an hx-target attribute is never closed:\n%s", region)
		targets = append(targets, rest[:j])
		rest = rest[j:]
	}
}

// formActionValues is every action= URL in a region, in document order.
func formActionValues(region string) []string {
	const open = `action="`
	var out []string
	rest := region
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

// controlActionFor is the action= URL of the control for verb: the verb's
// own route for a non-destructive control, and the confirmation page for
// the destructive one.
func controlActionFor(t *testing.T, region, verb string) string {
	t.Helper()
	for _, action := range formActionValues(region) {
		if strings.HasSuffix(action, "/"+verb) || strings.HasSuffix(action, "/"+verb+"/confirm") {
			return action
		}
	}
	t.Fatalf("the callout renders no control for %q; actions: %v", verb, formActionValues(region))
	return ""
}

// hiddenValueIn is the value a region submits under name, requiring the
// input to be hidden -- the FR's "taken from the page, never typed".
func hiddenValueIn(t *testing.T, region, name string) string {
	t.Helper()
	const open = `type="hidden" name="`
	i := strings.Index(region, open+name+`" value="`)
	require.GreaterOrEqual(t, i, 0, "%s must be rendered as a hidden input:\n%s", name, region)
	rest := region[i+len(open)+len(name)+len(`" value="`):]
	j := strings.Index(rest, `"`)
	require.GreaterOrEqual(t, j, 0)
	return rest[:j]
}

// countHiddenIn is how many hidden inputs a region submits under name, so a
// test can assert the guard rides EVERY control rather than one of them.
func countHiddenIn(region, name string) int {
	return strings.Count(region, `type="hidden" name="`+name+`"`)
}

// ---------------------------------------------------------------------------
// 1. the offer set, derived from the shared predicate
// ---------------------------------------------------------------------------

// claimedDetailTask / escalatedDetailTask / readyDetailTask are one task per
// state the legality table names, each carrying the lane it sits in.
func claimedDetailTask(title string, claimID uuid.UUID, lane store.Lane) store.Task {
	return store.Task{Title: title, CurrentLane: lane, CurrentClaimID: &claimID}
}

func escalatedDetailTask(title string, escalationID uuid.UUID, lane store.Lane) store.Task {
	return store.Task{Title: title, CurrentLane: lane, CurrentEscalationID: &escalationID}
}

// TestTaskDetailOffersExactlyTheVerbsTheSharedPredicateAllows walks every
// state the FR's legality table names -- escalated, claimed, ready,
// cancelled, and the Done lane's subtraction over each of them -- and pins
// the detail's offer set to legalInterventions' answer for that state.
//
// The expectation is the SHARED predicate's output, not a second table:
// the detail offering a verb the predicate withholds (or withholding one it
// allows) fails here, which is the drift the FR's "reuse the one predicate"
// exists to prevent. A state the predicate offers nothing for renders no
// callout at all, which is asserted as the absence of the callout rather
// than as an empty box.
func TestTaskDetailOffersExactlyTheVerbsTheSharedPredicateAllows(t *testing.T) {
	claimID, escalationID := uuid.New(), uuid.New()
	cancelledAt := time.Now().Add(-time.Hour)

	for _, tc := range []struct {
		name string
		task store.Task
	}{
		{"escalated", escalatedDetailTask("an escalated task", escalationID, store.LaneTesting)},
		{"claimed", claimedDetailTask("a claimed task", claimID, store.LaneImplementation)},
		{"ready", store.Task{Title: "a ready task", CurrentLane: store.LaneImplementation}},
		{"cancelled", store.Task{Title: "a cancelled task", CurrentLane: store.LaneImplementation, CancelledAt: &cancelledAt}},
		{
			// Cancelled is terminal even when the row still carries a claim:
			// the state is mutually exclusive and the terminal one wins.
			"cancelled with a stale claim",
			store.Task{Title: "a cancelled task", CurrentLane: store.LaneImplementation, CancelledAt: &cancelledAt, CurrentClaimID: &claimID},
		},
		{"done-lane claimed", claimedDetailTask("a finished claimed task", claimID, store.LaneDone)},
		{"done-lane escalated", escalatedDetailTask("a finished escalated task", escalationID, store.LaneDone)},
		{"done-lane ready", store.Task{Title: "a finished ready task", CurrentLane: store.LaneDone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDetailFixture(t)
			task := f.add(tc.task)
			_, frag := f.get(task.ID.String(), true)

			want := legalInterventions(taskInterventionStateOf(task), task.CurrentLane)
			if len(want) == 0 {
				assert.NotContains(t, frag, `data-krill="task-actions"`,
					"a state that offers no action renders no callout at all -- an empty box would read as a control that failed to render")
				return
			}
			require.Contains(t, frag, `data-krill="task-actions"`)
			assert.Equal(t, want, offeredVerbs(t, detailActionsOf(t, frag)),
				"the detail must offer exactly the verbs the shared predicate allows, in its order")
		})
	}
}

// TestTaskDetailOfferSetOnTheHeadlineStates states the FR's own table
// literally, so this file pins the milestone's contract and not only the
// agreement between two callers of one function. The predicate's own table
// is intervention_legality_test.go's; these are the four shape claims
// af61631d makes about the detail.
func TestTaskDetailOfferSetOnTheHeadlineStates(t *testing.T) {
	claimID, escalationID := uuid.New(), uuid.New()

	t.Run("escalated offers Requeue then Cancel, and never Release", func(t *testing.T) {
		f := newDetailFixture(t)
		task := f.add(escalatedDetailTask("an escalated task", escalationID, store.LaneTesting))
		_, frag := f.get(task.ID.String(), true)
		region := detailActionsOf(t, frag)

		assert.Equal(t, []string{actionRequeue, actionCancel}, offeredVerbs(t, region),
			"an escalated task holds no claim, so there is nothing to release")
		assert.NotContains(t, region, "/"+actionRelease)
	})

	t.Run("claimed offers Release, Escalate and Cancel", func(t *testing.T) {
		f := newDetailFixture(t)
		task := f.add(claimedDetailTask("a claimed task", claimID, store.LaneImplementation))
		_, frag := f.get(task.ID.String(), true)

		assert.Equal(t, []string{actionRelease, actionEscalate, actionCancel},
			offeredVerbs(t, detailActionsOf(t, frag)))
	})

	t.Run("ready offers Escalate and Cancel", func(t *testing.T) {
		f := newDetailFixture(t)
		task := f.add(store.Task{Title: "a ready task", CurrentLane: store.LaneImplementation})
		_, frag := f.get(task.ID.String(), true)

		assert.Equal(t, []string{actionEscalate, actionCancel},
			offeredVerbs(t, detailActionsOf(t, frag)))
	})

	t.Run("a Done-lane task is offered neither Escalate nor Cancel", func(t *testing.T) {
		f := newDetailFixture(t)
		task := f.add(claimedDetailTask("a finished claimed task", claimID, store.LaneDone))
		_, frag := f.get(task.ID.String(), true)
		region := detailActionsOf(t, frag)

		assert.NotContains(t, region, "/"+actionEscalate, "there is nothing left to flag for attention")
		assert.NotContains(t, region, "/"+actionCancel, "a finished task cannot be dead-lettered")
		assert.Contains(t, region, "/"+actionRelease,
			"the claim a finished task still holds is releasable")
	})
}

// TestTaskDetailCancelledTaskOffersNoCalloutAtAll pins the FR's "Cancelled:
// none" as the absence of the region, from the whole page and from the
// swapped fragment: the terminal state is the offer set's empty case, and
// an empty "Actions" card would read as a broken control instead.
func TestTaskDetailCancelledTaskOffersNoCalloutAtAll(t *testing.T) {
	f := newDetailFixture(t)
	cancelledAt := time.Now().Add(-time.Hour)
	task := f.add(store.Task{
		Title: "a cancelled task", CurrentLane: store.LaneImplementation,
		CancelledAt: &cancelledAt, CurrentClaimID: nil,
	})

	_, frag := f.get(task.ID.String(), true)
	_, full := f.get(task.ID.String(), false)
	for _, body := range []string{frag, full} {
		assert.NotContains(t, body, `data-krill="task-actions"`)
		assert.NotContains(t, body, `hx-post="/ops/tasks/`)
	}
}

// ---------------------------------------------------------------------------
// 2. what each control carries as its observed-state guard
// ---------------------------------------------------------------------------

// TestTaskDetailControlsCarryTheObservedIDThePageRead is the FR's guard
// clause: the claim id for Release, Escalate and Cancel on a claimed task,
// the escalation id for Requeue and Cancel on an escalated one, and neither
// on a ready task. Every control in the callout carries it -- not one of
// them -- and it is a hidden input, because the operator cannot type it and
// must not be asked to.
func TestTaskDetailControlsCarryTheObservedIDThePageRead(t *testing.T) {
	t.Run("a claimed task's controls carry its claim id", func(t *testing.T) {
		f := newDetailFixture(t)
		claimID := uuid.New()
		task := f.add(claimedDetailTask("a claimed task", claimID, store.LaneTesting))
		f.store.claim = store.Claim{ID: claimID, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}

		region := detailActionsOf(t, f.mustFragTask(t, task))

		assert.Equal(t, len(offeredVerbs(t, region)), countHiddenIn(region, expectedClaimIDParam),
			"every control carries the claim the page observed, or one of them posts unguarded")
		assert.Equal(t, claimID.String(), hiddenValueIn(t, region, expectedClaimIDParam))
		assert.Equal(t, 0, countHiddenIn(region, escalatedGuardField),
			"a claimed task carries no escalation guard")
	})

	t.Run("an escalated task's controls carry its escalation id", func(t *testing.T) {
		f := newDetailFixture(t)
		escalationID := uuid.New()
		task := f.add(escalatedDetailTask("an escalated task", escalationID, store.LaneTesting))
		f.store.escalation = store.EscalationEvent{ID: escalationID, TaskID: task.ID, Reason: store.EscalationReasonManual}

		region := detailActionsOf(t, f.mustFragTask(t, task))

		assert.Equal(t, len(offeredVerbs(t, region)), countHiddenIn(region, escalatedGuardField))
		assert.Equal(t, escalationID.String(), hiddenValueIn(t, region, escalatedGuardField))
		assert.Equal(t, 0, countHiddenIn(region, expectedClaimIDParam),
			"an escalated task holds no claim, so it carries no claim guard")
	})

	t.Run("a ready task's controls carry neither", func(t *testing.T) {
		f := newDetailFixture(t)
		task := f.add(store.Task{Title: "a ready task", CurrentLane: store.LaneTesting})

		region := detailActionsOf(t, f.mustFragTask(t, task))

		assert.Equal(t, 0, countHiddenIn(region, expectedClaimIDParam),
			"the FR names a ready task's Cancel the one unguarded shape")
		assert.Equal(t, 0, countHiddenIn(region, escalatedGuardField))
		// The controls still exist: unguarded is not unoffered.
		assert.Equal(t, []string{actionEscalate, actionCancel}, offeredVerbs(t, region))
	})
}

// mustFrag is the HX-Request fragment for a task, which is the region an
// intervention re-derives and swaps.
func (f *detailFixture) mustFragTask(t *testing.T, task store.Task) string {
	t.Helper()
	code, frag := f.get(task.ID.String(), true)
	require.Equal(t, http.StatusOK, code)
	return frag
}

// ---------------------------------------------------------------------------
// 3. Cancel's confirmation, both paths
// ---------------------------------------------------------------------------

// TestTaskDetailCancelConfirmsInTheBrowserNamingTheTask pins FR af61631d's
// confirmation clause on the detail's own control: the htmx half shows the
// FR's exact copy -- naming THIS task -- before it posts anything, and the
// no-JS half is a link to the existing confirmation page rather than a
// post.
func TestTaskDetailCancelConfirmsInTheBrowserNamingTheTask(t *testing.T) {
	f := newDetailFixture(t)
	claimID := uuid.New()
	task := f.add(claimedDetailTask("a claimed task", claimID, store.LaneTesting))

	region := detailActionsOf(t, f.mustFragTask(t, task))

	// The FR's copy, verbatim: the confirmation names the task it will
	// cancel, so it cannot confirm a different one.
	want := "Cancel a claimed task? It moves to Cancelled and cannot be claimed again."
	assert.Contains(t, region, `hx-confirm="`+want+`"`)
	assert.Contains(t, region, `hx-post="`+opsTaskActionBase+task.ID.String()+`/`+actionCancel+`"`,
		"with JavaScript the same control posts the verb in place")

	// Without JavaScript the same control is a GET to the confirmation page,
	// which posts nothing until its own form is submitted.
	assert.Contains(t, region, `method="get"`)
	assert.Contains(t, region, `action="`+opsTaskActionBase+task.ID.String()+cancelConfirmSuffix+`"`)

	// The three non-destructive verbs never confirm: only the
	// irreversible one is reached through a confirmation.
	assert.Equal(t, 1, strings.Count(region, "hx-confirm="))
}

// TestTaskDetailNoJSConfirmationCarriesTheObservedGuard follows the detail's
// own no-JS Cancel link, with the hidden inputs it renders, and asserts the
// existing confirmation page carries the observed id and the return path
// through to its own form -- so the eventual POST is guarded against the
// state the detail actually read, and a changed claim is refused rather
// than acted on.
func TestTaskDetailNoJSConfirmationCarriesTheObservedGuard(t *testing.T) {
	f := newDetailFixture(t)
	claimID := uuid.New()
	task := f.add(claimedDetailTask("a claimed task", claimID, store.LaneTesting))
	f.store.claim = store.Claim{ID: claimID, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}

	// The product-scoped detail -- the address the product-wide table's rows
	// link to, and the one the intervention return path knows how to come
	// back to (interventionReturnTo). The retired per-container URL is a
	// redirect now, so nothing renders a control on it.
	code, frag := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, http.StatusOK, code)
	region := detailActionsOf(t, frag)
	confirm := controlActionFor(t, region, actionCancel)
	require.Contains(t, confirm, cancelConfirmSuffix)

	// What the browser submits when the no-JS half is a GET form: the
	// control's own hidden inputs become the query string.
	path := confirm +
		"?return_to=" + url.QueryEscape(hiddenValueIn(t, region, "return_to")) +
		"&" + expectedClaimIDParam + "=" + url.QueryEscape(hiddenValueIn(t, region, expectedClaimIDParam))

	// The confirmation route production mounts, behind the same operator
	// gate; only the id and the guard are what this asserts, so the fixture
	// serves it directly.
	app := &App{spec: f.spec, tasks: f.store, scopes: chromeScopes{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ops/tasks/{id}/cancel/confirm", app.handleCancelConfirm)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	card := rec.Body.String()
	assert.Contains(t, card, `id="`+pages.CancelConfirmAnchor+`"`)
	assert.Contains(t, card, `type="hidden" name="`+expectedClaimIDParam+`" value="`+claimID.String()+`"`,
		"the confirmation page hands the observed claim on to the cancel it will post")
	assert.Contains(t, card, `type="hidden" name="return_to" value="`+
		hiddenValueIn(t, region, "return_to")+`"`,
		"and the path back to the detail, so the cancel lands where the operator acted")
}

// ---------------------------------------------------------------------------
// 4. the swap region: the detail section, which exists on both renderings
// ---------------------------------------------------------------------------

// TestTaskDetailControlsSwapTheDetailSectionThatExists is the Scaffold
// lane's correction, pinned: the detail's controls must not target the
// console's results block -- no element carries that id on this page, so
// htmx would find no target and do nothing at all. They target the detail
// section, and that id is present on BOTH the full page and the swapped
// fragment, or the first action would delete the region its own answer
// needs.
func TestTaskDetailControlsSwapTheDetailSectionThatExists(t *testing.T) {
	claimID, escalationID := uuid.New(), uuid.New()
	for _, tc := range []struct {
		name string
		task store.Task
	}{
		{"escalated", escalatedDetailTask("an escalated task", escalationID, store.LaneTesting)},
		{"claimed", claimedDetailTask("a claimed task", claimID, store.LaneTesting)},
		{"ready", store.Task{Title: "a ready task", CurrentLane: store.LaneTesting}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDetailFixture(t)
			task := f.add(tc.task)
			_, frag := f.get(task.ID.String(), true)
			region := detailActionsOf(t, frag)

			targets := controlTargets(t, region)
			require.Len(t, targets, len(offeredVerbs(t, region)),
				"every control names the region it swaps, or htmx guesses")
			for _, target := range targets {
				assert.Equal(t, "#"+pages.TaskDetailAnchor, target)
				assert.NotEqual(t, "#"+pages.OpsResultsAnchor, target,
					"the console's results block does not exist on the detail page")
			}

			// Both renderings carry the id the controls target. The
			// fragment's ROOT being the section is what makes the swap
			// replace the section rather than nest a second one.
			_, full := f.get(task.ID.String(), false)
			assert.Contains(t, full, `id="`+pages.TaskDetailAnchor+`"`)
			assert.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<section id="`+pages.TaskDetailAnchor+`"`),
				"the swapped fragment's root must be the section the controls target; got:\n%s", headOfLine(frag))
			assert.NotContains(t, frag, `id="`+pages.OpsResultsAnchor+`"`)
		})
	}
}

// headOfLine is the head of a rendering, for a failure message that does not
// dump a whole page.
func headOfLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---------------------------------------------------------------------------
// 5. Requeue is the escalated state's primary action
// ---------------------------------------------------------------------------

// TestTaskDetailRequeueIsThePrimaryControlOnAnEscalatedTask pins the FR's
// "Requeue (primary)": exactly one control on an escalated detail is the
// filled primary button and it is the Requeue one. The claimed and ready
// states mark none -- and, because the same builder renders the console's
// rows, this is also where "the detail's Primary flag did not leak into the
// rows' rendering" is observable: a row's control names no region either.
func TestTaskDetailRequeueIsThePrimaryControlOnAnEscalatedTask(t *testing.T) {
	escalationID := uuid.New()

	f := newDetailFixture(t)
	task := f.add(escalatedDetailTask("an escalated task", escalationID, store.LaneTesting))
	region := detailActionsOf(t, f.mustFragTask(t, task))

	assert.Equal(t, 1, strings.Count(region, "btn-primary"),
		"exactly one verb is the state's primary action")
	assert.Contains(t, region, `hx-post="`+opsTaskActionBase+task.ID.String()+`/`+actionRequeue+`"`)
	// The Requeue form, and only it, carries the filled button.
	assert.Contains(t, region, "btn-primary btn-xs")
	assert.Contains(t, region, "btn-error btn-xs")

	for _, tc := range []struct {
		name string
		task store.Task
	}{
		{"claimed", claimedDetailTask("a claimed task", uuid.New(), store.LaneTesting)},
		{"ready", store.Task{Title: "a ready task", CurrentLane: store.LaneTesting}},
	} {
		t.Run(tc.name+" marks no primary", func(t *testing.T) {
			f := newDetailFixture(t)
			task := f.add(tc.task)
			region := detailActionsOf(t, f.mustFragTask(t, task))
			assert.NotContains(t, region, "btn-primary",
				"only the escalated state's Requeue is primary; every other verb is the ghost button the rows use")
		})
	}
}
