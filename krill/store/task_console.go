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
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrNotImplemented is ListClaimedTasks' Scaffold-phase placeholder
// return -- mirrors krill/work/payload.go's own precedent (issue #2721)
// for a store method whose real query logic lands in this task's
// Implementation phase.
var ErrNotImplemented = errors.New("krill/store: not implemented")

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
// soonest-lease-to-lapse first (`lease_expires_at ASC, task.id ASC` --
// the useful operator order), bounded and continuable per PageParams
// (NFR6).
//
// Scaffold-phase stub: returns ErrNotImplemented unconditionally. This
// task's Implementation phase fills in the join this file's own doc
// comment describes: task JOIN task_claim (task.current_claim_id) JOIN
// milestone_ref (task.milestone_id), scope-qualified, restricted to a
// task with a live claim (task.current_claim_id IS NOT NULL).
func (s taskStore) ListClaimedTasks(ctx context.Context, params ListClaimedTasksParams) (Page[ClaimedTaskRow], error) {
	return Page[ClaimedTaskRow]{}, ErrNotImplemented
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
		JOIN milestone_ref ON milestone_ref.id = task.milestone_id
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
