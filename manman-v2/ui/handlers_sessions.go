package main

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manman/protos"
)

// SessionsPageData holds data for the sessions list page.
type SessionsPageData struct {
	Title        string
	Active       string
	User         *htmxauth.UserInfo
	Sessions     []*manmanpb.Session
	LiveOnly     bool
	StatusFilter string
}

// SessionDetailPageData holds data for a single session.
type SessionDetailPageData struct {
	Title   string
	Active  string
	User    *htmxauth.UserInfo
	Session *manmanpb.Session
}

func (app *App) handleSessions(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	liveOnly := r.URL.Query().Get("live_only") == "1"
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))

	req := &manmanpb.ListSessionsRequest{
		PageSize: 100,
		LiveOnly: liveOnly,
	}

	if statusFilter != "" {
		req.StatusFilter = splitCSV(statusFilter)
	}

	ctx := context.Background()
	sessions, err := app.grpc.ListSessionsWithFilters(ctx, req)
	if err != nil {
		log.Printf("Error fetching sessions: %v", err)
		http.Error(w, "Failed to fetch sessions", http.StatusInternalServerError)
		return
	}

	data := SessionsPageData{
		Title:        "Sessions",
		Active:       "sessions",
		User:         user,
		Sessions:     sessions,
		LiveOnly:     liveOnly,
		StatusFilter: statusFilter,
	}

	layout, err := renderWithLayout("sessions_content", data, LayoutData{
		Title:  data.Title,
		Active: data.Active,
		User:   data.User,
	})
	if err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := templates.ExecuteTemplate(w, "layout.html", layout); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (app *App) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		http.Error(w, "Invalid session ID", http.StatusBadRequest)
		return
	}

	sessionIDStr := pathParts[1]
	sessionID, err := strconv.ParseInt(sessionIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid session ID", http.StatusBadRequest)
		return
	}

	if len(pathParts) > 2 && pathParts[2] == "stop" {
		app.handleSessionStop(w, r, sessionID)
		return
	}

	ctx := context.Background()
	session, err := app.grpc.GetSession(ctx, sessionID)
	if err != nil {
		log.Printf("Error fetching session: %v", err)
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	data := SessionDetailPageData{
		Title:   "Session " + sessionIDStr,
		Active:  "sessions",
		User:    user,
		Session: session,
	}

	layout, err := renderWithLayout("session_detail_content", data, LayoutData{
		Title:  data.Title,
		Active: data.Active,
		User:   data.User,
	})
	if err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := templates.ExecuteTemplate(w, "layout.html", layout); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (app *App) handleSessionStop(w http.ResponseWriter, r *http.Request, sessionID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()
	_, err := app.grpc.StopSession(ctx, sessionID)
	if err != nil {
		log.Printf("Error stopping session: %v", err)
		http.Error(w, "Failed to stop session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/sessions/"+strconv.FormatInt(sessionID, 10))
	w.WriteHeader(http.StatusOK)
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	var result []string
	for _, part := range parts {
		clean := strings.TrimSpace(part)
		if clean != "" {
			result = append(result, clean)
		}
	}
	return result
}
