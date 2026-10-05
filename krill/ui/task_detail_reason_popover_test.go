package main

// The detail's optional reason popovers (FR 0cf360c5) as they reach the page.
//
// The console rows render their popovers OUTSIDE the table (no text input in a
// row); the detail has no table, so its popovers ride inside the actions
// callout beside the controls they belong to. What the two share, and what
// these tests pin, is the wiring: every trigger's popovertarget names a
// popover that IS rendered, and each popover's reason field and submit button
// name the control's own form, so a reason typed here is submitted with the
// guards on both the htmx and the native path.

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// TestTaskDetailRendersTheReasonPopoverEveryTriggerOpens is the regression
// this pass repairs. After the merge that moved an intervention's optional
// reason into a popover, the detail rendered the triggers but none of the
// popovers they open -- so every button pointed at an id the page never
// carried, and activating one did nothing. A trigger whose popover is missing
// is a dead button, so this is asserted per control, on the full page AND on
// the swapped fragment (the fragment is what a post-action re-render installs,
// so a popover that only survives the first render is dead from the second
// action on).
func TestTaskDetailRendersTheReasonPopoverEveryTriggerOpens(t *testing.T) {
	claimID, escalationID := uuid.New(), uuid.New()
	for _, tc := range []struct {
		name string
		task store.Task
	}{
		{"escalated", escalatedDetailTask("an escalated task", escalationID, store.LaneTesting)},
		{"claimed", claimedDetailTask("a claimed task", claimID, store.LaneImplementation)},
		{"ready", store.Task{Title: "a ready task", CurrentLane: store.LaneImplementation}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDetailFixture(t)
			task := f.add(tc.task)
			_, full := f.get(task.ID.String(), false)
			_, frag := f.get(task.ID.String(), true)

			for _, page := range []struct {
				which string
				html  string
			}{{"full page", full}, {"swapped fragment", frag}} {
				region := detailActionsOf(t, page.html)
				verbs := offeredVerbs(t, region)
				require.NotEmpty(t, verbs, "%s: the callout renders controls", page.which)

				// One popover per control, no more and no fewer. The popovers
				// are what makes the ids the triggers name resolvable, so a
				// count that drifts from the control count is the defect.
				assert.Equal(t, len(verbs), strings.Count(region, `data-krill="reason-popover"`),
					"%s: one reason popover per control; a trigger without one is a dead button", page.which)
				assert.Equal(t, len(verbs), strings.Count(region, `popovertargetaction="hide"`),
					"%s: every popover carries its own close control", page.which)

				for _, verb := range verbs {
					taskID := task.ID.String()
					formID := taskActionFormID(taskID, verb)
					popoverID := taskActionPopoverID(taskID, verb)
					reasonID := taskActionReasonID(taskID, verb)

					// The form the control posts, present in the callout.
					assert.Contains(t, region, `id="`+formID+`"`,
						"%s: %s loses the form its popover submits", page.which, verb)
					// The trigger opens exactly this control's popover...
					assert.Contains(t, region, `popovertarget="`+popoverID+`"`,
						"%s: %s's trigger must open its own popover", page.which, verb)
					// ...and that popover is actually rendered.
					assert.Contains(t, region, `id="`+popoverID+`" popover data-krill="reason-popover"`,
						"%s: %s's trigger points at a popover the page does not render", page.which, verb)
					// The reason field rides the control's form, so a reason
					// reaches the handler with the observed-state guard.
					assert.Contains(t, region, `id="`+reasonID+`" type="text" name="reason" form="`+formID+`"`,
						"%s: %s's reason must name the control's form", page.which, verb)
					// And the popover's own submit is the control's real one
					// on both paths: htmx intercepts the form, a no-JS browser
					// submits it natively.
					assert.Contains(t, region, `<button type="submit" form="`+formID+`"`,
						"%s: %s's popover submit must post the control's form", page.which, verb)
				}
			}
		})
	}
}

// TestTaskDetailReasonPopoverCheckBites is the negative control for the test
// above, in the shape the page's other detectors use (TestReadOnlySweepCatches
// AWriteInTheHeader). The pre-fix detail rendered the triggers and no popovers;
// splitAtReasonPopovers reproduces that markup as its trigger half, so the
// popover-rendered check must be FALSE there -- proving the assertion is about
// the rendered popover element and not merely about the trigger's
// popovertarget (which the pre-fix page carried too).
func TestTaskDetailReasonPopoverCheckBites(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(escalatedDetailTask("an escalated task", uuid.New(), store.LaneTesting))
	region := detailActionsOf(t, f.mustFragTask(t, task))

	triggers, popovers := splitAtReasonPopovers(t, region)
	popoverID := taskActionPopoverID(task.ID.String(), actionRequeue)
	assert.Contains(t, popovers, `id="`+popoverID+`" popover data-krill="reason-popover"`,
		"the popover element is rendered in the popover half")
	assert.Contains(t, triggers, `popovertarget="`+popoverID+`"`,
		"the trigger half still names the popover, as the pre-fix page did")
	assert.NotContains(t, triggers, `id="`+popoverID+`" popover data-krill="reason-popover"`,
		"but the pre-fix markup -- the triggers with no popover -- must fail the rendered check, or the check is vacuous")
}

// TestTaskDetailRendersNoReasonPopoverWhereNoVerbIsLegal is the other half:
// the popovers are paired with the controls, not rendered unconditionally. A
// state the predicate offers no verb for renders no callout -- and no orphan
// popovers either, which would be text inputs the page has no action for.
func TestTaskDetailRendersNoReasonPopoverWhereNoVerbIsLegal(t *testing.T) {
	cancelledAt := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name string
		task store.Task
	}{
		{"cancelled", store.Task{Title: "a cancelled task", CurrentLane: store.LaneImplementation, CancelledAt: &cancelledAt}},
		{"done-lane ready", store.Task{Title: "a finished ready task", CurrentLane: store.LaneDone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDetailFixture(t)
			task := f.add(tc.task)
			_, frag := f.get(task.ID.String(), true)

			assert.NotContains(t, frag, `data-krill="task-actions"`)
			assert.NotContains(t, frag, `data-krill="reason-popover"`,
				"a state offering no verb renders no callout and no popover")
		})
	}
}
