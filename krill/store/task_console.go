// This file (issue #2869, FR4, NFR6) is M5's console query surface over
// store.TaskStore: the home for FR4's ListClaimedTasks (this task), FR5's
// escalated-task query (#2875), and FR10's cancelled-task query (#2873)
// -- all three share paging.go's keyset paging/continuation-token
// machinery and this file's own identifying-context join (a task's title
// plus its delivery reference, resolved from `milestone_ref`) rather than
// each rolling its own (this task's own issue body, "Why this task").
//
// FR4 is explicit that a bare-id result set does not satisfy this
// milestone (root plan issue #2851's Assumption 8: "they answer 'which
// ids', leaving the operator to cross-reference by hand, which is the
// posture this milestone exists to remove") -- every row here carries a
// real title and delivery reference, never just a surrogate id.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ClaimedTaskDeliveryRef is one ClaimedTaskRow's delivery-axis reference
// (LB6, M4 FR1) -- the `milestone_ref` row task.milestone_id names, of
// either Kind, never a Feature/Requirement id (mirrors Task.MilestoneID's
// own doc comment, task.go, for the row this is resolved from).
type ClaimedTaskDeliveryRef struct {
	ID    uuid.UUID
	Kind  MilestoneKind
	Title string
}

// ClaimedTaskRow is one row of ListClaimedTasks' page (FR4): everything
// an operator needs to act on a claimed task without a second lookup --
// task id, title, delivery reference, claimant, current lane, lease
// expiry, and attempt count.
type ClaimedTaskRow struct {
	TaskID      uuid.UUID
	Title       string
	DeliveryRef ClaimedTaskDeliveryRef

	// ClaimantSessionID/ClaimantActing/ClaimantOnBehalfOf are the current
	// claim's own session id and both LB4 subject pairs (task_claim) --
	// never task's own CreatedByActing/CreatedByOnBehalfOf, which name
	// whoever created the task, not whoever holds the live claim on it
	// right now.
	ClaimantSessionID  SessionID
	ClaimantActing     Subject
	ClaimantOnBehalfOf Subject

	CurrentLane    Lane
	LeaseExpiresAt time.Time
	AttemptCount   int
}

// ListClaimedTasksParams is ListClaimedTasks' input: scopeID (NFR1) plus
// this query's own PageParams (NFR6).
type ListClaimedTasksParams struct {
	ScopeID uuid.UUID
	Page    PageParams
}

// ListClaimedTasks returns scopeID's currently-claimed tasks (FR4),
// soonest-lease-to-lapse first (`task.lease_expires_at ASC, task.id ASC` --
// the useful operator order), bounded and continuable per PageParams
// (NFR6).
//
// Joins task (WHERE current_claim_id IS NOT NULL) to milestone_ref (the
// identifying delivery reference this file's own doc comment describes)
// and to task_claim (task.current_claim_id) for the claimant session and
// both LB4 subject pairs -- task_claim's created_by_acting/
// created_by_on_behalf_of columns name whoever created the claim (the
// claimant), per ClaimedTaskRow's own doc comment. task.current_claim_id
// carries no DB-level FK onto task_claim(id) (migration 015's own note:
// task_claim is created later in the same migration), so this join is
// enforced here in Go/SQL, not by the schema.
func (s taskStore) ListClaimedTasks(ctx context.Context, params ListClaimedTasksParams) (Page[ClaimedTaskRow], error) {
	pageSize := ResolvePageSize(params.Page.PageSize)

	var cursor *Cursor
	if params.Page.ContinuationToken != "" {
		c, err := DecodeContinuationToken(params.ScopeID, params.Page.ContinuationToken)
		if err != nil {
			return Page[ClaimedTaskRow]{}, err
		}
		cursor = &c
	}

	args := []any{params.ScopeID}
	query := `
		SELECT task.id, task.title, milestone_ref.id, milestone_ref.kind, milestone_ref.name,
			tc.session_id,
			tc.created_by_acting_iss, tc.created_by_acting_sub, tc.created_by_acting_kind,
			tc.created_by_on_behalf_of_iss, tc.created_by_on_behalf_of_sub, tc.created_by_on_behalf_of_kind,
			task.current_lane, task.lease_expires_at, task.attempt_count
		FROM task
		JOIN milestone_ref ON milestone_ref.id = task.milestone_id AND milestone_ref.valid_to IS NULL
		JOIN task_claim tc ON tc.id = task.current_claim_id
		WHERE task.scope_id = $1 AND task.current_claim_id IS NOT NULL
	`
	if cursor != nil {
		sortVal, err := time.Parse(time.RFC3339Nano, cursor.SortKey)
		if err != nil {
			return Page[ClaimedTaskRow]{}, fmt.Errorf("%w: malformed cursor sort key", ErrInvalidContinuationToken)
		}
		query += fmt.Sprintf(` AND (task.lease_expires_at > $%d OR (task.lease_expires_at = $%d AND task.id > $%d))`, len(args)+1, len(args)+1, len(args)+2)
		args = append(args, sortVal, cursor.ID)
	}
	// Fetch one extra row beyond pageSize -- its presence, not a second
	// COUNT query, is what decides whether NextToken is populated.
	query += fmt.Sprintf(` ORDER BY task.lease_expires_at ASC, task.id ASC LIMIT $%d`, len(args)+1)
	args = append(args, pageSize+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page[ClaimedTaskRow]{}, fmt.Errorf("query claimed tasks: %w", err)
	}
	defer rows.Close()

	var items []ClaimedTaskRow
	for rows.Next() {
		var row ClaimedTaskRow
		var deliveryKind string
		var sessionID uuid.UUID
		var actingKind, onBehalfOfKind string
		var currentLane string
		if err := rows.Scan(
			&row.TaskID, &row.Title, &row.DeliveryRef.ID, &deliveryKind, &row.DeliveryRef.Title,
			&sessionID,
			&row.ClaimantActing.Iss, &row.ClaimantActing.Sub, &actingKind,
			&row.ClaimantOnBehalfOf.Iss, &row.ClaimantOnBehalfOf.Sub, &onBehalfOfKind,
			&currentLane, &row.LeaseExpiresAt, &row.AttemptCount,
		); err != nil {
			return Page[ClaimedTaskRow]{}, fmt.Errorf("scan claimed task row: %w", err)
		}
		row.DeliveryRef.Kind = MilestoneKind(deliveryKind)
		row.ClaimantSessionID = SessionID(sessionID)
		row.ClaimantActing.Kind = SubjectKind(actingKind)
		row.ClaimantOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
		row.CurrentLane = Lane(currentLane)
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return Page[ClaimedTaskRow]{}, fmt.Errorf("iterate claimed tasks: %w", err)
	}

	page := Page[ClaimedTaskRow]{Items: items}
	if len(items) > pageSize {
		page.Items = items[:pageSize]
		last := page.Items[pageSize-1]
		page.NextToken = EncodeContinuationToken(params.ScopeID, Cursor{
			SortKey: last.LeaseExpiresAt.Format(time.RFC3339Nano),
			ID:      last.TaskID,
		})
	}
	return page, nil
}

// CancelledTaskDeliveryRef is one CancelledTaskRow's delivery-axis
// reference (LB6, FR10) -- the same `milestone_ref` row task.milestone_id
// names, of either Kind, that ClaimedTaskDeliveryRef (FR4) names for a
// claimed row. Kept as its own type, not a shared alias, since FR10's own
// query resolves it independently rather than composing FR4's.
type CancelledTaskDeliveryRef struct {
	ID    uuid.UUID
	Kind  MilestoneKind
	Title string
}

// CancelledTaskRow is one row of ListCancelledTasks' page (FR10, issue
// #2873): a cancelled task's id, title, delivery reference, and the
// cancellation's own acting/on-behalf-of subjects (NFR3) and timestamp --
// "what was dead-lettered, and who did it" answerable without a second
// lookup. CancelledByActing/CancelledByOnBehalfOf/CancelledAt are read
// from the task's own `task_intervention_event` row with action='cancel'
// -- CancelTask (task_cancel.go) refuses to cancel an already-cancelled
// task (ErrTaskAlreadyCancelled), so exactly one such row exists per
// cancelled task.
type CancelledTaskRow struct {
	TaskID      uuid.UUID
	Title       string
	DeliveryRef CancelledTaskDeliveryRef

	CancelledByActing     Subject
	CancelledByOnBehalfOf Subject
	CancelledAt           time.Time
}

// ListCancelledTasksParams is ListCancelledTasks' input: scopeID (NFR1)
// plus this query's own PageParams (NFR6).
type ListCancelledTasksParams struct {
	ScopeID uuid.UUID
	Page    PageParams
}

// ListCancelledTasks returns every cancelled task in params.ScopeID
// (FR10), most-recently-cancelled first (`task_intervention_event.
// created_at DESC, task.id DESC` -- the useful operator order: "what was
// just dead-lettered"), bounded and continuable per params.Page (NFR6).
//
// Joins task (WHERE cancelled_at IS NOT NULL) to milestone_ref (the
// identifying delivery reference this file's own doc comment describes)
// and to the task's own cancel task_intervention_event row (the acting/
// on-behalf-of subjects and timestamp FR10 requires) -- one query, never
// a per-row second lookup.
func (s taskStore) ListCancelledTasks(ctx context.Context, params ListCancelledTasksParams) (Page[CancelledTaskRow], error) {
	pageSize := ResolvePageSize(params.Page.PageSize)

	var cursor *Cursor
	if params.Page.ContinuationToken != "" {
		c, err := DecodeContinuationToken(params.ScopeID, params.Page.ContinuationToken)
		if err != nil {
			return Page[CancelledTaskRow]{}, err
		}
		cursor = &c
	}

	args := []any{params.ScopeID}
	query := `
		SELECT task.id, task.title, milestone_ref.id, milestone_ref.kind, milestone_ref.name,
			ev.created_by_acting_iss, ev.created_by_acting_sub, ev.created_by_acting_kind,
			ev.created_by_on_behalf_of_iss, ev.created_by_on_behalf_of_sub, ev.created_by_on_behalf_of_kind,
			ev.created_at
		FROM task
		JOIN milestone_ref ON milestone_ref.id = task.milestone_id AND milestone_ref.valid_to IS NULL
		JOIN task_intervention_event ev ON ev.task_id = task.id AND ev.action = 'cancel'
		WHERE task.scope_id = $1 AND task.cancelled_at IS NOT NULL
	`
	if cursor != nil {
		sortVal, err := time.Parse(time.RFC3339Nano, cursor.SortKey)
		if err != nil {
			return Page[CancelledTaskRow]{}, fmt.Errorf("%w: malformed cursor sort key", ErrInvalidContinuationToken)
		}
		query += fmt.Sprintf(` AND (ev.created_at < $%d OR (ev.created_at = $%d AND task.id < $%d))`, len(args)+1, len(args)+1, len(args)+2)
		args = append(args, sortVal, cursor.ID)
	}
	// Fetch one extra row beyond pageSize -- its presence, not a second
	// COUNT query, is what decides whether NextToken is populated.
	query += fmt.Sprintf(` ORDER BY ev.created_at DESC, task.id DESC LIMIT $%d`, len(args)+1)
	args = append(args, pageSize+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page[CancelledTaskRow]{}, fmt.Errorf("query cancelled tasks: %w", err)
	}
	defer rows.Close()

	var items []CancelledTaskRow
	for rows.Next() {
		var row CancelledTaskRow
		var kind string
		var actingKind, onBehalfOfKind string
		if err := rows.Scan(
			&row.TaskID, &row.Title, &row.DeliveryRef.ID, &kind, &row.DeliveryRef.Title,
			&row.CancelledByActing.Iss, &row.CancelledByActing.Sub, &actingKind,
			&row.CancelledByOnBehalfOf.Iss, &row.CancelledByOnBehalfOf.Sub, &onBehalfOfKind,
			&row.CancelledAt,
		); err != nil {
			return Page[CancelledTaskRow]{}, fmt.Errorf("scan cancelled task row: %w", err)
		}
		row.DeliveryRef.Kind = MilestoneKind(kind)
		row.CancelledByActing.Kind = SubjectKind(actingKind)
		row.CancelledByOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return Page[CancelledTaskRow]{}, fmt.Errorf("iterate cancelled tasks: %w", err)
	}

	page := Page[CancelledTaskRow]{Items: items}
	if len(items) > pageSize {
		page.Items = items[:pageSize]
		last := page.Items[pageSize-1]
		page.NextToken = EncodeContinuationToken(params.ScopeID, Cursor{
			SortKey: last.CancelledAt.Format(time.RFC3339Nano),
			ID:      last.TaskID,
		})
	}
	return page, nil
}

// EscalatedTaskDeliveryRef is one EscalatedTaskRow's delivery-axis
// reference (LB6, FR5) -- the same `milestone_ref` row task.milestone_id
// names, of either Kind, that ClaimedTaskDeliveryRef/CancelledTaskDeliveryRef
// name for their own rows. Kept as its own type rather than a shared alias,
// mirroring CancelledTaskDeliveryRef's own doc comment on why: this query
// resolves it independently rather than composing FR4's.
type EscalatedTaskDeliveryRef struct {
	ID    uuid.UUID
	Kind  MilestoneKind
	Title string
}

// EscalatedTaskRow is one row of ListEscalatedTasks' page (FR5, issue
// #2875): everything an operator needs to tell "what is stuck, and why"
// without a second lookup -- task id, title, delivery reference, the
// escalation's own reason/counter/cap/lane/timestamp/acting subject, and
// **summary history only** (FR5, NFR6, #2851 Assumption 11): attempt
// count, failing-verdict count, the most recent verdict, and note count --
// never the task's full attempt/verdict/note history inline. The drill-in
// for that full history is the already-shipped M4 FR10 per-task fetch
// (GET /tasks/{id}), not this row.
//
// CounterValue/CapValue are nil for EscalationReasonManual (no triggering
// counter, mirroring EscalationEvent's own doc comment) and both non-nil
// for the two automatic reasons.
//
// MostRecentVerdict is derived, not read back from a stored column: M4's
// CompleteTask (task_complete.go) records a task_attempt row with
// outcome='completed' for every completion, pass or fail alike -- neither
// task_attempt nor any other table this milestone's migration 016 ships
// carries a persisted pass/fail value per attempt (this task's own "no new
// migration" constraint rules out adding one). The one case a verdict is
// nonetheless knowable with certainty is EscalationReasonThrashCap: FR2
// defines a thrash-cap escalation as being tripped BY a failing verdict, so
// MostRecentVerdict is VerdictFail there, deterministically, not inferred.
// For EscalationReasonAttemptCap and EscalationReasonManual, the
// escalation's own triggering event (a lapse, an abandon, a release, or an
// operator's manual call) is never itself a verdict -- any earlier
// CompleteTask verdict against the task predates that event with no
// durable link back to it, so MostRecentVerdict is nil rather than a
// guess.
type EscalatedTaskRow struct {
	TaskID      uuid.UUID
	Title       string
	DeliveryRef EscalatedTaskDeliveryRef

	Reason       EscalationReason
	CounterValue *int
	CapValue     *int

	Lane        Lane
	EscalatedAt time.Time

	EscalatedByActing     Subject
	EscalatedByOnBehalfOf Subject

	AttemptCount        int
	FailingVerdictCount int
	MostRecentVerdict   *Verdict
	NoteCount           int
}

// ListEscalatedTasksParams is ListEscalatedTasks' input: scopeID (NFR1)
// plus this query's own PageParams (NFR6).
type ListEscalatedTasksParams struct {
	ScopeID uuid.UUID
	Page    PageParams
}

// ListEscalatedTasks returns every task in params.ScopeID with an active
// escalation (task.current_escalation_id IS NOT NULL) -- FR5 -- most-
// recently-escalated first (`task_escalation_event.created_at DESC,
// task.id DESC`, matching the continuation cursor exactly), bounded and
// continuable per params.Page (NFR6).
//
// Joins task to milestone_ref (the identifying delivery reference this
// file's own doc comment describes) and to the task's one active
// task_escalation_event row (task.current_escalation_id) for the
// reason/counter/cap/timestamp/acting subject FR5 requires, plus a scalar
// subquery for the note count (FR5's summary-history rule: a count, never
// the note list itself -- ListNotesForTask/work.Assembler are the
// full-history path, M4 FR10). AttemptCount/FailingVerdictCount are read
// straight off task.attempt_count/task.thrash_count -- NFR4's two
// independently-fed counters -- never recomputed from task_attempt, which
// this query does not touch at all.
//
// A requeued task (current_escalation_id cleared) or a cancelled task
// (which is never escalated -- CancelTask and EscalateTask are distinct
// terminal/active states) never appears here: requeue's own drill-in is
// FR6, cancellation's is FR10's ListCancelledTasks above.
func (s taskStore) ListEscalatedTasks(ctx context.Context, params ListEscalatedTasksParams) (Page[EscalatedTaskRow], error) {
	pageSize := ResolvePageSize(params.Page.PageSize)

	var cursor *Cursor
	if params.Page.ContinuationToken != "" {
		c, err := DecodeContinuationToken(params.ScopeID, params.Page.ContinuationToken)
		if err != nil {
			return Page[EscalatedTaskRow]{}, err
		}
		cursor = &c
	}

	args := []any{params.ScopeID}
	query := `
		SELECT task.id, task.title, milestone_ref.id, milestone_ref.kind, milestone_ref.name,
			ev.reason, ev.counter_value, ev.cap_value, ev.created_at,
			ev.created_by_acting_iss, ev.created_by_acting_sub, ev.created_by_acting_kind,
			ev.created_by_on_behalf_of_iss, ev.created_by_on_behalf_of_sub, ev.created_by_on_behalf_of_kind,
			task.current_lane, task.attempt_count, task.thrash_count,
			(SELECT COUNT(*) FROM task_note WHERE task_note.task_id = task.id)
		FROM task
		JOIN milestone_ref ON milestone_ref.id = task.milestone_id AND milestone_ref.valid_to IS NULL
		JOIN task_escalation_event ev ON ev.id = task.current_escalation_id
		WHERE task.scope_id = $1 AND task.current_escalation_id IS NOT NULL
	`
	if cursor != nil {
		sortVal, err := time.Parse(time.RFC3339Nano, cursor.SortKey)
		if err != nil {
			return Page[EscalatedTaskRow]{}, fmt.Errorf("%w: malformed cursor sort key", ErrInvalidContinuationToken)
		}
		query += fmt.Sprintf(` AND (ev.created_at < $%d OR (ev.created_at = $%d AND task.id < $%d))`, len(args)+1, len(args)+1, len(args)+2)
		args = append(args, sortVal, cursor.ID)
	}
	// Fetch one extra row beyond pageSize -- its presence, not a second
	// COUNT query, is what decides whether NextToken is populated.
	query += fmt.Sprintf(` ORDER BY ev.created_at DESC, task.id DESC LIMIT $%d`, len(args)+1)
	args = append(args, pageSize+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page[EscalatedTaskRow]{}, fmt.Errorf("query escalated tasks: %w", err)
	}
	defer rows.Close()

	var items []EscalatedTaskRow
	for rows.Next() {
		var row EscalatedTaskRow
		var deliveryKind string
		var reason string
		var actingKind, onBehalfOfKind string
		var currentLane string
		if err := rows.Scan(
			&row.TaskID, &row.Title, &row.DeliveryRef.ID, &deliveryKind, &row.DeliveryRef.Title,
			&reason, &row.CounterValue, &row.CapValue, &row.EscalatedAt,
			&row.EscalatedByActing.Iss, &row.EscalatedByActing.Sub, &actingKind,
			&row.EscalatedByOnBehalfOf.Iss, &row.EscalatedByOnBehalfOf.Sub, &onBehalfOfKind,
			&currentLane, &row.AttemptCount, &row.FailingVerdictCount,
			&row.NoteCount,
		); err != nil {
			return Page[EscalatedTaskRow]{}, fmt.Errorf("scan escalated task row: %w", err)
		}
		row.DeliveryRef.Kind = MilestoneKind(deliveryKind)
		row.Reason = EscalationReason(reason)
		row.EscalatedByActing.Kind = SubjectKind(actingKind)
		row.EscalatedByOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
		row.Lane = Lane(currentLane)

		// EscalatedTaskRow's own doc comment: deterministically fail for
		// thrash-cap (FR2's own definition), nil for the other two reasons
		// -- never a guess.
		if row.Reason == EscalationReasonThrashCap {
			fail := VerdictFail
			row.MostRecentVerdict = &fail
		}

		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return Page[EscalatedTaskRow]{}, fmt.Errorf("iterate escalated tasks: %w", err)
	}

	page := Page[EscalatedTaskRow]{Items: items}
	if len(items) > pageSize {
		page.Items = items[:pageSize]
		last := page.Items[pageSize-1]
		page.NextToken = EncodeContinuationToken(params.ScopeID, Cursor{
			SortKey: last.EscalatedAt.Format(time.RFC3339Nano),
			ID:      last.TaskID,
		})
	}
	return page, nil
}
