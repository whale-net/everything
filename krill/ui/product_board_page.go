// The Board view of the product-wide task scope (FR cf000440): one
// swimlane per milestone that has tasks, each with five counted lane
// columns, laid out to scroll sideways inside the board rather than
// sideways across the page.
//
// Two reads answer it and neither is derived from the other. The task read
// (product_task_scope.go) says which container each row belongs to and in
// which lane it sits, in the read's own order; the per-container progress
// read says how far along each container is. Both are asked for the very
// same resolved scope, so a lane's cards and the "N of M done" printed in
// its header can never describe different filters.
//
// The builder is pure: it takes the rows and the progress aggregate and
// returns the view model, so the shape of a board is testable without a
// server.
package main

import (
	"context"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// boardProgressRead reads the per-container progress aggregate for a
// resolved scope -- the read the Overview's in-flight panel already uses,
// asked here of the same scope the board's rows were read with.
//
// It returns its zero value alongside the error rather than an error alone
// so a caller cannot accidentally render a board from an absent aggregate.
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

// boardProgressError is a failed progress read in the operator's words.
//
// It is a sentence in place of the swimlanes rather than an empty board,
// for the Overview panel's reason: "this scope has no tasks" and "we could
// not check how far along it is" are different answers, and a board with
// no lanes in it states the first one while the second is true.
const boardProgressError = "How far along each milestone is could not be read. See the logs."

// productBoardPageOf builds the Board view model from one scope's rows and
// its per-container progress.
//
// tasksPath is the Tasks view of this same scope and filter set -- the way
// out of the board when the read paged and the board cannot show
// everything.
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
		// Read off the region rather than derived here, so the Board cannot
		// disagree with the Tasks view about where "back to the roadmap"
		// goes for this scope.
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

// boardProgressIndex is one progress aggregate indexed by the two ids a
// swimlane can be keyed by, so the lane a row belongs to can be found
// without a scan per row.
//
// byMilestone holds the MILESTONE rows only. A milepebble's own row is
// deliberately kept out of it: that row's PerLane covers the milepebble
// alone, while the milestone row's covers the whole cut, and letting the
// two answer for one id is how a header's "N of M done" comes to disagree
// with the columns under it.
type boardProgressIndex struct {
	byContainer map[uuid.UUID]store.ContainerTaskProgress
	byMilestone map[uuid.UUID]store.ContainerTaskProgress
}

// indexBoardProgress builds the two indexes. A milepebble row never
// overwrites its parent milestone's entry in byMilestone; in byContainer
// the two are separate keys and both survive.
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

// boardLaneKeyOf is the swimlane one task row belongs to.
//
// A task scoped to a milepebble aggregates under its milestone whenever the
// milestone is itself in scope -- that is the whole point of a milestone's
// PerLane covering the cut -- and gets a lane of its own when it is not.
// The case is a shipped milestone with a milepebble still in design: the
// parent has dropped out of the all-incomplete scope and its lane would
// claim to describe work the board is not showing.
//
// The milepebble scope keys on the milepebble itself, which is the one
// case FR cf000440 asks to be named and badged on its own terms.
func boardLaneKeyOf(row store.ProductTaskRow, milepebbleScope bool, idx boardProgressIndex) uuid.UUID {
	if row.Milepebble == nil {
		return row.Milestone.ID
	}
	// The milepebble scope is about the milepebble, full stop: it names one
	// container and the board is about that one.
	if milepebbleScope {
		return row.Milepebble.ID
	}
	// Everywhere else a milepebble's task aggregates under its milestone,
	// for as long as the milestone is itself in scope.
	if _, parentInScope := idx.byMilestone[row.Milestone.ID]; parentInScope {
		return row.Milestone.ID
	}
	return row.Milepebble.ID
}

// boardLanesOf partitions the scope's rows into swimlanes.
//
// The lanes come out in the order the rows arrive -- the task read's own
// milestone position DESCENDING order, which is also the order the store's
// product-wide read and FR cf000440 both name -- so the board's lane order
// is the read's, not a second sort spelled out here that could disagree
// with it.
//
// A lane exists only where a card sits in it. A container the active lane
// or only-stuck filter emptied out would otherwise render five columns
// headed by zeros, which reads as a milestone that has fallen behind rather
// than one whose tasks were filtered away.
//
// now is the read instant, handed to every card so one lease is judged
// against the same clock as the next -- see boardCardOf.
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

	// The counts are the cards' own length, counted once the lane is
	// built. They are the board's own rendering, not the progress read's:
	// the read answers for the whole container while these answer for what
	// this scope and these filters put on the board.
	for i := range lanes {
		for c := range lanes[i].Columns {
			lanes[i].Columns[c].Count = len(lanes[i].Columns[c].Cards)
		}
	}
	return lanes
}

// boardLaneOf is a new lane's header, taken from the progress read's own
// row for that container.
//
// The figures are that row's Done() and Total() handed over whole. Summing
// the five lanes again here would be a second definition of how a
// cancelled task counts, and that is exactly how a header comes to
// disagree with the breakdown printed beside it.
//
// A container the read did not answer for -- a read that was asked about
// the same scope and did not cover it -- falls back to the row's own
// milestone ref for the name and badge, with no figures rather than invented
// ones.
func boardLaneOf(productID, key uuid.UUID, row store.ProductTaskRow, idx boardProgressIndex) pages.BoardLane {
	lane := pages.BoardLane{
		ID:         key.String(),
		DetailPath: milestoneDetailHref(productID, key),
		// Five columns from the start, whatever the read covered: a lane
		// with one card in one of them still shows the other four, headed
		// by zero.
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

// boardCardOf is one task's card (FR f6b62cc7): the title linking to the
// detail page, the milepebble it came from when the milestone is cut, its
// state badges, its attempts against the cap, and the claim identity the
// row observed with its lease expiry.
//
// The badges come from the same derivation the Tasks table makes over this
// same read's row -- one state mapper, applied once -- so a task that is
// Claimed on the list cannot read as lease-expired on the board.
func boardCardOf(productID uuid.UUID, row store.ProductTaskRow, now time.Time) pages.TaskRow {
	badges := boardCardBadges(row, now)
	card := pages.TaskRow{
		ID:         row.TaskID.String(),
		Title:      row.Title,
		Lane:       string(row.CurrentLane),
		DetailPath: taskDetailPath(productID, row.Milestone.ID, row.TaskID),
		Attempts:   taskAttemptsLabel(row.AttemptCount),
		Badges:     badges,
		// The carve-out: a Done-lane task with nothing outstanding is
		// finished work, and a card badging its attempts would read as
		// work still to do.
		Quiet: row.CurrentLane == store.LaneDone && len(badges) == 0,
	}
	if row.Milepebble != nil {
		card.Milepebble = row.Milepebble.Name
	}
	// Both halves, or neither -- the rule the read's own row guarantees
	// and the Tasks table follows: a claim whose expiry was not reported
	// cannot be judged live or lapsed, so badging it would contradict the
	// claim id the card does or does not carry.
	if row.ClaimID != nil && row.LeaseExpiresAt != nil {
		card.ClaimID = row.ClaimID.String()
		card.LeaseExpiresAt = row.LeaseExpiresAt.UTC().Format(time.RFC3339)
	}
	return card
}

// boardCardBadges is one card's state badges, derived over the product
// read's own row.
//
// It is the same derivation taskStateBadges makes over a TaskSummary, and
// the same one the Tasks table makes over this row: a claim whose lease
// has lapsed is "lease-expired" and never "claimed", and a task in no
// state yields no badges -- which is what leaves a Done card's badge row
// empty. The colours come from components.TaskStateStyle, so the badge's
// appearance cannot drift from the list's or the detail's.
//
// The cap is store.DefaultAttemptCap rather than the row's own AttemptCap
// field for the same reason taskAttemptsLabel uses it: that is the cap the
// read reports and the one every view counts against, so two spellings
// would be two answers to "when is this capped".
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
	if row.State == store.TaskStateEscalated {
		badges = append(badges, pages.TaskBadge{Key: "escalated", Label: "Escalated"})
	}
	if row.CancelledAt != nil {
		badges = append(badges, pages.TaskBadge{Key: "cancelled", Label: "Cancelled"})
	}
	return badges
}

// emptyBoardColumns is store.CanonicalLaneOrder's five lanes as empty
// columns, so a lane that has one card in one lane still carries all five
// and the zeros are visible rather than absent.
func emptyBoardColumns() []pages.BoardColumn {
	cols := make([]pages.BoardColumn, 0, len(store.CanonicalLaneOrder))
	for _, lane := range store.CanonicalLaneOrder {
		cols = append(cols, pages.BoardColumn{Lane: string(lane)})
	}
	return cols
}

// boardColumnIndex is a task's lane as a position in the five columns.
// A lane outside store.CanonicalLaneOrder cannot be stored
// (task.current_lane is CHECK-constrained to exactly these five), so
// anything else is treated as the first column rather than panicking a
// read-only page over a row that should not exist.
func boardColumnIndex(lane store.Lane) int {
	for i, l := range store.CanonicalLaneOrder {
		if l == lane {
			return i
		}
	}
	return 0
}

// boardTasksPath is the Tasks view of this board's own scope and filter
// set: the same product, the same query, the other view -- so the link the
// "Showing X of Y" line offers lands on exactly the rows the board was
// showing rather than on an unfiltered list.
func boardTasksPath(productID uuid.UUID, query url.Values) string {
	path := productHref(productID, tasksSuffix)
	if len(query) == 0 {
		return path
	}
	return path + "?" + query.Encode()
}
