// This file (FR d0a63ffb-8e80-47c1-8a64-2729d4950522) is the
// product-wide design-session aggregate read: one call that returns every
// design session of one product with its derived Stage and its open
// blocking/non-blocking question counts, plus the product-level blocking
// totals. It exists because the sessions table and every session detail
// page otherwise need ListLatestSignoffBySessionIDs plus one
// ListOpenQuestions per session -- an N+1 this file replaces.
//
// Stage is derived here, never stored: design_session has no stage column
// (migration 008's boundary comment -- a DesignSession's mutable state is
// entirely derived from its RevisionEventStore log). The open-question set
// is derived by exactly the replay open_questions.go's ListOpenQuestions
// performs, widened from one session to every session of a product in one
// statement -- never a second notion of "open".
package store

import (
	"context"

	"github.com/google/uuid"
)

// Stage is one design session's derived lifecycle position -- the single
// vocabulary every surface (UI badge, MCP response, HTTP wire) renders a
// session's state with. Derived, never stored: there is no `stage` column
// anywhere, exactly as there is no question-status table (see
// open_questions.go's package doc comment for why neither exists).
//
// The derivation, in one place so no caller re-derives it: take the
// session's latest signoff event and its latest revision event. If the
// latest signoff is approved, the session is StageApproved -- terminal,
// and it wins over any later non-signoff event's effect, matching the
// follow-up form being hidden once a session is signed off. Otherwise the
// stage follows the latest revision event's type:
//
//	latest signoff changes_requested -> StageChangesRequested
//	reconciliation                  -> StageArchitectReview
//	draft                           -> StageInDraft
//	answer                          -> StageAnswered
//	ruling                          -> StageRuled
//
// A session with no revision events yet is StageOpened.
type Stage string

const (
	StageOpened           Stage = "opened"
	StageApproved         Stage = "approved"
	StageChangesRequested Stage = "changes_requested"
	StageArchitectReview  Stage = "architect_review"
	StageInDraft          Stage = "in_draft"
	StageAnswered         Stage = "answered"
	StageRuled            Stage = "ruled"
)

// DesignSessionSummary is one session's row of the product-wide aggregate
// read: the design_session columns the sessions table already shows, plus
// the two values that otherwise cost an extra round trip each (Stage, and
// the open-question counts).
type DesignSessionSummary struct {
	// DesignSession is the underlying design_session row itself, carried
	// whole so this summary can never drift from ListByProduct's own
	// projection of the same row.
	DesignSession

	// Stage is derived per Stage's own doc comment -- never read from a
	// column, and never left empty.
	Stage Stage

	// OpenBlockingQuestions and OpenNonBlockingQuestions are this
	// session's currently-open question counts, split by the blocking flag
	// every OpenQuestion already carries. They count the same set
	// ListOpenQuestions returns for this session, never a second
	// notion of "open".
	OpenBlockingQuestions    int
	OpenNonBlockingQuestions int
}

// ProductDesignSessionsSummary is one product's whole design-session
// aggregate: every session newest-first, plus the two product-level totals
// the Overview and the sessions table header show.
type ProductDesignSessionsSummary struct {
	ProductID uuid.UUID

	// Sessions is every design session of ProductID, newest first
	// (created_at DESC, id DESC -- a stable tiebreak so two sessions
	// opened in one transaction keep one order across reads).
	Sessions []DesignSessionSummary

	// OpenBlockingQuestionCount is the product-wide total of open blocking
	// questions across every session above.
	OpenBlockingQuestionCount int

	// SessionsHoldingOpenBlocking is how many of Sessions hold at least
	// one open blocking question -- the "N sessions blocked" figure, which
	// is not derivable from the per-row counts alone.
	SessionsHoldingOpenBlocking int
}

// SummarizeByProduct returns productID's whole design-session aggregate
// (design_session.go's interface method), newest session first.
//
// Scaffold-phase stub: returns ErrNotImplemented unconditionally. This
// task's Implementation phase fills in the one-statement read this file's
// own doc comment describes -- design_session LEFT JOIN'd against the
// same open_questions_delta last-event-wins window open_questions.go's
// ListOpenQuestions already spells out, widened from one session to every
// session of the product, plus a DISTINCT ON (session_id) pass for each
// session's latest revision event.
func (s designSessionStore) SummarizeByProduct(ctx context.Context, productID uuid.UUID) (ProductDesignSessionsSummary, error) {
	return ProductDesignSessionsSummary{}, ErrNotImplemented
}
