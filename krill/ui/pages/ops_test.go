// Markup-level coverage for the ops console's components, asserted on the
// specific markup each one emits rather than on a golden snapshot
// (htmxui ARCHITECTURE §14): the doubled-form halves, the destructive
// link, the self-terminating poll's presence/absence, and the empty
// state are the behavioural claims worth pinning.
package pages

import (
	"bytes"
	"context"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
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
func TestTaskActionsDoublesTheInlineForm(t *testing.T) {
	got := render(t, TaskActions([]TaskActionControl{{
		Kind:       "form",
		Label:      "Release",
		Action:     "/ops/tasks/t1/release",
		ReasonHint: "why release the claim? (optional)",
		ReturnTo:   "/ops/claimed",
	}}))

	assert.Contains(t, got, `<form method="post" action="/ops/tasks/t1/release"`)
	assert.Contains(t, got, `hx-post="/ops/tasks/t1/release"`, "the htmx half posts to the same route")
	assert.Contains(t, got, `hx-target="#ops-results"`, "the htmx half swaps the view's whole results block")
	assert.Contains(t, got, `hx-swap="outerHTML"`)
	assert.Contains(t, got, `name="return_to" value="/ops/claimed"`)
	assert.Contains(t, got, `placeholder="why release the claim? (optional)"`)
	assert.Contains(t, got, "Release")
}

// TestTaskActionsRendersDestructiveVerbAsALinkNotAForm requires the
// destructive verb to stay a link: the confirmation step IS the affordance,
// so nothing may post to the cancel route from a console row.
func TestTaskActionsRendersDestructiveVerbAsALinkNotAForm(t *testing.T) {
	got := render(t, TaskActions([]TaskActionControl{{
		Kind:     "confirm",
		Label:    "Cancel",
		Action:   "/ops/tasks/t1/cancel/confirm?return_to=%2Fops%2Fclaimed",
		ReturnTo: "/ops/claimed",
	}}))

	assert.Contains(t, got, `<a href="/ops/tasks/t1/cancel/confirm?return_to=`)
	assert.NotContains(t, got, "<form", "the destructive verb posts nothing from the row")
}

// TestCancelConfirmCardIsASelfTargetingDoubledForm requires the confirm
// card to carry the id its own form swaps, alongside both halves of the
// doubled form, so a refused confirm re-renders the card in place.
func TestCancelConfirmCardIsASelfTargetingDoubledForm(t *testing.T) {
	got := render(t, CancelConfirmCard(CancelConfirmData{
		TaskID:   "t1",
		Action:   "/ops/tasks/t1/cancel",
		ReturnTo: "/ops/claimed",
	}))

	assert.Contains(t, got, `id="cancel-confirm"`)
	assert.Contains(t, got, `hx-target="#cancel-confirm"`)
	assert.Contains(t, got, `<form method="post" action="/ops/tasks/t1/cancel"`)
	assert.Contains(t, got, `hx-post="/ops/tasks/t1/cancel"`)
	assert.Contains(t, got, `required`, "the reason is required on the irreversible path")
	assert.Contains(t, got, "Back to the console")
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
			TaskID:   "t1",
			Title:    "a settled claim",
			Lease:    "2026-01-02T03:04:05Z",
			Claimant: "https://kc alice (human)",
		}},
		Href: "/ops/claimed",
	}
	assert.Equal(t, render(t, ClaimedResults(data)), render(t, ClaimedResults(data)))
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
