// Testing-lane coverage for FR 0cf360c5: an optional intervention reason is
// entered in a small popover on the action, never in a text input in every row.
//
// FR 0cf360c5, read live: "Given the table, then no row contains a text input;
// activating Requeue, Release, Escalate or Cancel offers an optional reason in a
// small popover on that action, and a reason entered there is forwarded as the
// intervention's reason (...) and appears in the task's history; submitting
// with no reason is allowed."
//
// The four things this lane owes, each driven through the tab's OWN loader and
// the production routes rather than a URL a test spelled:
//
//  1. every row control's trigger is a popover BUTTON, never a submit button --
//     the defect the Implementation lane found and fixed, since a submit button
//     with a form owner submits and returns before any popover invoker
//     behaviour runs;
//  2. the popover's reason field and its submit button both name the row's own
//     form through the HTML form= attribute, so the reason rides the same
//     guarded write the action already carried;
//  3. that reason reaches the api body for all four verbs, and its absence is a
//     null exactly as the MCP tool's absent optional reason is; and
//  4. Cancel carries the reason on BOTH its paths -- the htmx hx-post half, and
//     the no-JS half that hands it to the confirm page, whose own required
//     reason field is PRE-FILLED from it rather than silently dropped.
//
// The asymmetry in (4) is deliberate and asserted: the popover's reason is
// optional (FR 0cf360c5 is scoped "Given the table"), while the no-JS confirm
// page's own field stays required -- a console-side guard on the irreversible
// path (dependency 1946643c) that this task's body says to keep working, not to
// relax.
package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// ---------------------------------------------------------------------------
// targeting one control's form and its popover
// ---------------------------------------------------------------------------

// elementContaining returns the markup of the single element containing needle:
// from the '<' that opens it to the first '>' after needle. The console's
// elements carry no nested '>' inside an open tag, so this bounds the tag the
// attribute belongs to rather than a whole region.
func elementContaining(t *testing.T, markup, needle string) string {
	t.Helper()
	i := strings.Index(markup, needle)
	require.GreaterOrEqual(t, i, 0, "no element contains %q", needle)
	start := strings.LastIndex(markup[:i], "<")
	require.GreaterOrEqual(t, start, 0, "the element containing %q is not opened by a '<'", needle)
	end := strings.Index(markup[i:], ">")
	require.GreaterOrEqual(t, end, 0, "the element containing %q is not closed", needle)
	return markup[start : i+end+1]
}

// rowFormForVerb is a tab region's row control for one verb. Cancel's row form
// is its GET half (the confirm page), so its hx-post is the route it posts; the
// other three post their own route directly.
func rowFormForVerb(t *testing.T, region, verb string) string {
	t.Helper()
	if verb == actionCancel {
		return cancelControl(t, region)
	}
	return formContaining(t, region, "/"+verb+`"`)
}

// cloneValues is a url.Values a test can mutate without touching the form it
// was read from.
func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// bodyOf decodes a recorded write's JSON body.
func bodyOf(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// reasonCase is one tab/verb pair an operator can actually act from, with the
// rows the fixture needs. Cancel is legal from both tabs, so it appears twice.
type reasonCase struct {
	name      string
	tab       string
	verb      string
	claimed   store.ClaimedTaskRow
	escalated store.EscalatedTaskRow
}

// reasonCases is the tab/verb matrix: the Claimed tab offers Release, Escalate
// and Cancel; the Escalated tab offers Requeue and Cancel.
func reasonCases() []reasonCase {
	claimed, escalated := tabClaimedRow(), tabEscalatedRow()
	return []reasonCase{
		{"claimed Release", needsAttentionTabClaimed, actionRelease, claimed, escalated},
		{"claimed Escalate", needsAttentionTabClaimed, actionEscalate, claimed, escalated},
		{"claimed Cancel", needsAttentionTabClaimed, actionCancel, claimed, escalated},
		{"escalated Requeue", needsAttentionTabEscalated, actionRequeue, claimed, escalated},
		{"escalated Cancel", needsAttentionTabEscalated, actionCancel, claimed, escalated},
	}
}

// fixtureFor wires one reason case's rows and returns the fixture plus the task
// id the acting row carries.
func fixtureFor(t *testing.T, tc reasonCase) (*tabInterventionFixture, uuid.UUID) {
	t.Helper()
	f := newTabInterventionFixture(t)
	f.tasks.claimed = []store.ClaimedTaskRow{tc.claimed}
	f.tasks.escalated = []store.EscalatedTaskRow{tc.escalated}
	if tc.tab == needsAttentionTabEscalated {
		return f, tc.escalated.TaskID
	}
	return f, tc.claimed.TaskID
}

// ---------------------------------------------------------------------------
// 1. the trigger opens the popover; it is never a submit button
// ---------------------------------------------------------------------------

// TestEachVerbsTriggerIsAPopoverButtonNeverASubmit is the Implementation lane's
// found defect, pinned for all four verbs. Per the WHATWG button activation
// behaviour a submit button with a form owner submits the form and returns
// before any popover invoker behaviour runs, so a type="submit" trigger would
// POST the verb at once and never open the popover -- defeating the FR's
// central sentence. The scaffold shipped exactly that, and its own test comment
// wrongly read "posts nothing itself" while asserting type="submit".
func TestEachVerbsTriggerIsAPopoverButtonNeverASubmit(t *testing.T) {
	for _, verb := range []string{actionRelease, actionRequeue, actionEscalate, actionCancel} {
		t.Run(verb, func(t *testing.T) {
			taskID := uuid.NewString()
			row := mustRenderComponent(renderTaskActions(taskID, taskTitle, "/ops/claimed", verb))
			popovers := mustRenderComponent(renderTaskActionPopovers(taskID, taskTitle, "/ops/claimed", verb))
			popoverID := taskActionPopoverID(taskID, verb)

			form := formContaining(t, row, `action="`)
			trigger := elementContaining(t, form, `popovertarget="`)

			assert.Contains(t, trigger, `type="button"`,
				"the trigger is a plain button: a submit button with a form owner never opens its popover")
			assert.NotContains(t, trigger, `type="submit"`,
				"a submit trigger would post the verb and return before the popover opened")
			assert.Contains(t, trigger, `popovertarget="`+popoverID+`"`,
				"the trigger opens this control's own popover")
			assert.Contains(t, popovers, `id="`+popoverID+`"`,
				"the popover the trigger opens is actually rendered")
			assert.NotContains(t, row, `type="submit" popovertarget`,
				"no row control is a submit button that also opens a popover")

			// The same extraction applied to the scaffold's own trigger -- the
			// markup this task had to fix -- yields a submit button, so the
			// assertion above discriminates rather than passing vacuously.
			scaffoldRow := `<form method="post" action="/x"><button type="submit" popovertarget="` +
				popoverID + `" class="btn">` + actionLabel(verb) + `</button></form>`
			assert.Contains(t, elementContaining(t, scaffoldRow, `popovertarget="`), `type="submit"`,
				"the scaffold's trigger WAS a submit button, and this extraction sees it")
		})
	}
}

// ---------------------------------------------------------------------------
// 2. the reason rides the row's form; a blank reason is a legitimate submit
// ---------------------------------------------------------------------------

// TestEachVerbForwardsTheReasonEnteredInItsPopover covers the FR's forwarding
// clause for all four verbs (Cancel from both tabs): each row's popover carries
// a reason field and a submit button associated with that row's own form, and
// submitting with a reason reaches the api's intervention body with it.
func TestEachVerbForwardsTheReasonEnteredInItsPopover(t *testing.T) {
	const reason = "the operator's own rationale"

	for _, tc := range reasonCases() {
		t.Run(tc.name, func(t *testing.T) {
			f, taskID := fixtureFor(t, tc)

			region := f.region(t, tc.tab)
			form := rowFormForVerb(t, region, tc.verb)
			formID := formAttr(t, form, "id")

			// The popover's reason field submits THIS row's form, and is a text
			// field living outside the table.
			reasonInput := elementContaining(t, region, `name="reason" form="`+formID+`"`)
			assert.Contains(t, reasonInput, `type="text"`)
			// ...and the popover's own submit button submits the same form.
			assert.Contains(t, region, `type="submit" form="`+formID+`"`,
				"the popover's submit button is the control's real submit")

			post := cloneValues(hiddenInputs(form))
			post.Set("reason", reason)
			rec := hxFormPost(f.mux, formAttr(t, form, "hx-post"), post, f.session)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			write := f.api.writeRequest(t)
			assert.Equal(t, "/tasks/"+taskID.String()+"/"+tc.verb, write.Path)
			assert.Equal(t, reason, bodyOf(t, write.Body)["reason"],
				"the reason typed in the popover is forwarded as the intervention's reason")
		})
	}
}

// TestEachVerbAllowsSubmittingWithNoReason is the FR's "submitting with no
// reason is allowed" at every verb: a row control submitted without a reason
// still posts, and the api body carries a null exactly as the MCP tool's absent
// optional reason does.
func TestEachVerbAllowsSubmittingWithNoReason(t *testing.T) {
	for _, tc := range reasonCases() {
		t.Run(tc.name, func(t *testing.T) {
			f, taskID := fixtureFor(t, tc)

			region := f.region(t, tc.tab)
			form := rowFormForVerb(t, region, tc.verb)
			formID := formAttr(t, form, "id")
			assert.NotContains(t, elementContaining(t, region, `name="reason" form="`+formID+`"`), "required",
				"the popover's reason is optional, so a blank submission is allowed")

			// No reason field at all -- the operator just confirms.
			rec := hxFormPost(f.mux, formAttr(t, form, "hx-post"), hiddenInputs(form), f.session)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			write := f.api.writeRequest(t)
			assert.Equal(t, "/tasks/"+taskID.String()+"/"+tc.verb, write.Path)
			assert.Contains(t, string(write.Body), `"reason":null`,
				"an omitted reason is forwarded as a null, exactly as the MCP tool's absent optional reason")
			assert.Nil(t, bodyOf(t, write.Body)["reason"])
		})
	}
}

// ---------------------------------------------------------------------------
// 3. Cancel's no-JS path: the confirm page is handed the reason and pre-fills it
// ---------------------------------------------------------------------------

// TestNoJSCancelConfirmPagePrefillsThePopoverReason walks Cancel's other path.
// The row's popover reason rides the no-JS GET onto the confirm page as a query
// parameter (the popover's field names the row's GET form, so a browser
// serialises it there); the returned card PRE-FILLS its own field with it, so
// the operator's rationale is not silently dropped; and the card's own POST
// still carries it to the api, guarded by the id the row observed.
func TestNoJSCancelConfirmPagePrefillsThePopoverReason(t *testing.T) {
	const reason = "dead-lettered after the third failed attempt"

	for _, tc := range []struct {
		name    string
		tab     string
		field   string
		wantRow func(*cancelRowFixture) (taskID, observed string)
	}{
		{
			name: "a Claimed row",
			tab:  needsAttentionTabClaimed,
			wantRow: func(f *cancelRowFixture) (string, string) {
				r := f.claimedRow(store.LaneImplementation)
				f.tasks.claimed = []store.ClaimedTaskRow{r}
				return r.TaskID.String(), r.ClaimID.String()
			},
			field: expectedClaimIDParam,
		},
		{
			name: "an Escalated row",
			tab:  needsAttentionTabEscalated,
			wantRow: func(f *cancelRowFixture) (string, string) {
				r := f.escalatedRow(store.LaneImplementation)
				f.tasks.escalated = []store.EscalatedTaskRow{r}
				return r.TaskID.String(), r.EscalationID.String()
			},
			field: escalatedGuardField,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCancelRowFixture(t)
			taskID, observed := tc.wantRow(f)

			region := f.region(t, tc.tab)
			control := rowFormForVerb(t, region, actionCancel)

			// The browser's no-JS GET: the row form's own hidden fields, plus
			// the reason the popover's field contributes through form=.
			get := cloneValues(hiddenInputs(control))
			get.Set("reason", reason)
			getURL := formAttr(t, control, "action") + "?" + get.Encode()
			getRec := serveWithCookie(f.mux, http.MethodGet, getURL, "", f.session)
			require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())
			page := getRec.Body.String()

			textarea := elementContaining(t, page, reason)
			require.Contains(t, textarea, "<textarea",
				"the card's own reason field is the element the reason pre-fills")
			open := strings.Index(textarea, ">") + 1
			closeAt := strings.LastIndex(textarea, "</textarea>")
			require.Greater(t, closeAt, open, "the reason textarea is properly closed")
			assert.Equal(t, reason, textarea[open:closeAt],
				"the no-JS confirm page pre-fills its reason field from the row's popover")
			// It stays the required, editable guard it always was.
			assert.Contains(t, textarea, "required",
				"the confirm page's own reason stays required: FR 0cf360c5 is scoped to the row's popover")
			assert.NotContains(t, textarea, "disabled")
			assert.NotContains(t, textarea, "readonly")

			// Submitting the card -- the browser re-sends the pre-filled value --
			// reaches the api with the reason and the row's observed guard.
			cardForm := cancelCardForm(t, page, taskID)
			post := cloneValues(hiddenInputs(cardForm))
			post.Set("reason", reason)
			postRec := serveFormPost(f.mux, formAttr(t, cardForm, "action"), post, f.session)
			require.Equal(t, http.StatusSeeOther, postRec.Code, postRec.Body.String())

			write := f.api.writeRequest(t)
			assert.Equal(t, "/tasks/"+taskID+"/cancel", write.Path)
			sent := bodyOf(t, write.Body)
			assert.Equal(t, reason, sent["reason"], "the no-JS half carries the reason to the api")
			assert.Equal(t, observed, sent[tc.field], "and the guard the row observed")
		})
	}
}

// ---------------------------------------------------------------------------
// 4. the deliberate asymmetry: popover optional, confirm page required
// ---------------------------------------------------------------------------

// TestPopoverReasonIsOptionalWhileTheConfirmPageStaysRequired pins the reading
// the Implementation lane judged, so a later change cannot silently flip it in
// either direction. FR 0cf360c5 opens "Given the table", so its "submitting with
// no reason is allowed" governs the row action's popover; the no-JS confirm
// PAGE is a separate affordance (FR 336335f1's fallback, dependency 1946643c)
// whose required field is a console-side guard on the irreversible path, and
// this task's body says to keep it working.
func TestPopoverReasonIsOptionalWhileTheConfirmPageStaysRequired(t *testing.T) {
	taskID := uuid.NewString()
	popovers := mustRenderComponent(renderTaskActionPopovers(taskID, taskTitle, "/ops/claimed",
		actionRelease, actionRequeue, actionEscalate, actionCancel))
	for _, verb := range []string{actionRelease, actionRequeue, actionEscalate, actionCancel} {
		input := elementContaining(t, popovers, `name="reason" form="`+taskActionFormID(taskID, verb)+`"`)
		assert.NotContains(t, input, "required",
			"the %s popover's reason is optional", verb)
	}

	card := mustRenderComponent(pages.CancelConfirmCard(pages.CancelConfirmData{
		TaskID:   taskID,
		Action:   opsTaskActionBase + taskID + "/" + actionCancel,
		ReturnTo: "/ops/claimed",
	}))
	textarea := elementContaining(t, card, "<textarea")
	assert.Contains(t, textarea, "required",
		"the confirm page's reason stays required, the deliberate no-JS guard")
}
