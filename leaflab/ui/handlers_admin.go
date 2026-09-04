package main

import (
	"fmt"
	"net/http"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/leaflab/ui/components"
	"github.com/whale-net/everything/leaflab/ui/pages"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// handleAdminBoards is the admin ownership screen (#1777: FR11, the screen
// half of FR14), routed at "/admin/boards". It calls ListOwnedBoards and
// ListUsers on leaflab-api with the signed-in user's own access token
// (NFR2, forwarded by htmxauth.Authenticator.WithAccessToken in
// setupRoutes) and renders exactly what the API returns -- this handler
// runs no SQL and makes no local role determination (the task issue's UI
// screen section: "the UI must not make its own local role determination
// that could diverge from the server's").
//
// layoutData.IsAdmin is set from whether ListOwnedBoards itself succeeded:
// a non-admin gets codes.PermissionDenied from requireAdmin before this
// handler ever gets a boards slice back, so a successful call is itself
// proof of admin status -- no separate "am I admin" RPC is needed. This is
// presentation only (components.LayoutData.IsAdmin's doc comment); it
// gates nothing this handler itself enforces.
//
// Scaffold scope (#1777): a non-admin's codes.PermissionDenied currently
// renders through the same generic loadErr alert as any other failure.
// Rendering it as a dedicated 403-style page (Testing criterion 17) is
// this task's own Implementation-phase work, once ListOwnedBoards itself
// is wired to requireAdmin server-side.
func (app *App) handleAdminBoards(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	boardsResp, err := app.api.ListOwnedBoards(r.Context())
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unauthenticated {
			// Same re-authenticate flow as handleBoards' identical branch
			// -- see its comment for why this is a redirect, not an error
			// page.
			loginURL := fmt.Sprintf("/auth/login?next=%s", r.URL.RequestURI())
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", loginURL)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		app.log().Warn("ListOwnedBoards failed", "err", err)
	}

	// Best-effort: a users-load failure just leaves the reassign picker
	// empty, it does not block rendering the boards themselves.
	usersResp, usersErr := app.api.ListUsers(r.Context())
	if usersErr != nil {
		app.log().Warn("ListUsers failed", "err", usersErr)
	}

	layoutData := components.LayoutData{
		Title:   "Board Ownership",
		User:    user,
		IsAdmin: err == nil,
	}

	if renderErr := RenderTempl(w, r, "Board Ownership", pages.AdminBoards(layoutData, boardsResp.GetBoards(), usersResp.GetUsers(), err)); renderErr != nil {
		app.log().Error("failed to render admin boards page", "err", renderErr)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleReassignBoardOwner is the reassign picker's POST target (#1777:
// FR12), routed at "POST /admin/boards/{board_id}/reassign". It calls
// ReassignBoardOwner on leaflab-api with the signed-in user's own access
// token (NFR2) and always re-renders the "#owned-boards-table" fragment
// via renderOwnedBoardsFragment afterward -- mirroring
// handleRenameBoard's re-fetch-then-render-fragment pattern
// (handlers_boards.go), so the fragment always reflects what leaflab-api
// actually has, including on a rejected reassign (unowned board, reassign
// to the current owner, or unknown user -- server.go's ReassignBoardOwner
// RPC).
func (app *App) handleReassignBoardOwner(w http.ResponseWriter, r *http.Request) {
	boardID, parseErr := strconv.ParseInt(r.PathValue("board_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	newOwnerID, parseErr := strconv.ParseInt(r.FormValue("new_owner_leaflab_user_id"), 10, 64)
	if parseErr != nil {
		http.Error(w, "invalid new_owner_leaflab_user_id", http.StatusBadRequest)
		return
	}

	if _, err := app.api.ReassignBoardOwner(r.Context(), boardID, newOwnerID); err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unauthenticated {
			loginURL := "/auth/login?next=/admin/boards"
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", loginURL)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		app.log().Info("reassign board owner refused or failed", "board_id", boardID, "new_owner_leaflab_user_id", newOwnerID, "err", err)
	}

	app.renderOwnedBoardsFragment(w, r)
}

// handleClearBoardOwner is the clear button's POST target (#1777: FR13),
// routed at "POST /admin/boards/{board_id}/clear". It calls
// ClearBoardOwner on leaflab-api with the signed-in user's own access
// token (NFR2) and always re-renders the "#owned-boards-table" fragment
// via renderOwnedBoardsFragment afterward -- see
// handleReassignBoardOwner's doc comment for why.
func (app *App) handleClearBoardOwner(w http.ResponseWriter, r *http.Request) {
	boardID, parseErr := strconv.ParseInt(r.PathValue("board_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	if _, err := app.api.ClearBoardOwner(r.Context(), boardID); err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unauthenticated {
			loginURL := "/auth/login?next=/admin/boards"
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", loginURL)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		app.log().Info("clear board owner refused or failed", "board_id", boardID, "err", err)
	}

	app.renderOwnedBoardsFragment(w, r)
}

// renderOwnedBoardsFragment re-fetches ListOwnedBoards/ListUsers and
// re-renders exactly the "#owned-boards-table" fragment -- shared by
// handleReassignBoardOwner and handleClearBoardOwner after their mutation
// attempt, per the task issue's UI screen section ("Mutations re-render
// the affected table fragment via HTMX"). The board's/users' current state
// is always re-fetched rather than assumed from the request, so the
// fragment reflects what leaflab-api actually has even on a rejected
// mutation.
func (app *App) renderOwnedBoardsFragment(w http.ResponseWriter, r *http.Request) {
	boardsResp, err := app.api.ListOwnedBoards(r.Context())
	if err != nil {
		app.log().Warn("ListOwnedBoards failed after mutation attempt", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	usersResp, usersErr := app.api.ListUsers(r.Context())
	if usersErr != nil {
		app.log().Warn("ListUsers failed after mutation attempt", "err", usersErr)
	}

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	if renderErr := pages.OwnedBoardsTable(boardsResp.GetBoards(), usersResp.GetUsers()).Render(r.Context(), w); renderErr != nil {
		app.log().Error("failed to render owned boards table fragment", "err", renderErr)
	}
}
