// The Overview's four stat tiles: Escalated, Claimed, Open notes and
// Blocking questions, each linking to whatever it counts (FR c0baeb2e).
//
// Every figure comes from a read P0 already shipped, narrowed to the
// current product and to no milestone -- an escalation in another
// milestone of the same product is still something this operator must see.
//
// A tile's sub-line is counted by the same store read that counts its
// figure, over the same FROM/JOIN/WHERE with one conjunct added, rather
// than by a second read that could be describing different rows. That is
// what store.ConsoleOverviewCounts does for the three console queues: one
// call answers all six figures, and the escalated figure it returns is
// CountEscalatedTasks' own answer to the same params.
//
// Each tile's figures are built independently and carried on the tile
// itself, so a region whose read fails costs that region its figure alone
// rather than the whole strip.
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

// overviewTileError is the failure text a tile renders in place of its
// figure. It is phrased as the missing fact rather than as an apology: the
// operator needs to know which number is missing, and the logs carry why.
func overviewTileError(label string) string {
	return label + " could not be read. See the logs."
}

// overviewStatTiles builds the four tiles for one product.
//
// The escalated figure is the count the Needs-attention badge already
// holds, not a second read of it: the badge and this tile are required to
// show the same number, and two reads is how two numbers happen. The
// remaining figures, and every sub-line, come from one product-wide
// console read plus the product's design-session summary.
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

// escalatedStatTile is the Escalated tile.
//
// Its figure is the badge's own, so the sidebar count, this tile, and the
// unfiltered Escalated tab are one number by construction rather than by
// three queries that happen to agree. Its sub-line narrows that same queue
// to the recent window, counted by the console read that counts the other
// two queues -- so the two numbers describe one row set at two moments,
// and an operator who escalates something and sees the figure rise knows
// which part of the queue is new.
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

// recentEscalationSubLine reads "N new in the last hour" beneath an
// escalated figure. It is a narrowing of that figure by time, never the
// figure again: an escalation from yesterday is still in the queue and is
// not what this line is counting.
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

// leaseSubLine reads "N leases expire within 10 min" beside a claimed
// figure. It names the window the store measured, so an operator can tell
// an imminent lapse from a lease that merely exists.
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

// scopeNoteSubLine narrows an open-notes figure to the scope notes among
// them, which are the ones that mean work was deferred rather than merely
// recorded.
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
		// The product's own session list, never the typed-id design root:
		// an operator following this figure wants the sessions holding the
		// questions, not a page asking which product to look at.
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

// designSessionSubLine reads "in N design sessions" beneath a
// blocking-question figure. It is the count of sessions holding at least
// one open blocking question, not the question count again -- a number
// repeated under itself tells an operator nothing they cannot already
// read, and this one says how many places an answer is owed.
func designSessionSubLine(sessions int) string {
	if sessions == 1 {
		return "in 1 design session"
	}
	return "in " + strconv.Itoa(sessions) + " design sessions"
}

// consoleOverviewCounts reads every console figure the tiles show, in one
// call, narrowed to one product.
//
// The escalated row's params are deliberately the badge's params: the
// store counts both over the same clause, so the tile's figure and the
// badge's cannot disagree about which escalations are in the queue.
//
// Now is passed explicitly rather than left to the store's clock so one
// render answers for one moment, and a test can hold the clock still and
// assert that a sub-line agrees with the figure it is derived from.
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

// clock is the instant the Overview's two windows are measured from. It is
// a field so a test can hold time still and pin a sub-line against the
// figure it is derived from; a nil field reads the wall clock, which is
// the only behaviour production ever sees.
func (app *App) clock() time.Time {
	if app.now != nil {
		return app.now()
	}
	return time.Now()
}