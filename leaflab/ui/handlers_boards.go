package main

import (
	"log"
	"net/http"

	"github.com/whale-net/everything/leaflab/ui/components"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// handleBoards renders the Phase 1 read-only boards screen (NFR18.2,
// NFR19, FR64 rendering): the first screen in this UI backed by real
// leaflab-api data end to end (person -> BFF -> forwarded token ->
// grpcauth -> API), rather than a placeholder like handleHome's own
// content used to be. Wired onto the authenticated router only in
// main.go's setupRoutes -- app.auth.RequireAuthFunc + app.auth.WithAccessToken
// wrap it exactly like handleHome, so every ListBoards call here carries
// the signed-in user's own forwarded token (NFR18.1), never a service
// account. handleHome itself delegates straight here once its own health
// probe (NFR14) reports the API up, making this screen the Phase 1
// post-login landing route.
//
// Reads an optional ?page_token= query param (FR61's opaque keyset page
// token) for page one; every page after that is fetched by
// handleBoardsRows below, from the "Load more" control components.BoardsRows
// renders.
func (app *App) handleBoards(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	resp, err := app.fetchBoardsPage(r.Context(), r.URL.Query().Get("page_token"))
	if err != nil {
		log.Printf("fetchBoardsPage failed: %v", err)
		http.Error(w, "Failed to load boards from leaflab-api", http.StatusBadGateway)
		return
	}

	page := components.BoardsPage(user, resp.GetBoards(), resp.GetServerNow(), resp.GetPage().GetNextPageToken())
	if err := RenderTempl(w, r, "Boards", page); err != nil {
		log.Printf("Failed to render boards page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// handleBoardsRows serves FR61's "load more" continuation: an htmx
// partial hit from components.BoardsRows's own "Load more" button
// (hx-get, targeting #boards-load-more, hx-swap="outerHTML"). It renders
// only the next page's <tr> rows -- plus a fresh "Load more" row if
// another page remains -- never the full page shell, so the browser's own
// DOM is the only place a page beyond the first ever exists: nothing on
// this BFF accumulates the full board list across pages ("No client-side
// accumulation of an unbounded list").
//
// Registered on the authenticated router only, same wrapping as
// handleBoards, so this continuation carries the same forwarded token
// (NFR18.1) as the page it continues.
func (app *App) handleBoardsRows(w http.ResponseWriter, r *http.Request) {
	resp, err := app.fetchBoardsPage(r.Context(), r.URL.Query().Get("page_token"))
	if err != nil {
		log.Printf("fetchBoardsPage failed: %v", err)
		http.Error(w, "Failed to load boards from leaflab-api", http.StatusBadGateway)
		return
	}

	rows := components.BoardsRows(resp.GetBoards(), resp.GetServerNow(), resp.GetPage().GetNextPageToken())
	if err := rows.Render(r.Context(), w); err != nil {
		log.Printf("Failed to render boards rows: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
