// This file (FR 59f664ff-3aa9-4d90-a861-d7758ecee040) is the product-wide
// per-container task-progress read: one call that returns, for every
// milestone and milepebble of a product, how many tasks the container
// holds, how many sit in each of the five lanes, and how many of them are
// Done. It exists because the "N of M tasks done" figure on the Overview,
// the Milestones list, a milestone's detail page and each Board swimlane
// header would otherwise cost one count query per container.
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
// ProductTaskScopeMilepebble walks one milepebble whatever its status. The
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
// Scaffold-phase stub: returns ErrNotImplemented unconditionally. This
// task's Implementation phase fills in the single statement this file's
// doc comment describes -- `milestone_ref` left-joined to `task` on the
// container row task.milestone_id names, grouped by (container, lane) so
// one pass produces every container's whole breakdown including the
// zero-task containers a LEFT JOIN keeps.
func (s taskStore) SummarizeProductTaskProgress(ctx context.Context, params ProductTaskProgressParams) (ProductTaskProgress, error) {
	return ProductTaskProgress{}, ErrNotImplemented
}
