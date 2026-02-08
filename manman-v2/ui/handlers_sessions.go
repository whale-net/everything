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
	ServerGameConfigID string
	Servers      []*manmanpb.Server
	ServerConfigs []*manmanpb.ServerGameConfig
	SelectedServerID string
	SelectedServerStatus string
	StartWarning string
	StartError string
	LiveSessionByConfig map[int64]*manmanpb.Session
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
	serverGameConfigIDStr := strings.TrimSpace(r.URL.Query().Get("server_game_config_id"))
	selectedServerIDStr := strings.TrimSpace(r.URL.Query().Get("server_id"))
	startError := strings.TrimSpace(r.URL.Query().Get("start_error"))

	req := &manmanpb.ListSessionsRequest{
		PageSize: 100,
		LiveOnly: liveOnly,
	}

	if statusFilter != "" {
		req.StatusFilter = splitCSV(statusFilter)
	}

	if serverGameConfigIDStr != "" {
		serverGameConfigID, err := strconv.ParseInt(serverGameConfigIDStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid server_game_config_id", http.StatusBadRequest)
			return
		}
		req.ServerGameConfigId = serverGameConfigID
	}

	if selectedServerIDStr != "" {
		serverID, err := strconv.ParseInt(selectedServerIDStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid server_id", http.StatusBadRequest)
			return
		}
		req.ServerId = serverID
	}

	ctx := context.Background()
	sessions, err := app.grpc.ListSessionsWithFilters(ctx, req)
	if err != nil {
		log.Printf("Error fetching sessions: %v", err)
		http.Error(w, "Failed to fetch sessions", http.StatusInternalServerError)
		return
	}

	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		servers = []*manmanpb.Server{}
	}

	var serverConfigs []*manmanpb.ServerGameConfig
	var selectedServerStatus string
	var liveSessionByConfig map[int64]*manmanpb.Session
	if selectedServerIDStr != "" {
		serverID, err := strconv.ParseInt(selectedServerIDStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid server_id", http.StatusBadRequest)
			return
		}
		configsResp, err := app.grpc.GetAPI().ListServerGameConfigs(ctx, &manmanpb.ListServerGameConfigsRequest{
			ServerId: serverID,
			PageSize: 100,
		})
		if err != nil {
			log.Printf("Error fetching server configs: %v", err)
		} else {
			serverConfigs = configsResp.Configs
		}

		for _, server := range servers {
			if server.ServerId == serverID {
				selectedServerStatus = server.Status
				break
			}
		}

		liveReq := &manmanpb.ListSessionsRequest{
			ServerId: serverID,
			LiveOnly: true,
			PageSize: 100,
		}
		liveSessions, err := app.grpc.ListSessionsWithFilters(ctx, liveReq)
		if err != nil {
			log.Printf("Error fetching live sessions: %v", err)
		} else {
			liveSessionByConfig = make(map[int64]*manmanpb.Session, len(liveSessions))
			for _, session := range liveSessions {
				liveSessionByConfig[session.ServerGameConfigId] = session
			}
		}
	}

	var startWarning string
	if selectedServerIDStr != "" && selectedServerStatus != "" && selectedServerStatus != "online" {
		startWarning = "Selected server is offline. Starting a session may fail."
	}

	data := SessionsPageData{
		Title:        "Sessions",
		Active:       "sessions",
		User:         user,
		Sessions:     sessions,
		LiveOnly:     liveOnly,
		StatusFilter: statusFilter,
		ServerGameConfigID: serverGameConfigIDStr,
		Servers:      servers,
		ServerConfigs: serverConfigs,
		SelectedServerID: selectedServerIDStr,
		SelectedServerStatus: selectedServerStatus,
		StartWarning: startWarning,
		StartError: startError,
		LiveSessionByConfig: liveSessionByConfig,
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

func (app *App) handleSessionStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	serverGameConfigIDStr := strings.TrimSpace(r.FormValue("server_game_config_id"))
	selectedServerIDStr := strings.TrimSpace(r.FormValue("server_id"))
	if serverGameConfigIDStr == "" {
		http.Error(w, "Missing server_game_config_id", http.StatusBadRequest)
		return
	}

	serverGameConfigID, err := strconv.ParseInt(serverGameConfigIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid server_game_config_id", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	session, err := app.grpc.StartSession(ctx, serverGameConfigID, nil)
	if err != nil {
		log.Printf("Error starting session: %v", err)
		redirectURL := "/sessions?start_error=Failed+to+start+session"
		if selectedServerIDStr != "" {
			redirectURL += "&server_id=" + selectedServerIDStr
		}
		if r.Header.Get("HX-Request") != "" {
			w.Header().Set("HX-Redirect", redirectURL)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, redirectURL, http.StatusSeeOther)
		return
	}

	redirectURL := "/sessions/" + strconv.FormatInt(session.SessionId, 10)
	if selectedServerIDStr != "" {
		redirectURL += "?server_id=" + selectedServerIDStr
	}

	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}

	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
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
