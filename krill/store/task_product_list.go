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

// completeContainerStatuses are the two statuses that take a container out
// of the product-wide incomplete scope. This slice is the single source for
// both the Go predicate and the SQL predicate the query runs -- see
// incompleteContainerFilterSQL -- so neither copy of the rule can drift from
// the other.
var completeContainerStatuses = []MilestoneStatus{
	MilestoneStatusShipped,
	MilestoneStatusAbandoned,
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
	for _, complete := range completeContainerStatuses {
		if status == complete {
			return false
		}
	}
	return true
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

	// CreatedAt is the task's own creation instant -- the third component
	// of ProductTaskSortKey, and the reason it lives on the row at all.
	CreatedAt time.Time

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

// currentContainerStatusSQL is the latest-status-event derivation this
// read uses for a container, joined per row so a page needs no second
// query: the same `ORDER BY created_at DESC, id DESC LIMIT 1` row
// milestone_status.go's currentStatusTx picks, and the same
// COALESCE-to-MilestoneStatusNotStarted "absence of history is not
// started" rule. Kept as one constant so the row's own container status
// and the milestone it hangs under can never drift apart in ordering or
// in the absence case.
const currentContainerStatusSQL = `
	COALESCE((
		SELECT mse.status FROM milestone_status_event mse
		WHERE mse.milestone_id = %s
		ORDER BY mse.created_at DESC, mse.id DESC
		LIMIT 1
	), $1)`

// incompleteContainerFilterSQL renders the product-wide scope's
// "still in scope" predicate against the named container column: its
// current status must not be one of completeContainerStatuses. Rendering
// the list from the same slice IsIncompleteContainerStatus reads keeps the
// SQL and the Go predicate from being two copies of one rule that can drift.
func incompleteContainerFilterSQL(containerColumn string) string {
	quoted := make([]string, len(completeContainerStatuses))
	for i, status := range completeContainerStatuses {
		quoted[i] = "'" + string(status) + "'"
	}
	return fmt.Sprintf(currentContainerStatusSQL, containerColumn) +
		" NOT IN (" + strings.Join(quoted, ", ") + ")"
}

// ListProductTasks returns one page of params.ProductID's tasks over
// params.Scope, with params.Lane/params.OnlyStuck applied, bounded and
// continuable per params.Page (NFR6). Its continuation token is bound to
// params' filter set (FR3), so resuming it under a different scope or a
// different filter is refused with ErrTokenFilterMismatch.
//
// One query. `task` is joined to its own container (the `milestone_ref`
// row task.milestone_id names) and from there to that container's
// milestone -- itself for a milestone-scoped task, parent_milestone_id
// for a milepebble-scoped one -- so each row names its milestone without
// a second lookup and each milepebble row carries its own container.
// Both containers' statuses come from the same latest-status-event
// derivation CurrentStatus performs, never a stored column.
//
// "Incomplete" (the product-wide scope only) is judged per the row's own
// container, matching IsIncompleteContainerStatus: shipped and abandoned
// drop out, everything else -- including partially complete -- stays, so
// a shipped milestone whose milepebble is still in design keeps that
// milepebble's tasks in scope while its own direct tasks leave it. The
// backlog bucket is excluded from the product-wide scope outright;
// CreateTask already refuses a task scoped to one (task.go's NFR7 note),
// so this is belt-and-braces, not the only thing keeping it out.
func (s taskStore) ListProductTasks(ctx context.Context, params ListProductTasksParams) (Page[ProductTaskRow], error) {
	pageSize := ResolvePageSize(params.Page.PageSize)
	filters := params.FilterSet()

	var cursor *Cursor
	if params.Page.ContinuationToken != "" {
		c, err := DecodeFilteredContinuationToken(params.ScopeID, filters, params.Page.ContinuationToken)
		if err != nil {
			return Page[ProductTaskRow]{}, err
		}
		cursor = &c
	}
	var sortKey ProductTaskSortKey
	if cursor != nil {
		var err error
		if sortKey, err = DecodeProductTaskSortKey(cursor.SortKey); err != nil {
			return Page[ProductTaskRow]{}, err
		}
	}

	if params.Scope.RequiresContainer() {
		owns, err := productOwnsContainer(ctx, s.pool, params.ScopeID, params.ProductID, params.Scope.ContainerID)
		if err != nil {
			return Page[ProductTaskRow]{}, err
		}
		// A container this product does not own is a caller error, refused
		// distinctly (LB1) rather than answered as a plausible-looking empty
		// page that would read as "this milestone has no work".
		if !owns {
			return Page[ProductTaskRow]{}, ErrMilestoneOutsideProduct
		}
	}

	args := []any{string(MilestoneStatusNotStarted), params.ScopeID, params.ProductID}
	containerFilter := ""
	switch params.Scope.Kind {
	case ProductTaskScopeIncomplete:
		containerFilter = " AND c.kind <> 'backlog' AND " + incompleteContainerFilterSQL("c.id")
	case ProductTaskScopeMilestone:
		containerFilter = " AND (c.id = $4 OR c.parent_milestone_id = $4)"
		args = append(args, params.Scope.ContainerID)
	case ProductTaskScopeMilepebble:
		containerFilter = " AND c.id = $4"
		args = append(args, params.Scope.ContainerID)
	default:
		return Page[ProductTaskRow]{}, fmt.Errorf("unknown product task scope kind %q", params.Scope.Kind)
	}

	if params.Lane != nil {
		args = append(args, string(*params.Lane))
		containerFilter += fmt.Sprintf(" AND task.current_lane = $%d", len(args))
	}
	if params.OnlyStuck {
		// Exactly expired-lease, at-the-cap, escalated, or cancelled. The
		// cap is the package-wide DefaultAttemptCap (no per-task column
		// carries it), passed in rather than inlined so the predicate and
		// the row's reported AttemptCap can never disagree.
		args = append(args, DefaultAttemptCap)
		containerFilter += fmt.Sprintf(
			" AND (task.current_claim_id IS NOT NULL AND task.lease_expires_at < NOW()"+
				" OR task.attempt_count >= $%d"+
				" OR task.current_escalation_id IS NOT NULL"+
				" OR task.cancelled_at IS NOT NULL)", len(args))
	}
	if cursor != nil {
		args = append(args, sortKey.MilestonePosition, sortKey.MilestoneID, sortKey.TaskCreatedAt, cursor.ID)
		p, m, c, t := len(args)-3, len(args)-2, len(args)-1, len(args)
		// The composite order's keyset half: milestone position descending,
		// then milestone id, then the task's own created_at, with the task
		// id as the final tiebreaker. Written as an explicit disjunction
		// rather than a row comparison so each level's direction is
		// readable at a glance -- only the first level is descending.
		containerFilter += fmt.Sprintf(
			" AND (m.position < $%d"+
				" OR (m.position = $%d AND m.id > $%d"+
				" OR (m.position = $%d AND m.id = $%d AND (task.created_at > $%d"+
				" OR (task.created_at = $%d AND task.id > $%d)))))",
			p, p, m, p, m, c, c, t)
	}

	query := `
		SELECT task.id, task.title, task.created_at,
			m.id, m.name, ` + fmt.Sprintf(currentContainerStatusSQL, "m.id") + `,
			c.kind, c.id, c.name, ` + fmt.Sprintf(currentContainerStatusSQL, "c.id") + `,
			task.current_lane, task.current_escalation_id, esc.reason,
			task.cancelled_at, task.attempt_count, task.lease_expires_at, task.current_claim_id,
			m.position
		FROM task
		JOIN milestone_ref c ON c.id = task.milestone_id AND c.valid_to IS NULL AND c.scope_id = $2
		JOIN milestone_ref m ON m.id = COALESCE(c.parent_milestone_id, c.id) AND m.valid_to IS NULL AND m.scope_id = $2
		LEFT JOIN task_escalation_event esc ON esc.id = task.current_escalation_id
		WHERE task.scope_id = $2 AND m.product_id = $3` + containerFilter

	// Fetch one extra row beyond pageSize -- its presence, not a second
	// COUNT query, is what decides whether NextToken is populated.
	query += fmt.Sprintf(` ORDER BY m.position DESC, m.id ASC, task.created_at ASC, task.id ASC LIMIT $%d`, len(args)+1)
	args = append(args, pageSize+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Page[ProductTaskRow]{}, fmt.Errorf("query product tasks: %w", err)
	}
	defer rows.Close()

	var items []ProductTaskRow
	// positions rides alongside items rather than on the row: the row's own
	// sort key is Position DESC then Milestone.ID, and only the last row of
	// a full page ever needs its position back, to build the next token.
	var positions []int
	for rows.Next() {
		var row ProductTaskRow
		var milestoneStatus, containerKind, containerName, containerStatus, currentLane string
		var containerID uuid.UUID
		var escalationID *uuid.UUID
		var reason *string
		var position int
		if err := rows.Scan(
			&row.TaskID, &row.Title, &row.CreatedAt,
			&row.Milestone.ID, &row.Milestone.Name, &milestoneStatus,
			&containerKind, &containerID, &containerName, &containerStatus,
			&currentLane, &escalationID, &reason,
			&row.CancelledAt, &row.AttemptCount, &row.LeaseExpiresAt, &row.ClaimID,
			&position,
		); err != nil {
			return Page[ProductTaskRow]{}, fmt.Errorf("scan product task row: %w", err)
		}
		row.Milestone.Status = MilestoneStatus(milestoneStatus)
		row.CurrentLane = Lane(currentLane)
		row.AttemptCap = DefaultAttemptCap
		// State is derived from the presence of an active escalation event,
		// never stored -- the same rule task_escalation.go's TaskState sets.
		if escalationID != nil {
			row.State = TaskStateEscalated
			if reason != nil {
				escalation := EscalationReason(*reason)
				row.EscalationReason = &escalation
			}
		} else {
			row.State = TaskStateActive
		}
		// A milestone-scoped task's container IS its milestone, so only a
		// milepebble container becomes the row's own Milepebble ref.
		if containerKind == string(MilestoneKindMilepebble) {
			row.Milepebble = &ProductTaskMilepebbleRef{
				ID:     containerID,
				Name:   containerName,
				Status: MilestoneStatus(containerStatus),
			}
		}
		items = append(items, row)
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		return Page[ProductTaskRow]{}, fmt.Errorf("iterate product tasks: %w", err)
	}

	page := Page[ProductTaskRow]{Items: items}
	if len(items) > pageSize {
		page.Items = items[:pageSize]
		positions = positions[:pageSize]
		last := page.Items[len(page.Items)-1]
		page.NextToken = EncodeFilteredContinuationToken(params.ScopeID, filters, Cursor{
			SortKey: EncodeProductTaskSortKey(ProductTaskSortKey{
				MilestonePosition: positions[len(positions)-1],
				MilestoneID:       last.Milestone.ID,
				TaskCreatedAt:     last.CreatedAt,
			}),
			ID: last.TaskID,
		})
	}
	return page, nil
}

// productOwnsContainer reports whether containerID names a current
// `milestone_ref` row belonging to productID within scopeID -- the
// membership check behind ListProductTasks' two single-container scopes.
// Unlike currentRowExists (errors.go) it also compares product_id, since
// a container id is only meaningful relative to the product the read is
// anchored to: a real milestone id from another product in the same
// scope must still be refused, not silently returned as empty.
func productOwnsContainer(ctx context.Context, q txQuerier, scopeID, productID, containerID uuid.UUID) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM milestone_ref
			WHERE id = $1 AND scope_id = $2 AND product_id = $3 AND valid_to IS NULL
		)
	`, containerID, scopeID, productID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check container ownership: %w", err)
	}
	return exists, nil
}
