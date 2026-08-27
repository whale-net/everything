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
// grpcauth -> API), rather than a placeholder like handleHome's. Wired
// onto the authenticated router only in main.go's setupRoutes --
// app.auth.RequireAuthFunc + app.auth.WithAccessToken wrap it exactly like
// handleHome, so every ListBoards call here carries the signed-in user's
// own forwarded token (NFR18.1), never a service account.
//
// Scaffold scope (#1330): reads an optional ?page_token= query param
// (FR61's opaque keyset page token) and renders whatever page it gets, but
// does not yet drive a "load more" control from the response's
// next_page_token, and this route is not yet the post-login landing page.
// Both are Implementation-phase work on this same task -- see
// boards_data.go and components/boards.templ's doc comments.
func (app *App) handleBoards(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	resp, err := app.fetchBoardsPage(r.Context(), r.URL.Query().Get("page_token"))
	if err != nil {
		log.Printf("fetchBoardsPage failed: %v", err)
		http.Error(w, "Failed to load boards from leaflab-api", http.StatusBadGateway)
		return
	}

	page := components.BoardsPage(user, resp.GetBoards(), resp.GetPage().GetNextPageToken())
	if err := RenderTempl(w, r, "Boards", page); err != nil {
		log.Printf("Failed to render boards page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
