package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// defaultDeploymentStopPollInterval/Timeout govern how restart's
// stop-then-start waits for the live session to actually disappear before
// starting a new one (FR6). They are package-level defaults rather than
// hardcoded inline so tests can override App.deploymentStopPollInterval/
// deploymentStopTimeout with small values and finish quickly instead of
// waiting out the real 15s deadline.
const (
	defaultDeploymentStopPollInterval = 500 * time.Millisecond
	defaultDeploymentStopTimeout      = 15 * time.Second
)

// handleDeploymentAction routes POST /sessions/deployments/{sgcID}/{start|stop|restart}.
//
// Registered ahead of the pre-existing "/sessions/" catch-all in main.go;
// Go's ServeMux longest-pattern-wins means this handler claims
// "/sessions/deployments/" before handleSessionDetail's "/sessions/" ever
// sees it, so handleSessionDetail's positional path-segment parsing never
// mistakes "deployments" for a session id.
//
// No new authz here: per the plan, Admin and Server Manager get identical
// behavior in M2, so the existing RequireAuthFunc wrapper (applied in
// main.go's route registration) is the whole policy.
func (app *App) handleDeploymentAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/sessions/deployments/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "Invalid deployment action path", http.StatusBadRequest)
		return
	}

	sgcID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "Invalid server_game_config_id", http.StatusBadRequest)
		return
	}

	verb := parts[1]
	if verb != "start" && verb != "stop" && verb != "restart" {
		http.Error(w, "Unknown deployment action", http.StatusNotFound)
		return
	}

	ctx := r.Context()

	var actionErr string
	switch verb {
	case "start":
		if _, err := app.grpc.StartSession(ctx, sgcID, false); err != nil {
			log.Printf("Error starting deployment %d: %v", sgcID, err)
			actionErr = deploymentStartErrorMessage(err)
		}
	case "stop":
		actionErr = app.stopDeployment(ctx, sgcID)
	case "restart":
		actionErr = app.restartDeployment(ctx, sgcID)
	}

	app.renderDeploymentRow(w, r, sgcID, actionErr)
}

// stopDeployment resolves the deployment's live session and stops it,
// returning a human-readable inline error ("" on success). Per FR8, a
// deployment with no live session any more (raced with a crash/stop) is
// not an error -- it renders an inline notice instead, and StopSession is
// never called.
func (app *App) stopDeployment(ctx context.Context, sgcID int64) string {
	live, err := app.getLiveSession(ctx, sgcID)
	if err != nil {
		log.Printf("Error checking live session for deployment %d: %v", sgcID, err)
		return "Failed to check the running session for this deployment."
	}
	if live == nil {
		return "No running session to stop for this deployment."
	}
	if _, err := app.grpc.StopSession(ctx, live.SessionId); err != nil {
		log.Printf("Error stopping session %d for deployment %d: %v", live.SessionId, sgcID, err)
		return "Failed to stop the running session."
	}
	return ""
}

// restartDeployment is literally stop-then-start over the same helpers as
// stopDeployment/handleDeploymentAction's start case -- no distinct RPC, no
// distinct config resolution (FR6). A deployment with no live session (e.g.
// crashed/lost) degenerates to the start step alone.
func (app *App) restartDeployment(ctx context.Context, sgcID int64) string {
	live, err := app.getLiveSession(ctx, sgcID)
	if err != nil {
		log.Printf("Error checking live session for deployment %d: %v", sgcID, err)
		return "Failed to check the running session for this deployment."
	}

	if live != nil {
		if _, err := app.grpc.StopSession(ctx, live.SessionId); err != nil {
			log.Printf("Error stopping session %d for deployment %d during restart: %v", live.SessionId, sgcID, err)
			return "Failed to stop the running session."
		}

		interval, timeout := app.deploymentStopPoll()
		if err := app.waitForNoLiveSession(ctx, sgcID, interval, timeout); err != nil {
			log.Printf("Timed out waiting for deployment %d to stop before restart: %v", sgcID, err)
			return "Stop did not complete in time; deployment left in its current state."
		}
	}

	if _, err := app.grpc.StartSession(ctx, sgcID, false); err != nil {
		log.Printf("Error starting deployment %d during restart: %v", sgcID, err)
		return deploymentStartErrorMessage(err)
	}
	return ""
}

// deploymentStopPoll returns the interval/timeout restartDeployment should
// poll with, falling back to the production defaults when the App wasn't
// configured with overrides (tests inject small values via these fields so
// TestDeploymentAction_Restart_StopNeverCompletes_DoesNotStart doesn't wait
// out the real 15s deadline).
func (app *App) deploymentStopPoll() (time.Duration, time.Duration) {
	interval := app.deploymentStopPollInterval
	if interval <= 0 {
		interval = defaultDeploymentStopPollInterval
	}
	timeout := app.deploymentStopTimeout
	if timeout <= 0 {
		timeout = defaultDeploymentStopTimeout
	}
	return interval, timeout
}

// waitForNoLiveSession polls the deployment's live session on the given
// interval until none remains (success) or timeout elapses / ctx is
// cancelled (error). It checks immediately before the first sleep, so a
// deployment with no live session at call time returns right away.
func (app *App) waitForNoLiveSession(ctx context.Context, sgcID int64, interval, timeout time.Duration) error {
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		live, err := app.getLiveSession(deadlineCtx, sgcID)
		if err != nil {
			return err
		}
		if live == nil {
			return nil
		}

		select {
		case <-deadlineCtx.Done():
			return deadlineCtx.Err()
		case <-ticker.C:
		}
	}
}

// getLiveSession returns the deployment's live session, or nil when there
// isn't one.
func (app *App) getLiveSession(ctx context.Context, sgcID int64) (*manmanpb.Session, error) {
	sessions, err := app.grpc.ListSessionsWithFilters(ctx, &manmanpb.ListSessionsRequest{
		ServerGameConfigId: sgcID,
		LiveOnly:           true,
		PageSize:           10,
	})
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	return sessions[0], nil
}

// deploymentStartErrorMessage turns a StartSession error into a
// human-readable inline message, mirroring handleSessionStart's existing
// "active session" special-case (handlers_sessions.go) rather than leaking
// raw gRPC status text into the row when a clearer message exists.
func deploymentStartErrorMessage(err error) string {
	if strings.Contains(err.Error(), "active session") {
		return "A session is already active for this deployment."
	}
	return "Failed to start the deployment."
}

// renderDeploymentRow re-fetches a single deployment's state and writes the
// pages.DeploymentRow fragment, with actionErr surfaced inline (FR8).
//
// For HTMX requests (the row's forms carry hx-post/hx-target/hx-swap, wired
// in #1626), this writes the fragment directly -- mirroring
// handlers_home.go's components.DashboardSummary(data).Render(r.Context(), w)
// pattern -- with no layout and no redirect, so hx-swap="outerHTML" can
// replace the row in place. For non-HTMX requests (no HX-Request header,
// the no-JS form fallback from #1626), it redirects back to /sessions
// preserving server_id instead of returning a bare fragment.
//
// Every outcome, success or failure, responds 200 with the row re-derived
// from freshly observed state (FR7, FR8): never an assumed "success" render,
// never a redirect/HX-Redirect for the HTMX path.
func (app *App) renderDeploymentRow(w http.ResponseWriter, r *http.Request, sgcID int64, actionErr string) {
	if r.Header.Get("HX-Request") == "" {
		redirectURL := "/sessions"
		if serverID := strings.TrimSpace(r.FormValue("server_id")); serverID != "" {
			redirectURL += "?server_id=" + serverID
		}
		http.Redirect(w, r, redirectURL, http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	data, err := app.buildDeploymentRowData(ctx, sgcID)
	if err != nil {
		log.Printf("Error building deployment row data for SGC %d: %v", sgcID, err)
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}
	data.ActionError = actionErr

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	if err := pages.DeploymentRow(data).Render(ctx, w); err != nil {
		log.Printf("Error rendering deployment row for SGC %d: %v", sgcID, err)
	}
}

// handleDeploymentRowFragment serves GET /api/deployments/{sgcID}/row: the
// target the row's own self-terminating poll (#1628, DeploymentRow's
// deploymentRowPollAttrs wiring in sessions.templ) hits to refresh itself
// in place. It reuses buildDeploymentRowData + pages.DeploymentRow from
// #1627 -- no second derivation of status or action availability -- and
// always renders freshly observed state with no ActionError: a poll
// refresh is not itself an action attempt, so a stale error from a prior
// Start/Stop/Restart click is cleared on the next successful poll (the
// error belongs to a single action attempt, not to the row's persistent
// state). Because the response is itself a pages.DeploymentRow, a settled
// row's fragment carries no hx-trigger and the poll loop stops on its own.
//
// Registered as "/api/deployments/" in main.go, a distinct prefix from
// "/api/sessions/" (handleSessionStdin's catch-all) so the two never
// collide under Go's ServeMux longest-pattern-wins dispatch.
func (app *App) handleDeploymentRowFragment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/api/deployments/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "row" {
		http.Error(w, "Invalid deployment row path", http.StatusBadRequest)
		return
	}

	sgcID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "Invalid server_game_config_id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	data, err := app.buildDeploymentRowData(ctx, sgcID)
	if err != nil {
		log.Printf("Error building deployment row data for SGC %d: %v", sgcID, err)
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	if err := pages.DeploymentRow(data).Render(ctx, w); err != nil {
		log.Printf("Error rendering deployment row fragment for SGC %d: %v", sgcID, err)
	}
}

// buildDeploymentRowData resolves one deployment's row view model from the
// API: the SGC itself (for status + display-name resolution), its latest
// session (Start/Stop/Restart availability, LatestSession badge), and its
// current live session (Live Session cell) -- the same derivation
// handleSessions uses for each row in DeploymentRows
// (components.LatestSession + components.ComputeDeploymentActions), scoped
// down to a single SGC.
func (app *App) buildDeploymentRowData(ctx context.Context, sgcID int64) (pages.DeploymentRowData, error) {
	sgcResp, err := app.grpc.GetAPI().GetServerGameConfig(ctx, &manmanpb.GetServerGameConfigRequest{
		ServerGameConfigId: sgcID,
	})
	if err != nil {
		return pages.DeploymentRowData{}, fmt.Errorf("failed to get server game config %d: %w", sgcID, err)
	}
	sgc := sgcResp.Config

	displayName := fmt.Sprintf("SGC %d", sgcID)
	if sgc != nil {
		if gc, err := app.grpc.GetGameConfig(ctx, sgc.GameConfigId); err == nil {
			if game, err := app.grpc.GetGame(ctx, gc.GameId); err == nil {
				displayName = fmt.Sprintf("%s (%s)", gc.Name, game.Name)
			} else {
				displayName = gc.Name
			}
		}
	}

	allSessions, err := app.grpc.ListSessionsWithFilters(ctx, &manmanpb.ListSessionsRequest{
		ServerGameConfigId: sgcID,
		PageSize:           200,
	})
	if err != nil {
		return pages.DeploymentRowData{}, fmt.Errorf("failed to list sessions for server game config %d: %w", sgcID, err)
	}
	latest := components.LatestSession(allSessions)

	liveSession, err := app.getLiveSession(ctx, sgcID)
	if err != nil {
		// Not fatal to the row render: fall back to "no live session"
		// rather than failing the whole fragment over the Live Session
		// cell alone.
		log.Printf("Error fetching live session for deployment %d: %v", sgcID, err)
		liveSession = nil
	}

	return pages.DeploymentRowData{
		ServerGameConfigID: sgcID,
		DisplayName:        displayName,
		SGCStatus:          sgc.GetStatus(),
		LatestSession:      latest,
		LiveSession:        liveSession,
		Actions:            components.ComputeDeploymentActions(latest),
	}, nil
}
