// Markup-level coverage for the ops console's components, asserted on the
// specific markup each one emits rather than on a golden snapshot
// (htmxui ARCHITECTURE §14): the doubled-form halves, the destructive
// link, the self-terminating poll's presence/absence, and the empty
// state are the behavioural claims worth pinning.
package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// render renders a component to a string, failing the test on error.
// Every component here is templ-compiled and every value is this
// package's own, so an error is a programming mistake.
func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

// TestTaskActionsDoublesTheInlineForm requires a non-destructive verb to
// render one form carrying BOTH branches of the doubled-form rule: the
// no-JS half (method="post" + action=) and the htmx half (hx-post +
// hx-target + hx-swap). A form missing either half silently breaks one
// of the two browsers, and neither failure shows up in the other one's
// tests.
//
// The reason prompt is NOT in the row any more (FR 0cf360c5): it lives in
// the control's popover, which is rendered outside the table and submits the
// row's form through the form= attribute. This case asserts both halves, so
// a prompt that drifted away from its form would fail here.
func TestTaskActionsDoublesTheInlineForm(t *testing.T) {
	control := TaskActionControl{
		Kind:        "form",
		Label:       "Release",
		Action:      "/ops/tasks/t1/release",
		ReasonHint:  "why release the claim? (optional)",
		SubmitLabel: "Release",
		ReturnTo:    "/ops/claimed",
		FormID:      "krill-action-form-t1-release",
		PopoverID:   "krill-reason-popover-t1-release",
		ReasonID:    "krill-reason-popover-t1-release-reason",
	}
	got := render(t, TaskActions([]TaskActionControl{control}))

	assert.Contains(t, got, `<form method="post" action="/ops/tasks/t1/release" id="krill-action-form-t1-release"`)
	assert.Contains(t, got, `hx-post="/ops/tasks/t1/release"`, "the htmx half posts to the same route")
	assert.Contains(t, got, `hx-target="#ops-results"`, "the htmx half swaps the view's whole results block")
	assert.Contains(t, got, `hx-swap="outerHTML"`)
	assert.Contains(t, got, `name="return_to" value="/ops/claimed"`)
	assert.Contains(t, got, "Release")
	assert.NotContains(t, got, "<input type=\"text\"", "the row control renders no text input")

	popovers := render(t, TaskActionPopovers([]TaskActionControl{control}))
	assert.Contains(t, popovers, `id="krill-reason-popover-t1-release"`)
	assert.Contains(t, popovers, `popover`)
	assert.Contains(t, popovers, `id="krill-reason-popover-t1-release-reason"`)
	assert.Contains(t, popovers, `type="text" name="reason" form="krill-action-form-t1-release"`,
		"the popover's reason field submits the row's own form")
	assert.Contains(t, popovers, `placeholder="why release the claim? (optional)"`)
	assert.Contains(t, popovers, `type="submit" form="krill-action-form-t1-release"`,
		"the popover's submit button submits the row's own form")
}

// TestTaskActionsDoublesTheDestructiveControl requires the destructive verb's
// control to carry both halves of the doubled rule, and to carry them as two
// ROUTES rather than one: the htmx half confirms (hx-confirm) before it posts
// the verb in place, and the no-JS half is a GET to the verb's confirmation
// page. Nothing may post to the destructive route from a browser that never
// confirmed -- neither half may be method="post" to the verb -- and neither
// half may drop the observed-state ids the row rendered.
func TestTaskActionsDoublesTheDestructiveControl(t *testing.T) {
	got := render(t, TaskActions([]TaskActionControl{{
		Kind:            "confirm",
		Label:           "Cancel",
		Action:          "/ops/tasks/t1/cancel/confirm",
		PostAction:      "/ops/tasks/t1/cancel",
		Confirm:         "Cancel a task? It moves to Cancelled and cannot be claimed again.",
		SubmitLabel:     "Confirm cancel",
		ReturnTo:        "/ops/claimed",
		FormID:          "krill-action-form-t1-cancel",
		PopoverID:       "krill-reason-popover-t1-cancel",
		ReasonID:        "krill-reason-popover-t1-cancel-reason",
		ObservedClaimID: "bbbbbbbb-1111-2222-3333-444444444444",
	}}))

	// The no-JS half opens the confirmation page and carries the row's guard
	// there, so the page's own form can post it.
	assert.Contains(t, got, `<form method="get" action="/ops/tasks/t1/cancel/confirm" id="krill-action-form-t1-cancel"`)
	// The htmx half posts the verb, but only after the confirmation.
	assert.Contains(t, got, `hx-post="/ops/tasks/t1/cancel"`)
	assert.Contains(t, got, `hx-confirm="Cancel a task? It moves to Cancelled and cannot be claimed again."`)
	assert.Contains(t, got, `hx-target="#ops-results"`, "the htmx half swaps the view's whole results block")
	assert.Contains(t, got, `hx-swap="outerHTML"`)
	assert.Contains(t, got, `type="hidden" name="return_to" value="/ops/claimed"`)
	assert.Contains(t, got, `type="hidden" name="expected_claim_id" value="bbbbbbbb-1111-2222-3333-444444444444"`)
	assert.Contains(t, got, `type="button" popovertarget="krill-reason-popover-t1-cancel" class="btn btn-error btn-xs">Cancel</button>`,
		"the row's trigger opens the control's reason popover and posts nothing itself")
	assert.NotContains(t, got, `type="submit" popovertarget`,
		"the trigger is not a submit button: a submit button with a form owner submits it and returns before any popover invoker behaviour runs")
	assert.NotContains(t, got, "<input type=\"text\"", "the destructive row control renders no text input")
	assert.NotContains(t, got, `<form method="post" action="/ops/tasks/t1/cancel"`,
		"nothing posts the destructive verb from the row without the htmx confirm")
	assert.NotContains(t, got, `href="/ops/tasks/t1/cancel/confirm"`,
		"the no-JS half is the doubled form's own GET, not a second affordance")
}

// TestCancelConfirmCardIsASelfTargetingDoubledForm requires the confirm
// card to carry the id its own form swaps, alongside both halves of the
// doubled form, so a refused confirm re-renders the card in place.
//
// It also requires the card to carry the observed-state guard the acting row
// handed it: the card IS the no-JS half of that row's Cancel, so dropping the
// id here would post an unguarded cancel and lose the guard the row rendered.
func TestCancelConfirmCardIsASelfTargetingDoubledForm(t *testing.T) {
	got := render(t, CancelConfirmCard(CancelConfirmData{
		TaskID:          "t1",
		Action:          "/ops/tasks/t1/cancel",
		ReturnTo:        "/ops/claimed",
		ObservedID:      "dddddddd-1111-2222-3333-444444444444",
		ObservedField:   "expected_escalation_id",
		ObservedClaimID: "bbbbbbbb-1111-2222-3333-444444444444",
	}))

	assert.Contains(t, got, `id="cancel-confirm"`)
	assert.Contains(t, got, `hx-target="#cancel-confirm"`)
	assert.Contains(t, got, `<form method="post" action="/ops/tasks/t1/cancel"`)
	assert.Contains(t, got, `hx-post="/ops/tasks/t1/cancel"`)
	assert.Contains(t, got, `type="hidden" name="expected_claim_id" value="bbbbbbbb-1111-2222-3333-444444444444"`)
	assert.Contains(t, got, `type="hidden" name="expected_escalation_id" value="dddddddd-1111-2222-3333-444444444444"`)
	assert.Contains(t, got, `required`, "the reason is required on the irreversible path")
	assert.Contains(t, got, "Back to the console")
}

// rowSections returns every <tr>...</tr> run in markup, so a test can ask
// what a ROW contains rather than what the page does.
func rowSections(markup string) []string {
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

// tableSection is the markup from the first <table to its close, the region
// the FR's "no row contains a text input" is about.
func tableSection(t *testing.T, markup string) string {
	t.Helper()
	i := strings.Index(markup, "<table")
	require.GreaterOrEqual(t, i, 0, "the results region renders a table")
	j := strings.Index(markup[i:], "</table>")
	require.GreaterOrEqual(t, j, 0, "the table is closed")
	return markup[i : i+j+len("</table>")]
}

// TestRowActionsRenderNoTextInputAndHoistTheReasonPopover is FR 0cf360c5's
// shape at the component level: a row's action cell renders only buttons, no
// other row contains a text input, and the reason fields live in popovers
// rendered AFTER the table, each naming the row's form through form= so it
// still submits that row's action.
func TestRowActionsRenderNoTextInputAndHoistTheReasonPopover(t *testing.T) {
	control := TaskActionControl{
		Kind:        "form",
		Label:       "Release",
		Action:      "/ops/tasks/t1/release",
		ReasonHint:  "why release the claim? (optional)",
		SubmitLabel: "Release",
		ReturnTo:    "/ops/claimed",
		FormID:      "krill-action-form-t1-release",
		PopoverID:   "krill-reason-popover-t1-release",
		ReasonID:    "krill-reason-popover-t1-release-reason",
	}
	actions := TaskActions([]TaskActionControl{control})
	popovers := TaskActionPopovers([]TaskActionControl{control})

	claimed := render(t, claimedTable([]ClaimedRow{{TaskID: "t1", Title: "a task", ClaimID: "c1", Actions: actions, Popovers: popovers}}))
	escalated := render(t, escalatedQueueTable([]NeedsAttentionEscalatedRow{{TaskID: "t2", Title: "another", Actions: actions, Popovers: popovers}}))

	for name, markup := range map[string]string{"claimed": claimed, "escalated": escalated} {
		t.Run(name, func(t *testing.T) {
			table := tableSection(t, markup)
			assert.NotContains(t, table, `<input type="text"`, "no row contains a text input")
			assert.NotContains(t, table, `<textarea`, "no row contains a textarea either")
			for _, row := range rowSections(table) {
				assert.NotContains(t, row, `type="text"`,
					"a row carries only its hidden guard/return_to fields, never a text field")
			}

			// The popover is the table's SIBLING, after its close -- never in it.
			pop := strings.Index(markup, `data-krill="reason-popover"`)
			require.GreaterOrEqual(t, pop, 0, "the row's action renders its reason popover")
			assert.Greater(t, pop, strings.Index(markup, "</table>"),
				"the popover renders after the table, never inside a row")
		})
	}
}

// TestReasonPopoverOpensFromTheRowControlAndSubmitsItsForm pins the
// open/close behaviour of the scaffold: the row's button is the popover's
// invoker (popovertarget), the popover carries the native popover attribute,
// a close control hides it, and the reason field plus the submit button both
// name the row's form so the action carries the reason the operator typed.
func TestReasonPopoverOpensFromTheRowControlAndSubmitsItsForm(t *testing.T) {
	control := TaskActionControl{
		Kind:        "confirm",
		Label:       "Cancel",
		Action:      "/ops/tasks/t1/cancel/confirm",
		PostAction:  "/ops/tasks/t1/cancel",
		Confirm:     "Cancel a task? It moves to Cancelled and cannot be claimed again.",
		ReasonHint:  "why dead-letter it? (optional)",
		SubmitLabel: "Confirm cancel",
		ReturnTo:    "/ops/claimed",
		FormID:      "krill-action-form-t1-cancel",
		PopoverID:   "krill-reason-popover-t1-cancel",
		ReasonID:    "krill-reason-popover-t1-cancel-reason",
	}

	row := render(t, TaskActions([]TaskActionControl{control}))
	assert.Contains(t, row, `popovertarget="krill-reason-popover-t1-cancel"`,
		"the row's own button opens the control's popover")

	popover := render(t, TaskActionPopovers([]TaskActionControl{control}))
	assert.Contains(t, popover, `id="krill-reason-popover-t1-cancel"`)
	assert.Contains(t, popover, ` popover`, "the popover uses the native popover attribute")
	assert.Contains(t, popover, `popovertarget="krill-reason-popover-t1-cancel" popovertargetaction="hide"`,
		"the popover has a close control")
	assert.Contains(t, popover, `type="text" name="reason" form="krill-action-form-t1-cancel"`,
		"the reason field submits the row's form")
	assert.Contains(t, popover, `type="submit" form="krill-action-form-t1-cancel"`,
		"and so does the popover's submit button")
	assert.Contains(t, popover, `placeholder="why dead-letter it? (optional)"`)
	assert.Contains(t, popover, ">Confirm cancel</button>")
}

// TestCancelConfirmCardShowsARefusalInline requires a refusal to render
// as an alert inside the card, not to vanish with the card.
func TestCancelConfirmCardShowsARefusalInline(t *testing.T) {
	plain := render(t, CancelConfirmCard(CancelConfirmData{TaskID: "t1", Action: "/ops/tasks/t1/cancel", ReturnTo: "/ops/claimed"}))
	assert.NotContains(t, plain, "alert", "a card with no refusal carries no alert")

	refused := render(t, CancelConfirmCard(CancelConfirmData{
		TaskID:   "t1",
		Action:   "/ops/tasks/t1/cancel",
		ReturnTo: "/ops/claimed",
		Error:    "krill rejected the Cancel. task is already cancelled",
	}))
	assert.Contains(t, refused, "krill rejected the Cancel. task is already cancelled")
	assert.Contains(t, refused, "alert-error")
}

// TestClaimedPollAttrsAppearOnlyWhileTransient is the self-terminating
// poll's whole contract: attributes while a lease is near expiry, none
// once it is settled -- so the loop stops by itself with no client-side
// timer bookkeeping, because the poll's response IS this same fragment.
func TestClaimedPollAttrsAppearOnlyWhileTransient(t *testing.T) {
	polling := render(t, ClaimedResults(ClaimedData{
		Rows:    []ClaimedRow{{TaskID: "t1", Title: "a claim"}},
		Href:    "/ops/claimed",
		Polling: true,
	}))
	assert.Contains(t, polling, `hx-get="/ops/claimed"`, "the poll targets the view's own route")
	assert.Contains(t, polling, `hx-trigger="every 3s"`)
	assert.Contains(t, polling, `hx-target="this"`)
	assert.Contains(t, polling, `hx-swap="outerHTML"`)

	settled := render(t, ClaimedResults(ClaimedData{
		Rows: []ClaimedRow{{TaskID: "t1", Title: "a claim"}},
		Href: "/ops/claimed",
	}))
	assert.NotContains(t, settled, "hx-trigger")
	assert.NotContains(t, settled, "hx-", "a settled fragment has nothing left to refresh on a timer")
}

// TestSettledResultsCarryNoPollAttributes guards the same rule for the
// other three views, which have no transient state to watch at all.
func TestSettledResultsCarryNoPollAttributes(t *testing.T) {
	for name, c := range map[string]templ.Component{
		"escalated": EscalatedResults(EscalatedData{Href: "/ops/escalated"}),
		"cancelled": CancelledResults(CancelledData{Href: "/ops/cancelled"}),
		"notes":     NotesResults(NotesData{Href: "/ops/notes"}),
	} {
		t.Run(name, func(t *testing.T) {
			got := render(t, c)
			assert.Contains(t, got, `id="ops-results"`)
			assert.NotContains(t, got, "hx-trigger")
		})
	}
}

// TestResultsBlockIsByteStableAcrossRenders requires one unchanged view
// to render the same bytes every time. The claimed block is polled, and a
// fragment that shifted for no state change would make every poll a
// spurious change.
func TestResultsBlockIsByteStableAcrossRenders(t *testing.T) {
	data := ClaimedData{
		Rows: []ClaimedRow{{
			TaskID:         "t1",
			Title:          "a settled claim",
			Milestone:      "M5",
			ClaimedSince:   "2026-01-01T00:00:00Z",
			LeaseExpiresAt: "2026-01-02T03:04:05Z",
			Claimant:       "by https://kc alice (human) for https://svc swarm",
		}},
		Href: "/ops/claimed",
	}
	assert.Equal(t, render(t, ClaimedResults(data)), render(t, ClaimedResults(data)))
}

// TestClaimedTableRendersClaimedSince requires the claimed view to show
// when the claim was taken, not only when its lease runs out: an
// operator watching a stalled claim needs to know how long it has been
// held.
func TestClaimedTableRendersClaimedSince(t *testing.T) {
	got := render(t, ClaimedResults(ClaimedData{
		Rows: []ClaimedRow{{
			TaskID:         "t1",
			ClaimedSince:   "2026-01-01T00:00:00Z",
			LeaseExpiresAt: "2026-01-02T03:04:05Z",
		}},
		Href: "/ops/claimed",
	}))
	assert.Contains(t, got, "Claimed since")
	assert.Contains(t, got, "2026-01-01T00:00:00Z")
	assert.Contains(t, got, "2026-01-02T03:04:05Z", "the lease expiry still renders alongside it")
}

// TestResultsBlockCarriesInlineRefusalAndNextLink covers the two things
// the shared furniture adds around a view's rows.
func TestResultsBlockCarriesInlineRefusalAndNextLink(t *testing.T) {
	got := render(t, ClaimedResults(ClaimedData{
		Rows:     []ClaimedRow{{TaskID: "t1", Title: "a claim"}},
		NextHref: "/ops/claimed?page_size=2&page_token=tok",
		Error:    "krill rejected the Release. claim is already gone",
		Href:     "/ops/claimed",
	}))
	assert.Contains(t, got, "krill rejected the Release. claim is already gone")
	assert.Contains(t, got, `role="alert"`)
	assert.Contains(t, got, "Next page →")
	assert.Contains(t, got, "page_token=tok")
}

// TestEmptyRowsRenderTheSharedEmptyState requires a view with nothing to
// show to say so with the shared primitive rather than an empty table.
func TestEmptyRowsRenderTheSharedEmptyState(t *testing.T) {
	for name, c := range map[string]templ.Component{
		"claimed":   ClaimedResults(ClaimedData{Href: "/ops/claimed"}),
		"escalated": EscalatedResults(EscalatedData{Href: "/ops/escalated"}),
		"cancelled": CancelledResults(CancelledData{Href: "/ops/cancelled"}),
		"notes":     NotesResults(NotesData{Href: "/ops/notes"}),
	} {
		t.Run(name, func(t *testing.T) {
			assert.NotContains(t, render(t, c), "<table", "an empty view renders no empty table")
		})
	}
}

// TestPageWrapsResultsWithARefreshButton requires each view's page to
// offer a manual refresh pointing at the view's own route. The button
// lives outside the results block, so a swap never takes it away.
func TestPageWrapsResultsWithARefreshButton(t *testing.T) {
	for name, tc := range map[string]struct {
		page templ.Component
		href string
	}{
		"claimed":   {ClaimedPage(ClaimedData{Href: "/ops/claimed"}), "/ops/claimed"},
		"escalated": {EscalatedPage(EscalatedData{Href: "/ops/escalated"}), "/ops/escalated"},
		"cancelled": {CancelledPage(CancelledData{Href: "/ops/cancelled"}), "/ops/cancelled"},
		"notes":     {NotesPage(NotesData{Href: "/ops/notes"}), "/ops/notes"},
	} {
		t.Run(name, func(t *testing.T) {
			got := render(t, tc.page)
			assert.Contains(t, got, ">Refresh<")
			assert.Contains(t, got, `hx-get="`+tc.href+`"`)
			assert.Contains(t, got, `hx-target="#ops-results"`)
		})
	}
}

// TestInterventionErrorRendersTheApiMessageNotJSON requires the no-JS
// rejection page to present api's {"error"} message as readable text,
// with a way back to the console, and never to leak the raw body.
func TestInterventionErrorRendersTheApiMessageNotJSON(t *testing.T) {
	got := render(t, InterventionError(InterventionErrorData{
		Heading:  "krill rejected the Cancel.",
		Detail:   "task is already cancelled",
		ReturnTo: "/ops/claimed",
	}))
	assert.Contains(t, got, "krill rejected the Cancel.")
	assert.Contains(t, got, "task is already cancelled")
	assert.Contains(t, got, "Back to the console")
	assert.Contains(t, got, `href="/ops/claimed"`)
	assert.NotContains(t, got, `{"error"`)
}

// TestRowControlsCarryNoIdentity requires a row's intervention controls to
// carry nothing the browser could have forged into a write: the reason and
// the return_to view, and nothing else. Scope and both subjects are
// resolved server-side from the gated krill session.
func TestRowControlsCarryNoIdentity(t *testing.T) {
	got := render(t, TaskActions([]TaskActionControl{{
		Kind:       "form",
		Label:      "Release",
		Action:     "/ops/tasks/t1/release",
		ReasonHint: "why release the claim? (optional)",
		ReturnTo:   "/ops/claimed",
	}}))

	assert.NotContains(t, got, "subject=")
	assert.NotContains(t, got, "iss=")
	assert.NotContains(t, got, "scope=")
}
