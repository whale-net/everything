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

	// resp.Get*() on a nil resp (the err != nil, non-Unauthenticated/
	// non-NotFound path above) is safe: generated proto getters nil-check
	// their receiver and return the zero value.
	if renderErr := RenderTempl(w, r, "Board Detail", pages.BoardDetail(layoutData, resp.GetBoardId(), resp.GetDeviceId(), resp.GetSensors(), err)); renderErr != nil {
		app.log().Error("failed to render board detail page", "board_id", boardID, "err", renderErr)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
