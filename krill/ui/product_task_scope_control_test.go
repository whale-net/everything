// Wire-driven coverage for the shared Tasks/Board scope control
// (FR 7191dba1): the three modes and which one is marked, the selects each
// mode reveals, the URL-carried state that makes a shared link show the
// same scope, and the rule that a container is only ever chosen from a
// select rather than typed.
//
// Every case mounts the real registrations through productTaskMux rather
// than calling a handler directly, because "which routes serve this" and
// "what the shell renders around it" are part of what is being pinned.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// scopeControlOf fetches the Tasks page and returns its body, so a case
// can read what the control actually rendered rather than what it meant to.
func scopeControlOf(t *testing.T, listingQuery string) string {
	t.Helper()
	mux := productTaskMux(t, &recordingProductTasks{total: 3}, productTaskListing(), nil)
	rec := fetch(t, mux, productTaskTasksURL(listingQuery))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return rec.Body.String()
}

// optionIndex is where an option carrying this container id appears in the
// body, or -1. A select's options are the only place a container id should
// appear now that the control is real, so this doubles as "the id is
// offered as a choice".
func optionIndex(body, id string) int {
	return strings.Index(body, `<option value="`+id+`"`)
}

// selectedOption is the value of the option a browser would show chosen in
// a select, read back the way a browser reads it: the selected attribute,
// or the first option when none carries one.
func selectedOption(t *testing.T, body, selectMarker string) string {
	t.Helper()
	at := strings.Index(body, selectMarker)
	require.NotEqual(t, -1, at, "no %s in the rendered control: %s", selectMarker, body)
	rest := body[at:]
	end := strings.Index(rest, "</select>")
	require.NotEqual(t, -1, end, "the select never closes: %s", body)
	options := rest[:end]

	const open = `<option value="`
	start := 0
	if sel := strings.Index(options, " selected"); sel != -1 {
		start = strings.LastIndex(options[:sel], open)
		require.NotEqual(t, -1, start, "a selected option with no value: %s", options)
	}
	if start == 0 {
		start = strings.Index(options, open)
		require.NotEqual(t, -1, start, "the select has no options: %s", options)
	}
	close := strings.Index(options[start+len(open):], `"`)
	require.NotEqual(t, -1, close, "an option value never closes: %s", options)
	return options[start+len(open) : start+len(open)+close]
}

// optionTextOf is the rendered text of the option whose <option value="..."
// starts at index at -- what the operator actually reads in the select.
func optionTextOf(body string, at int) string {
	open := strings.Index(body[at:], ">") + 1
	close := strings.Index(body[at:], "</option>")
	return body[at+open : at+close]
}

// TestScopeControlDefaultsToAllIncomplete: a URL with no query is the
// product-wide all-incomplete scope, that mode is the marked one, and the
// control reveals no container select -- the mode names no container, so
// there is nothing to choose.
func TestScopeControlDefaultsToAllIncomplete(t *testing.T) {
	for _, query := range []string{"", "scope=incomplete"} {
		t.Run(query, func(t *testing.T) {
			body := scopeControlOf(t, query)

			assert.Equal(t, "incomplete", checkedRadioValue(t, body),
				"the default mode is the one marked as chosen")
			assert.Contains(t, body, `value="milestone"`, "all three modes are offered")
			assert.Contains(t, body, `value="milepebble"`)
			assert.NotContains(t, body, `data-krill="scope-milestone-select"`, "the product-wide mode names no container")
			assert.NotContains(t, body, `data-krill="scope-milepebble-select"`)
			assert.Contains(t, body, "All incomplete milestones")
		})
	}
}

// TestScopeControlMilestoneModeSelectsTheHighestPosition: ?scope=milestone
// with no id picks the product's highest-position milestone (FR 7191dba1:
// "the first milestone in the Milestones table order (highest position)"),
// and that is both what the read is scoped to and what the control shows
// as chosen -- so the operator sees the default rather than having to
// infer it from the rows.
func TestScopeControlMilestoneModeSelectsTheHighestPosition(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{total: 3}, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL("scope=milestone"))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()
	assert.Equal(t, "milestone", checkedRadioValue(t, body), "the milestone mode is the one marked as chosen")
	assert.Equal(t, productTaskNewestMilestone.String(),
		selectedOption(t, body, `data-krill="scope-milestone-select"`),
		"the no-id default is the one the control shows selected")
	// The milestone select submits the same container parameter the store
	// read was scoped with, so submitting it back is a no-op round trip.
	assert.Contains(t, body, `name="container_id" class="select select-sm" data-krill="scope-milestone-select"`)
	assert.NotContains(t, body, `data-krill="scope-milepebble-select"`,
		"milestone mode names one container, not a pair")
}

// TestScopeControlMilepebbleModeRevealsOnlyThatMilestonesMilepebbles is
// FR 7191dba1's second reveal: "?scope=milepebble&milestone=<id> reveals
// only that milestone's milepebbles".
//
// The milestone parameter matters because a milepebble id alone cannot say
// which options the second select should offer. Without it the control
// would render an empty list and mark nothing chosen, which is the bug
// this case exists to keep fixed.
func TestScopeControlMilepebbleModeRevealsOnlyThatMilestonesMilepebbles(t *testing.T) {
	for _, tc := range []struct {
		name           string
		query          string
		wantMilestone  string
		wantMilepebble string
	}{
		{
			name:           "a named milestone with no milepebble chosen takes its first",
			query:          "scope=milepebble&milestone=" + productTaskMilestone.String(),
			wantMilestone:  productTaskMilestone.String(),
			wantMilepebble: productTaskMilepebble.String(),
		},
		{
			// Deliberately NOT the highest-position milepebble: deriving the
			// parent has to follow the milepebble the URL named, so this case
			// fails if the parent falls back to the no-id default instead --
			// which is exactly the bug this mode had when the resolved scope
			// carried no parent at all.
			name:           "a named milepebble with no milestone named derives its parent",
			query:          "scope=milepebble&container_id=" + productTaskMilepebble.String(),
			wantMilestone:  productTaskMilestone.String(),
			wantMilepebble: productTaskMilepebble.String(),
		},
		{
			// Switching from Milestone mode submits the milestone the
			// select held as container_id.
			name:           "a milestone submitted as the container becomes the parent",
			query:          "scope=milepebble&container_id=" + productTaskMilestone.String(),
			wantMilestone:  productTaskMilestone.String(),
			wantMilepebble: productTaskMilepebble.String(),
		},
		{
			name:           "neither named falls back to the highest-position milestone with one",
			query:          "scope=milepebble",
			wantMilestone:  productTaskNewestMilestone.String(),
			wantMilepebble: productTaskNewestMilepebble.String(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := scopeControlOf(t, tc.query)

			assert.Equal(t, "milepebble", checkedRadioValue(t, body), "the milepebble mode is the one marked as chosen")
			assert.Equal(t, tc.wantMilestone,
				selectedOption(t, body, `data-krill="scope-milestone-select"`),
				"the milestone select marks the milestone the options come from")
			assert.Equal(t, tc.wantMilepebble,
				selectedOption(t, body, `data-krill="scope-milepebble-select"`))

			// Only the named milestone's own children are on offer.
			options := body[strings.Index(body, `data-krill="scope-milepebble-select"`):]
			options = options[:strings.Index(options, "</select>")]
			if tc.wantMilestone == productTaskMilestone.String() {
				assert.Contains(t, options, productTaskMilepebble.String())
				assert.NotContains(t, options, productTaskNewestMilepebble.String(),
					"another milestone's milepebble is not on offer")
			} else {
				assert.Contains(t, options, productTaskNewestMilepebble.String())
				assert.NotContains(t, options, productTaskMilepebble.String())
			}

			// The two selects submit different parameters, because only the
			// milepebble is the read's container.
			assert.Contains(t, body, `name="milestone" class="select select-sm" data-krill="scope-milestone-select"`)
			assert.Contains(t, body, `name="container_id" class="select select-sm" data-krill="scope-milepebble-select"`)
		})
	}
}

// TestScopeControlChangingTheMilestoneSelectLandsOnTheNewMilestonesMilepebbles
// is the interaction the `milestone` parameter exists for: the operator
// changes the Milestone select, and the second select's options change to
// that milestone's own.
//
// A plain GET form submits BOTH selects at once, and a select cannot be
// emptied by choosing something else in it -- so the browser necessarily
// sends the OLD milepebble alongside the NEW milestone. That disagreeing
// pair is what the operator's own click produces, so it must resolve: the
// named milestone is the one the operator just chose, and its first
// milepebble is the answer (the same no-id rule the mode applies one level
// down). Refusing it instead 404s the one interaction that can only ever
// be performed from this page, and htmx -- which does not swap on a 4xx --
// leaves the operator clicking a control that appears to do nothing.
func TestScopeControlChangingTheMilestoneSelectLandsOnTheNewMilestonesMilepebbles(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{total: 3}, productTaskListing(), nil)

	// The operator is in milepebble mode on the middle milestone, with its
	// milepebble chosen.
	start := fetch(t, mux, productTaskTasksURL("scope=milepebble&milestone="+
		productTaskMilestone.String()))
	require.Equal(t, http.StatusOK, start.Code, "body: %s", start.Body.String())
	require.Equal(t, productTaskMilepebble.String(),
		selectedOption(t, start.Body.String(), `data-krill="scope-milepebble-select"`),
		"the starting point is a milepebble of the middle milestone")

	// They change ONLY the Milestone select. The form still carries the
	// middle milepebble, because that is what the second select holds.
	replay := "scope=milepebble&milestone=" + productTaskNewestMilestone.String() +
		"&container_id=" + productTaskMilepebble.String()

	for _, tc := range []struct {
		name string
		get  func(string) *httptest.ResponseRecorder
	}{
		// The no-JS path: a full submit of the same form.
		{name: "a full-page submit", get: func(q string) *httptest.ResponseRecorder {
			return fetch(t, mux, productTaskTasksURL(q))
		}},
		// The htmx path: the control's own hx-get, which must not 404 --
		// htmx is configured noSwap on 4xx, so a refusal here is a dead
		// control, not a visible error.
		{name: "an in-place swap", get: func(q string) *httptest.ResponseRecorder {
			return htmxGet(mux, productTaskTasksURL(q))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.get(replay)
			require.Equal(t, http.StatusOK, rec.Code,
				"changing the Milestone select must not be refused: %s", rec.Body.String())
			body := rec.Body.String()

			assert.Equal(t, "milepebble", checkedRadioValue(t, body), "the mode is unchanged")
			assert.Equal(t, productTaskNewestMilestone.String(),
				selectedOption(t, body, `data-krill="scope-milestone-select"`),
				"the milestone select shows what the operator chose")
			assert.Equal(t, productTaskNewestMilepebble.String(),
				selectedOption(t, body, `data-krill="scope-milepebble-select"`),
				"the milepebble select re-offers the NEW milestone's, and takes its first")

			options := body[strings.Index(body, `data-krill="scope-milepebble-select"`):]
			options = options[:strings.Index(options, "</select>")]
			assert.Contains(t, options, productTaskNewestMilepebble.String())
			assert.NotContains(t, options, productTaskMilepebble.String(),
				"the previous milestone's milepebble is no longer on offer")
		})
	}
}

// TestScopeControlSurvivesTheOwnersDisagreeingPairReachesTheRegion: the
// same replay over htmx must still leave the operator a way out. A refusal
// that replaces the region with an alert takes the control with it -- and
// the control is the only way off a page scoped to a container.
func TestScopeControlSurvivesTheOwnersDisagreeingPairReachesTheRegion(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{total: 3}, productTaskListing(), nil)

	rec := htmxGet(mux, productTaskTasksURL("scope=milepebble&milestone="+
		productTaskNewestMilestone.String()+"&container_id="+productTaskMilepebble.String()))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), `data-krill="scope-control"`,
		"the answer to a scope change must carry the control that produced it")
}

// TestScopeControlMilepebbleModeOnAnUncutMilestoneKeepsAMilestoneSelect:
// milepebble mode pointed at a milestone that has no milepebbles is an
// ordinary empty result, not the loss of the control.
//
// It is reachable by one click from the mode's own milestone select, and
// the operator who lands there has named a real milestone of a real
// product -- so answering "No milestones yet" is wrong twice over: the
// product has milestones, and the select that would let them pick a cut
// one is the very thing that disappeared.
func TestScopeControlMilepebbleModeOnAnUncutMilestoneKeepsAMilestoneSelect(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{total: 3}, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL("scope=milepebble&milestone="+productTaskOldestMilestone.String()))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()

	assert.Contains(t, body, `data-krill="scope-control"`, "the operator's way out stays")
	assert.Equal(t, "milepebble", checkedRadioValue(t, body),
		"the mode the URL named is the mode that stays marked")
	assert.Contains(t, body, `data-krill="scope-milestone-select"`,
		"the milestone select is how the operator picks a cut milestone, so it stays")
	assert.Equal(t, productTaskOldestMilestone.String(),
		selectedOption(t, body, `data-krill="scope-milestone-select"`),
		"and it shows the milestone the operator named")
	assert.NotContains(t, body, "No milestones yet",
		"the product has three milestones; the chosen one simply has none cut")
}

// TestScopeControlMilepebbleModeResolvesADisagreeingPairToTheNamedMilestone:
// naming a milepebble that is not under the named milestone resolves to
// that milestone's own first milepebble rather than being refused.
//
// This SUPERSEDES an earlier case here that asserted a 404, and the spec
// decides it: FR 7191dba1 requires the URL to carry the mode and the
// selected ids, and the control is a plain GET form over two selects. A
// select cannot be emptied by choosing something else in it, so the
// operator's own click on the Milestone select necessarily submits the
// previous milepebble alongside the new milestone. A 404 for that pair
// would break the one interaction the `milestone` parameter exists to
// enable -- and htmx is configured noSwap on 4xx, so it would break
// silently. The named milestone is the more specific statement of what the
// operator is looking at, and the no-id rule the mode already applies one
// level down settles the milepebble.
//
// A milepebble from ANOTHER product is still a 404 -- that is the
// membership check, and it is unchanged. What is narrowed here is only the
// pair a control interaction necessarily produces.
func TestScopeControlMilepebbleModeResolvesADisagreeingPairToTheNamedMilestone(t *testing.T) {
	tasks := &recordingProductTasks{total: 1}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL("scope=milepebble&milestone="+
		productTaskMilestone.String()+"&container_id="+productTaskNewestMilepebble.String()))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Len(t, tasks.listed, 1, "the scope resolves rather than being refused")
	assert.Equal(t, store.ProductTaskScope{
		Kind:        store.ProductTaskScopeMilepebble,
		ContainerID: productTaskMilepebble,
	}, tasks.listed[0].Scope,
		"the read follows the named milestone, not the stale container")
}

// TestScopeControlMilepebbleModeStillRefusesAnotherProductsMilepebble: the
// narrowing above must not weaken the membership check. A milepebble the
// product does not own is still a 404, and the store is still never asked.
func TestScopeControlMilepebbleModeStillRefusesAnotherProductsMilepebble(t *testing.T) {
	tasks := &recordingProductTasks{rows: nil, total: 1}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL("scope=milepebble&milestone="+
		productTaskMilestone.String()+"&container_id="+productTaskOtherMilestone.String()))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, tasks.listed, "the store must never be asked for another product's container")
}

// TestScopeControlMilepebbleModeRefusesAMilepebbleAsTheParentMilestone:
// the `milestone` parameter names a PARENT milestone. Handed a milepebble
// id it is not found as a milestone of this product, which is the same
// refusal as any other id the product does not own under that kind.
//
// It is the reverse pairing -- a milepebble where a milestone belongs --
// that TestScopeControlMilepebbleModeReadsAMilestoneContainerAsTheParent covers.
func TestScopeControlMilepebbleModeRefusesAMilepebbleAsTheParentMilestone(t *testing.T) {
	tasks := &recordingProductTasks{}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL("scope=milepebble&milestone="+productTaskMilepebble.String()))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, tasks.listed)
}

// TestScopeControlMilepebbleModeReadsAMilestoneContainerAsTheParent: the
// container parameter in milepebble mode names a MILEPEBBLE, but switching
// the scope from Milestone submits the chosen milestone in that parameter.
// It is read as the parent whose milepebbles are on offer, never as the
// read's container: the store is only ever asked for a milepebble's tasks.
func TestScopeControlMilepebbleModeReadsAMilestoneContainerAsTheParent(t *testing.T) {
	for _, tc := range []struct {
		name          string
		query         string
		wantMilestone string
		wantRead      bool
	}{
		{
			name:          "a milestone of this product that has milepebbles cut under it",
			query:         "scope=milepebble&container_id=" + productTaskMilestone.String(),
			wantMilestone: productTaskMilestone.String(),
			wantRead:      true,
		},
		{
			name:          "the same, alongside a parent that agrees with it",
			query:         "scope=milepebble&milestone=" + productTaskMilestone.String() + "&container_id=" + productTaskMilestone.String(),
			wantMilestone: productTaskMilestone.String(),
			wantRead:      true,
		},
		{
			name:          "an uncut milestone, which has no milepebble to offer",
			query:         "scope=milepebble&container_id=" + productTaskOldestMilestone.String(),
			wantMilestone: productTaskOldestMilestone.String(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &recordingProductTasks{total: 1}
			mux := productTaskMux(t, tasks, productTaskListing(), nil)

			rec := fetch(t, mux, productTaskTasksURL(tc.query))

			assert.Equal(t, http.StatusOK, rec.Code, "the operator is never stranded on a mode switch")
			body := rec.Body.String()
			assert.Equal(t, "milepebble", checkedRadioValue(t, body))
			assert.Equal(t, tc.wantMilestone, selectedOption(t, body, `data-krill="scope-milestone-select"`),
				"the submitted milestone is the one whose milepebbles are on offer")
			for _, params := range tasks.listed {
				assert.NotEqual(t, tc.wantMilestone, params.Scope.ContainerID.String(),
					"the store is never asked for a milestone's tasks in milepebble mode")
			}
			if tc.wantRead {
				require.Len(t, tasks.listed, 1)
				assert.Equal(t, productTaskMilepebble.String(), tasks.listed[0].Scope.ContainerID.String(),
					"the read is the milestone's first milepebble")
			}
		})
	}
}

// TestScopeControlShipsAMilepebbleModeOnEveryView: the control is one
// component both views render, and the two views build it from the same
// resolved scope -- so the same URL marks the same mode and the same
// option on the Tasks page and on the Board page.
func TestScopeControlShipsAMilepebbleModeOnEveryView(t *testing.T) {
	query := "scope=milepebble&milestone=" + productTaskMilestone.String()
	mux := productTaskMux(t, &recordingProductTasks{total: 3}, productTaskListing(), nil)

	tasksRec := fetch(t, mux, productTaskTasksURL(query))
	boardRec := fetch(t, mux, "/products/"+productTaskProduct.String()+"/board?"+query)

	for _, pair := range []struct {
		view string
		body string
		code int
	}{
		{view: "Tasks", body: tasksRec.Body.String(), code: tasksRec.Code},
		{view: "Board", body: boardRec.Body.String(), code: boardRec.Code},
	} {
		t.Run(pair.view, func(t *testing.T) {
			require.Equal(t, http.StatusOK, pair.code, "body: %s", pair.body)
			body := pair.body
			// Both views carry the control and the marked mode; the view
			// name is the only thing that differs between them.
			assert.Contains(t, body, `data-krill="scope-control"`)
			assert.Equal(t, "milepebble", checkedRadioValue(t, body), "the milepebble mode is the one marked as chosen")
			assert.Equal(t, productTaskMilepebble.String(),
				selectedOption(t, body, `data-krill="scope-milepebble-select"`))
			assert.Contains(t, body, `data-krill-view="`+pair.view+`"`)
		})
	}
}

// TestScopeControlURLCarriesTheWholeScope is FR 7191dba1's "the URL
// carries the mode and the selected ids, so a reload or a shared link
// shows the same scope".
//
// It is checked the way an operator would: by reading the control's own
// form, taking the fields it renders, and asking the server what that
// query resolves to. A control whose selects carry the wrong parameter
// names would resolve to something else here.
func TestScopeControlURLCarriesTheWholeScope(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  store.ProductTaskScope
	}{
		{
			name:  "the default, with no query at all",
			query: "",
			want:  store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
		},
		{
			name:  "a milestone the select offers",
			query: "scope=milestone&container_id=" + productTaskMilestone.String(),
			want:  store.ProductTaskScope{Kind: store.ProductTaskScopeMilestone, ContainerID: productTaskMilestone},
		},
		{
			name:  "a milepebble under the milestone the select offers",
			query: "scope=milepebble&milestone=" + productTaskMilestone.String() + "&container_id=" + productTaskMilepebble.String(),
			want:  store.ProductTaskScope{Kind: store.ProductTaskScopeMilepebble, ContainerID: productTaskMilepebble},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &recordingProductTasks{total: 2}
			mux := productTaskMux(t, tasks, productTaskListing(), nil)

			// Read the control as rendered...
			first := fetch(t, mux, productTaskTasksURL(tc.query))
			require.Equal(t, http.StatusOK, first.Code, "body: %s", first.Body.String())
			body := first.Body.String()

			// ...and replay exactly what its form would submit.
			require.Contains(t, body, `method="get"`)
			replay := scopeFormQueryOf(t, body)
			second := fetch(t, mux, productTaskTasksURL(replay))

			require.Equal(t, http.StatusOK, second.Code, "body: %s", second.Body.String())
			require.Len(t, tasks.listed, 2)
			assert.Equal(t, tc.want, tasks.listed[1].Scope,
				"submitting the rendered control must resolve to the scope the operator was looking at")
		})
	}
}

// scopeFormQueryOf is the query string the rendered control's own form
// would submit: the action's path is the Tasks URL, and the fields are
// whatever the control rendered. Built from the markup rather than
// hard-coded, so a select that submits the wrong parameter shows up as a
// failed round trip instead of passing.
func scopeFormQueryOf(t *testing.T, body string) string {
	t.Helper()
	form := body[strings.Index(body, `data-krill="scope-control"`):]
	form = form[:strings.Index(form, "</form>")]

	var q strings.Builder
	appendField := func(name, value string) {
		if q.Len() > 0 {
			q.WriteByte('&')
		}
		q.WriteString(name + "=" + value)
	}

	// The checked radio is the mode; the selected option of each select is
	// that select's value.
	appendField("scope", checkedRadioValue(t, form))
	for _, marker := range []string{`data-krill="scope-milestone-select"`, `data-krill="scope-milepebble-select"`} {
		at := strings.Index(form, marker)
		if at == -1 {
			continue
		}
		selectTag := form[strings.LastIndex(form[:at], "<select"):at]
		name := selectTag[strings.Index(selectTag, `name="`)+len(`name="`):]
		name = name[:strings.Index(name, `"`)]
		appendField(name, selectedOption(t, form, marker))
	}
	return q.String()
}

// checkedRadioValue is the value of the checked scope radio in a form's
// markup -- the mode the control would submit.
func checkedRadioValue(t *testing.T, form string) string {
	t.Helper()
	const value = `value="`
	for rest := form; ; {
		at := strings.Index(rest, `type="radio"`)
		require.NotEqual(t, -1, at, "the control offers no modes: %s", form)
		tag := rest[at:]
		tag = tag[:strings.Index(tag, ">")]
		rest = rest[at+1:]
		if !strings.Contains(tag, " checked") {
			continue
		}
		start := strings.Index(tag, value)
		require.NotEqual(t, -1, start, "a mode radio with no value: %s", tag)
		return tag[start+len(value) : start+len(value)+strings.Index(tag[start+len(value):], `"`)]
	}
}

// TestScopeControlNeverAsksTheOperatorToTypeAnID is FR 7191dba1's "No
// field asks the operator to type an id" and the design rule behind it:
// humans pick, they never type. Every container is chosen from a select
// built from the product's own delivery listing.
//
// The check is on the rendered markup rather than on a count of inputs,
// because what matters is the KIND: a select's value is a legitimate
// id-shaped string that the browser submits without the operator typing
// it, while a text input is exactly what must not exist.
func TestScopeControlNeverAsksTheOperatorToTypeAnID(t *testing.T) {
	for _, query := range []string{
		"",
		"scope=milestone",
		"scope=milestone&container_id=" + productTaskMilestone.String(),
		"scope=milepebble&milestone=" + productTaskMilestone.String(),
	} {
		t.Run(query, func(t *testing.T) {
			body := scopeControlOf(t, query)
			control := body[strings.Index(body, `data-krill="scope-control"`):]
			control = control[:strings.Index(control, "</form>")]

			for _, forbidden := range []string{`type="text"`, `type="search"`, `type="number"`} {
				assert.NotContains(t, control, forbidden,
					"the scope control must not ask the operator to type a value")
			}
			// Every id the control carries is an option's value or a radio's
			// value -- never an input's typed content.
			assert.NotContains(t, control, "placeholder")
			assert.Contains(t, control, `type="radio"`, "the modes are a choice, and the choices are offered")
			if strings.Contains(control, `name="container_id"`) || strings.Contains(control, `name="milestone"`) {
				assert.Contains(t, control, "<select", "a container is chosen from a select")
			}
		})
	}
}

// TestScopeControlIsAPlainGetFormWithHtmxOnTop is the mechanism half of
// FR 7191dba1: "A plain GET form is the mechanism; no JavaScript is
// required for it to work", with the in-place swap layered on top.
//
// It is asserted in both halves because either alone is not enough: an
// hx-get with no form works only with JavaScript, and a form with no
// hx-get reloads the whole page instead of swapping in place.
func TestScopeControlIsAPlainGetFormWithHtmxOnTop(t *testing.T) {
	body := scopeControlOf(t, "scope=milepebble&milestone="+productTaskMilestone.String())

	assert.Contains(t, body, `<form method="get"`, "a plain GET form is the mechanism")
	assert.Contains(t, body, `action="`+productTaskTasksURL("")+`"`,
		"the form submits to this very path, so a no-JS submit lands on the same scope")
	assert.Contains(t, body, `hx-get="`+productTaskTasksURL("")+`"`,
		"the in-place swap asks for the same URL the form would")
	assert.Contains(t, body, `hx-target="#`+pages.ProductTasksAnchor+`"`,
		"the swap replaces the region, so the answer and the control that produced it move together")
	assert.Contains(t, body, `hx-swap="outerHTML"`)
	assert.Contains(t, body, `hx-push-url="true"`,
		"the URL follows the scope, so a reload and a shared link show the same one")
}

// TestScopeControlChangeSwapsInPlace drives the half of the mechanism that
// the other cases only read in the markup: the request the control's hx-get
// makes, answered with the fragment that replaces the region.
//
// It matters because htmx does not swap on a non-2xx. A scope change that
// answered 404 would leave the operator clicking a control that appears to
// do nothing.
func TestScopeControlChangeSwapsInPlace(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{total: 5}, productTaskListing(), nil)

	rec := htmxGet(mux, productTaskTasksURL("scope=milepebble&milestone="+productTaskMilestone.String()))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `id="`+pages.ProductTasksAnchor+`"`,
		"the fragment keeps the region's own id, or the swap deletes its own target")
	assert.Equal(t, productTaskMilepebble.String(),
		selectedOption(t, body, `data-krill="scope-milepebble-select"`))
	assert.Contains(t, body, ">5<", "the swapped region carries the new count")
}

// TestScopeControlCarriesTheSiblingFilters: changing the scope must not
// silently drop the lane and only-stuck filters the operator set, on
// either view.
//
// The two views carry them by different mechanisms, and the difference is
// the point. The Tasks page offers the lane select and the only-stuck
// checkbox (FR 61d7fb7b), so the form carries them as controls -- the
// selected option and the checked box. The Board reads the same parsed
// scope but offers neither control, so its form carries them as hidden
// fields. Either way a scope change re-submits them; what would break this
// is a Board that dropped them, leaving an operator who filtered on Tasks
// to land on a Board showing everything with no indication why.
func TestScopeControlCarriesTheSiblingFilters(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "a lane filter", query: "scope=milestone&container_id=" + productTaskMilestone.String() + "&lane=Testing"},
		{name: "only stuck", query: "scope=milestone&container_id=" + productTaskMilestone.String() + "&only_stuck=true"},
		{name: "both", query: "scope=milestone&container_id=" + productTaskMilestone.String() + "&lane=Testing&only_stuck=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hasLane := strings.Contains(tc.query, "lane=")
			hasStuck := strings.Contains(tc.query, "only_stuck")

			tasks := scopeControlFormOf(t, productTaskTasksURL(tc.query))
			if hasLane {
				assert.Equal(t, "Testing", selectedOption(t, tasks, `data-krill="lane-filter-select"`),
					"the Tasks form's lane select shows the filtered lane")
			} else {
				assert.Equal(t, "", selectedOption(t, tasks, `data-krill="lane-filter-select"`),
					"an unfiltered URL opens on Any lane, not on whichever lane happens to sort first")
			}
			if hasStuck {
				assert.Contains(t, checkedBoxOf(tasks), "checked",
					"the Tasks form's only-stuck box is checked, so a scope change keeps the filter")
			} else {
				assert.NotContains(t, checkedBoxOf(tasks), "checked",
					"an unfiltered URL must not open with the box already ticked")
			}

			board := scopeControlFormOf(t, "/products/"+productTaskProduct.String()+"/board?"+tc.query)
			assert.NotContains(t, board, `data-krill="lane-filter-select"`,
				"the Board offers no lane control to change")
			if hasLane {
				assert.Contains(t, board, `name="lane" value="Testing"`,
					"the Board carries the lane forward as a hidden field")
			}
			if hasStuck {
				assert.Contains(t, board, `name="only_stuck" value="true"`,
					"the Board carries only-stuck forward as a hidden field")
			}
		})
	}
}

// checkedBoxOf is the only-stuck checkbox's own opening tag, so an
// assertion that it is CHECKED is made about that input rather than about
// the word appearing anywhere in the form -- templ renders `checked` after
// the class list, so pinning an attribute order would pin templ's output
// rather than the state.
func checkedBoxOf(form string) string {
	at := strings.Index(form, `data-krill="only-stuck-filter"`)
	if at == -1 {
		return ""
	}
	start := strings.LastIndex(form[:at], "<input")
	if start == -1 {
		return ""
	}
	return form[start:at]
}

// scopeControlFormOf is the scope control's own form, cut out of the page
// the given URL served -- so an assertion about what the form submits is
// made about the form rather than about whatever else the page carries.
func scopeControlFormOf(t *testing.T, url string) string {
	t.Helper()
	mux := productTaskMux(t, &recordingProductTasks{total: 3}, productTaskListing(), nil)

	rec := fetch(t, mux, url)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()
	at := strings.Index(body, `data-krill="scope-control"`)
	require.NotEqual(t, -1, at, "no scope control rendered: %s", body)
	rest := body[at:]
	end := strings.Index(rest, "</form>")
	require.NotEqual(t, -1, end, "the scope control never closes: %s", body)
	return rest[:end]
}

// TestScopeControlLeavesACleanProductWithSomethingToChooseFrom: a product
// with no milestones has nothing to put in a select, so the control offers
// the modes without rendering a select an operator cannot use. The empty
// state still names the condition -- the modes staying is what lets the
// operator leave.
func TestScopeControlLeavesACleanProductWithSomethingToChooseFrom(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{}, emptyDeliveryListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL("scope=milestone"))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `data-krill="scope-control"`, "the modes stay, so the operator has a way out")
	assert.Contains(t, body, `data-krill="product-tasks-empty"`)
	assert.NotContains(t, body, `data-krill="scope-milestone-select"`,
		"an empty select would be a control that cannot be used")
}

// TestScopeControlKeepsAShippedMilestoneOfferedButMarked is FR 7191dba1's
// status rule, on the control: a shipped milestone is outside the
// product-wide all-incomplete scope, but picking it explicitly still shows
// its tasks.
//
// The exclusion itself is the store's query, pinned in //krill/store. What
// the console owns is the consequence: the milestone must still be in the
// select (dropping it would make "whatever its status" unreachable), and it
// must SAY it is outside the default scope, so an operator who finds a
// shipped milestone in the list is not reading it as a contradiction.
func TestScopeControlKeepsAShippedMilestoneOfferedButMarked(t *testing.T) {
	listing := productTaskListing()
	listing.Milestones = append(listing.Milestones, slice.MilestoneListingEntry{
		ID:     productTaskShippedMilestone,
		Name:   "Shipped milestone",
		Status: store.MilestoneStatusShipped,
	})
	tasks := &recordingProductTasks{total: 2}
	mux := productTaskMux(t, tasks, listing, nil)

	rec := fetch(t, mux, productTaskTasksURL("scope=milestone"))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()

	at := optionIndex(body, productTaskShippedMilestone.String())
	require.NotEqual(t, -1, at, "a shipped milestone stays in the select: %s", body)
	option := optionTextOf(body, at)
	assert.Contains(t, option, "outside the all-incomplete scope",
		"the option says why it is here rather than in the default scope")

	// Picking it explicitly reads it, whatever its status.
	require.Len(t, tasks.listed, 1)
	assert.Equal(t, store.ProductTaskScope{
		Kind:        store.ProductTaskScopeMilestone,
		ContainerID: productTaskShippedMilestone,
	}, tasks.listed[0].Scope)
}

// TestScopeControlDoesNotMarkIncompleteMilestonesOutOfScope: the marking
// is for containers the default scope EXCLUDES. Marking everything would
// be noise that trains the operator to ignore the one that matters.
func TestScopeControlDoesNotMarkIncompleteMilestonesOutOfScope(t *testing.T) {
	body := scopeControlOf(t, "scope=milestone")

	for _, m := range []uuid.UUID{productTaskNewestMilestone, productTaskMilestone} {
		at := optionIndex(body, m.String())
		require.NotEqual(t, -1, at, "every milestone is offered: %s", body)
		option := optionTextOf(body, at)
		assert.NotContains(t, option, "outside the all-incomplete scope",
			"an in-progress milestone is in the default scope")
	}
}

// TestScopeControlDoesNotRecordAProductOnASwap: the last-viewed cookie
// answers "where does my next un-prefixed page land", and a scope change
// is a swap inside a page the operator is already on -- not a navigation.
// Writing it on every hx-get would rewrite it repeatedly for a
// navigation nobody made.
func TestScopeControlDoesNotRecordAProductOnASwap(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{}, productTaskListing(), nil)

	page := fetch(t, mux, productTaskTasksURL(""))
	swap := htmxGet(mux, productTaskTasksURL("scope=milestone"))

	assert.Contains(t, page.Header().Get("Set-Cookie"), lastViewedProductCookie,
		"a real page view records the product")
	assert.NotContains(t, swap.Header().Get("Set-Cookie"), lastViewedProductCookie,
		"an in-place swap is not a page view")
}
