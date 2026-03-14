package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// SGCDisplayInfo holds a server game config ID and a human-readable label for dropdowns.
type SGCDisplayInfo = pages.SGCDisplayInfo

// SessionsPageData holds data for the sessions list page.
type SessionsPageData = pages.SessionsPageData

// SessionDetailPageData holds data for a single session.
type SessionDetailPageData struct {
	Title         string
	Active        string
	User          *htmxauth.UserInfo
	Session       *manmanpb.Session
	SGC           *manmanpb.ServerGameConfig
	GameConfig    *manmanpb.GameConfig
	Game          *manmanpb.Game
	Actions       []*manmanpb.ActionDefinition
	Libraries     []*manmanpb.WorkshopLibrary
	Installations []*manmanpb.WorkshopInstallation
}

func (app *App) handleSessions(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	liveOnly := r.URL.Query().Get("live_only") == "1"
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	serverGameConfigIDStr := strings.TrimSpace(r.URL.Query().Get("server_game_config_id"))
	selectedServerIDStr := strings.TrimSpace(r.URL.Query().Get("server_id"))
	startError := strings.TrimSpace(r.URL.Query().Get("start_error"))
	showForce := r.URL.Query().Get("show_force") == "1"
	forceSGCID := r.URL.Query().Get("sgc_id")

	ctx := r.Context()

	// Fetch all servers to build layout data
	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		servers = []*manmanpb.Server{}
	}

	// Determine selected server: query param > cookie > default
	var selectedServerID int64
	if selectedServerIDStr != "" {
		if id, err := strconv.ParseInt(selectedServerIDStr, 10, 64); err == nil {
			selectedServerID = id
		}
	}
	if selectedServerID == 0 {
		selectedServerID = app.getSelectedServerID(r, servers)
	}

	// Build session list request
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

	// Always scope to selected server
	if selectedServerID > 0 {
		req.ServerId = selectedServerID
	}

	sessions, err := app.grpc.ListSessionsWithFilters(ctx, req)
	if err != nil {
		log.Printf("Error fetching sessions: %v", err)
		http.Error(w, "Failed to fetch sessions", http.StatusInternalServerError)
		return
	}

	var serverConfigs []*manmanpb.ServerGameConfig
	var selectedServerStatus string
	var liveSessionByConfig map[int64]*manmanpb.Session
	if selectedServerID > 0 {
		configsResp, err := app.grpc.GetAPI().ListServerGameConfigs(ctx, &manmanpb.ListServerGameConfigsRequest{
			ServerId: selectedServerID,
			PageSize: 100,
		})
		if err != nil {
			log.Printf("Error fetching server configs: %v", err)
		} else {
			serverConfigs = configsResp.Configs
		}

		for _, server := range servers {
			if server.ServerId == selectedServerID {
				selectedServerStatus = server.Status
				break
			}
		}

		liveReq := &manmanpb.ListSessionsRequest{
			ServerId: selectedServerID,
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

	// Resolve display names for SGCs: "ConfigName (GameName)"
	var sgcOptions []SGCDisplayInfo
	sgcDisplayNames := make(map[int64]string)
	for _, sgc := range serverConfigs {
		displayName := fmt.Sprintf("SGC %d", sgc.ServerGameConfigId)
		gc, err := app.grpc.GetGameConfig(ctx, sgc.GameConfigId)
		if err == nil {
			game, err := app.grpc.GetGame(ctx, gc.GameId)
			if err == nil {
				displayName = fmt.Sprintf("%s (%s)", gc.Name, game.Name)
			} else {
				displayName = gc.Name
			}
		}
		sgcOptions = append(sgcOptions, SGCDisplayInfo{
			ServerGameConfigId: sgc.ServerGameConfigId,
			DisplayName:        displayName,
		})
		sgcDisplayNames[sgc.ServerGameConfigId] = displayName
	}

	var startWarning string
	if selectedServerID > 0 && selectedServerStatus != "" && selectedServerStatus != "online" {
		startWarning = "Selected server is offline. Starting a session may fail."
	}

	data := SessionsPageData{
		Sessions:     sessions,
		LiveOnly:     liveOnly,
		StatusFilter: statusFilter,
		ServerGameConfigID: serverGameConfigIDStr,
		ServerConfigs: serverConfigs,
		SGCOptions:   sgcOptions,
		SGCDisplayNames: sgcDisplayNames,
		SelectedServerID: strconv.FormatInt(selectedServerID, 10),
		StartWarning: startWarning,
		StartError: startError,
		ShowForce:    showForce,
		ForceSGCID:   forceSGCID,
		LiveSessionByConfig: liveSessionByConfig,
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Sessions", URL: "/sessions"},
	}

	layoutData, err := app.buildTemplLayoutData(r, "Sessions", "Sessions", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := RenderTempl(w, r, "Sessions", pages.Sessions(layoutData, data)); err != nil {
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

	if len(pathParts) > 3 && pathParts[2] == "actions" && pathParts[3] == "execute" {
		app.handleExecuteAction(w, r)
		return
	}

	if len(pathParts) > 3 && pathParts[2] == "logs" && pathParts[3] == "stream" {
		app.handleSessionLogsStream(w, r)
		return
	}

	if len(pathParts) > 3 && pathParts[2] == "logs" && pathParts[3] == "histogram" {
		app.handleLogHistogram(w, r)
		return
	}

	if len(pathParts) > 3 && pathParts[2] == "logs" && pathParts[3] == "load" {
		app.handleLoadHistoricalLogs(w, r)
		return
	}

	ctx := r.Context()
	sessionResp, err := app.grpc.GetSession(ctx, &manmanpb.GetSessionRequest{
		SessionId: sessionID,
	})
	if err != nil {
		log.Printf("Error fetching session: %v", err)
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	// Fetch available actions for this session
	actions, err := app.grpc.GetSessionActions(ctx, sessionID)
	if err != nil {
		log.Printf("Error fetching session actions: %v", err)
		// Don't fail the request if actions can't be loaded
		actions = []*manmanpb.ActionDefinition{}
	}

	// Fetch SGC, GameConfig, and Game details
	var sgc *manmanpb.ServerGameConfig
	var gameConfig *manmanpb.GameConfig
	var game *manmanpb.Game
	var libraries []*manmanpb.WorkshopLibrary
	var installations []*manmanpb.WorkshopInstallation

	if sessionResp.Session.ServerGameConfigId != 0 {
		sgcResp, err := app.grpc.GetAPI().GetServerGameConfig(ctx, &manmanpb.GetServerGameConfigRequest{
			ServerGameConfigId: sessionResp.Session.ServerGameConfigId,
		})
		if err != nil {
			log.Printf("Warning: failed to get SGC for session detail: %v", err)
		} else {
			sgc = sgcResp.Config

			// Fetch GameConfig
			if sgc.GameConfigId != 0 {
				gcResp, err := app.grpc.GetGameConfig(ctx, sgc.GameConfigId)
				if err != nil {
					log.Printf("Warning: failed to get GameConfig for session detail: %v", err)
				} else {
					gameConfig = gcResp

					// Fetch Game
					if gameConfig.GameId != 0 {
						game, err = app.grpc.GetGame(ctx, gameConfig.GameId)
						if err != nil {
							log.Printf("Warning: failed to get Game for session detail: %v", err)
						}
					}
				}
			}
		}

		// Fetch workshop libraries and installations
		libraries, err = app.grpc.ListSGCLibraries(ctx, sessionResp.Session.ServerGameConfigId)
		if err != nil {
			log.Printf("Warning: failed to list SGC libraries for session detail: %v", err)
			libraries = []*manmanpb.WorkshopLibrary{}
		}
		installations, err = app.grpc.ListWorkshopInstallations(ctx, sessionResp.Session.ServerGameConfigId)
		if err != nil {
			log.Printf("Warning: failed to list workshop installations for session detail: %v", err)
			installations = []*manmanpb.WorkshopInstallation{}
		}
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Sessions", URL: "/sessions"},
		{Label: "Session " + sessionIDStr, URL: ""},
	}
	layoutData, err := app.buildTemplLayoutData(r, "Session "+sessionIDStr, "sessions", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	pageData := pages.SessionDetailPageData{
		Layout:        layoutData,
		Session:       sessionResp.Session,
		SGC:           sgc,
		GameConfig:    gameConfig,
		Game:          game,
		Actions:       actions,
		Libraries:     libraries,
		Installations: installations,
	}

	RenderTempl(w, r, "Session "+sessionIDStr, pages.SessionDetail(pageData))
}

func (app *App) handleSessionStop(w http.ResponseWriter, r *http.Request, sessionID int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	_, err := app.grpc.StopSession(ctx, sessionID)
	if err != nil {
		log.Printf("Error stopping session: %v", err)
		http.Error(w, "Failed to stop session", http.StatusInternalServerError)
		return
	}

	redirectURL := "/sessions"
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}

	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (app *App) handleExecuteAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// Extract session ID from URL path: /sessions/{id}/actions/execute
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		http.Error(w, "Invalid session ID", http.StatusBadRequest)
		return
	}

	sessionID, err := strconv.ParseInt(pathParts[1], 10, 64)
	if err != nil {
		http.Error(w, "Invalid session ID", http.StatusBadRequest)
		return
	}

	// Get action ID
	actionIDStr := r.FormValue("action_id")
	if actionIDStr == "" {
		http.Error(w, "Missing action_id", http.StatusBadRequest)
		return
	}

	actionID, err := strconv.ParseInt(actionIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid action_id", http.StatusBadRequest)
		return
	}

	// Collect input values (all form fields except action_id)
	inputValues := make(map[string]string)
	for key, values := range r.Form {
		if key != "action_id" && len(values) > 0 {
			inputValues[key] = values[0]
		}
	}

	// Execute the action
	ctx := r.Context()
	resp, err := app.grpc.ExecuteAction(ctx, sessionID, actionID, inputValues)
	if err != nil {
		log.Printf("Error executing action: %v", err)

		// Return error message for HTMX
		if r.Header.Get("HX-Request") != "" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `<div class="alert alert-danger" role="alert">Error: %s</div>`, err.Error())
			return
		}

		http.Error(w, fmt.Sprintf("Failed to execute action: %v", err), http.StatusInternalServerError)
		return
	}

	// Check if action execution succeeded
	if !resp.Success {
		log.Printf("Action execution failed: %s", resp.ErrorMessage)

		if r.Header.Get("HX-Request") != "" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `<div class="alert alert-warning" role="alert">%s</div>`, resp.ErrorMessage)
			return
		}

		http.Error(w, resp.ErrorMessage, http.StatusBadRequest)
		return
	}

	// Success response
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `<div class="alert alert-success" role="alert">Command sent: <code>%s</code></div>`, resp.RenderedCommand)
		return
	}

	// Non-HTMX redirect back to session detail
	http.Redirect(w, r, fmt.Sprintf("/sessions/%d", sessionID), http.StatusSeeOther)
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

	force := r.FormValue("force") == "true"

	ctx := r.Context()
	session, err := app.grpc.StartSession(ctx, serverGameConfigID, force)
	if err != nil {
		log.Printf("Error starting session: %v", err)

		errMsg := err.Error()
		showForceOption := false
		if strings.Contains(errMsg, "active session") {
			errMsg = "Session is already active. You can attempt to force start a new session, which will attempt to stop the existing one first."
			showForceOption = true
		}

		redirectURL := "/sessions?start_error=" + url.QueryEscape(errMsg)
		if showForceOption {
			redirectURL += "&show_force=1&sgc_id=" + serverGameConfigIDStr
		}
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

// handleCheckActiveSession returns an HTML fragment indicating if there's an active session for the given SGC.
func (app *App) handleCheckActiveSession(w http.ResponseWriter, r *http.Request) {
	sgcIDStr := strings.TrimSpace(r.URL.Query().Get("server_game_config_id"))

	if sgcIDStr == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	ctx := r.Context()

	// Check for active sessions on this SGC
	req := &manmanpb.ListSessionsRequest{
		PageSize:           10,
		ServerGameConfigId: sgcID,
		LiveOnly:           true,
	}
	sessions, err := app.grpc.ListSessionsWithFilters(ctx, req)
	if err != nil {
		log.Printf("Error checking active sessions: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	if len(sessions) == 0 {
		// No active sessions - return empty (clears the warning div)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Found active session(s) - return warning HTML
	session := sessions[0]
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)

	warningHTML := `<div style="margin-top: 0.75rem; padding: 0.75rem; border-left: 4px solid #f39c12; background: #fff8e1;">
    <strong>⚠️ Active Session Detected</strong>
    <p style="margin: 0.5rem 0 0 0;">Session ` + strconv.FormatInt(session.SessionId, 10) + ` is currently ` + session.Status + ` for this config.</p>
    <p style="margin: 0.5rem 0 0 0; font-size: 0.9rem; color: #666;">
        You may need to check the <strong>Force start</strong> option below to stop the existing session and start a new one.
    </p>
</div>`

	w.Write([]byte(warningHTML))
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

// handleSessionLogsStream streams logs for a session via Server-Sent Events (SSE)
func (app *App) handleSessionLogsStream(w http.ResponseWriter, r *http.Request) {
	// Extract session ID from URL path
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 4 || pathParts[2] != "logs" || pathParts[3] != "stream" {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}

	sessionIDStr := pathParts[1]
	sessionID, err := strconv.ParseInt(sessionIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid session ID", http.StatusBadRequest)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Create gRPC stream directly to log-processor
	stream, err := app.logProcessor.StreamSessionLogs(r.Context(), &manmanpb.StreamSessionLogsRequest{
		SessionId: sessionID,
	})
	if err != nil {
		log.Printf("Failed to create log stream for session %d: %v", sessionID, err)
		http.Error(w, "Failed to start log stream", http.StatusInternalServerError)
		return
	}

	// Send connection acknowledgment
	fmt.Fprintf(w, "data: {\"type\":\"connected\"}\n\n")
	flusher.Flush()

	// Stream logs as SSE events
	for {
		msg, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				// Stream closed normally
				log.Printf("Log stream closed for session %d", sessionID)
			} else {
				log.Printf("Error receiving log for session %d: %v", sessionID, err)
			}
			return
		}

		// Format as JSON for easier client parsing
		data := map[string]interface{}{
			"timestamp": msg.Timestamp,
			"source":    msg.Source,
			"message":   msg.Message,
		}
		jsonData, err := json.Marshal(data)
		if err != nil {
			log.Printf("Failed to marshal log message: %v", err)
			continue
		}

		fmt.Fprintf(w, "data: %s\n\n", jsonData)
		flusher.Flush()
	}
}

// handleHistoricalLogs handles API requests for historical logs
func (app *App) handleHistoricalLogs(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	sessionIDStr := r.URL.Query().Get("session_id")
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")

	if sessionIDStr == "" || startStr == "" || endStr == "" {
		http.Error(w, "Missing required parameters: session_id, start, end", http.StatusBadRequest)
		return
	}

	sessionID, err := strconv.ParseInt(sessionIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid session_id", http.StatusBadRequest)
		return
	}

	startTimestamp, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid start timestamp", http.StatusBadRequest)
		return
	}

	endTimestamp, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid end timestamp", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Call gRPC GetHistoricalLogs directly with session ID
	resp, err := app.grpc.GetHistoricalLogs(ctx, &manmanpb.GetHistoricalLogsRequest{
		SessionId:      sessionID,
		StartTimestamp: startTimestamp,
		EndTimestamp:   endTimestamp,
	})
	if err != nil {
		log.Printf("Error fetching historical logs for session %d: %v", sessionID, err)
		http.Error(w, "Failed to fetch historical logs", http.StatusInternalServerError)
		return
	}

	// Return JSON response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("Error encoding response: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// handleLogHistogram handles API requests for log histogram data
func (app *App) handleLogHistogram(w http.ResponseWriter, r *http.Request) {
	// Extract session ID from URL path: /sessions/{id}/logs/histogram
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		http.Error(w, "Invalid URL path", http.StatusBadRequest)
		return
	}

	sessionID, err := strconv.ParseInt(pathParts[1], 10, 64)
	if err != nil {
		http.Error(w, "Invalid session_id", http.StatusBadRequest)
		return
	}

	// Check for optional zoom range parameters
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")

	ctx := r.Context()

	// If zoom range provided, fetch histogram for that range only
	if startStr != "" && endStr != "" {
		startTime, err := strconv.ParseInt(startStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid start timestamp", http.StatusBadRequest)
			return
		}

		endTime, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid end timestamp", http.StatusBadRequest)
			return
		}

		// For now, just use the full histogram
		// TODO: Add range support to GetLogHistogram RPC to filter buckets by time range
		_ = startTime
		_ = endTime
	}

	// Call gRPC GetLogHistogram
	resp, err := app.grpc.GetLogHistogram(ctx, &manmanpb.GetLogHistogramRequest{
		SessionId: sessionID,
	})
	if err != nil {
		log.Printf("Error fetching log histogram for session %d: %v", sessionID, err)
		http.Error(w, fmt.Sprintf("Failed to fetch log histogram: %v", err), http.StatusInternalServerError)
		return
	}

	// Return JSON response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("Error encoding response: %v", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

// handleLoadHistoricalLogs handles HTMX requests to load paginated historical logs
func (app *App) handleLoadHistoricalLogs(w http.ResponseWriter, r *http.Request) {
	// Extract session ID from URL path: /sessions/{id}/logs/load
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		http.Error(w, "Invalid URL path", http.StatusBadRequest)
		return
	}

	sessionID, err := strconv.ParseInt(pathParts[1], 10, 64)
	if err != nil {
		http.Error(w, "Invalid session_id", http.StatusBadRequest)
		return
	}

	// Parse query parameters
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")
	offsetStr := r.URL.Query().Get("offset")
	limitStr := r.URL.Query().Get("limit")

	if startStr == "" || endStr == "" {
		http.Error(w, "Missing required parameters: start, end", http.StatusBadRequest)
		return
	}

	startTimestamp, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid start timestamp", http.StatusBadRequest)
		return
	}

	endTimestamp, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid end timestamp", http.StatusBadRequest)
		return
	}

	// Auto-truncate if range > 6 hours
	maxDuration := int64(6 * 60 * 60) // 6 hours
	if endTimestamp-startTimestamp > maxDuration {
		endTimestamp = startTimestamp + maxDuration
	}

	offset := int32(0)
	if offsetStr != "" {
		offsetVal, err := strconv.ParseInt(offsetStr, 10, 32)
		if err == nil {
			offset = int32(offsetVal)
		}
	}

	limit := int32(10000)
	if limitStr != "" {
		limitVal, err := strconv.ParseInt(limitStr, 10, 32)
		if err == nil {
			limit = int32(limitVal)
		}
	}

	ctx := r.Context()

	// Call gRPC GetHistoricalLogs with pagination
	resp, err := app.grpc.GetHistoricalLogs(ctx, &manmanpb.GetHistoricalLogsRequest{
		SessionId:      sessionID,
		StartTimestamp: startTimestamp,
		EndTimestamp:   endTimestamp,
		Offset:         offset,
		Limit:          limit,
	})
	if err != nil {
		log.Printf("Error fetching historical logs for session %d: %v", sessionID, err)
		http.Error(w, fmt.Sprintf("Failed to fetch logs: %v", err), http.StatusInternalServerError)
		return
	}

	// Render HTML response
	w.Header().Set("Content-Type", "text/html")

	// Show truncation warning if we auto-truncated
	if endTimestamp != startTimestamp+maxDuration && endTimestamp-startTimestamp == maxDuration {
		fmt.Fprintf(w, `<div class="alert alert-warning" style="margin-bottom: 1rem; padding: 0.75rem; background: #fff3cd; border: 1px solid #ffc107; border-radius: 4px; color: #856404;">
			Selection reduced to 6 hours (maximum allowed)
		</div>`)
	}

	// Render log content
	fmt.Fprintf(w, `<div class="log-viewer-container"><pre id="log-output" class="log-viewer-output">`)
	for _, batch := range resp.Batches {
		fmt.Fprintf(w, "%s", batch.Content)
	}
	fmt.Fprintf(w, `</pre></div>`)

	// Add "Load More" button if there's more data
	if resp.HasMore {
		nextOffset := offset + limit
		fmt.Fprintf(w, `
		<div style="text-align: center; padding: 1rem;">
			<button class="btn btn-primary"
				hx-get="/sessions/%d/logs/load?start=%d&end=%d&offset=%d&limit=%d"
				hx-target="#log-content"
				hx-swap="beforeend"
				hx-indicator="#log-loading">
				Load More (%d lines remaining)
			</button>
			<span id="log-loading" class="htmx-indicator" style="margin-left: 0.5rem;">Loading...</span>
		</div>`, sessionID, startTimestamp, endTimestamp, nextOffset, limit, resp.TotalLines-int32(offset)-limit)
	}
}

// handleSessionStdin handles API requests to send stdin to a session
func (app *App) handleSessionStdin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract session ID from URL path
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 3 || pathParts[0] != "api" || pathParts[1] != "sessions" {
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}

	sessionIDStr := pathParts[2]
	sessionID, err := strconv.ParseInt(sessionIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid session ID", http.StatusBadRequest)
		return
	}

	// Parse JSON body
	var req struct {
		Input string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	if req.Input == "" {
		http.Error(w, "Input cannot be empty", http.StatusBadRequest)
		return
	}

	// Add newline to input if not present (common for stdin)
	input := req.Input
	if !strings.HasSuffix(input, "\n") {
		input += "\n"
	}

	// Call gRPC SendInput with 10s timeout
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	_, err = app.grpc.SendInput(ctx, sessionID, []byte(input))
	if err != nil {
		log.Printf("Error sending input to session %d: %v", sessionID, err)
		http.Error(w, fmt.Sprintf("Failed to send input: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
