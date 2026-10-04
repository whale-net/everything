// This file (FR 59f664ff-3aa9-4d90-a861-d7758ecee040) is the product-wide
// per-container task-progress read: one call that returns, for every
// milestone and milepebble of a product, how many tasks the container
// holds, how many sit in each of the five lanes, and how many of them are
// Done. It exists because the "N of M tasks done" figures the Overview's
// milestones-in-flight panel, the Milestones list, a milestone's detail
// page and each Board swimlane header need would otherwise cost one count
// query per container. Only the Overview panel renders it today; the rest
// are M13's operator-UI facelift (status planned).
//
// The per-lane counts and the "N of M done" figure this read returns are
// the same numbers by construction -- Done IS the Done lane's count -- so
// no caller can render a per-lane breakdown that disagrees with the
// progress bar beside it. The counts agree with the product task read's
// total for the same scope (task_product_list.go) because both derive
// their container set from the same predicate, IsIncompleteContainerStatus
// and the incompleteContainerFilterSQL rendering of it, and both walk
// `milestone_ref` for `task.milestone_id` exactly as the task read does.
package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// TaskLaneCounts is one container's task count per lane -- the five fixed
// values of task.current_lane (Lane's own vocabulary, CanonicalLaneOrder),
// as named int fields rather than a map keyed by a free string, so a typo
// cannot become a lane.
//
// A container's five counts always partition its total: every task the
// container holds is counted in exactly one of them, whatever lane it was
// last left in. Total is therefore the sum and can never disagree with
// the breakdown rendered above it.
type TaskLaneCounts struct {
	Scaffold       int
	Implementation int
	Testing        int
	Validation     int
	Done           int
}

// Total is the container's whole task count -- the "M" of "N of M tasks
// done". It is the sum of the five per-lane counts by definition, never a
// separately counted number that could drift from them.
func (c TaskLaneCounts) Total() int {
	return c.Scaffold + c.Implementation + c.Testing + c.Validation + c.Done
}

// ContainerTaskProgress is one delivery container's row: which container it
// is, and every task count a progress bar or swimlane header needs for it.
// Both containers' statuses are derived from the latest
// milestone_status_event row (currentContainerStatusSQL, the same
// derivation ListProductTasks performs), never read from a stored column.
type ContainerTaskProgress struct {
	// Milestone is the container's milestone: its id, name, and own
	// current status. A milepebble row carries its parent's milestone here
	// alongside its own Milepebble ref below.
	Milestone ProductTaskMilestoneRef

	// Milepebble names the container itself when it is a milepebble, and
	// is nil when the row IS a milestone.
	Milepebble *ProductTaskMilepebbleRef

	// PerLane partitions this container's tasks by current_lane. For a
	// milestone row the partition covers the milestone's own direct tasks
	// plus every one of its milepebbles' tasks, so the milestone's total
	// is the whole cut's, never just its own slice of it.
	PerLane TaskLaneCounts

	// Cancelled is how many of this container's tasks are cancelled
	// (task.cancelled_at set). It is reported so the definition below is
	// inspectable from one read rather than re-derived per display.
	Cancelled int
}

// Total is the container's whole task count, and the "M" of "N of M tasks
// done" -- the sum of the five per-lane counts by definition.
func (p ContainerTaskProgress) Total() int { return p.PerLane.Total() }

// Done is the container's "N": the number of its tasks whose current lane
// is Done. It is the Done lane's own count, not a second count of the same
// thing, so the per-lane breakdown and the progress bar cannot disagree.
func (p ContainerTaskProgress) Done() int { return p.PerLane.Done }

// CancelledTaskCounting is this read's single, package-wide definition of
// how a cancelled task counts toward a container's progress -- the rule
// every progress display renders identically:
//
//   - A cancelled task counts in the container's Total, in the lane it was
//     left in, and in Cancelled. It is work the container was given and
//     will never be done, so hiding it would make a container look
//     emptier than it was.
//   - Cancelling never moves a task: task_cancel.go leaves current_lane
//     exactly where it was (a cancelled task is not "in" any lane). So a
//     cancelled task is Done only if it had already completed when it was
//     cancelled -- dead-lettering finished work does not un-complete it.
//   - Because Total, Done, Cancelled and the per-lane counts are all
//     read in one statement, the four can never be drawn from different
//     snapshots of a moving board.
//
// The corollary every caller relies on: the five per-lane counts sum to
// Total exactly, whether or not any of those tasks is cancelled.
const CancelledTaskCounting = "cancelled tasks count in the total and in the lane they were left in, never in Done unless they had already completed"

// ProductTaskProgress is one product's whole per-container task progress:
// every milestone and milepebble of the product in scope, in the roadmap
// order a progress list renders (milestone position ascending, then id;
// each milestone immediately followed by its own milepebbles, also by
// position then id).
//
// Containers is always non-nil, so a caller can index it without a nil
// check, and it includes containers with no tasks at all (a zero total,
// which a caller renders as "No tasks yet" and never as "0 of 0").
type ProductTaskProgress struct {
	ProductID  uuid.UUID
	Containers []ContainerTaskProgress
}

// ProductTaskProgressParams is SummarizeProductTaskProgress' input:
// ScopeID (NFR1) and ProductID (the product whose containers the read
// walks), plus Scope -- the very same container selection ListProductTasks
// takes, reused rather than re-derived, so the two reads can never disagree
// about which containers a scope names.
type ProductTaskProgressParams struct {
	ScopeID   uuid.UUID
	ProductID uuid.UUID
	Scope     ProductTaskScope
}

// SummarizeProductTaskProgress returns productID's whole per-container
// task-progress aggregate (TaskStore's own method), in one statement and
// never one query per container.
//
// The container set is params.Scope's, carried over from the product task
// read unchanged: ProductTaskScopeIncomplete (the default) walks every
// milestone and milepebble whose own current status is neither shipped nor
// abandoned -- the same IsIncompleteContainerStatus predicate, and the
// same incompleteContainerFilterSQL rendering of it, that read applies;
// ProductTaskScopeMilestone walks one milestone and its milepebbles;
// ProductTaskScopeMilepebble walks one milepebble whatever its status;
// ProductTaskScopeAll walks every milestone and milepebble of the product
// whatever its status, which is what a surface listing the whole roadmap
// (rather than its open work) needs -- a shipped container's finished
// counts are an answer there and an omission in the in-flight scope. The
// backlog bucket is never a container here, for the same reason it is
// never one there. A container the product does not own is refused with
// ErrMilestoneOutsideProduct, exactly as the task read refuses it, so the
// two can never describe different container sets.
//
// Each milestone's PerLane covers its own direct tasks plus every one of
// its milepebbles'; each milepebble's covers its own. Both statuses on a
// row come from the same latest-status-event derivation CurrentStatus
// performs, via currentContainerStatusSQL.
//
// One statement, never one query per container: `milestone_ref` is left
// joined to `task` on the container the task was scoped to, grouped by
// (container, lane), so one pass produces every container's whole
// breakdown -- including the zero-task containers the LEFT JOIN keeps.
// A milestone's own join also reaches its milepebbles' tasks, which is
// what makes its PerLane the whole cut's rather than its own slice.
//
// ErrMilestoneOutsideProduct for a container the product does not own, and
// ErrNotFound (never an empty aggregate) for a product with no current
// row, so a caller never reads "no tasks yet" where the truth is "no such
// product" -- the same disambiguation SummarizeByProduct makes.
func (s taskStore) SummarizeProductTaskProgress(ctx context.Context, params ProductTaskProgressParams) (ProductTaskProgress, error) {
	if params.Scope.RequiresContainer() {
		owns, err := productOwnsContainer(ctx, s.pool, params.ScopeID, params.ProductID, params.Scope.ContainerID)
		if err != nil {
			return ProductTaskProgress{}, err
		}
		if !owns {
			return ProductTaskProgress{}, ErrMilestoneOutsideProduct
		}
	}

	// $1/$2 are scope and product, matching ListProductTasks' own argument
	// order so one reader recognises both statements. The "no status event
	// is not started" seed is inlined by currentContainerStatusSQL rather
	// than bound, so there is no leading argument to shift.
	args := []any{params.ScopeID, params.ProductID}
	containerFilter := ""
	switch params.Scope.Kind {
	case ProductTaskScopeIncomplete:
		containerFilter = " AND c.kind <> 'backlog' AND " + incompleteContainerFilterSQL("c.id")
	case ProductTaskScopeMilestone:
		containerFilter = " AND (c.id = $3 OR c.parent_milestone_id = $3)"
		args = append(args, params.Scope.ContainerID)
	case ProductTaskScopeMilepebble:
		containerFilter = " AND c.id = $3"
		args = append(args, params.Scope.ContainerID)
	case ProductTaskScopeAll:
		// Every container but the backlog bucket, whatever its status. No
		// status predicate at all: a shipped milestone's row is the answer
		// a roadmap listing needs, and an in-flight-only read would answer
		// "no tasks" for a container that is finished.
		containerFilter = " AND c.kind <> 'backlog'"
	default:
		return ProductTaskProgress{}, fmt.Errorf("unknown product task scope kind %q", params.Scope.Kind)
	}

	containerStatusSQL := currentContainerStatusSQL("c.id")
	milestoneStatusSQL := currentContainerStatusSQL("m.id")

	// A milestone's partition spans the whole cut, so its join reaches the
	// tasks scoped to any of its current milepebbles; a milepebble's own
	// join reaches only the tasks scoped to it. Both arms are the same
	// immutable container ids task.milestone_id names, so neither can
	// drift from what ListProductTasks counts.
	//
	// Positions are ordered on but not selected: the query returns one row
	// per (container, lane), and the order -- roadmap position, each
	// milestone immediately followed by its own milepebbles -- is what the
	// Go side folds consecutive rows of one container on.
	query := `
		SELECT c.id, c.kind, c.name, ` + containerStatusSQL + `,
			m.id, m.name, ` + milestoneStatusSQL + `, task.current_lane,
			COUNT(task.id),
			COUNT(task.id) FILTER (WHERE task.cancelled_at IS NOT NULL)
		FROM milestone_ref c
		JOIN milestone_ref m ON m.id = COALESCE(c.parent_milestone_id, c.id) AND m.valid_to IS NULL AND m.scope_id = $1
		LEFT JOIN task ON task.scope_id = $1 AND (
			task.milestone_id = c.id
			OR task.milestone_id IN (
				SELECT child.id FROM milestone_ref child
				WHERE child.parent_milestone_id = c.id AND child.valid_to IS NULL
			)
		)
		WHERE c.valid_to IS NULL AND c.scope_id = $1 AND c.product_id = $2` + containerFilter + `
		GROUP BY c.id, c.kind, c.name, c.position, c.parent_milestone_id, ` + containerStatusSQL + `,
			m.id, m.name, m.position, ` + milestoneStatusSQL + `, task.current_lane
		ORDER BY m.position ASC, m.id ASC, (c.parent_milestone_id IS NOT NULL) ASC, c.position ASC, c.id ASC`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return ProductTaskProgress{}, fmt.Errorf("query product task progress: %w", err)
	}
	defer rows.Close()

	out := ProductTaskProgress{ProductID: params.ProductID, Containers: []ContainerTaskProgress{}}
	// rows arrive one per (container, lane), already in render order, so
	// the current container's index is the tail of the slice.
	for rows.Next() {
		var (
			row                            ContainerTaskProgress
			containerID                    uuid.UUID
			containerKind, containerName   string
			containerStatus, milestoneName string
			milestoneStatus                string
			lane                           *string
			laneCount, cancelledCount      int
		)
		if err := rows.Scan(
			&containerID, &containerKind, &containerName, &containerStatus,
			&row.Milestone.ID, &milestoneName, &milestoneStatus,
			&lane, &laneCount, &cancelledCount,
		); err != nil {
			return ProductTaskProgress{}, fmt.Errorf("scan product task progress: %w", err)
		}
		row.Milestone.Name = milestoneName
		row.Milestone.Status = MilestoneStatus(milestoneStatus)

		if len(out.Containers) == 0 || containerRefOf(out.Containers[len(out.Containers)-1]) != containerID {
			if containerKind == string(MilestoneKindMilepebble) {
				row.Milepebble = &ProductTaskMilepebbleRef{
					ID:     containerID,
					Name:   containerName,
					Status: MilestoneStatus(containerStatus),
				}
			}
			out.Containers = append(out.Containers, row)
		}
		// A zero-task container's single LEFT JOIN row has no lane and no
		// count; there is nothing to add to any lane for it.
		if lane != nil && laneCount > 0 {
			cur := &out.Containers[len(out.Containers)-1]
			cur.Cancelled += cancelledCount
			switch Lane(*lane) {
			case LaneScaffold:
				cur.PerLane.Scaffold += laneCount
			case LaneImplementation:
				cur.PerLane.Implementation += laneCount
			case LaneTesting:
				cur.PerLane.Testing += laneCount
			case LaneValidation:
				cur.PerLane.Validation += laneCount
			case LaneDone:
				cur.PerLane.Done += laneCount
			default:
				// task.current_lane is CHECK-constrained to the five lanes,
				// so this is unreachable; failing beats a silent zero.
				return ProductTaskProgress{}, fmt.Errorf("unknown task lane %q in container %s", *lane, containerID)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return ProductTaskProgress{}, fmt.Errorf("query product task progress: %w", err)
	}

	if len(out.Containers) == 0 {
		// No containers is ambiguous: a product whose every container is
		// shipped, or no such product. Only the second is an error, and the
		// common case never pays for the probe.
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM product WHERE id = $1 AND valid_to IS NULL)`, params.ProductID).Scan(&exists); err != nil {
			return ProductTaskProgress{}, fmt.Errorf("check current product exists: %w", err)
		}
		if !exists {
			return ProductTaskProgress{}, fmt.Errorf("%w: product id %s", ErrNotFound, params.ProductID)
		}
	}
	return out, nil
}

// containerRefOf is the id the row's own container carries -- the
// milepebble's when the row is one, otherwise the milestone's own. Rows
// arrive grouped by container, so it is how consecutive rows of the same
// container are folded into one.
func containerRefOf(p ContainerTaskProgress) uuid.UUID {
	if p.Milepebble != nil {
		return p.Milepebble.ID
	}
	return p.Milestone.ID
}
