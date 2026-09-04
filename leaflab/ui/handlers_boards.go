package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/leaflab/ui/components"
	"github.com/whale-net/everything/leaflab/ui/pages"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// handleBoards is the boards list screen (#1502: FR4, FR5, NFR1). It calls
// ListBoardsWithState on leaflab-api with the signed-in user's own access
// token (NFR2 -- forwarded onto the request context by
// htmxauth.Authenticator.WithAccessToken in setupRoutes) and renders
// exactly what the API returns: every board (FR4), each board's
// API-supplied ReportingState verbatim (FR5), and nothing on a timer
// (NFR1). This handler runs no SQL and touches no board/sensor/
// sensor_reading table itself.
func (app *App) handleBoards(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	resp, err := app.api.ListBoardsWithState(r.Context())
	if err != nil {
		// A codes.Unauthenticated response means the token
		// WithAccessToken already validated locally (session-side) was
		// rejected server-side -- e.g. revoked or expired between
		// WithAccessToken's check and this call. The fix is the same
		// re-authenticate flow WithAccessToken itself takes on a missing/
		// expired local token (libs/go/htmxauth/auth.go), not an error
		// page: mirror that redirect here rather than falling through to
		// the generic loadErr rendering below.
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unauthenticated {
			loginURL := fmt.Sprintf("/auth/login?next=%s", r.URL.RequestURI())
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", loginURL)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		app.log().Warn("ListBoardsWithState failed", "err", err)
	}

	layoutData := components.LayoutData{
		Title: "Boards",
		User:  user,
	}

	// resp.GetBoards() on a nil resp (the err != nil, non-Unauthenticated
	// path above) is safe: generated proto getters nil-check their
	// receiver and return the zero value.
	if renderErr := RenderTempl(w, r, "Boards", pages.Boards(layoutData, resp.GetBoards(), err)); renderErr != nil {
		app.log().Error("failed to render boards page", "err", renderErr)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleBoardDetail is the board detail screen (#1503: FR6, FR7, NFR1),
// routed at "/boards/{board_id}". It calls GetBoardDetail on leaflab-api
// with the signed-in user's own access token (NFR2, forwarded by
// htmxauth.Authenticator.WithAccessToken in setupRoutes) and renders every
// sensor the API returns (FR6) with its own reporting state and, when
// present, its most recent reading including its valid flag (FR7). This
// handler runs no SQL and touches no board/sensor/sensor_reading table
// itself.
//
// A malformed or unknown board_id gets a real HTTP 404
// (http.NotFound/codes.NotFound below) rather than the "loadErr" banner
// handleBoards uses for a generic failure -- the issue's Empty and error
// states section requires a 404 be distinguishable from "board has no
// sensors", which an error banner on a 200 response would not be.
func (app *App) handleBoardDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	boardID, parseErr := strconv.ParseInt(r.PathValue("board_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	// Set only by handleClaimBoard's post-claim redirect (#1765) -- empty on
	// a normal navigation to this page.
	claimErr := r.URL.Query().Get("claim_error")

	resp, err := app.api.GetBoardDetail(r.Context(), boardID)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.Unauthenticated:
				// Same re-authenticate flow as handleBoards' identical
				// branch -- see its comment for why this is a redirect,
				// not an error page.
				loginURL := fmt.Sprintf("/auth/login?next=%s", r.URL.RequestURI())
				if r.Header.Get("HX-Request") == "true" {
					w.Header().Set("HX-Redirect", loginURL)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, loginURL, http.StatusSeeOther)
				return
			case codes.NotFound:
				http.NotFound(w, r)
				return
			}
		}
		app.log().Warn("GetBoardDetail failed", "board_id", boardID, "err", err)
	}

	layoutData := components.LayoutData{
		Title: "Board Detail",
		User:  user,
	}

	// resp is passed through as-is (nil on the err != nil,
	// non-Unauthenticated/non-NotFound path above) -- pages.BoardDetail's
	// own comment covers why that's safe: every field access goes through a
	// generated proto getter, which nil-checks its receiver.
	if renderErr := RenderTempl(w, r, "Board Detail", pages.BoardDetail(layoutData, resp, err, claimErr)); renderErr != nil {
		app.log().Error("failed to render board detail page", "board_id", boardID, "err", renderErr)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleClaimBoard is the Claim button's POST target (#1765: FR1, FR2),
// routed at "/boards/{board_id}/claim". It calls ClaimBoard on leaflab-api
// with the signed-in user's own access token (same NFR2 forwarding as
// every other app.api call, via htmxauth.Authenticator.WithAccessToken in
// setupRoutes) and always redirects back to the board's own detail page --
// there is no dedicated fragment-render endpoint for this button to target
// (see boards.templ's claimForm doc comment), so a normal POST-redirect-GET
// is the mechanism, matching the query-param-carried error pattern already
// used by manmanv2/ui/handlers_sessions.go's handleSessionStart.
//
// A codes.FailedPrecondition (already owned, including a re-claim by the
// current owner) is carried onto the redirect as claim_error and rendered
// inline by handleBoardDetail/pages.BoardDetail -- never a 500, and never a
// silent no-op that leaves the page looking like nothing happened.
func (app *App) handleClaimBoard(w http.ResponseWriter, r *http.Request) {
	boardID, parseErr := strconv.ParseInt(r.PathValue("board_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	redirectTo := fmt.Sprintf("/boards/%d", boardID)

	_, err := app.api.ClaimBoard(r.Context(), boardID)
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.Unauthenticated:
				// Same re-authenticate flow as handleBoards/handleBoardDetail's
				// identical branch -- see handleBoards' comment for why this is
				// a redirect, not an error page.
				loginURL := fmt.Sprintf("/auth/login?next=%s", r.URL.RequestURI())
				if r.Header.Get("HX-Request") == "true" {
					w.Header().Set("HX-Redirect", loginURL)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, loginURL, http.StatusSeeOther)
				return
			case codes.NotFound:
				http.NotFound(w, r)
				return
			}
		}
		app.log().Info("claim board refused or failed", "board_id", boardID, "err", err)
		redirectTo += "?claim_error=" + url.QueryEscape(claimErrorMessage(err))
	}

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", redirectTo)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

// claimErrorMessage renders a ClaimBoard error for display on the board
// detail page. codes.FailedPrecondition gets a purpose-built "already
// owned" message (server.go's ClaimBoard doesn't echo the current owner's
// name on the wire, so this can't say who); anything else falls back to
// the raw gRPC status message rather than hiding it, consistent with
// handleBoards'/handleBoardDetail's existing generic-error rendering.
func claimErrorMessage(err error) string {
	if st, ok := status.FromError(err); ok && st.Code() == codes.FailedPrecondition {
		return "This board is already owned."
	}
	return "Failed to claim board: " + err.Error()
}

// handleRenameBoard is FR3's write path, routed at
// "POST /boards/{board_id}/rename" (main.go's setupRoutes). It calls
// RenameBoard on leaflab-api with the signed-in user's own access token
// (NFR2) and re-renders exactly the "#board-header" fragment
// (pages.BoardHeader) via HTMX, matching the renameBoardForm's
// hx-target="#board-header" hx-swap="outerHTML" -- never the full page,
// and never a 500 for the two expected rejection cases:
//
//   - codes.InvalidArgument (empty/whitespace-only name) and
//     codes.PermissionDenied (non-owner or unowned board, FR5 has no admin
//     exception) both render as an inline message inside the fragment, not
//     an error page or a hard failure.
//   - Any other failure (transport/Internal) logs a warning and still
//     re-renders the fragment with a generic inline message -- the rename
//     attempt failed, but the page must not break.
//
// After either outcome, the board's current state is re-fetched via
// GetBoardDetail (not assumed from the request) so the fragment always
// reflects what leaflab-api actually has -- including on a rejected
// rename, where the form must re-show the unchanged current name, not
// whatever the caller typed.
func (app *App) handleRenameBoard(w http.ResponseWriter, r *http.Request) {
	boardID, parseErr := strconv.ParseInt(r.PathValue("board_id"), 10, 64)
	if parseErr != nil {
		http.NotFound(w, r)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")

	var renameErr string
	if _, err := app.api.RenameBoard(r.Context(), boardID, name); err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.Unauthenticated:
				// Same re-authenticate flow as handleBoardDetail's
				// identical branch -- see its comment for why this is a
				// redirect, not an error page.
				loginURL := fmt.Sprintf("/auth/login?next=/boards/%d", boardID)
				if r.Header.Get("HX-Request") == "true" {
					w.Header().Set("HX-Redirect", loginURL)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				http.Redirect(w, r, loginURL, http.StatusSeeOther)
				return
			case codes.InvalidArgument, codes.PermissionDenied:
				renameErr = st.Message()
			default:
				app.log().Warn("RenameBoard failed", "board_id", boardID, "err", err)
				renameErr = "Failed to rename board."
			}
		} else {
			app.log().Warn("RenameBoard failed", "board_id", boardID, "err", err)
			renameErr = "Failed to rename board."
		}
	}

	resp, err := app.api.GetBoardDetail(r.Context(), boardID)
	if err != nil {
		app.log().Warn("GetBoardDetail failed after rename attempt", "board_id", boardID, "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	if renderErr := pages.BoardHeader(boardID, resp.GetDeviceId(), resp.GetBoardName(), resp.GetOwnedByCaller(), renameErr).Render(r.Context(), w); renderErr != nil {
		app.log().Error("failed to render board header fragment", "board_id", boardID, "err", renderErr)
	}
}
