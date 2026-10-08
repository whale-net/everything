// The Board view of the product-wide task scope: one swimlane per milestone with tasks,
// five lane columns each. The task read and the per-container progress read are asked for
// the same resolved scope, so a lane's cards and its "N of M done" share filters.
package main

import (
	"context"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// boardProgressRead reads the per-container progress aggregate for a resolved scope.
func (app *App) boardProgressRead(ctx context.Context, productID uuid.UUID, scope resolvedProductTaskScope) (store.ProductTaskProgress, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		return store.ProductTaskProgress{}, err
	}
	return app.tasks.SummarizeProductTaskProgress(ctx, store.ProductTaskProgressParams{
		ScopeID:   scopeID,
		ProductID: productID,
		Scope:     scope.Store,
	})
}

// boardProgressError replaces the swimlanes when progress is unreadable; an empty board
// would falsely claim the scope has no tasks.
const boardProgressError = "How far along each milestone is could not be read. See the logs."

// productBoardPageOf builds the Board view model. tasksPath is the Tasks view of the same
// scope and filters, for when the read paged.
func productBoardPageOf(
	product store.Product,
	region pages.ProductTaskRegion,
	scope resolvedProductTaskScope,
	rows []store.ProductTaskRow,
	progress store.ProductTaskProgress,
	progressErr error,
	tasksPath string,
	now time.Time,
) pages.ProductBoardPage {
	board := pages.ProductBoardPage{
		Product:    productHeaderOf(product),
		View:       region.View,
		Path:       region.Path,
		Scope:      region.Scope,
		ScopeLabel: region.ScopeLabel,
		Lane:       region.Lane,
		OnlyStuck:  region.OnlyStuck,
		UpdatedAt:  now.UTC().Format(time.RFC3339),
		TasksPath:  tasksPath,
		// From the region so Board and Tasks agree on where "back to the roadmap" goes.
		MilestonesPath: region.MilestonesPath,
		Shown:          len(rows),
		Total:          region.Total,
	}
	if progressErr != nil {
		board.Error = boardProgressError
		return board
	}
	board.Lanes = boardLanesOf(product.ID, scope, rows, progress, now)
	board.Empty = len(board.Lanes) == 0
	return board
}

// boardProgressIndex indexes progress by container and by milestone. byMilestone holds
// milestone rows only: a milepebble row's PerLane covers just itself, not the whole cut.
type boardProgressIndex struct {
	byContainer map[uuid.UUID]store.ContainerTaskProgress
	byMilestone map[uuid.UUID]store.ContainerTaskProgress
}

// indexBoardProgress builds both indexes; a milepebble row never overwrites its parent in byMilestone.
func indexBoardProgress(containers []store.ContainerTaskProgress) boardProgressIndex {
	idx := boardProgressIndex{
		byContainer: make(map[uuid.UUID]store.ContainerTaskProgress, len(containers)),
		byMilestone: make(map[uuid.UUID]store.ContainerTaskProgress, len(containers)),
	}
	for _, c := range containers {
		if c.Milepebble == nil {
			idx.byMilestone[c.Milestone.ID] = c
			idx.byContainer[c.Milestone.ID] = c
			continue
		}
		idx.byContainer[c.Milepebble.ID] = c
	}
	return idx
}

// boardLaneKeyOf is a row's swimlane. A milepebble task aggregates under its milestone while
// the milestone is in scope (e.g. not when a shipped milestone has a milepebble in design);
// the milepebble scope always keys on the milepebble.
func boardLaneKeyOf(row store.ProductTaskRow, milepebbleScope bool, idx boardProgressIndex) uuid.UUID {
	if row.Milepebble == nil {
		return row.Milestone.ID
	}
	if milepebbleScope {
		return row.Milepebble.ID
	}
	if _, parentInScope := idx.byMilestone[row.Milestone.ID]; parentInScope {
		return row.Milestone.ID
	}
	return row.Milepebble.ID
}

// boardLanesOf partitions rows into swimlanes in the read's order (milestone position
// descending). Only lanes with a card exist, so filtered-away containers do not render
// as zero columns.
func boardLanesOf(productID uuid.UUID, scope resolvedProductTaskScope, rows []store.ProductTaskRow, progress store.ProductTaskProgress, now time.Time) []pages.BoardLane {
	idx := indexBoardProgress(progress.Containers)
	milepebbleScope := scope.Parsed.Kind == store.ProductTaskScopeMilepebble

	lanes := make([]pages.BoardLane, 0, len(idx.byContainer))
	byKey := make(map[uuid.UUID]int, len(idx.byContainer))

	for _, row := range rows {
		key := boardLaneKeyOf(row, milepebbleScope, idx)
		pos, seen := byKey[key]
		if !seen {
			lanes = append(lanes, boardLaneOf(productID, key, row, idx))
			byKey[key] = len(lanes) - 1
			pos = byKey[key]
		}
		column := &lanes[pos].Columns[boardColumnIndex(row.CurrentLane)]
		column.Cards = append(column.Cards, boardCardOf(productID, row, now))
	}

	// Counts are what this scope and filter put on the board, not the progress read's figures.
	for i := range lanes {
		for c := range lanes[i].Columns {
			lanes[i].Columns[c].Count = len(lanes[i].Columns[c].Cards)
		}
	}
	return lanes
}

// boardLaneOf is a new lane's header from the progress read's row; Done/Total are handed
// over whole so cancelled-task counting has one definition. A container missing from the
// read falls back to the row's milestone ref with no figures.
func boardLaneOf(productID, key uuid.UUID, row store.ProductTaskRow, idx boardProgressIndex) pages.BoardLane {
	lane := pages.BoardLane{
		ID:         key.String(),
		DetailPath: milestoneDetailHref(productID, key),
		// Always five columns, so empty ones show zero.
		Columns: emptyBoardColumns(),
	}
	if container, ok := idx.byContainer[key]; ok {
		lane.Done = container.Done()
		lane.Total = container.Total()
		if container.Milepebble != nil {
			lane.Name = container.Milepebble.Name
			lane.Status = string(container.Milepebble.Status)
			lane.MilestoneName = container.Milestone.Name
			lane.MilestoneDetailPath = milestoneDetailHref(productID, container.Milestone.ID)
			return lane
		}
		lane.Name = container.Milestone.Name
		lane.Status = string(container.Milestone.Status)
		return lane
	}

	lane.Name = row.Milestone.Name
	lane.Status = string(row.Milestone.Status)
	if row.Milepebble != nil {
		lane.Name = row.Milepebble.Name
		lane.Status = string(row.Milepebble.Status)
		lane.MilestoneName = row.Milestone.Name
		lane.MilestoneDetailPath = milestoneDetailHref(productID, row.Milestone.ID)
	}
	return lane
}

// boardCardOf is one task's card, with the same badge derivation as the Tasks table so a
// task cannot read differently on list and board.
func boardCardOf(productID uuid.UUID, row store.ProductTaskRow, now time.Time) pages.TaskRow {
	badges := boardCardBadges(row, now)
	card := pages.TaskRow{
		ID:         row.TaskID.String(),
		Title:      row.Title,
		Lane:       string(row.CurrentLane),
		DetailPath: taskDetailPath(productID, row.Milestone.ID, row.TaskID),
		Attempts:   taskAttemptsLabel(row.AttemptCount),
		Badges:     badges,
		// A Done task with nothing outstanding is finished work; badging attempts would read as to-do.
		Quiet: row.CurrentLane == store.LaneDone && len(badges) == 0,
	}
	if row.Milepebble != nil {
		card.Milepebble = row.Milepebble.Name
	}
	// Both claim halves or neither: a claim without a reported expiry cannot be judged live or lapsed.
	if row.ClaimID != nil && row.LeaseExpiresAt != nil {
		card.ClaimID = row.ClaimID.String()
		card.LeaseExpiresAt = row.LeaseExpiresAt.UTC().Format(time.RFC3339)
	}
	return card
}

// boardCardBadges is taskStateBadges over a product read row: a lapsed lease is
// "lease-expired", never "claimed", and the cap is store.DefaultAttemptCap like every view.
func boardCardBadges(row store.ProductTaskRow, now time.Time) []pages.TaskBadge {
	var badges []pages.TaskBadge
	if row.ClaimID != nil && row.LeaseExpiresAt != nil {
		if !row.LeaseExpiresAt.After(now) {
			badges = append(badges, pages.TaskBadge{Key: "lease-expired", Label: "Lease expired"})
		} else {
			badges = append(badges, pages.TaskBadge{Key: "claimed", Label: "Claimed"})
		}
	}
	if row.AttemptCount >= store.DefaultAttemptCap {
		badges = append(badges, pages.TaskBadge{Key: "capped", Label: "Capped"})
	}
	// A cancelled task keeps its escalation on record but is terminal.
	if row.State == store.TaskStateEscalated && row.CancelledAt == nil {
		badges = append(badges, pages.TaskBadge{Key: "escalated", Label: "Escalated"})
	}
	if row.CancelledAt != nil {
		badges = append(badges, pages.TaskBadge{Key: "cancelled", Label: "Cancelled"})
	}
	return badges
}

// emptyBoardColumns is store.CanonicalLaneOrder's five lanes as empty columns.
func emptyBoardColumns() []pages.BoardColumn {
	cols := make([]pages.BoardColumn, 0, len(store.CanonicalLaneOrder))
	for _, lane := range store.CanonicalLaneOrder {
		cols = append(cols, pages.BoardColumn{Lane: string(lane)})
	}
	return cols
}

// boardColumnIndex is a lane's column position. task.current_lane is CHECK-constrained to the
// five lanes, so an unknown lane falls to column 0 rather than panicking.
func boardColumnIndex(lane store.Lane) int {
	for i, l := range store.CanonicalLaneOrder {
		if l == lane {
			return i
		}
	}
	return 0
}

// boardTasksPath is the Tasks view of this board's scope and filters, so "Showing X of Y"
// links to the same rows.
func boardTasksPath(productID uuid.UUID, query url.Values) string {
	path := productHref(productID, tasksSuffix)
	if len(query) == 0 {
		return path
	}
	return path + "?" + query.Encode()
}
