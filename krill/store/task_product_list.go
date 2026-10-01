// This file (FR cfcd1104-b015-4ffd-a07a-abb7c6e435d3) is the
// product-wide paged task read: one call that returns a page of a
// product's tasks across its incomplete milestones, one milestone, or one
// milepebble, with optional lane and only-stuck filters. It is the
// substrate every later console view pages over -- the existing
// ListTasksByMilestone (task.go) stays unchanged, since it remains the
// unpaginated per-container read its own callers already hold.
//
// Unlike the four console queue reads (task_console.go), this read is not
// scope-wide: it is anchored to a product and joined out to that product's
// delivery containers, so each row can name the milestone (and the
// milepebble under it, when the milestone is cut) the task belongs to
// without a second lookup.
package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ProductTaskScopeKind discriminates the three container selections
// ListProductTasks accepts (FR2): every incomplete milestone of the
// product, one milestone (with its milepebbles' tasks), or one
// milepebble.
type ProductTaskScopeKind string

const (
	// ProductTaskScopeIncomplete is the product-wide default: every
	// milestone and milepebble of the product whose own current status is
	// neither shipped nor abandoned. The product's backlog bucket is never
	// a milestone in the result.
	ProductTaskScopeIncomplete ProductTaskScopeKind = "incomplete"
	// ProductTaskScopeMilestone is one milestone of the product, whatever
	// its status -- its direct tasks plus every milepebble's, each row
	// naming the milepebble it came from.
	ProductTaskScopeMilestone ProductTaskScopeKind = "milestone"
	// ProductTaskScopeMilepebble is one milepebble of the product, whatever
	// its status.
	ProductTaskScopeMilepebble ProductTaskScopeKind = "milepebble"
)

// ValidProductTaskScopeKinds is the fixed set of the three values above --
// the HTTP/MCP surfaces' 400 for anything else names these.
var ValidProductTaskScopeKinds = []ProductTaskScopeKind{
	ProductTaskScopeIncomplete,
	ProductTaskScopeMilestone,
	ProductTaskScopeMilepebble,
}

// ProductTaskScope is ListProductTasks' container selection: Kind above,
// plus ContainerID, the milestone_ref id the two single-container kinds
// name. Ignored for ProductTaskScopeIncomplete, which names no container.
type ProductTaskScope struct {
	Kind        ProductTaskScopeKind
	ContainerID uuid.UUID
}

// RequiresContainer reports whether this scope's kind needs a ContainerID
// to be meaningful -- the two single-container kinds do; the
// product-wide incomplete kind does not.
func (s ProductTaskScope) RequiresContainer() bool {
	return s.Kind == ProductTaskScopeMilestone || s.Kind == ProductTaskScopeMilepebble
}

// IsIncompleteContainerStatus is FR2's "incomplete" predicate, in one
// place so no caller re-derives it: a container counts as incomplete
// unless its own current status is shipped or abandoned -- so partially
// complete counts as incomplete, and a shipped milestone whose milepebble
// is still in design keeps that milepebble in scope. The status itself is
// always derived from the latest milestone_status_event row (absence of
// one means MilestoneStatusNotStarted), never read from a column; see
// milestone_status.go's CurrentStatus.
func IsIncompleteContainerStatus(status MilestoneStatus) bool {
	return status != MilestoneStatusShipped && status != MilestoneStatusAbandoned
}

// ProductTaskMilestoneRef is one ProductTaskRow's milestone: the id, name
// and own current status of the milestone the task was scoped to directly.
type ProductTaskMilestoneRef struct {
	ID     uuid.UUID
	Name   string
	Status MilestoneStatus
}

// ProductTaskMilepebbleRef is one ProductTaskRow's milepebble, present
// only when the row's milestone has been cut and the task was scoped to one
// of its milepebbles rather than to the milestone directly.
type ProductTaskMilepebbleRef struct {
	ID     uuid.UUID
	Name   string
	Status MilestoneStatus
}

// ProductTaskRow is one row of ListProductTasks' page (FR2): everything a
// console needs to render a task without a second lookup -- its delivery
// containers, lane, state, attempt/lease/claim identity.
type ProductTaskRow struct {
	TaskID uuid.UUID
	Title  string

	Milestone  ProductTaskMilestoneRef
	Milepebble *ProductTaskMilepebbleRef

	CurrentLane Lane

	// State is derived from task.current_escalation_id, never stored
	// (task_escalation.go's TaskState). EscalationReason is non-nil
	// exactly when State is TaskStateEscalated, naming why.
	State            TaskState
	EscalationReason *EscalationReason

	// CancelledAt is non-nil for a dead-lettered task -- the one other
	// half of only-stuck's predicate that neither the state nor the
	// counters distinguish, since a cancelled task is never itself
	// escalated.
	CancelledAt *time.Time

	AttemptCount int
	AttemptCap   int

	// LeaseExpiresAt is the current open claim's lease expiry, nil when
	// no claim is open. ClaimID names that same claim -- the two always
	// agree, so a reader never has to join them.
	LeaseExpiresAt *time.Time
	ClaimID        *uuid.UUID
}

// ListProductTasksParams is ListProductTasks' input: ScopeID (NFR1) and
// ProductID (the product whose delivery containers the read walks), Scope
// (which containers), the optional Lane and OnlyStuck filters, and this
// query's own PageParams (NFR6).
type ListProductTasksParams struct {
	ScopeID   uuid.UUID
	ProductID uuid.UUID
	Scope     ProductTaskScope

	// Lane, when non-nil, keeps only tasks whose current_lane is that
	// Lane. Nil means every lane, never "no lanes".
	Lane *Lane

	// OnlyStuck keeps exactly the tasks with an expired lease, at
	// DefaultAttemptCap, escalated, or cancelled.
	OnlyStuck bool

	Page PageParams
}

// FilterSet is the filter set ListProductTasks binds its continuation
// token to (FR3): the product, the container selection, and whichever of
// the two optional filters this request applied. Present on every request,
// so a token is never indistinguishable from an unfiltered one.
func (p ListProductTasksParams) FilterSet() FilterSet {
	f := FilterUUID("product_id", p.ProductID).
		With("scope_kind", string(p.Scope.Kind)).
		WithBool("only_stuck", p.OnlyStuck)
	if p.Scope.RequiresContainer() {
		f = f.WithUUID("scope_container_id", p.Scope.ContainerID)
	}
	if p.Lane != nil {
		f = f.With("lane", string(*p.Lane))
	}
	return f
}

// ProductTaskSortKey is this read's keyset position (NFR6): the composite
// sort column at the last row of the previous page -- the row's
// milestone's Position, that milestone's id, and the task's own CreatedAt.
// The task's own id is the tiebreaker and travels separately, in the
// paging.Cursor's ID field.
type ProductTaskSortKey struct {
	// MilestonePosition is the row's milestone's Position, carried for a
	// milepebble's tasks too -- a milepebble inherits its parent's
	// position for this read's ordering, never a position of its own.
	MilestonePosition int
	MilestoneID       uuid.UUID
	TaskCreatedAt     time.Time
}

// EncodeProductTaskSortKey renders k as the opaque paging.Cursor.SortKey
// string this read stores in a continuation token. The three fields are
// joined by a separator no field can contain, so the encoding is
// unambiguous in both directions.
func EncodeProductTaskSortKey(k ProductTaskSortKey) string {
	return fmt.Sprintf("%d|%s|%s",
		k.MilestonePosition, k.MilestoneID, k.TaskCreatedAt.Format(time.RFC3339Nano))
}

// DecodeProductTaskSortKey is EncodeProductTaskSortKey's inverse. A
// sort key that does not parse is ErrInvalidContinuationToken -- a
// malformed token, never a silently wrong page.
func DecodeProductTaskSortKey(s string) (ProductTaskSortKey, error) {
	malformed := fmt.Errorf("%w: malformed cursor sort key", ErrInvalidContinuationToken)

	parts := strings.Split(s, "|")
	if len(parts) != 3 {
		return ProductTaskSortKey{}, malformed
	}
	position, err := strconv.Atoi(parts[0])
	if err != nil {
		return ProductTaskSortKey{}, malformed
	}
	var milestoneID uuid.UUID
	if err := milestoneID.UnmarshalText([]byte(parts[1])); err != nil {
		return ProductTaskSortKey{}, malformed
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[2])
	if err != nil {
		return ProductTaskSortKey{}, malformed
	}
	return ProductTaskSortKey{
		MilestonePosition: position,
		MilestoneID:       milestoneID,
		TaskCreatedAt:     createdAt,
	}, nil
}

// ErrMilestoneOutsideProduct is ListProductTasks' distinct refusal (FR2,
// LB1) for a milestone or milepebble that does not belong to the product
// the read is anchored to. Never an empty page and never another
// product's tasks: a scope that names a container it does not own is a
// caller error, surfaced as this rather than as a plausible-looking page.
var ErrMilestoneOutsideProduct = errors.New("krill/store: milestone does not belong to this product")

// ListProductTasks returns one page of params.ProductID's tasks over
// params.Scope, with params.Lane/params.OnlyStuck applied, bounded and
// continuable per params.Page (NFR6). Its continuation token is bound to
// params' filter set (FR3), so resuming it under a different scope or a
// different filter is refused with ErrTokenFilterMismatch.
//
// Scaffold-phase stub: returns ErrNotImplemented unconditionally. This
// task's Implementation phase fills in the single query this file's own
// doc comment describes -- task joined to milestone_ref (the row's
// milestone, plus its milepebble when the task was scoped to one), with
// each container's status resolved through the same latest-event
// derivation milestone_status.go's CurrentStatus performs, keyed on
// ProductTaskSortKey.
func (s taskStore) ListProductTasks(ctx context.Context, params ListProductTasksParams) (Page[ProductTaskRow], error) {
	return Page[ProductTaskRow]{}, ErrNotImplemented
}
