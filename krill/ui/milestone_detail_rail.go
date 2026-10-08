// The Milestone detail page's properties rail: Status, FR budget, Tasks, Status
// history and Id. Figures come from reads the page already renders, so a rail
// number and the thing it counts cannot drift apart.
package main

import (
	"context"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// milestoneStatusHistorySuffix hangs under the detail URL so a copied link
// resolves the same prefixes and the {mid} wildcard can never shadow it.
const milestoneStatusHistorySuffix = "/status-history"

// milestoneStatusHistoryHref is the status-history view's URL for one
// container. The rail's Status history link points here.
func milestoneStatusHistoryHref(productID, containerID uuid.UUID) string {
	return productHref(productID, milestonesSuffix+"/"+containerID.String()) +
		milestoneStatusHistorySuffix
}

// milestoneRailReads holds the rail's two reads apart from the view model so a
// failure shows as a failure, not a zero. Each fails independently of the page.
type milestoneRailReads struct {
	Progress store.ContainerTaskProgress
	History  []store.MilestoneStatusEvent

	// ProgressErrored says whether the progress row accounts for this container,
	// which differs from the read returning an error.
	ProgressErr     error
	HistoryErr      error
	ProgressErrored bool
}

// buildMilestoneDetailRail assembles the rail. It takes the listing entry
// because FR budget lives there, not on taskContainer.
func buildMilestoneDetailRail(productID uuid.UUID, entry slice.MilestoneListingEntry, c taskContainer, reads milestoneRailReads) pages.MilestoneRail {
	return pages.MilestoneRail{
		Status:            string(c.Status),
		FRBudget:          frBudgetString(entry.FRBudget),
		Tasks:             milestoneRailTasksCell(reads),
		StatusHistoryPath: milestoneStatusHistoryHref(productID, c.ID),
		StatusChanges:     len(reads.History),
		HistoryError:      milestoneRailHistoryError(reads.HistoryErr),
		ID:                c.ID.String(),
	}
}

// milestoneRailTasksCell reuses the Milestones table's progressCell so both
// show one count.
func milestoneRailTasksCell(reads milestoneRailReads) pages.ProgressCell {
	if reads.ProgressErr != nil {
		return pages.ProgressCell{ProgressError: milestonesProgressError}
	}
	if reads.ProgressErrored {
		return pages.ProgressCell{ProgressError: milestonesProgressError}
	}
	return pages.ProgressCell{Done: reads.Progress.Done(), Total: reads.Progress.Total()}
}

// milestoneRailHistoryError is the Status history row's failure text, or "".
// A failed read is never shown as "0 changes".
func milestoneRailHistoryError(err error) string {
	if err == nil {
		return ""
	}
	return "Status history could not be read. See the logs."
}

// milestoneRailEntry finds the listing entry for one container id without
// re-reading, so a container missing from the listing gets no invented budget.
func milestoneRailEntry(listing slice.DeliveryListing, id uuid.UUID) slice.MilestoneListingEntry {
	for _, m := range listing.Milestones {
		if m.ID == id {
			return m
		}
		for _, mp := range m.Milepebbles {
			if mp.ID == id {
				// A milepebble declares no budget of its own, so the rail shows
				// "no FR budget declared".
				return slice.MilestoneListingEntry{ID: mp.ID, Name: mp.Name, Status: mp.Status}
			}
		}
	}
	return slice.MilestoneListingEntry{}
}

// readMilestoneRailProgress reads this container's own progress under the
// single-container scope. The milestone scope also returns milepebble rows, and
// a milepebble row carries its parent's id, so the matching row is selected.
func (app *App) readMilestoneRailProgress(ctx context.Context, productID uuid.UUID, c taskContainer) milestoneRailReads {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		return milestoneRailReads{ProgressErr: err}
	}
	kind := store.ProductTaskScopeMilestone
	if c.Kind == string(store.MilestoneKindMilepebble) {
		kind = store.ProductTaskScopeMilepebble
	}
	progress, err := app.tasks.SummarizeProductTaskProgress(ctx, store.ProductTaskProgressParams{
		ScopeID:   scopeID,
		ProductID: productID,
		Scope:     store.ProductTaskScope{Kind: kind, ContainerID: c.ID},
	})
	if err != nil {
		return milestoneRailReads{ProgressErr: err}
	}
	for _, row := range progress.Containers {
		if progressAccountsFor(row, c.ID) {
			return milestoneRailReads{Progress: row}
		}
	}
	// The read skipped this container: distinct from zero tasks, so flagged.
	return milestoneRailReads{ProgressErrored: true}
}

// readMilestoneRailHistory reads the full transition list, not a count, so the
// linked number and the status-history view's rows cannot disagree.
func (app *App) readMilestoneRailHistory(ctx context.Context, c taskContainer) milestoneRailReads {
	events, err := app.spec.StatusHistory(ctx, c.ID)
	if err != nil {
		logger.Error("milestone detail: status history read failed",
			"container", c.ID.String(), "error", err)
		return milestoneRailReads{HistoryErr: err}
	}
	return milestoneRailReads{History: events}
}
