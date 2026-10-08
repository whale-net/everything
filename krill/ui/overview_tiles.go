// The Overview's four stat tiles (Escalated, Claimed, Open notes, Blocking questions),
// product-wide across milestones. Each sub-line is counted by the same store read as its
// figure, and each tile carries its own error so one failed read costs only that tile.
package main

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// overviewTileError is the text a tile shows in place of an unreadable figure.
func overviewTileError(label string) string {
	return label + " could not be read. See the logs."
}

// overviewStatTiles builds the four tiles for one product. The escalated figure reuses
// the Needs-attention badge count so the two cannot disagree.
func (app *App) overviewStatTiles(r *http.Request, productID uuid.UUID, badge navBadge) []pages.OverviewStatTile {
	counts, countsErr := app.consoleOverviewCounts(r.Context(), productID)
	countsRead := countsErr == nil

	return []pages.OverviewStatTile{
		app.escalatedStatTile(badge, counts, countsRead),
		claimedStatTile(counts, countsRead),
		openNotesStatTile(counts, countsRead),
		app.blockingQuestionsStatTile(r.Context(), productID),
	}
}

// escalatedStatTile is the Escalated tile; its figure is the badge's own, and its
// sub-line narrows the same queue to the recent window.
func (app *App) escalatedStatTile(badge navBadge, counts store.ConsoleOverviewCounts, countsRead bool) pages.OverviewStatTile {
	tile := pages.OverviewStatTile{
		Label:      "Escalated",
		Href:       escalatedTabHref,
		ValueClass: "text-error",
	}
	if !badge.readable {
		tile.Err = overviewTileError("Escalated tasks")
		return tile
	}
	tile.Count = strconv.Itoa(badge.count)
	if countsRead {
		tile.Sub = recentEscalationSubLine(counts.EscalatedRecently)
	}
	return tile
}

// recentEscalationSubLine reads "N new in the last hour": a time-narrowing of the
// escalated figure, not the figure again.
func recentEscalationSubLine(recent int) string {
	if recent == 1 {
		return "1 new in the last hour"
	}
	return strconv.Itoa(recent) + " new in the last hour"
}

// claimedStatTile is the Claimed tile: how many tasks are held by a live
// claim, and how many of those leases lapse within the next ten minutes.
func claimedStatTile(counts store.ConsoleOverviewCounts, read bool) pages.OverviewStatTile {
	tile := pages.OverviewStatTile{Label: "Claimed", Href: opsClaimedPath}
	if !read {
		tile.Err = overviewTileError("Claimed tasks")
		return tile
	}
	tile.Count = strconv.Itoa(counts.Claimed)
	tile.Sub = leaseSubLine(counts.ClaimsExpiringSoon)
	return tile
}

// leaseSubLine reads "N leases expire within 10 min" beside a claimed figure.
func leaseSubLine(expiringSoon int) string {
	if expiringSoon == 1 {
		return "1 lease expires within 10 min"
	}
	return strconv.Itoa(expiringSoon) + " leases expire within 10 min"
}

// openNotesStatTile is the Open notes tile: every note still open on the
// product's work, and how many of those are scope notes.
func openNotesStatTile(counts store.ConsoleOverviewCounts, read bool) pages.OverviewStatTile {
	tile := pages.OverviewStatTile{Label: "Open notes", Href: opsNotesPath}
	if !read {
		tile.Err = overviewTileError("Open notes")
		return tile
	}
	tile.Count = strconv.Itoa(counts.OpenNotes)
	tile.Sub = scopeNoteSubLine(counts.OpenScopeNotes)
	return tile
}

// scopeNoteSubLine counts the scope notes among open notes, i.e. deferred work.
func scopeNoteSubLine(n int) string {
	if n == 1 {
		return "1 scope note"
	}
	return strconv.Itoa(n) + " scope notes"
}

// blockingQuestionsStatTile is the Blocking questions tile: how many
// questions are open across the product's design sessions, and how many
// sessions are waiting on at least one of them.
func (app *App) blockingQuestionsStatTile(ctx context.Context, productID uuid.UUID) pages.OverviewStatTile {
	tile := pages.OverviewStatTile{
		Label:      "Blocking questions",
		ValueClass: "text-warning",
		// The product's session list, not the design root, so the link lands on the questions.
		Href: designProductSessionsPath(productID),
	}
	summary, err := app.designSessions.SummarizeByProduct(ctx, productID)
	if err != nil {
		logger.Warn("overview blocking-questions read failed",
			"product", productID.String(), "error", err)
		tile.Err = overviewTileError("Blocking questions")
		return tile
	}
	tile.Count = strconv.Itoa(summary.OpenBlockingQuestionCount)
	tile.Sub = designSessionSubLine(summary.SessionsHoldingOpenBlocking)
	return tile
}

// designSessionSubLine reads "in N design sessions": the sessions holding at least one
// open blocking question, i.e. how many places an answer is owed.
func designSessionSubLine(sessions int) string {
	if sessions == 1 {
		return "in 1 design session"
	}
	return "in " + strconv.Itoa(sessions) + " design sessions"
}

// consoleOverviewCounts reads every console figure for one product in one call. Escalated
// params match the badge's, and Now is explicit so one render answers for one moment.
func (app *App) consoleOverviewCounts(ctx context.Context, productID uuid.UUID) (store.ConsoleOverviewCounts, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		logger.Warn("overview console counts: could not resolve scope", "error", err)
		return store.ConsoleOverviewCounts{}, err
	}
	filter := store.ConsoleFilter{ProductID: &productID}
	counts, err := app.tasks.CountConsoleOverview(ctx, store.ConsoleOverviewParams{
		Escalated: store.ListEscalatedTasksParams{ScopeID: scopeID, ConsoleFilter: filter},
		Claimed:   store.ListClaimedTasksParams{ScopeID: scopeID, ConsoleFilter: filter},
		Notes:     store.ListOpenNotesParams{ScopeID: scopeID, ConsoleFilter: filter},
		Now:       app.clock(),
	})
	if err != nil {
		logger.Warn("overview console counts unreadable", "product", productID.String(), "error", err)
		return store.ConsoleOverviewCounts{}, err
	}
	return counts, nil
}

// clock is the instant the Overview's windows are measured from; app.now lets tests
// hold time still, nil reads the wall clock.
func (app *App) clock() time.Time {
	if app.now != nil {
		return app.now()
	}
	return time.Now()
}
