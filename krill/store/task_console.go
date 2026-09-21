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
