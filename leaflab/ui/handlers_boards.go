package main

import (
	"context"
	"log"
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/ui/pages"
	"github.com/whale-net/everything/leaflab/ui/viewdata"
)

// handleBoards is screen: board list (FR13, FR61, FR64). Every authenticated user
// sees every board — household scoping is FR5 and lands in Phase 2. The page
// exercises keyset pagination per FR61's token contract; times are rendered as
// elapsed instants per FR64.
func (app *App) handleBoards(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}

	data := app.buildBoardsList(r.Context(), r)
	if err := RenderTempl(w, r, "Boards", pages.Boards(user, data)); err != nil {
		log.Printf("Failed to render boards page: %v", err)
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
	}
}

// buildBoardsList fetches the list of boards from the API with pagination
// support. It extracts the page_token from the request query and calls
// ListBoards with the keyset pagination contract from FR61.
func (app *App) buildBoardsList(ctx context.Context, r *http.Request) *viewdata.BoardsData {
	pageToken := r.URL.Query().Get("page_token")

	resp, err := app.apiClient.API.ListBoards(ctx, &pb.ListBoardsRequest{
		Page: &pb.PageRequest{
			PageToken: pageToken,
		},
	})

	if err != nil {
		log.Printf("buildBoardsList: ListBoards failed: %v", err)
		return &viewdata.BoardsData{
			Err: err,
		}
	}

	nextToken := ""
	if resp.GetPage() != nil {
		nextToken = resp.GetPage().GetNextPageToken()
	}

	return &viewdata.BoardsData{
		Boards:        resp.GetBoards(),
		NextPageToken: nextToken,
	}
}
