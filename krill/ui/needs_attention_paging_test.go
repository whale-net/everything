// The Needs attention paging footer's contract (FR 7f10bd5d): the total
// comes from the count read for the SAME filters the rows were read with,
// Previous and Next are disabled at their ends, paging keeps the tab and
// the filters, a continuation token from another scope or filter set is
// refused with an inline error and a way back, and Refresh re-requests the
// operator's actual URL.
//
// The fixture is a real pager, not a single-row fake: every list read pages
// a filtered slice through the store's own ResolvePageSize /
// EncodeFilteredContinuationToken / DecodeFilteredContinuationToken
// machinery, and every count read counts that same filtered slice under the
// same params type the matching list took. So "the footer's total and the
// rows are the same set" is asserted against a read that could actually
// disagree with it, and a cross-filter token is refused by the store's own
// decoder rather than by a stand-in error.
package main

import (
	"context"
	"html"
	"net/http"
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

// pagingRead is one recorded read: the tab it served, the filter set a
// continuation token would bind, and the product the read was narrowed to,
// so the rows and the footer's total can be proved to describe one set.
type pagingRead struct {
	tab     string
	filters store.FilterSet
	product *uuid.UUID
}

// pagingNeedsAttentionTasks is the page's store surface as a working pager:
// each list read narrows the fixture to the filter's container (and, on the
// escalated queue, its reason), pages it with the store's own machinery, and
// mints a real store token when more rows remain; each count read answers
// the size of the same filtered set.
type pagingNeedsAttentionTasks struct {
	store.TaskStore

	// scopeID is the scope the fake decodes tokens against -- the same one
	// the shell resolves (chromeScopeID), so a token minted for another scope
	// really is refused.
	scopeID uuid.UUID

	claimed   []store.ClaimedTaskRow
	escalated []store.EscalatedTaskRow
	cancelled []store.CancelledTaskRow
	notes     []store.OpenNoteRow

	lists   []pagingRead
	counts  []pagingRead
	listErr error
}

// pagingByContainer keeps only rows whose delivery container is the one the
// milestone filter names -- the store's own `ref.id = $n OR
// ref.parent_milestone_id = $n` narrowing, miniatured. A nil filter keeps
// every row. A row with no container (a spec-entity note) is dropped when
// the filter is set: it has no milestone to match.
func pagingByContainer[T any](rows []T, milestone *uuid.UUID, containerOf func(T) uuid.UUID) []T {
	if milestone == nil {
		return rows
	}
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		if containerOf(row) == *milestone {
			out = append(out, row)
		}
	}
	return out
}

// pagingSlice is the store's page shape, miniatured: resolve the page size,
// resume AFTER the cursor the token names, and mint a token exactly when a
// row remained beyond the page -- the same "fetch one extra, its presence
// decides NextToken" rule the real queries use.
func pagingSlice[T any](scopeID uuid.UUID, filters store.FilterSet, page store.PageParams, rows []T,
	idOf func(T) uuid.UUID, sortKeyOf func(T) string) (store.Page[T], error) {

	size := store.ResolvePageSize(page.PageSize)
	start := 0
	if page.ContinuationToken != "" {
		cursor, err := store.DecodeFilteredContinuationToken(scopeID, filters, page.ContinuationToken)
		if err != nil {
			return store.Page[T]{}, err
		}
		start = len(rows)
		for i, row := range rows {
			if idOf(row) == cursor.ID {
				start = i + 1
				break
			}
		}
	}
	end := start + size
	if end > len(rows) {
		end = len(rows)
	}
	out := store.Page[T]{Items: rows[start:end]}
	if end < len(rows) {
		last := rows[end-1]
		out.NextToken = store.EncodeFilteredContinuationToken(scopeID, filters, store.Cursor{
			SortKey: sortKeyOf(last),
			ID:      idOf(last),
		})
	}
	return out, nil
}

func (s *pagingNeedsAttentionTasks) record(list *[]pagingRead, tab string, filters store.FilterSet, product *uuid.UUID) {
	*list = append(*list, pagingRead{tab: tab, filters: filters, product: product})
}

func (s *pagingNeedsAttentionTasks) ListClaimedTasks(_ context.Context, p store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	s.record(&s.lists, needsAttentionTabClaimed, p.Filters(), p.ProductID)
	rows := pagingByContainer(s.claimed, p.MilestoneID, func(r store.ClaimedTaskRow) uuid.UUID { return r.DeliveryRef.ID })
	page, err := pagingSlice(p.ScopeID, p.Filters(), p.Page, rows,
		func(r store.ClaimedTaskRow) uuid.UUID { return r.TaskID },
		func(r store.ClaimedTaskRow) string { return r.LeaseExpiresAt.UTC().Format(time.RFC3339Nano) })
	s.listErr = err
	return page, err
}

func (s *pagingNeedsAttentionTasks) ListEscalatedTasks(_ context.Context, p store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	s.record(&s.lists, needsAttentionTabEscalated, p.Filters(), p.ProductID)
	rows := pagingByContainer(s.escalated, p.MilestoneID, func(r store.EscalatedTaskRow) uuid.UUID { return r.DeliveryRef.ID })
	if p.Reason != nil {
		kept := make([]store.EscalatedTaskRow, 0, len(rows))
		for _, row := range rows {
			if row.Reason == *p.Reason {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	page, err := pagingSlice(p.ScopeID, p.Filters(), p.Page, rows,
		func(r store.EscalatedTaskRow) uuid.UUID { return r.TaskID },
		func(r store.EscalatedTaskRow) string { return r.EscalatedAt.UTC().Format(time.RFC3339Nano) })
	s.listErr = err
	return page, err
}

func (s *pagingNeedsAttentionTasks) ListCancelledTasks(_ context.Context, p store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	s.record(&s.lists, needsAttentionTabCancelled, p.Filters(), p.ProductID)
	rows := pagingByContainer(s.cancelled, p.MilestoneID, func(r store.CancelledTaskRow) uuid.UUID { return r.DeliveryRef.ID })
	page, err := pagingSlice(p.ScopeID, p.Filters(), p.Page, rows,
		func(r store.CancelledTaskRow) uuid.UUID { return r.TaskID },
		func(r store.CancelledTaskRow) string { return r.CancelledAt.UTC().Format(time.RFC3339Nano) })
	s.listErr = err
	return page, err
}

func (s *pagingNeedsAttentionTasks) ListOpenNotes(_ context.Context, p store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	s.record(&s.lists, needsAttentionTabNotes, p.Filters(), p.ProductID)
	rows := pagingByContainer(s.notes, p.MilestoneID, pagingNoteContainer)
	page, err := pagingSlice(p.ScopeID, p.Filters(), p.Page, rows,
		func(r store.OpenNoteRow) uuid.UUID { return r.NoteID },
		func(r store.OpenNoteRow) string { return r.CreatedAt.UTC().Format(time.RFC3339Nano) })
	s.listErr = err
	return page, err
}

// pagingNoteContainer is a note's delivery container: the task it targets,
// when it targets one. A spec-entity note has none, which is why a milestone
// filter drops it (store.ListOpenNotesParams' own rule).
func pagingNoteContainer(r store.OpenNoteRow) uuid.UUID {
	if r.TaskContext == nil {
		return uuid.Nil
	}
	return r.TaskContext.DeliveryRef.ID
}

func (s *pagingNeedsAttentionTasks) CountClaimedTasks(_ context.Context, p store.ListClaimedTasksParams) (int, error) {
	s.record(&s.counts, needsAttentionTabClaimed, p.Filters(), p.ProductID)
	return len(pagingByContainer(s.claimed, p.MilestoneID, func(r store.ClaimedTaskRow) uuid.UUID { return r.DeliveryRef.ID })), nil
}

func (s *pagingNeedsAttentionTasks) CountEscalatedTasks(_ context.Context, p store.ListEscalatedTasksParams) (int, error) {
	s.record(&s.counts, needsAttentionTabEscalated, p.Filters(), p.ProductID)
	rows := pagingByContainer(s.escalated, p.MilestoneID, func(r store.EscalatedTaskRow) uuid.UUID { return r.DeliveryRef.ID })
	if p.Reason != nil {
		kept := make([]store.EscalatedTaskRow, 0, len(rows))
		for _, row := range rows {
			if row.Reason == *p.Reason {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	return len(rows), nil
}

func (s *pagingNeedsAttentionTasks) CountCancelledTasks(_ context.Context, p store.ListCancelledTasksParams) (int, error) {
	s.record(&s.counts, needsAttentionTabCancelled, p.Filters(), p.ProductID)
	return len(pagingByContainer(s.cancelled, p.MilestoneID, func(r store.CancelledTaskRow) uuid.UUID { return r.DeliveryRef.ID })), nil
}

func (s *pagingNeedsAttentionTasks) CountOpenNotes(_ context.Context, p store.ListOpenNotesParams) (int, error) {
	s.record(&s.counts, needsAttentionTabNotes, p.Filters(), p.ProductID)
	return len(pagingByContainer(s.notes, p.MilestoneID, pagingNoteContainer)), nil
}

// seed fills every queue: three rows under the filter milestone, one under
// another milestone, and -- on the notes queue -- one with no container at
// all, so a filtered read has a row to exclude of each kind.
func (s *pagingNeedsAttentionTasks) seed() {
	in, out := needsAttentionFilterMilestone, needsAttentionFilterOtherMilestone

	s.claimed = []store.ClaimedTaskRow{}
	s.escalated = []store.EscalatedTaskRow{}
	s.cancelled = []store.CancelledTaskRow{}
	s.notes = []store.OpenNoteRow{}

	for _, title := range []string{"claimed one", "claimed two", "claimed three"} {
		s.claimed = append(s.claimed, pagingClaimedRow(title, in))
	}
	s.claimed = append(s.claimed, pagingClaimedRow("claimed other", out))

	s.escalated = append(s.escalated,
		pagingEscalatedRow("escalated one", in, store.EscalationReasonThrashCap),
		pagingEscalatedRow("escalated two", in, store.EscalationReasonThrashCap),
		pagingEscalatedRow("escalated three", in, store.EscalationReasonManual),
		pagingEscalatedRow("escalated other", out, store.EscalationReasonThrashCap),
	)

	for _, title := range []string{"cancelled one", "cancelled two", "cancelled three"} {
		s.cancelled = append(s.cancelled, pagingCancelledRow(title, in))
	}
	s.cancelled = append(s.cancelled, pagingCancelledRow("cancelled other", out))

	for _, body := range []string{"note one", "note two", "note three"} {
		s.notes = append(s.notes, pagingNoteRow(body, in))
	}
	s.notes = append(s.notes, pagingNoteRow("note other", out))
	s.notes = append(s.notes, pagingEntityNoteRow("a spec-entity note"))
}

func pagingClaimedRow(title string, container uuid.UUID) store.ClaimedTaskRow {
	return store.ClaimedTaskRow{
		TaskID:         uuid.New(),
		Title:          title,
		DeliveryRef:    store.ClaimedTaskDeliveryRef{ID: container, Kind: store.MilestoneKindMilestone, Title: "M"},
		ClaimID:        uuid.New(),
		CurrentLane:    store.LaneImplementation,
		LeaseExpiresAt: needsAttentionNow.Add(time.Minute),
	}
}

func pagingEscalatedRow(title string, container uuid.UUID, reason store.EscalationReason) store.EscalatedTaskRow {
	return store.EscalatedTaskRow{
		TaskID:       uuid.New(),
		Title:        title,
		DeliveryRef:  store.EscalatedTaskDeliveryRef{ID: container, Kind: store.MilestoneKindMilestone, Title: "M"},
		EscalationID: uuid.New(),
		Reason:       reason,
		Lane:         store.LaneImplementation,
		EscalatedAt:  needsAttentionNow.Add(-time.Minute),
		AttemptCount: 2,
	}
}

func pagingCancelledRow(title string, container uuid.UUID) store.CancelledTaskRow {
	return store.CancelledTaskRow{
		TaskID:      uuid.New(),
		Title:       title,
		DeliveryRef: store.CancelledTaskDeliveryRef{ID: container, Kind: store.MilestoneKindMilestone, Title: "M"},
		CancelledAt: needsAttentionNow.Add(-time.Minute),
	}
}

func pagingNoteRow(body string, container uuid.UUID) store.OpenNoteRow {
	return store.OpenNoteRow{
		NoteID:    uuid.New(),
		Kind:      store.NoteKindScopeNote,
		Body:      body,
		CreatedAt: needsAttentionNow.Add(-time.Minute),
		TaskContext: &store.OpenNoteTaskContext{
			TaskID:      uuid.New(),
			Title:       "a note target",
			DeliveryRef: store.ClaimedTaskDeliveryRef{ID: container, Kind: store.MilestoneKindMilestone, Title: "M"},
		},
	}
}

// pagingEntityNoteRow is a note on a spec-axis entity: it has no delivery
// container, so a milestone filter excludes it (and the tab says so).
func pagingEntityNoteRow(body string) store.OpenNoteRow {
	return store.OpenNoteRow{
		NoteID:    uuid.New(),
		Kind:      store.NoteKindScopeNote,
		Body:      body,
		CreatedAt: needsAttentionNow.Add(-time.Minute),
		EntityContext: &store.OpenNoteEntityContext{
			EntityKind: store.NoteEntityKindFeature,
			EntityID:   uuid.New(),
			Title:      "a spec entity",
		},
	}
}

// newNeedsAttentionPagingFixture wires the pager behind the page fixture,
// keeping the delivery listing so the milestone filter's label resolves.
func newNeedsAttentionPagingFixture(t *testing.T) (*needsAttentionFixture, *pagingNeedsAttentionTasks) {
	t.Helper()
	f := newNeedsAttentionFilterFixture(t)
	tasks := &pagingNeedsAttentionTasks{scopeID: chromeScopeID}
	tasks.seed()
	f.app.tasks = tasks
	return f, tasks
}

// pagingControl returns one footer control's whole element and whether it is
// a link rather than a disabled button -- which is the FR's whole distinction
// between "can move" and "disabled at its end".
func pagingControl(t *testing.T, body, control string) (string, bool) {
	t.Helper()
	marker := `data-krill="paging-` + control + `"`
	i := strings.Index(body, marker)
	require.GreaterOrEqual(t, i, 0, "the footer renders a %s control:\n%s", control, body)
	start := strings.LastIndex(body[:i], "<")
	require.GreaterOrEqual(t, start, 0)
	linked := strings.HasPrefix(body[start:], "<a ")
	close := "</button>"
	if linked {
		close = "</a>"
	}
	end := strings.Index(body[start:], close)
	require.GreaterOrEqual(t, end, 0, "the %s control is unterminated:\n%s", control, body)
	return body[start : start+end+len(close)], linked
}

// pagingNextHref reads the address the footer's Next control points at,
// unescaping the attribute so it can be requested as a URL.
func pagingNextHref(t *testing.T, body string) string {
	t.Helper()
	tag, linked := pagingControl(t, body, "next")
	require.True(t, linked, "Next is a link on this page, not a disabled control:\n%s", tag)
	return html.UnescapeString(optionAttribute(tag, "href"))
}

// pagingAssertsDisabled pins a control that must not move: a <button
// disabled>, never an <a> that would land the operator on a page they did
// not ask for.
func pagingAssertsDisabled(t *testing.T, body, control, label string) {
	t.Helper()
	tag, linked := pagingControl(t, body, control)
	assert.False(t, linked, "%s is rendered disabled, not as a link:\n%s", control, tag)
	assert.Contains(t, tag, "disabled", "%s carries the disabled attribute", control)
	assert.Contains(t, tag, ">"+label+"<", "%s is still present so its absence cannot read as a bug", control)
}

// assertFooterTotalIsTheCountForTheSameSet is (a): the footer's total and
// the rows above it were read under ONE filter set -- same product, same
// container, and on the escalated tab the same reason -- so the footer
// cannot foot a set the table did not render.
func assertFooterTotalIsTheCountForTheSameSet(t *testing.T, f *needsAttentionFixture, tasks *pagingNeedsAttentionTasks, tab string) {
	t.Helper()
	var list *pagingRead
	for i := range tasks.lists {
		if tasks.lists[i].tab == tab {
			list = &tasks.lists[i]
		}
	}
	require.NotNil(t, list, "the %s tab read its rows", tab)
	require.NotNil(t, list.product, "the row read is narrowed to a product")
	assert.Equal(t, f.pid, *list.product, "the row read is narrowed to the page's product")

	want := store.CanonicalFilterSet(list.filters)
	for _, count := range tasks.counts {
		if count.tab != tab || store.CanonicalFilterSet(count.filters) != want {
			continue
		}
		if assert.NotNil(t, count.product, "%s: the count names a product", tab) {
			assert.Equal(t, f.pid, *count.product, "%s: the count and the rows name the same product", tab)
		}
		return
	}
	t.Errorf("%s: no count read used the rows' filter set %s; counts were %+v", tab, want, tasks.counts)
}

// ---------------------------------------------------------------------------
// 1. the total, per tab, from a real pageable read
// ---------------------------------------------------------------------------

// TestNeedsAttentionPagingFooterTotalIsTheCountForTheSameSet walks all four
// tabs against a working pager: three rows match the filter and are paged
// two-at-a-time, so the footer must read "Showing 2 of 3" -- the third row
// is the page the store minted a token for, and the excluded other-milestone
// row must not be in either the two on screen or the total of three.
func TestNeedsAttentionPagingFooterTotalIsTheCountForTheSameSet(t *testing.T) {
	f, tasks := newNeedsAttentionPagingFixture(t)

	for _, tc := range []struct{ tab, excluded string }{
		{needsAttentionTabEscalated, "escalated other"},
		{needsAttentionTabClaimed, "claimed other"},
		{needsAttentionTabCancelled, "cancelled other"},
		{needsAttentionTabNotes, "note other"},
	} {
		t.Run(tc.tab, func(t *testing.T) {
			tasks.lists, tasks.counts = nil, nil

			body := fetch(t, f.mux, f.filterPath(tc.tab, url.Values{
				needsAttentionMilestoneParam: {needsAttentionFilterMilestone.String()},
				opsPageSizeParam:             {"2"},
			})).Body.String()

			assert.Contains(t, body, `data-krill="paging-summary">Showing 2 of 3 tasks<`,
				"the footer's total is the matching count, not the page's length")
			assert.Equal(t, 3, needsAttentionBadge(t, body, tc.tab),
				"the badge and the footer answer the same count read")
			assert.NotContains(t, body, tc.excluded,
				"a row outside the milestone filter reaches neither the table nor the total")
			assertFooterTotalIsTheCountForTheSameSet(t, f, tasks, tc.tab)
		})
	}

	// The notes filter also drops the spec-entity note, which has no
	// container to match -- and the count drops it too, so the total stays
	// three rather than four.
	assert.NotContains(t, fetch(t, f.mux, f.filterPath(needsAttentionTabNotes, url.Values{
		needsAttentionMilestoneParam: {needsAttentionFilterMilestone.String()},
	})).Body.String(), "a spec-entity note")
}

// TestNeedsAttentionEscalatedPagingTotalBindsTheReasonFilter is the reason
// half of "same filters": the escalated footer's total is the count read
// WITH the reason the rows were read with, so a reason selection narrows the
// total as well as the table.
func TestNeedsAttentionEscalatedPagingTotalBindsTheReasonFilter(t *testing.T) {
	f, tasks := newNeedsAttentionPagingFixture(t)

	body := fetch(t, f.mux, f.filterPath(needsAttentionTabEscalated, url.Values{
		needsAttentionMilestoneParam: {needsAttentionFilterMilestone.String()},
		needsAttentionReasonParam:    {string(store.EscalationReasonThrashCap)},
	})).Body.String()

	assert.Contains(t, body, `data-krill="paging-summary">Showing 2 of 2 tasks<`,
		"the reason filter narrows the total to the rows it left")
	assert.Equal(t, 2, needsAttentionBadge(t, body, needsAttentionTabEscalated))
	assert.NotContains(t, body, "escalated three", "the manual row is outside the reason filter")
	assertFooterTotalIsTheCountForTheSameSet(t, f, tasks, needsAttentionTabEscalated)
}

// ---------------------------------------------------------------------------
// 2. the ends: Next moves, Previous does not
// ---------------------------------------------------------------------------

// TestNeedsAttentionPagingNextCarriesTheTabTheFiltersAndTheToken is "paging
// keeps the tab and the filters": the Next address is the operator's own
// request URI with the store's token as its position, and following it lands
// on the same tab, under the same filters, one page on -- not on another
// tab's first page.
func TestNeedsAttentionPagingNextCarriesTheTabTheFiltersAndTheToken(t *testing.T) {
	f, _ := newNeedsAttentionPagingFixture(t)
	milestone := needsAttentionFilterMilestone.String()

	body := fetch(t, f.mux, f.filterPath(needsAttentionTabClaimed, url.Values{
		needsAttentionMilestoneParam: {milestone},
		opsPageSizeParam:             {"2"},
	})).Body.String()

	next := pagingNextHref(t, body)
	u, err := url.Parse(next)
	require.NoError(t, err)
	assert.Equal(t, productHref(f.pid, needsAttentionSuffix), u.Path,
		"the next page stays on the page's own path")
	q := u.Query()
	assert.Equal(t, needsAttentionTabClaimed, q.Get(needsAttentionTabParam), "the tab survives paging")
	assert.Equal(t, milestone, q.Get(needsAttentionMilestoneParam), "the milestone filter survives paging")
	assert.Equal(t, "2", q.Get(opsPageSizeParam), "the page size survives paging")
	assert.NotEmpty(t, q.Get(opsPageTokenParam), "the store's token is the page position")

	second := fetch(t, f.mux, next).Body.String()
	assert.Contains(t, second, "claimed three", "the next page is the rest of the same filtered set")
	assert.NotContains(t, second, "claimed one", "not a re-read of page one")
	assert.NotContains(t, second, "claimed other", "and not the row the filter excluded")
	assert.Contains(t, second, `data-krill-tab="claimed"`, "the tab the URL named is the tab shown")
}

// TestNeedsAttentionPagingControlsAtTheEnds is the FR's "Previous and Next
// are disabled at the ends": Next is a link while a following page exists
// and a disabled control on the last, while Previous is a disabled control
// wherever it appears -- never a link, and never one to a guessed page.
func TestNeedsAttentionPagingControlsAtTheEnds(t *testing.T) {
	f, _ := newNeedsAttentionPagingFixture(t)

	first := fetch(t, f.mux, f.filterPath(needsAttentionTabClaimed, url.Values{
		needsAttentionMilestoneParam: {needsAttentionFilterMilestone.String()},
		opsPageSizeParam:             {"2"},
	})).Body.String()

	// Page one of two: Next can move, Previous cannot.
	_, linked := pagingControl(t, first, "next")
	assert.True(t, linked, "a following page makes Next a real link")
	pagingAssertsDisabled(t, first, "previous", "Previous")

	last := fetch(t, f.mux, pagingNextHref(t, first)).Body.String()
	assert.Contains(t, last, `data-krill="paging-summary">Showing 1 of 3 tasks<`,
		"the last page still reports the whole filtered total")
	pagingAssertsDisabled(t, last, "next", "Next")
	pagingAssertsDisabled(t, last, "previous", "Previous")
	// The disabled Next says why, so an operator reaching for it is not left
	// to conclude the control is broken.
	assert.Contains(t, last, "This is the last page")

	// Previous is inert on EVERY page, because the store's keyset issues
	// forward-only tokens: a token names the position to resume AFTER, and
	// nothing in this layer names the position before the one on screen, so
	// there is no preceding-page address to build. A link here would be a
	// guess at a page the operator did not ask for.
	for _, body := range []string{first, last} {
		tag, linked := pagingControl(t, body, "previous")
		assert.False(t, linked, "Previous is never a link:\n%s", tag)
		assert.Contains(t, tag, "pages forward only", "and it says why it is inert")
	}
}

// ---------------------------------------------------------------------------
// 3. a token from another filter set, or another scope
// ---------------------------------------------------------------------------

// TestNeedsAttentionCrossFilterTokenIsRefusedWithAWayBack is (b) and (e):
// a token the store minted under one filter set, replayed under a changed
// milestone, is the caller's error -- 400 with the shell kept and a way
// back, an inline alert on the fragment paths, never an empty table and
// never a footer. The refusal is asserted to come from the store's own
// ErrTokenFilterMismatch, the sentinel DecodeFilteredContinuationToken
// actually returns for a cross-filter token.
func TestNeedsAttentionCrossFilterTokenIsRefusedWithAWayBack(t *testing.T) {
	f, tasks := newNeedsAttentionPagingFixture(t)

	// The store's own token, minted for the fixture milestone.
	token := store.EncodeFilteredContinuationToken(chromeScopeID,
		store.ConsoleFilter{ProductID: &f.pid, MilestoneID: &needsAttentionFilterMilestone}.Filters(),
		store.Cursor{SortKey: needsAttentionNow.UTC().Format(time.RFC3339Nano), ID: uuid.New()})

	// Replayed under a DIFFERENT milestone: the filter set the page now
	// applies is not the one the token binds.
	target := f.filterPath(needsAttentionTabClaimed, url.Values{
		needsAttentionMilestoneParam: {needsAttentionFilterOtherMilestone.String()},
		opsPageTokenParam:            {token},
	})

	rec := fetch(t, f.mux, target)
	require.Equal(t, http.StatusBadRequest, rec.Code,
		"a cross-filter token is the caller's error, not a 500")
	require.ErrorIs(t, tasks.listErr, store.ErrTokenFilterMismatch,
		"the refusal is the store's own cross-filter sentinel")
	body := rec.Body.String()
	assert.Contains(t, body, `data-krill="workspace-shell"`, "the full-page refusal keeps the shell")
	assert.Contains(t, body, `data-krill="ops-query-error"`, "with krill's own message and a way back")
	// The way back is the view's own URI with the rejected token dropped, so
	// following it is itself the recovery.
	back, err := url.Parse(target)
	require.NoError(t, err)
	q := back.Query()
	q.Del(opsPageTokenParam)
	back.RawQuery = q.Encode()
	assert.Contains(t, body, `href="`+strings.ReplaceAll(back.String(), "&", "&amp;")+`"`)
	assert.NotContains(t, body, "krill/store", "no package-qualified store text reaches the operator")
	assert.NotContains(t, body, "different filter set")
	assertPagingRefusalHasNoTable(t, body)

	// The panel swap keeps the strip and the filter bar, so the operator can
	// change the filter rather than being stranded.
	panel := fetchHX(t, f.mux, target, pages.NeedsAttentionAnchor)
	assert.Equal(t, http.StatusOK, panel.Code)
	panelBody := panel.Body.String()
	assert.Contains(t, panelBody, `id="`+pages.NeedsAttentionAnchor+`"`)
	assert.Contains(t, panelBody, `data-krill="needs-attention-tabs"`)
	assert.Contains(t, panelBody, `data-krill="ops-inline-error"`)
	assertPagingRefusalHasNoTable(t, panelBody)

	// The results block alone, which a poll or Refresh names.
	results := fetchHX(t, f.mux, target, "ops-results").Body.String()
	assert.Contains(t, results, `data-krill="ops-inline-error"`)
	assertPagingRefusalHasNoTable(t, results)
}

// assertPagingRefusalHasNoTable is (e): a rejected token renders neither an
// empty table nor a footer for a table that does not exist.
func assertPagingRefusalHasNoTable(t *testing.T, body string) {
	t.Helper()
	assert.NotContains(t, body, `data-krill="needs-attention-paging"`, "a rejection never renders a footer")
	assert.NotContains(t, body, `data-krill="needs-attention-empty"`, "nor the filtered-empty state")
	assert.NotContains(t, body, "No claimed tasks.", "nor the queue's own empty state")
	assert.NotContains(t, body, `data-krill="paging-summary"`, "nor a total for rows that were never read")
}

// TestNeedsAttentionCrossScopeTokenIsRefused is the other half of the
// rejection path: a token minted under another scope never becomes a path
// across the scope boundary, and it too is the caller's error at 400.
func TestNeedsAttentionCrossScopeTokenIsRefused(t *testing.T) {
	f, tasks := newNeedsAttentionPagingFixture(t)

	token := store.EncodeContinuationToken(uuid.New(),
		store.Cursor{SortKey: needsAttentionNow.UTC().Format(time.RFC3339Nano), ID: uuid.New()})

	target := f.filterPath(needsAttentionTabClaimed, url.Values{opsPageTokenParam: {token}})

	rec := fetch(t, f.mux, target)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.ErrorIs(t, tasks.listErr, store.ErrTokenScopeMismatch,
		"the refusal is the store's cross-scope sentinel")
	body := rec.Body.String()
	assert.Contains(t, body, `data-krill="workspace-shell"`)
	assert.Contains(t, body, `data-krill="ops-query-error"`)
	assert.NotContains(t, body, "krill/store")
	assertPagingRefusalHasNoTable(t, body)
}

// TestNeedsAttentionMalformedTokenIsRefused is the third sentinel: a token
// that does not decode at all is the same caller's error with the same
// shape, so the operator gets a way back rather than a 500.
func TestNeedsAttentionMalformedTokenIsRefused(t *testing.T) {
	f, tasks := newNeedsAttentionPagingFixture(t)

	target := f.filterPath(needsAttentionTabClaimed, url.Values{opsPageTokenParam: {"not-a-token"}})

	rec := fetch(t, f.mux, target)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.ErrorIs(t, tasks.listErr, store.ErrInvalidContinuationToken)
	assert.Contains(t, rec.Body.String(), `data-krill="ops-query-error"`)
	assertPagingRefusalHasNoTable(t, rec.Body.String())
}

// ---------------------------------------------------------------------------
// 4. Refresh re-requests the operator's actual URL
// ---------------------------------------------------------------------------

// TestNeedsAttentionRefreshKeepsTheOperatorsFilteredPagedURI is the FR's
// last sentence: a Refresh while filtered AND paged re-requests the actual
// request URI -- path and query -- rather than a rebuilt default route, so
// it cannot silently snap the operator back to page one of the whole
// product.
func TestNeedsAttentionRefreshKeepsTheOperatorsFilteredPagedURI(t *testing.T) {
	f, _ := newNeedsAttentionPagingFixture(t)
	milestone := needsAttentionFilterMilestone.String()

	first := f.filterPath(needsAttentionTabClaimed, url.Values{
		needsAttentionMilestoneParam: {milestone},
		opsPageSizeParam:             {"2"},
	})
	firstBody := fetch(t, f.mux, first).Body.String()
	assertRefreshHrefIs(t, firstBody, first)

	// Page two, reached the way an operator reaches it: by following Next.
	second := pagingNextHref(t, firstBody)
	secondBody := fetch(t, f.mux, second).Body.String()
	assertRefreshHrefIs(t, secondBody, second)

	// The refresh address is not the page's bare route: it keeps the tab,
	// the filter, the page size and the token.
	defaultRoute := productHref(f.pid, needsAttentionSuffix)
	assert.NotEqual(t, defaultRoute, second, "the second page is not the default route")
	u, err := url.Parse(second)
	require.NoError(t, err)
	assert.Equal(t, needsAttentionTabClaimed, u.Query().Get(needsAttentionTabParam))
	assert.Equal(t, milestone, u.Query().Get(needsAttentionMilestoneParam))
	assert.NotEmpty(t, u.Query().Get(opsPageTokenParam))
}

// assertRefreshHrefIs pins the Refresh control's hx-get against the exact
// URI the request carried.
func assertRefreshHrefIs(t *testing.T, body, uri string) {
	t.Helper()
	assert.Contains(t, body, `hx-get="`+strings.ReplaceAll(uri, "&", "&amp;")+`"`,
		"Refresh re-requests the operator's actual URI, %s", uri)
}
