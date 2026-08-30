package main

import (
	"fmt"
	"log"
	"net/http"

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
		log.Printf("ListBoardsWithState failed: %v", err)
	}

	layoutData := components.LayoutData{
		Title: "Boards",
		User:  user,
	}

	// resp.GetBoards() on a nil resp (the err != nil, non-Unauthenticated
	// path above) is safe: generated proto getters nil-check their
	// receiver and return the zero value.
	if renderErr := RenderTempl(w, r, "Boards", pages.Boards(layoutData, resp.GetBoards(), err)); renderErr != nil {
		log.Printf("Error rendering boards page: %v", renderErr)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
