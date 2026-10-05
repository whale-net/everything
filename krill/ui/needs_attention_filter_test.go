// The Needs attention filter bar's contract (FR 6369e312): a milestone
// select defaulting to "All milestones" and, on the Escalated tab, a
// reason select built from the store's own enumeration. Both selections
// narrow every tab's read and its count, both ride in the URL, and both are
// echoed back in the page -- so a copied link opens on the table it named.
//
// The fixture is a record-keeping task store plus a real delivery listing,
// so "the select offers the product's containers" and "the read was
// narrowed" are asserted against what the page actually asked for rather
// than against a restatement of it.
package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// The fixture's containers, spelled as literals so a failure names the same
// ids every run.
var (
	needsAttentionFilterMilestone      = uuid.MustParse("11112222-3333-4444-5555-666677778888")
	needsAttentionFilterMilepebble     = uuid.MustParse("99990000-1111-2222-3333-444455556666")
	needsAttentionFilterOtherMilestone = uuid.MustParse("aaaabbbb-cccc-dddd-eeee-ffff00001111")
)

// needsAttentionFilterListing is the product's delivery listing: two
// milestones, the first cut with a milepebble, so the select's options come
// from a real container read and a milepebble id has a label to echo.
func needsAttentionFilterListing() slice.DeliveryListing {
	return slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{
		{
			ID:     needsAttentionFilterMilestone,
			Name:   "Filter milestone",
			Status: store.MilestoneStatusInProgress,
			Milepebbles: []slice.MilepebbleListingEntry{
				{ID: needsAttentionFilterMilepebble, Name: "Nested milepebble", Status: store.MilestoneStatusInDesign},
			},
		},
		{ID: needsAttentionFilterOtherMilestone, Name: "Other milestone", Status: store.MilestoneStatusShipped},
	}}
}

// newNeedsAttentionFilterFixture is the page fixture with the delivery
// listing above behind it, so the milestone select is populated from the
// same read the Tasks/Board scope control uses.
func newNeedsAttentionFilterFixture(t *testing.T) *needsAttentionFixture {
	t.Helper()
	f := newNeedsAttentionFixture(t)
	f.app.spec = scopedProductsReader{
		specReadClient: &fakeSpecReader{listing: needsAttentionFilterListing()},
		products:       []store.Product{{ID: f.pid, Name: "Test product"}},
	}
	return f
}

// filterPath is the page's URL for one tab with the filter query appended,
// spelled with the production parameter names so a test and the page cannot
// disagree about their spelling.
func (f *needsAttentionFixture) filterPath(tab string, q url.Values) string {
	vals := url.Values{needsAttentionTabParam: {tab}}
	for k, vs := range q {
		vals[k] = vs
	}
	return productHref(f.pid, needsAttentionSuffix) + "?" + vals.Encode()
}

// renderedOption is one <option> as the page emitted it.
type renderedOption struct {
	Value    string
	Label    string
	Selected bool
}

// selectOptions returns the options inside the select carrying marker, so a
// test asserts the select's own state (which value is selected, in what
// order) rather than a substring another option could satisfy.
func selectOptions(t *testing.T, body, marker string) []renderedOption {
	t.Helper()
	start := strings.Index(body, marker)
	require.GreaterOrEqual(t, start, 0, "the select %q is not on the page", marker)
	rest := body[start:]
	end := strings.Index(rest, "</select>")
	require.GreaterOrEqual(t, end, 0, "the select %q is unterminated", marker)

	var opts []renderedOption
	for _, chunk := range strings.Split(rest[:end], "<option")[1:] {
		close := strings.Index(chunk, ">")
		require.GreaterOrEqual(t, close, 0)
		tag, text := chunk[:close], chunk[close+1:]
		if i := strings.Index(text, "</option>"); i >= 0 {
			text = text[:i]
		}
		opts = append(opts, renderedOption{
			Value:    optionAttribute(tag, "value"),
			Label:    strings.TrimSpace(text),
			Selected: strings.Contains(tag, "selected"),
		})
	}
	return opts
}

// optionAttribute reads one quoted attribute off an <option> tag.
func optionAttribute(tag, name string) string {
	marker := name + `="`
	i := strings.Index(tag, marker)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// optionOf finds one option by value, failing when the select does not
// offer it at all.
func optionOf(t *testing.T, opts []renderedOption, value string) renderedOption {
	t.Helper()
	for _, o := range opts {
		if o.Value == value {
			return o
		}
	}
	t.Fatalf("the select offers no option with value %q; options: %+v", value, opts)
	return renderedOption{}
}

// ---------------------------------------------------------------------------
// 1. the selects, from real option reads
// ---------------------------------------------------------------------------

// TestNeedsAttentionMilestoneSelectOffersTheProductsContainers pins the
// first half of the FR: a milestone select, defaulting to "All milestones",
// whose options are the product's own milestone_ref containers -- read from
// the same delivery listing the Tasks/Board scope control reads, and
// carrying a milestone_ref id of either kind.
func TestNeedsAttentionMilestoneSelectOffersTheProductsContainers(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)

	body := fetch(t, f.mux, f.path(needsAttentionTabEscalated)).Body.String()
	opts := selectOptions(t, body, `data-krill="needs-attention-milestone-select"`)

	all := optionOf(t, opts, "")
	assert.Equal(t, "All milestones", all.Label, "the default option is All milestones")
	assert.True(t, all.Selected, "a request with no milestone filter selects the default")

	milestone := optionOf(t, opts, needsAttentionFilterMilestone.String())
	assert.Equal(t, "Filter milestone", milestone.Label)
	assert.False(t, milestone.Selected, "an unfiltered request selects no container")

	milepebble := optionOf(t, opts, needsAttentionFilterMilepebble.String())
	assert.Contains(t, milepebble.Label, "Nested milepebble",
		"a milestone_ref id of the milepebble kind is offered too, and labelled by its milestone")

	other := optionOf(t, opts, needsAttentionFilterOtherMilestone.String())
	assert.Equal(t, "Other milestone", other.Label,
		"every milestone is offered, shipped or not -- the page's scope is the whole product")
}

// TestNeedsAttentionReasonSelectIsTheStoreEnumeration pins the second half:
// the reason select offers "Any reason" and the store's own three reasons,
// valued by the store's wire strings and labelled with the shared
// components wording -- so the select and the row badges below it cannot
// word one reason two ways.
func TestNeedsAttentionReasonSelectIsTheStoreEnumeration(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)

	body := fetch(t, f.mux, f.path(needsAttentionTabEscalated)).Body.String()
	opts := selectOptions(t, body, `data-krill="needs-attention-reason-select"`)

	require.Len(t, opts, 4, "Any reason plus the store's three reasons")
	assert.Equal(t, "Any reason", opts[0].Label)
	assert.Empty(t, opts[0].Value, "Any reason is the read's nil reason, spelled as an empty value")
	assert.True(t, opts[0].Selected, "an unfiltered request selects Any reason")
	for i, want := range []struct{ value, label string }{
		{string(store.EscalationReasonThrashCap), "thrash cap"},
		{string(store.EscalationReasonAttemptCap), "attempt cap"},
		{string(store.EscalationReasonManual), "manual"},
	} {
		assert.Equal(t, want.value, opts[i+1].Value)
		assert.Equal(t, want.label, opts[i+1].Label)
		assert.False(t, opts[i+1].Selected)
	}
}

// TestNeedsAttentionReasonSelectIsOfferedOnTheEscalatedTabAlone is the
// FR's "on the Escalated tab" clause: the reason select is the escalated
// queue's own control, and the other three tabs do not carry it.
func TestNeedsAttentionReasonSelectIsOfferedOnTheEscalatedTabAlone(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)

	escalated := fetch(t, f.mux, f.path(needsAttentionTabEscalated)).Body.String()
	assert.Contains(t, escalated, `data-krill="needs-attention-reason-select"`)

	for _, tab := range []string{needsAttentionTabClaimed, needsAttentionTabCancelled, needsAttentionTabNotes} {
		t.Run(tab, func(t *testing.T) {
			body := fetch(t, f.mux, f.path(tab)).Body.String()
			assert.NotContains(t, body, `data-krill="needs-attention-reason-select"`)
			assert.Contains(t, body, `data-krill="needs-attention-milestone-select"`,
				"the milestone select is every tab's")
		})
	}
}

// ---------------------------------------------------------------------------
// 2. the selections narrow the reads and the counts
// ---------------------------------------------------------------------------

// TestNeedsAttentionFiltersNarrowEveryTabsReadAndCount is the FR's "they
// filter the table in place ... and tab counts reflect the filters": the
// milestone filter reaches all four tabs' reads, the reason reaches the
// escalated one, and every count is read through the same params type and
// the same narrowing as the table it labels.
func TestNeedsAttentionFiltersNarrowEveryTabsReadAndCount(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)

	rec := fetch(t, f.mux, f.filterPath(needsAttentionTabEscalated, url.Values{
		needsAttentionMilestoneParam: {needsAttentionFilterMilestone.String()},
		needsAttentionReasonParam:    {string(store.EscalationReasonThrashCap)},
	}))
	require.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, f.tasks.listFilters, 1, "the selected tab's table is read once")
	if assert.NotNil(t, f.tasks.listFilters[0].MilestoneID) {
		assert.Equal(t, needsAttentionFilterMilestone, *f.tasks.listFilters[0].MilestoneID,
			"the milestone selection narrows the table's read")
	}
	require.Len(t, f.tasks.listReasons, 1, "the escalated table's reason narrowing is the store's own field")
	if assert.NotNil(t, f.tasks.listReasons[0]) {
		assert.Equal(t, store.EscalationReasonThrashCap, *f.tasks.listReasons[0])
	}

	// Every tab's count is read with the milestone narrowing. The escalated
	// queue is also read by the shell's own sidebar badge, which is
	// unfiltered by design, so the assertion is that the page's read --
	// narrowed -- is among them rather than that every read is.
	for _, tab := range needsAttentionTabOrder {
		reads := f.tasks.countFilters[tab]
		require.NotEmpty(t, reads, "%s: every tab's count is read", tab)
		narrowed := 0
		for _, read := range reads {
			if read.MilestoneID != nil && *read.MilestoneID == needsAttentionFilterMilestone {
				narrowed++
			}
		}
		assert.Equal(t, 1, narrowed, "%s: the page's own count read carries the milestone filter", tab)
	}
	// The reason narrows the escalated count too -- a badge that ignored it
	// would report a queue larger than the table beneath it.
	escalatedReasons := f.tasks.countReasons[needsAttentionTabEscalated]
	require.NotEmpty(t, escalatedReasons)
	filtered := 0
	for _, reason := range escalatedReasons {
		if reason != nil && *reason == store.EscalationReasonThrashCap {
			filtered++
		}
	}
	assert.Equal(t, 1, filtered, "the escalated count carries the reason filter")
	// And it reaches no other queue: a reason is the escalated read's own
	// narrowing, not a ConsoleFilter the other three share.
	for _, tab := range []string{needsAttentionTabClaimed, needsAttentionTabCancelled, needsAttentionTabNotes} {
		for _, reason := range f.tasks.countReasons[tab] {
			assert.Nil(t, reason, "%s: the reason filter must not narrow another queue's count", tab)
		}
	}
}

// TestNeedsAttentionMilestoneFilterNarrowsEveryTabInTurn walks the same
// narrowing across all four tabs, so "every tab's read" is asserted rather
// than inferred from one.
func TestNeedsAttentionMilestoneFilterNarrowsEveryTabInTurn(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)
	milestone := needsAttentionFilterMilestone.String()

	for _, tab := range needsAttentionTabOrder {
		t.Run(tab, func(t *testing.T) {
			f.tasks.listFilters = nil
			f.tasks.listReasons = nil
			f.tasks.countReasons = nil

			rec := fetch(t, f.mux, f.filterPath(tab, url.Values{
				needsAttentionMilestoneParam: {milestone},
			}))
			require.Equal(t, http.StatusOK, rec.Code)

			require.Len(t, f.tasks.listFilters, 1)
			if assert.NotNil(t, f.tasks.listFilters[0].MilestoneID) {
				assert.Equal(t, needsAttentionFilterMilestone, *f.tasks.listFilters[0].MilestoneID)
			}
			if tab == needsAttentionTabEscalated {
				require.Len(t, f.tasks.listReasons, 1)
				assert.Nil(t, f.tasks.listReasons[0], "a milestone filter alone sets no reason")
			} else {
				for _, reason := range f.tasks.countReasons[tab] {
					assert.Nil(t, reason)
				}
			}
		})
	}
}

// TestNeedsAttentionBadgeIsTheNarrowedCountRead pins the figure against the
// narrowing: the badge renders the count the store answered for the same
// filters, not a number derived from the rows on the page.
func TestNeedsAttentionBadgeIsTheNarrowedCountRead(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)
	f.tasks.counts = map[string]int{
		needsAttentionTabEscalated: 4,
		needsAttentionTabClaimed:   6,
	}
	f.tasks.escalated = nil // a filtered read that came back empty

	body := fetch(t, f.mux, f.filterPath(needsAttentionTabEscalated, url.Values{
		needsAttentionMilestoneParam: {needsAttentionFilterMilestone.String()},
		needsAttentionReasonParam:    {string(store.EscalationReasonAttemptCap)},
	})).Body.String()

	assert.Equal(t, 4, needsAttentionBadge(t, body, needsAttentionTabEscalated))
	assert.Equal(t, 6, needsAttentionBadge(t, body, needsAttentionTabClaimed))
}

// ---------------------------------------------------------------------------
// 3. the URL carries both, and the page echoes them
// ---------------------------------------------------------------------------

// TestNeedsAttentionFilterSelectionsAreEchoedInThePage is the round-trip's
// read half: a filtered URL's selections are the ones the selects mark, so
// a copied link opens on the table it named.
func TestNeedsAttentionFilterSelectionsAreEchoedInThePage(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)

	body := fetch(t, f.mux, f.filterPath(needsAttentionTabEscalated, url.Values{
		needsAttentionMilestoneParam: {needsAttentionFilterMilepebble.String()},
		needsAttentionReasonParam:    {string(store.EscalationReasonManual)},
	})).Body.String()

	milestones := selectOptions(t, body, `data-krill="needs-attention-milestone-select"`)
	assert.False(t, optionOf(t, milestones, "").Selected, "a filtered request does not select the default")
	assert.True(t, optionOf(t, milestones, needsAttentionFilterMilepebble.String()).Selected,
		"a milepebble id is echoed back as the selected option")
	assert.False(t, optionOf(t, milestones, needsAttentionFilterMilestone.String()).Selected)

	reasons := selectOptions(t, body, `data-krill="needs-attention-reason-select"`)
	assert.False(t, optionOf(t, reasons, "").Selected)
	assert.True(t, optionOf(t, reasons, string(store.EscalationReasonManual)).Selected)
}

// TestNeedsAttentionTabLinksCarryTheFilters is the FR's "the URL query
// carries both" across a tab switch: every tab's link spells the filters in
// force, so switching tabs narrows the new tab the same way instead of
// silently dropping back to the whole product.
func TestNeedsAttentionTabLinksCarryTheFilters(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)
	milestone := needsAttentionFilterMilestone.String()

	body := fetch(t, f.mux, f.filterPath(needsAttentionTabEscalated, url.Values{
		needsAttentionMilestoneParam: {milestone},
		needsAttentionReasonParam:    {string(store.EscalationReasonThrashCap)},
	})).Body.String()

	for _, tab := range needsAttentionTabOrder {
		t.Run(tab, func(t *testing.T) {
			q := url.Values{
				needsAttentionTabParam:       {tab},
				needsAttentionMilestoneParam: {milestone},
				needsAttentionReasonParam:    {string(store.EscalationReasonThrashCap)},
			}
			want := productHref(f.pid, needsAttentionSuffix) + "?" + q.Encode()
			// The body is HTML, so the href's own & separators are escaped
			// in the attribute; the link is the same URL either way.
			want = strings.ReplaceAll(want, "&", "&amp;")
			assert.Contains(t, body, `href="`+want+`"`,
				"the %s tab's link keeps the filters in force", tab)
		})
	}

	// The filter form carries the tab, so submitting a changed filter stays
	// on the tab the operator is reading rather than falling back to the
	// default one.
	assert.Contains(t, body, `<input type="hidden" name="`+needsAttentionTabParam+`" value="escalated"`,
		"the filter form re-submits the current tab")
	assert.Contains(t, body, `name="`+needsAttentionMilestoneParam+`"`)
	assert.Contains(t, body, `name="`+needsAttentionReasonParam+`"`)
	// The form addresses the page's own path, so the no-JavaScript submit
	// and the in-place swap ask for the same query.
	assert.Contains(t, body, `data-krill="needs-attention-filters"`)
	assert.Contains(t, body, `action="`+productHref(f.pid, needsAttentionSuffix)+`"`)
}

// TestNeedsAttentionFiltersReachTheResultsSwapToo is the other half of
// "in place": the fragment a poll or Refresh fetches is the read under the
// same filters, so the swap cannot replace a filtered table with an
// unfiltered one.
func TestNeedsAttentionFiltersReachTheResultsSwapToo(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)

	rec := fetchHX(t, f.mux, f.filterPath(needsAttentionTabEscalated, url.Values{
		needsAttentionMilestoneParam: {needsAttentionFilterMilestone.String()},
	}), "ops-results")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, f.tasks.listFilters, 1)
	if assert.NotNil(t, f.tasks.listFilters[0].MilestoneID) {
		assert.Equal(t, needsAttentionFilterMilestone, *f.tasks.listFilters[0].MilestoneID)
	}
}

// ---------------------------------------------------------------------------
// 4. the two empty states, and the Open notes sentence
// ---------------------------------------------------------------------------

// TestNeedsAttentionFilteredEmptyNamesTheFilters is the FR's first bullet's
// second sentence: a filtered read that matched nothing says so, naming the
// filters in force rather than reporting an empty queue.
func TestNeedsAttentionFilteredEmptyNamesTheFilters(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)
	f.tasks.escalated = nil

	body := fetch(t, f.mux, f.filterPath(needsAttentionTabEscalated, url.Values{
		needsAttentionMilestoneParam: {needsAttentionFilterMilestone.String()},
		needsAttentionReasonParam:    {string(store.EscalationReasonManual)},
	})).Body.String()

	assert.Contains(t, body, `data-krill="needs-attention-empty"`)
	assert.Contains(t, body, "No escalated tasks match these filters.")
	assert.Contains(t, body, "milestone Filter milestone", "the empty state names the milestone in force")
	assert.Contains(t, body, "reason manual", "and the reason")
	// The generic empty state would claim the queue is empty, which is a
	// different (and here untrue) statement.
	assert.NotContains(t, body, "No task in this scope carries an active escalation.")
}

// TestNeedsAttentionUnfilteredEmptyIsTheQueuesOwnEmptyState is the other
// side of that rule: with no filter in force an empty tab is the queue
// being empty, and inventing a filter for it would name a cause that does
// not exist.
func TestNeedsAttentionUnfilteredEmptyIsTheQueuesOwnEmptyState(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)
	f.tasks.escalated = nil

	body := fetch(t, f.mux, f.path(needsAttentionTabEscalated)).Body.String()

	assert.Contains(t, body, "No escalated tasks.")
	assert.NotContains(t, body, `data-krill="needs-attention-empty"`)
}

// TestNeedsAttentionFilteredEmptyIsNotAReadFailure is the FR's distinction
// between an answered-with-nothing read and a read that could not be
// answered: the failure keeps the console's own refusal shape, and neither
// renders as the other.
func TestNeedsAttentionFilteredEmptyIsNotAReadFailure(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)
	f.app.tasks = &failingNeedsAttentionTasks{
		needsAttentionTasks: *f.tasks,
		err:                 store.ErrTokenScopeMismatch,
	}

	target := f.filterPath(needsAttentionTabEscalated, url.Values{
		needsAttentionMilestoneParam: {needsAttentionFilterMilestone.String()},
	})

	// The results block alone, which is where the failure rides inline.
	body := fetchHX(t, f.mux, target, "ops-results").Body.String()
	assert.Contains(t, body, `data-krill="ops-inline-error"`)
	assert.NotContains(t, body, `data-krill="needs-attention-empty"`)
	assert.NotContains(t, body, "match these filters")

	// The panel-swap shape keeps the strip and the filter bar, so the
	// operator can change the filter or switch tabs rather than being
	// stranded on a dead table.
	panel := fetchHX(t, f.mux, target, "krill-needs-attention-panel").Body.String()
	assert.Contains(t, panel, `data-krill="ops-inline-error"`)
	assert.Contains(t, panel, `data-krill="needs-attention-filters"`)
	assert.NotContains(t, panel, `data-krill="needs-attention-empty"`)

	// The full-page shape keeps the shell and the console's own query-error
	// page with a way back, never an empty table.
	page := fetch(t, f.mux, target).Body.String()
	assert.Contains(t, page, `data-krill="workspace-shell"`)
	assert.Contains(t, page, `data-krill="ops-query-error"`)
	assert.NotContains(t, page, `data-krill="needs-attention-empty"`)
}

// TestNeedsAttentionOpenNotesSaysAMilestoneFilterDropsSpecNotes is the
// FR's first bullet: a milestone filter excludes open notes attached to a
// spec entity -- those have no milestone -- and the Open notes tab says so
// when a milestone is selected.
func TestNeedsAttentionOpenNotesSaysAMilestoneFilterDropsSpecNotes(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)

	filtered := fetch(t, f.mux, f.filterPath(needsAttentionTabNotes, url.Values{
		needsAttentionMilestoneParam: {needsAttentionFilterMilestone.String()},
	})).Body.String()
	assert.Contains(t, filtered, `data-krill="needs-attention-notes-notice"`)
	assert.Contains(t, filtered, "Notes attached to a spec entity have no milestone",
		"the tab says what the milestone filter leaves out")

	unfiltered := fetch(t, f.mux, f.path(needsAttentionTabNotes)).Body.String()
	assert.NotContains(t, unfiltered, `data-krill="needs-attention-notes-notice"`,
		"with no milestone filter there is nothing to explain")

	// And no other tab carries the sentence: it is the notes queue's own.
	escalated := fetch(t, f.mux, f.filterPath(needsAttentionTabEscalated, url.Values{
		needsAttentionMilestoneParam: {needsAttentionFilterMilestone.String()},
	})).Body.String()
	assert.NotContains(t, escalated, `data-krill="needs-attention-notes-notice"`)
}

// ---------------------------------------------------------------------------
// 5. malformed input
// ---------------------------------------------------------------------------

// TestNeedsAttentionFilterRejectsMalformedValues refuses a mistyped filter
// the way the page's other URL inputs are refused: a value the URL spelled
// wrongly is the caller's error, never a table whose rows disagree with the
// filter the page claims is in force.
func TestNeedsAttentionFilterRejectsMalformedValues(t *testing.T) {
	f := newNeedsAttentionFilterFixture(t)

	for name, q := range map[string]url.Values{
		"milestone_id not a UUID": {needsAttentionMilestoneParam: {"not-a-uuid"}},
		"unknown reason":          {needsAttentionReasonParam: {"thrash"}},
	} {
		t.Run(name, func(t *testing.T) {
			rec := fetch(t, f.mux, f.filterPath(needsAttentionTabEscalated, q))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Empty(t, f.tasks.listFilters, "a refused filter reads nothing")
		})
	}
}
