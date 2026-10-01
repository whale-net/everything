// This file (FR c4ab6c68) is the console Overview's counts: the one read
// behind the headline numbers an operator sees before touching any queue
// -- how deep each queue is, plus the three sub-line figures that say
// which of those need attention now (recent escalations, leases about to
// lapse, open scope-notes).
//
// Every number here is a COUNT(*) over the same FROM/JOIN/WHERE its
// underlying queue list pages over, so the figure and the list beneath it
// cannot come to describe different rows. The three sub-line figures are
// the shared query narrowed by one conjunct each (escalation time,
// lease expiry, note kind) rather than separate queries written to look
// similar.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// OverviewRecentEscalationWindow is how far back the Overview's
// "escalated recently" sub-line reaches, and OverviewLeaseExpiryWindow
// how far ahead its "lease about to lapse" sub-line looks. Named
// constants rather than literals at the call sites so the window is one
// figure in one place.
const (
	OverviewRecentEscalationWindow = time.Hour
	OverviewLeaseExpiryWindow      = 10 * time.Minute
)

// ConsoleOverviewParams is CountConsoleOverview's input: one params value
// per queue whose size the Overview shows, each the very same type the
// queue's own list read takes -- so a product or milestone filter applied
// to the list is applied to its count with no second filter vocabulary to
// keep in step. Now is the instant the two windows are measured back from
// and forward to; it is explicit rather than read from the clock inside
// the query so one Overview read answers for one moment.
type ConsoleOverviewParams struct {
	Escalated ListEscalatedTasksParams
	Claimed   ListClaimedTasksParams
	Cancelled ListCancelledTasksParams
	Notes     ListOpenNotesParams

	// Now is the reference instant for EscalatedRecently and
	// ClaimsExpiringSoon. The zero value means "the store's own clock at
	// read time".
	Now time.Time
}

// ConsoleOverviewCounts is CountConsoleOverview's result: the size of each
// queue the Overview shows, and the three sub-line figures beneath them.
// The four totals are the same numbers the per-queue count reads return
// for the same params, so a caller can take the Overview alone or the
// individual counts without either disagreeing with the other.
type ConsoleOverviewCounts struct {
	// Escalated, Claimed, Cancelled and OpenNotes are the four queue sizes.
	Escalated int
	Claimed   int
	Cancelled int
	OpenNotes int

	// EscalatedRecently counts the Escalated rows whose escalation was
	// recorded within OverviewRecentEscalationWindow of Now.
	EscalatedRecently int

	// ClaimsExpiringSoon counts the Claimed rows whose lease lapses within
	// OverviewLeaseExpiryWindow after Now.
	ClaimsExpiringSoon int

	// OpenScopeNotes counts the OpenNotes rows whose kind is
	// NoteKindScopeNote.
	OpenScopeNotes int
}

// CountConsoleOverview returns every number the console Overview shows, in
// one read: the four queue sizes and the three sub-line figures, each
// counted over the same rows the matching queue list would page.
//
// Every figure here fails with its query's error rather than degrading to
// 0 -- a console that could not count a queue must not render it as an
// empty one, which is indistinguishable from a genuinely idle queue.
func (s taskStore) CountConsoleOverview(ctx context.Context, params ConsoleOverviewParams) (ConsoleOverviewCounts, error) {
	now := params.Now
	if now.IsZero() {
		now = time.Now()
	}

	// Each queue's guard runs before any figure is counted, so a narrowing
	// that crosses a scope or product boundary refuses the whole Overview
	// rather than half of it. The three sub-line counts reuse their own
	// queue's params, so they are covered by the same four calls.
	for _, q := range []struct {
		scopeID uuid.UUID
		filter  ConsoleFilter
	}{
		{params.Escalated.ScopeID, params.Escalated.ConsoleFilter},
		{params.Claimed.ScopeID, params.Claimed.ConsoleFilter},
		{params.Cancelled.ScopeID, params.Cancelled.ConsoleFilter},
		{params.Notes.ScopeID, params.Notes.ConsoleFilter},
	} {
		if err := s.guardConsoleFilter(ctx, q.scopeID, q.filter); err != nil {
			return ConsoleOverviewCounts{}, err
		}
	}
	if params.Escalated.Reason != nil && !validEscalationReasons[*params.Escalated.Reason] {
		return ConsoleOverviewCounts{}, fmt.Errorf("%w: %q", ErrUnknownEscalationReason, *params.Escalated.Reason)
	}

	escalatedSQL, escalatedArgs := escalatedTasksQuery(params.Escalated)
	recentSQL, recentArgs := escalatedTasksQuerySince(params.Escalated, now.Add(-OverviewRecentEscalationWindow))
	claimedSQL, claimedArgs := claimedTasksQuery(params.Claimed)
	expiringSQL, expiringArgs := claimedTasksQueryExpiringBefore(params.Claimed, now.Add(OverviewLeaseExpiryWindow))
	cancelledSQL, cancelledArgs := cancelledTasksQuery(params.Cancelled)
	notesSQL, notesArgs := openNotesQuery(params.Notes)
	scopeNotesSQL, scopeNotesArgs := openNotesQueryOfKind(params.Notes, NoteKindScopeNote)

	// Seven COUNT(*)s, each over the very clause the matching list pages
	// -- or, for the three sub-lines, that clause with one conjunct added.
	// The first failure returns its error and no partial figure: an
	// Overview that could not count something says so rather than
	// rendering it as empty.
	var counts ConsoleOverviewCounts
	for _, figure := range []struct {
		label string
		sql   string
		args  []any
		dst   *int
	}{
		{"escalated", escalatedSQL, escalatedArgs, &counts.Escalated},
		{"escalated recently", recentSQL, recentArgs, &counts.EscalatedRecently},
		{"claimed", claimedSQL, claimedArgs, &counts.Claimed},
		{"claims expiring soon", expiringSQL, expiringArgs, &counts.ClaimsExpiringSoon},
		{"cancelled", cancelledSQL, cancelledArgs, &counts.Cancelled},
		{"open notes", notesSQL, notesArgs, &counts.OpenNotes},
		{"open scope-notes", scopeNotesSQL, scopeNotesArgs, &counts.OpenScopeNotes},
	} {
		n, err := countConsoleRows(ctx, s.pool, figure.sql, figure.args)
		if err != nil {
			return ConsoleOverviewCounts{}, fmt.Errorf("count %s: %w", figure.label, err)
		}
		*figure.dst = n
	}
	return counts, nil
}
