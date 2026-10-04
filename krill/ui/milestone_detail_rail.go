// The Milestone detail page's properties rail (FR c208b777): the five
// label/value rows beside the content -- Status, FR budget, Tasks, Status
// history and Id.
//
// It lives in its own file rather than inside milestone_detail_page.go
// because it is a section of that page, not the page: the header, the
// outcome card and the delivery card are separate sections of one region,
// and each owns its own reads and its own builder.
//
// The rail's figures are not re-derived from anything. FR budget comes off
// the delivery listing entry the handler already resolved, Tasks come off
// the P0 progress read's own Done()/Total(), and the Status history count
// is the length of the very list the status-history view renders -- so a
// number in the rail and the thing it counts cannot drift apart.
package main

import (
	"context"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// milestoneStatusHistorySuffix is the status-history view's own path beneath
// a container's detail URL -- /products/{pid}/milestones/{mid}/status-history.
//
// It hangs UNDER the detail rather than beside it so a copied status-history
// link resolves its product and its container through the same prefixes a
// copied detail link does, and so the detail route's {mid} wildcard can never
// shadow it.
//
// The path is declared here, by the task that adds the LINK, rather than by
// the one that serves the target: a link is a claim about a URL, and a claim
// that arrives with the page is one the page can be held to. The route
// itself is registered by the status-history task (FR 9a6e7924), which reads
// the same helper this rail does -- until it lands, the link renders and
// leads to a route nothing serves yet.
const milestoneStatusHistorySuffix = "/status-history"

// milestoneStatusHistoryHref is the status-history view's URL for one
// container. The rail's Status history link points here.
func milestoneStatusHistoryHref(productID, containerID uuid.UUID) string {
	return productHref(productID, milestonesSuffix+"/"+containerID.String()) +
		milestoneStatusHistorySuffix
}

// milestoneRailReads is what the rail's own two reads answered, kept apart
// from the view model so a read failure is visible as a failure rather than
// as a zero.
//
// Progress and History are separate because they fail separately: a progress
// read that failed costs the Tasks row its figures, and a history read that
// failed costs the Status history row its count. Neither takes the page down,
// because neither is the page -- the container's own name, status and work
// links all come from reads the handler made before this one.
type milestoneRailReads struct {
	Progress store.ContainerTaskProgress
	History  []store.MilestoneStatusEvent

	// ProgressErr and HistoryErr are the failures, when there were any.
	// ProgressErrored says whether the progress row actually accounts for
	// THIS container, which is a different question from whether the read
	// returned an error -- see progressAccountsFor.
	ProgressErr     error
	HistoryErr      error
	ProgressErrored bool
}

// buildMilestoneDetailRail assembles the rail from one resolved container
// and the reads below.
//
// It takes the listing entry rather than the bare taskContainer because FR
// budget is a field of the listing entry and not of taskContainer: the
// container resolution the header uses carries name, kind and status, and
// reaching the budget through it would mean walking the listing twice.
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

// milestoneRailTasksCell is the rail's Tasks figures, or the sentence saying
// they could not be read.
//
// It reuses the Milestones table's own progressCell, so the rail's "N of M
// done" and that table's bar are one count rather than two arithmetic paths
// that could disagree about a cancelled task.
func milestoneRailTasksCell(reads milestoneRailReads) pages.ProgressCell {
	if reads.ProgressErr != nil {
		return pages.ProgressCell{ProgressError: milestonesProgressError}
	}
	if reads.ProgressErrored {
		return pages.ProgressCell{ProgressError: milestonesProgressError}
	}
	return pages.ProgressCell{Done: reads.Progress.Done(), Total: reads.Progress.Total()}
}

// milestoneRailHistoryError is what the Status history row says when the
// read failed, or "" when it answered.
//
// A failed read is never rendered as "0 changes": an absent count and a
// count of zero are different facts, and an operator who reads "0 changes"
// off a failed read concludes the container's status was never touched.
func milestoneRailHistoryError(err error) string {
	if err == nil {
		return ""
	}
	return "Status history could not be read. See the logs."
}

// milestoneRailEntry finds the listing entry one container id names -- the
// milestone's own row, or its milepebble's.
//
// It walks the listing the handler already read rather than re-reading, so
// the rail's budget and the header's name are two fields of ONE row, and a
// container the listing does not carry cannot have a budget invented for it
// here.
func milestoneRailEntry(listing slice.DeliveryListing, id uuid.UUID) slice.MilestoneListingEntry {
	for _, m := range listing.Milestones {
		if m.ID == id {
			return m
		}
		for _, mp := range m.Milepebbles {
			if mp.ID == id {
				// A milepebble is its own milestone_ref row, but the
				// listing nests it under its parent. Its budget is the
				// parent's own field and a milepebble declares none, so
				// this carries the ID and status only -- the rail's budget
				// row is then the honest "no FR budget declared".
				return slice.MilestoneListingEntry{ID: mp.ID, Name: mp.Name, Status: mp.Status}
			}
		}
	}
	return slice.MilestoneListingEntry{}
}

// readMilestoneRailProgress is the rail's Tasks read: this container's own
// progress under the SINGLE-container scope, whichever of the two kinds its
// id is.
//
// The scope is deliberately ProductTaskScopeMilestone/ProductTaskScopeMilepebble
// and not the Milestones table's all-containers ProductTaskScopeAll: this
// rail is about one container, and a whole-roadmap aggregate would make the
// figure depend on how many other milestones the product has.
//
// The milestone kind's scope spans the milestone AND its milepebbles --
// that is what the read means by "this milestone" -- so the result carries a
// row per container and the one that accounts for THIS id is selected. That
// selection is the same comparison the Milestones table makes for the same
// reason: the read hands back the PARENT's id in Milestone for a milepebble's
// row, so a milepebble's figures must never be read off its parent's.
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
	// The read answered without accounting for this container. Reported as
	// its own flag rather than as an error, because it is a different
	// condition: a zero-row container and a container the read skipped are
	// not the same fact, and rendering the second as the first is exactly
	// what this distinction exists to prevent.
	return milestoneRailReads{ProgressErrored: true}
}

// readMilestoneRailHistory is the rail's status-history read: the container's
// whole transition register, whose LENGTH is the count the link prints.
//
// It reads the list rather than a count because the sibling status-history
// view renders that same list -- so the number an operator follows and the
// rows they land on are two renderings of one read, and cannot disagree.
func (app *App) readMilestoneRailHistory(ctx context.Context, c taskContainer) milestoneRailReads {
	events, err := app.spec.StatusHistory(ctx, c.ID)
	if err != nil {
		logger.Error("milestone detail: status history read failed",
			"container", c.ID.String(), "error", err)
		return milestoneRailReads{HistoryErr: err}
	}
	return milestoneRailReads{History: events}
}