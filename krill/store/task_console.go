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

	// ClaimedAt is when the live claim was taken (task_claim.claimed_at),
	// never task.created_at: a task claimed much later than it was created
	// would otherwise read as having been held all that time.
	ClaimedAt time.Time

	CurrentLane    Lane
	LeaseExpiresAt time.Time
	AttemptCount   int

	// ClaimID is the current open claim's own id (task.current_claim_id)
	// -- the id an optimistic release carries back to be checked against
	// what the caller observed. Read from task.current_claim_id, never
	// derived.
	ClaimID uuid.UUID
}

// ListClaimedTasksParams is ListClaimedTasks' input: scopeID (NFR1), the
// optional ConsoleFilter narrowing, and this query's own PageParams (NFR6).
type ListClaimedTasksParams struct {
	ScopeID uuid.UUID
	ConsoleFilter
	Page PageParams
}

// Filters is the FilterSet ListClaimedTasks' continuation token binds.
func (p ListClaimedTasksParams) Filters() FilterSet {
	return p.ConsoleFilter.Filters()
}

// ListClaimedTasks returns scopeID's currently-claimed tasks (FR4),
// soonest-lease-to-lapse first (`task.lease_expires_at ASC, task.id ASC` --
// the useful operator order), bounded and continuable per PageParams
// (NFR6).
//
// Joins task (WHERE current_claim_id IS NOT NULL) to milestone_ref (the
// identifying delivery reference this file's own doc comment describes)
// and to task_claim (task.current_claim_id) for the claimant session,
// the claimed-since instant, and both LB4 subject pairs -- task_claim's
// created_by_acting/created_by_on_behalf_of columns name whoever created
// the claim (the claimant), per ClaimedTaskRow's own doc comment.
// task.current_claim_id
// carries no DB-level FK onto task_claim(id) (migration 015's own note:
// task_claim is created later in the same migration), so this join is
// enforced here in Go/SQL, not by the schema.
func (s taskStore) ListClaimedTasks(ctx context.Context, params ListClaimedTasksParams) (Page[ClaimedTaskRow], error) {
	pageSize := ResolvePageSize(params.Page.PageSize)

	// A narrowing that crosses a scope or a product boundary is refused
	// before the query runs, so the page can never answer with rows the
	// caller has no claim to.
	if err := s.guardConsoleFilter(ctx, params.ScopeID, params.ConsoleFilter); err != nil {
		return Page[ClaimedTaskRow]{}, err
	}

	var cursor *Cursor
	if params.Page.ContinuationToken != "" {
		c, err := DecodeFilteredContinuationToken(params.ScopeID, params.Filters(), params.Page.ContinuationToken)
		if err != nil {
			return Page[ClaimedTaskRow]{}, err
		}
		cursor = &c
	}

	fromWhere, args := claimedTasksQuery(params)
	query := `
		SELECT task.id, task.title, milestone_ref.id, milestone_ref.kind, milestone_ref.name,
			tc.session_id, tc.claimed_at,
			tc.created_by_acting_iss, tc.created_by_acting_sub, tc.created_by_acting_kind,
			tc.created_by_on_behalf_of_iss, tc.created_by_on_behalf_of_sub, tc.created_by_on_behalf_of_kind,
			task.current_lane, task.lease_expires_at, task.attempt_count, task.current_claim_id
	` + fromWhere
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
			&sessionID, &row.ClaimedAt,
			&row.ClaimantActing.Iss, &row.ClaimantActing.Sub, &actingKind,
			&row.ClaimantOnBehalfOf.Iss, &row.ClaimantOnBehalfOf.Sub, &onBehalfOfKind,
			&currentLane, &row.LeaseExpiresAt, &row.AttemptCount, &row.ClaimID,
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
		page.NextToken = EncodeFilteredContinuationToken(params.ScopeID, params.Filters(), Cursor{
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

// ListCancelledTasksParams is ListCancelledTasks' input: scopeID (NFR1),
// the optional ConsoleFilter narrowing, and this query's own PageParams
// (NFR6).
type ListCancelledTasksParams struct {
	ScopeID uuid.UUID
	ConsoleFilter
	Page PageParams
}

// Filters is the FilterSet ListCancelledTasks' continuation token binds.
func (p ListCancelledTasksParams) Filters() FilterSet {
	return p.ConsoleFilter.Filters()
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

	if err := s.guardConsoleFilter(ctx, params.ScopeID, params.ConsoleFilter); err != nil {
		return Page[CancelledTaskRow]{}, err
	}

	var cursor *Cursor
	if params.Page.ContinuationToken != "" {
		c, err := DecodeFilteredContinuationToken(params.ScopeID, params.Filters(), params.Page.ContinuationToken)
		if err != nil {
			return Page[CancelledTaskRow]{}, err
		}
		cursor = &c
	}

	fromWhere, args := cancelledTasksQuery(params)
	query := `
		SELECT task.id, task.title, milestone_ref.id, milestone_ref.kind, milestone_ref.name,
			ev.created_by_acting_iss, ev.created_by_acting_sub, ev.created_by_acting_kind,
			ev.created_by_on_behalf_of_iss, ev.created_by_on_behalf_of_sub, ev.created_by_on_behalf_of_kind,
			ev.created_at
	` + fromWhere
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
		page.NextToken = EncodeFilteredContinuationToken(params.ScopeID, params.Filters(), Cursor{
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

	// EscalationID is the current escalation's own id
	// (task.current_escalation_id) -- the id an optimistic requeue or
	// cancel carries back to be checked against what the caller
	// observed. Read from task.current_escalation_id, never derived.
	EscalationID uuid.UUID

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

// ListEscalatedTasksParams is ListEscalatedTasks' input: scopeID (NFR1),
// the optional ConsoleFilter narrowing, an optional reason filter over
// EscalationReason's fixed enumeration, and this query's own PageParams
// (NFR6).
type ListEscalatedTasksParams struct {
	ScopeID uuid.UUID
	ConsoleFilter

	// Reason keeps only escalations raised for this reason -- nil keeps
	// every reason, which is what the read returned before the filter
	// existed.
	Reason *EscalationReason

	Page PageParams
}

// Filters is the FilterSet ListEscalatedTasks' continuation token binds:
// the ConsoleFilter's, plus the reason when one is set.
func (p ListEscalatedTasksParams) Filters() FilterSet {
	filters := p.ConsoleFilter.Filters()
	if p.Reason != nil {
		filters = filters.With(filterKeyEscalationReason, string(*p.Reason))
	}
	return filters
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

	if err := s.guardConsoleFilter(ctx, params.ScopeID, params.ConsoleFilter); err != nil {
		return Page[EscalatedTaskRow]{}, err
	}
	if params.Reason != nil && !validEscalationReasons[*params.Reason] {
		return Page[EscalatedTaskRow]{}, fmt.Errorf("%w: %q", ErrUnknownEscalationReason, *params.Reason)
	}

	var cursor *Cursor
	if params.Page.ContinuationToken != "" {
		c, err := DecodeFilteredContinuationToken(params.ScopeID, params.Filters(), params.Page.ContinuationToken)
		if err != nil {
			return Page[EscalatedTaskRow]{}, err
		}
		cursor = &c
	}

	fromWhere, args := escalatedTasksQuery(params)
	query := `
		SELECT task.id, task.title, milestone_ref.id, milestone_ref.kind, milestone_ref.name,
			task.current_escalation_id,
			ev.reason, ev.counter_value, ev.cap_value, ev.created_at,
			ev.created_by_acting_iss, ev.created_by_acting_sub, ev.created_by_acting_kind,
			ev.created_by_on_behalf_of_iss, ev.created_by_on_behalf_of_sub, ev.created_by_on_behalf_of_kind,
			task.current_lane, task.attempt_count, task.thrash_count,
			(SELECT COUNT(*) FROM task_note WHERE task_note.task_id = task.id)
	` + fromWhere
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
			&row.EscalationID,
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
		page.NextToken = EncodeFilteredContinuationToken(params.ScopeID, params.Filters(), Cursor{
			SortKey: last.EscalatedAt.Format(time.RFC3339Nano),
			ID:      last.TaskID,
		})
	}
	return page, nil
}

// This file's second half is the count half of the same three queues
// (FR c4ab6c68): every number the console shows next to a queue -- the
// queue's own size, and the two Overview sub-line figures -- comes from a
// read that takes the same params type as the list it describes. Each
// queue's FROM/JOIN/WHERE therefore lives in one builder shared by both,
// so a count cannot silently come to describe a different row set than the
// list it is printed beside.

// claimedTasksQuery returns the FROM/JOIN/WHERE clause behind both
// ListClaimedTasks and CountClaimedTasks, plus the arguments it binds.
// Paging is deliberately absent: a count is of the whole filtered set, so
// the keyset clause and LIMIT stay with the list's own caller and can
// never narrow what the count reports. The ConsoleFilter narrowing rides
// in the same builder, so a count cannot describe a different row set
// than the list printed beside it.
func claimedTasksQuery(params ListClaimedTasksParams) (string, []any) {
	fromWhere := `
		FROM task
		JOIN milestone_ref ON milestone_ref.id = task.milestone_id AND milestone_ref.valid_to IS NULL
		JOIN task_claim tc ON tc.id = task.current_claim_id
		WHERE task.scope_id = $1 AND task.current_claim_id IS NOT NULL
	`
	args := []any{params.ScopeID}
	filterSQL, filterArgs := params.ConsoleFilter.sqlPredicate("milestone_ref", len(args)+1)
	return fromWhere + filterSQL, append(args, filterArgs...)
}

// claimedTasksQueryExpiringBefore is claimedTasksQuery narrowed to the
// claims whose lease lapses at or before a deadline -- the Overview
// sub-line "claims about to lapse", expressed as the same row set with
// one more conjunct rather than as a query of its own.
func claimedTasksQueryExpiringBefore(params ListClaimedTasksParams, before time.Time) (string, []any) {
	fromWhere, args := claimedTasksQuery(params)
	args = append(args, before)
	return fromWhere + fmt.Sprintf(` AND task.lease_expires_at <= $%d`, len(args)), args
}

// countConsoleRows runs COUNT(*) over one of this file's shared
// FROM/JOIN/WHERE clauses and returns the row count, or the query's own
// error. Every count read in this package goes through it, so "a failed
// count is an error, never a 0" is one implementation rather than a
// promise each read repeats.
func countConsoleRows(ctx context.Context, q txQuerier, fromWhere string, args []any) (int, error) {
	var n int
	if err := q.QueryRow(ctx, "SELECT COUNT(*)"+fromWhere, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count console rows: %w", err)
	}
	return n, nil
}

// CountClaimedTasks returns how many rows the unpaged ListClaimedTasks
// would hold for params -- the queue's full size, never the current
// page's length and never a figure bounded by params.Page, which this
// read ignores entirely.
//
// It runs COUNT(*) over claimedTasksQuery, the exact FROM/JOIN/WHERE
// ListClaimedTasks pages over, so the two cannot disagree about which
// claims are in the queue. A failed query returns its error, never 0:
// a queue that could not be counted must not render as an empty one.
func (s taskStore) CountClaimedTasks(ctx context.Context, params ListClaimedTasksParams) (int, error) {
	if err := s.guardConsoleFilter(ctx, params.ScopeID, params.ConsoleFilter); err != nil {
		return 0, err
	}
	fromWhere, args := claimedTasksQuery(params)
	return countConsoleRows(ctx, s.pool, fromWhere, args)
}

// cancelledTasksQuery is CountCancelledTasks' and ListCancelledTasks'
// shared FROM/JOIN/WHERE -- see claimedTasksQuery for why the two share
// one builder, and for the ConsoleFilter narrowing it carries.
func cancelledTasksQuery(params ListCancelledTasksParams) (string, []any) {
	fromWhere := `
		FROM task
		JOIN milestone_ref ON milestone_ref.id = task.milestone_id AND milestone_ref.valid_to IS NULL
		JOIN task_intervention_event ev ON ev.task_id = task.id AND ev.action = 'cancel'
		WHERE task.scope_id = $1 AND task.cancelled_at IS NOT NULL
	`
	args := []any{params.ScopeID}
	filterSQL, filterArgs := params.ConsoleFilter.sqlPredicate("milestone_ref", len(args)+1)
	return fromWhere + filterSQL, append(args, filterArgs...)
}

// CountCancelledTasks returns how many rows the unpaged
// ListCancelledTasks would hold for params -- the dead-lettered queue's
// full size, never the current page's length; params.Page is ignored.
// Shares cancelledTasksQuery with its list, and returns a failed count
// as its error rather than as 0.
func (s taskStore) CountCancelledTasks(ctx context.Context, params ListCancelledTasksParams) (int, error) {
	if err := s.guardConsoleFilter(ctx, params.ScopeID, params.ConsoleFilter); err != nil {
		return 0, err
	}
	fromWhere, args := cancelledTasksQuery(params)
	return countConsoleRows(ctx, s.pool, fromWhere, args)
}

// escalatedTasksQuery is CountEscalatedTasks' and ListEscalatedTasks'
// shared FROM/JOIN/WHERE -- see claimedTasksQuery for why the two share
// one builder. It also carries both of this read's own narrowings, the
// ConsoleFilter and the reason, so they intersect in one place and a
// count cannot be filtered differently from the list beside it.
func escalatedTasksQuery(params ListEscalatedTasksParams) (string, []any) {
	fromWhere := `
		FROM task
		JOIN milestone_ref ON milestone_ref.id = task.milestone_id AND milestone_ref.valid_to IS NULL
		JOIN task_escalation_event ev ON ev.id = task.current_escalation_id
		WHERE task.scope_id = $1 AND task.current_escalation_id IS NOT NULL
	`
	args := []any{params.ScopeID}
	filterSQL, filterArgs := params.ConsoleFilter.sqlPredicate("milestone_ref", len(args)+1)
	fromWhere += filterSQL
	args = append(args, filterArgs...)
	if params.Reason != nil {
		fromWhere += fmt.Sprintf("\n\t\t\tAND ev.reason = $%d", len(args)+1)
		args = append(args, string(*params.Reason))
	}
	return fromWhere, args
}

// escalatedTasksQuerySince is escalatedTasksQuery narrowed to the
// escalations recorded at or after an instant -- the Overview sub-line
// "escalated in the last hour", one extra conjunct over the same rows
// rather than a query of its own.
func escalatedTasksQuerySince(params ListEscalatedTasksParams, since time.Time) (string, []any) {
	fromWhere, args := escalatedTasksQuery(params)
	args = append(args, since)
	return fromWhere + fmt.Sprintf(` AND ev.created_at >= $%d`, len(args)), args
}

// CountEscalatedTasks returns how many rows the unpaged
// ListEscalatedTasks would hold for params -- the escalation queue's
// full size, never the current page's length; params.Page is ignored.
// Shares escalatedTasksQuery with its list, and returns a failed count
// as its error rather than as 0.
func (s taskStore) CountEscalatedTasks(ctx context.Context, params ListEscalatedTasksParams) (int, error) {
	if err := s.guardConsoleFilter(ctx, params.ScopeID, params.ConsoleFilter); err != nil {
		return 0, err
	}
	if params.Reason != nil && !validEscalationReasons[*params.Reason] {
		return 0, fmt.Errorf("%w: %q", ErrUnknownEscalationReason, *params.Reason)
	}
	fromWhere, args := escalatedTasksQuery(params)
	return countConsoleRows(ctx, s.pool, fromWhere, args)
}
