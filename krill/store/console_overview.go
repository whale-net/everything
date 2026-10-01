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
	"time"
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
	_ = ctx
	_ = params
	return ConsoleOverviewCounts{}, ErrNotImplemented
}
