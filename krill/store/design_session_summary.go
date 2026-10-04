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
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

	// OpenedBy is the identity of whoever opened this session, read by
	// joining design_session.opened_by_krill_session_id to the
	// krill_session that gated the open call (migration 003's
	// acting_iss/sub/kind).
	//
	// The join is a LEFT one on purpose. A design_session row stores no
	// operator identity of its own -- migration 008 makes that column
	// provenance only, and a later revision_event's attribution is never
	// resolved by following it back -- so when the krill_session row
	// cannot be read the honest answer is the zero Subject, and a caller
	// that renders a label must render none. Fabricating one here would
	// put an invented operator's name on a page whose whole point is
	// recording who actually did what.
	OpenedBy Subject
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

// designSessionAggregateCTEs is the derivation both aggregate reads share,
// verbatim: the latest revision event per session, the latest signoff per
// session, and the open-question replay narrowed to the `sessions` CTE the
// calling statement puts in front of it.
//
// It is a single const rather than one function called twice because the
// thing being protected is the SQL itself. SummarizeByProduct answers "every
// session of a product" and GetSummaryByID answers "this one session"; if
// each spelled the stage derivation and the open-question replay out for
// itself, the two could drift and a list and a detail would then disagree
// about the same session -- the exact failure the one-derivation rule in
// this file's package comment exists to prevent. A test that compares the
// two reads cannot catch a divergence in what "latest" means, only in what
// the answer is.
//
// $1 is the signoff event type in both callers. It is bound rather than
// inlined so the event-type vocabulary keeps exactly one owner (EventType's
// constants) and neither statement can be edited into naming a different
// round kind than the store does.
const designSessionAggregateCTEs = `
		latest_event AS (
			SELECT DISTINCT ON (re.session_id) re.session_id, re.event_type
			FROM revision_event re
			WHERE re.session_id IN (SELECT id FROM sessions)
			ORDER BY re.session_id, re.seq_no DESC
		),
		latest_signoff AS (
			SELECT DISTINCT ON (re.session_id) re.session_id, re.signoff_status
			FROM revision_event re
			WHERE re.event_type = $1
			  AND re.session_id IN (SELECT id FROM sessions)
			ORDER BY re.session_id, re.seq_no DESC
		),
		touches AS (
			SELECT
				re.session_id,
				re.seq_no,
				opened.elem ->> 'question_id' AS question_id,
				COALESCE((opened.elem ->> 'blocking')::boolean, false) AS blocking,
				'opened' AS action
			FROM revision_event re
			CROSS JOIN LATERAL jsonb_array_elements(
				CASE WHEN jsonb_typeof(re.open_questions_delta -> 'opened') = 'array'
					THEN re.open_questions_delta -> 'opened' ELSE '[]'::jsonb END
			) AS opened(elem)
			WHERE re.session_id IN (SELECT id FROM sessions)

			UNION ALL

			SELECT
				re.session_id,
				re.seq_no,
				trim(both '"' from resolved.elem::text) AS question_id,
				false AS blocking,
				'resolved' AS action
			FROM revision_event re
			CROSS JOIN LATERAL jsonb_array_elements(
				CASE WHEN jsonb_typeof(re.open_questions_delta -> 'resolved') = 'array'
					THEN re.open_questions_delta -> 'resolved' ELSE '[]'::jsonb END
			) AS resolved(elem)
			WHERE re.session_id IN (SELECT id FROM sessions)
		),
		ranked AS (
			SELECT
				session_id, blocking, action,
				ROW_NUMBER() OVER (PARTITION BY session_id, question_id ORDER BY seq_no DESC) AS rn
			FROM touches
		),
		open_counts AS (
			SELECT
				session_id,
				count(*) FILTER (WHERE blocking) AS blocking_count,
				count(*) FILTER (WHERE NOT blocking) AS non_blocking_count
			FROM ranked
			WHERE rn = 1 AND action = 'opened'
			GROUP BY session_id
		)`

// designSessionSummaryColumns is the projection both aggregate reads scan,
// in the order scanDesignSessionSummary expects -- the design_session row,
// the two derived stage inputs, the two open-question counts, and the
// opening operator's identity triple.
//
// krill_session is LEFT JOINed for OpenedBy: a design_session row carries no
// identity of its own (migration 008's boundary comment), so all three
// subject columns are NULL when the session row behind
// opened_by_krill_session_id cannot be read, and scanDesignSessionSummary
// turns that into the zero Subject rather than a made-up one.
const designSessionSummaryColumns = `
		SELECT
			ds.id, ds.scope_id, ds.product_id, ds.opening_submission,
			ds.opened_by_krill_session_id, ds.created_at,
			le.event_type, ls.signoff_status,
			COALESCE(oc.blocking_count, 0), COALESCE(oc.non_blocking_count, 0),
			ks.acting_iss, ks.acting_sub, ks.acting_kind
		FROM sessions ds
		LEFT JOIN latest_event le ON le.session_id = ds.id
		LEFT JOIN latest_signoff ls ON ls.session_id = ds.id
		LEFT JOIN open_counts oc ON oc.session_id = ds.id
		LEFT JOIN krill_session ks ON ks.id = ds.opened_by_krill_session_id`

// scanDesignSessionSummary reads one row of designSessionSummaryColumns into
// a DesignSessionSummary, deriving Stage through the one deriveStage
// implementation both aggregate reads share.
//
// The operator identity is read as three nullable strings rather than a
// scanned Subject: a NULL acting_iss means the krill_session row was not
// there, and that is the zero Subject, not a Subject with empty fields
// pretending to name somebody.
func scanDesignSessionSummary(row pgx.Row) (DesignSessionSummary, error) {
	var (
		summary       DesignSessionSummary
		openedBy      uuid.UUID
		eventType     *string
		latestSignoff *string
		openedIss     *string
		openedSub     *string
		openedKind    *string
	)
	if err := row.Scan(&summary.ID, &summary.ScopeID, &summary.ProductID, &summary.OpeningSubmission,
		&openedBy, &summary.CreatedAt, &eventType, &latestSignoff,
		&summary.OpenBlockingQuestions, &summary.OpenNonBlockingQuestions,
		&openedIss, &openedSub, &openedKind); err != nil {
		return DesignSessionSummary{}, err
	}
	summary.OpenedByKrillSessionID = SessionID(openedBy)
	summary.Stage = deriveStage(eventType, latestSignoff)
	if openedIss != nil && openedSub != nil && openedKind != nil {
		summary.OpenedBy = Subject{Iss: *openedIss, Sub: *openedSub, Kind: SubjectKind(*openedKind)}
	}
	return summary, nil
}

// SummarizeByProduct returns productID's whole design-session aggregate
// (design_session.go's interface method), newest session first.
//
// One statement, never a loop: every session's row, its latest revision
// event, its latest signoff, and its open-question counts all come back in
// a single round trip, so a caller replacing the old
// ListByProduct + ListLatestSignoffBySessionIDs + N-ListOpenQuestions loop
// makes one call per product instead of N+2.
//
// ErrNotFound (not an empty aggregate) when productID has no current
// product row, so a caller never reads "no sessions yet" where the truth
// is "no such product".
func (s designSessionStore) SummarizeByProduct(ctx context.Context, productID uuid.UUID) (ProductDesignSessionsSummary, error) {
	rows, err := s.pool.Query(ctx, `
		WITH owning_product AS (
			SELECT id FROM product WHERE id = $2 AND valid_to IS NULL
		),
		sessions AS (
			SELECT ds.id, ds.scope_id, ds.product_id, ds.opening_submission,
			       ds.opened_by_krill_session_id, ds.created_at
			FROM design_session ds, owning_product op
			WHERE ds.product_id = op.id
		),`+designSessionAggregateCTEs+`,`+designSessionSummaryColumns+`
		ORDER BY ds.created_at DESC, ds.id DESC
	`, string(EventTypeSignoff), productID)
	if err != nil {
		return ProductDesignSessionsSummary{}, fmt.Errorf("summarize design sessions by product: %w", err)
	}
	defer rows.Close()

	out := ProductDesignSessionsSummary{ProductID: productID, Sessions: []DesignSessionSummary{}}
	for rows.Next() {
		row, err := scanDesignSessionSummary(rows)
		if err != nil {
			return ProductDesignSessionsSummary{}, fmt.Errorf("scan design session summary: %w", err)
		}
		out.Sessions = append(out.Sessions, row)
		out.OpenBlockingQuestionCount += row.OpenBlockingQuestions
		if row.OpenBlockingQuestions > 0 {
			out.SessionsHoldingOpenBlocking++
		}
	}
	if err := rows.Err(); err != nil {
		return ProductDesignSessionsSummary{}, fmt.Errorf("summarize design sessions by product: %w", err)
	}

	if len(out.Sessions) == 0 {
		// No rows is ambiguous: either a live product with no sessions yet,
		// or no such product. Only the second is an error, so resolve it
		// with one cheap probe -- the common case (a product that does
		// exist) never pays for it.
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM product WHERE id = $1 AND valid_to IS NULL)`, productID).Scan(&exists); err != nil {
			return ProductDesignSessionsSummary{}, fmt.Errorf("check current product exists: %w", err)
		}
		if !exists {
			return ProductDesignSessionsSummary{}, fmt.Errorf("%w: product id %s", ErrNotFound, productID)
		}
	}

	return out, nil
}

// GetSummaryByID returns one session's DesignSessionSummary -- its row, its
// derived Stage, its open-question counts and the identity that opened it
// (design_session.go's interface method).
//
// The same statement SummarizeByProduct runs for a whole product, narrowed
// to this one session, over the same shared CTEs: a detail page and the
// list that links to it read one session's stage through one derivation, so
// the two can never disagree about what stage it is in. Re-deriving it
// here from a separate ListLatestSignoffBySessionIDs call is the N+1 this
// file was written to remove, in the shape of a single extra query.
//
// ErrNotFound (not a zero summary) when no such row exists, so a caller
// never renders a page for a session that was not there.
func (s designSessionStore) GetSummaryByID(ctx context.Context, id uuid.UUID) (DesignSessionSummary, error) {
	rows, err := s.pool.Query(ctx, `
		WITH sessions AS (
			SELECT ds.id, ds.scope_id, ds.product_id, ds.opening_submission,
			       ds.opened_by_krill_session_id, ds.created_at
			FROM design_session ds
			WHERE ds.id = $2
		),`+designSessionAggregateCTEs+`,`+designSessionSummaryColumns, string(EventTypeSignoff), id)
	if err != nil {
		return DesignSessionSummary{}, fmt.Errorf("get design session summary by id: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return DesignSessionSummary{}, fmt.Errorf("get design session summary by id: %w", err)
		}
		return DesignSessionSummary{}, fmt.Errorf("%w: design session id %s", ErrNotFound, id)
	}
	summary, err := scanDesignSessionSummary(rows)
	if err != nil {
		return DesignSessionSummary{}, fmt.Errorf("scan design session summary: %w", err)
	}
	// A second row would mean id named more than one session, which
	// design_session's primary key forbids; reading the first and not
	// draining would hide a real database fault behind a plausible page.
	rows.Close()
	if rows.Next() {
		return DesignSessionSummary{}, fmt.Errorf("get design session summary by id: %w", errMultipleDesignSessions)
	}
	return summary, rows.Err()
}

// errMultipleDesignSessions names the one fault GetSummaryByID refuses to
// paper over: more than one row for an id that is a primary key. It is a
// database fault, not a caller mistake, so it is distinct from ErrNotFound
// -- a caller that treated it as "no such session" would show a 404 for a
// session that does exist.
var errMultipleDesignSessions = errors.New("design session id matched more than one row")

// deriveStage is the single derivation Stage's doc comment specifies:
// the latest signoff's approval is terminal and wins over any later
// non-signoff event, otherwise the stage follows the latest revision
// event's type, and a session with no revision events yet is opened.
// Both arguments are nil for a session that has never received the
// corresponding kind of event.
func deriveStage(latestEventType, latestSignoff *string) Stage {
	if latestSignoff != nil && SignoffStatus(*latestSignoff) == SignoffStatusApproved {
		return StageApproved
	}
	if latestEventType == nil {
		return StageOpened
	}
	switch EventType(*latestEventType) {
	case EventTypeSignoff:
		return StageChangesRequested
	case EventTypeReconciliation:
		return StageArchitectReview
	case EventTypeDraft:
		return StageInDraft
	case EventTypeAnswer:
		return StageAnswered
	case EventTypeRuling:
		return StageRuled
	default:
		return StageOpened
	}
}
