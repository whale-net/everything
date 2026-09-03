package main

import (
	"context"
	"errors"
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
// background stop-then-start waits for the live session to actually
// disappear before starting a new one (FR6). They are package-level
// defaults rather than hardcoded inline so tests can override
// App.deploymentStopPollInterval/deploymentStopTimeout with small values
// and finish quickly instead of waiting out the real 15s deadline.
//
// defaultDeploymentActionTimeout bounds stopDeployment/restartDeployment's
// initial StopSession RPC call (#1664): defense-in-depth so a slow action
// degrades to a rendered inline error (FR8) well under main.go's 15s
// http.Server.WriteTimeout, rather than ever risking a dropped connection
// again if the host/API layer regresses on the fast-ack behavior #1663
// establishes.
const (
	defaultDeploymentStopPollInterval = 500 * time.Millisecond
	defaultDeploymentStopTimeout      = 15 * time.Second
	defaultDeploymentActionTimeout    = 8 * time.Second
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
//
// The StopSession call itself is wrapped in a bounded context.WithTimeout
// (#1664 defense-in-depth) so a slow/hung command surfaces as an inline
// timeout-flavored error (FR8) rather than ever risking main.go's 15s
// http.Server.WriteTimeout dropping the connection with no response at all.
func (app *App) stopDeployment(ctx context.Context, sgcID int64) string {
	live, err := app.getLiveSession(ctx, sgcID)
	if err != nil {
		log.Printf("Error checking live session for deployment %d: %v", sgcID, err)
		return "Failed to check the running session for this deployment."
	}
	if live == nil {
		return "No running session to stop for this deployment."
	}
	stopCtx, cancel := app.deploymentActionCtx(ctx)
	defer cancel()
	if _, err := app.grpc.StopSession(stopCtx, live.SessionId); err != nil {
		log.Printf("Error stopping session %d for deployment %d: %v", live.SessionId, sgcID, err)
		return deploymentStopErrorMessage(err)
	}
	return ""
}

// restartDeployment is literally stop-then-start over the same helpers as
// stopDeployment/handleDeploymentAction's start case -- no distinct RPC, no
// distinct config resolution (FR6). A deployment with no live session (e.g.
// crashed/lost) degenerates to the start step alone, synchronously, exactly
// as before -- that path was already fast and isn't implicated in #1662.
//
// When there is a live session, only the initial StopSession dispatch is
// awaited inline (bounded by the same defense-in-depth timeout as
// stopDeployment). Once that fast, ack-only dispatch succeeds, the request
// returns immediately -- rendering the transitional "stopping" row via the
// normal renderDeploymentRow path -- instead of blocking on
// waitForNoLiveSession, which is bounded by real container-stop time, not
// RPC-ack speed (#1662's restart-specific bug even after #1663's host-side
// fix). The wait-then-start is finished in a background goroutine using
// context.Background(), mirroring the host manager's own async-dispatch
// pattern from #1663, since the request's ctx is cancelled the moment the
// HTTP response is written. The pre-existing self-terminating row poll
// (#1628) picks up convergence from the "stopping" row through to
// stopped/crashed or starting/running with no new poll logic needed.
func (app *App) restartDeployment(ctx context.Context, sgcID int64) string {
	live, err := app.getLiveSession(ctx, sgcID)
	if err != nil {
		log.Printf("Error checking live session for deployment %d: %v", sgcID, err)
		return "Failed to check the running session for this deployment."
	}

	if live == nil {
		if _, err := app.grpc.StartSession(ctx, sgcID, false); err != nil {
			log.Printf("Error starting deployment %d during restart: %v", sgcID, err)
			return deploymentStartErrorMessage(err)
		}
		return ""
	}

	stopCtx, cancel := app.deploymentActionCtx(ctx)
	defer cancel()
	if _, err := app.grpc.StopSession(stopCtx, live.SessionId); err != nil {
		log.Printf("Error stopping session %d for deployment %d during restart: %v", live.SessionId, sgcID, err)
		return deploymentStopErrorMessage(err)
	}

	go app.finishRestartInBackground(sgcID)

	return ""
}

// finishRestartInBackground waits for the just-dispatched stop to actually
// converge (no live session left) and then starts a new session, entirely
// off the original request's goroutine/context. Errors and timeouts can no
// longer be surfaced inline to the request that triggered the restart, so
// they're only logged; the row's own self-terminating poll (#1628) is what
// surfaces the eventual observed state to the user.
func (app *App) finishRestartInBackground(sgcID int64) {
	interval, timeout := app.deploymentStopPoll()
	if err := app.waitForNoLiveSession(context.Background(), sgcID, interval, timeout); err != nil {
		log.Printf("Background restart: timed out waiting for deployment %d to stop: %v", sgcID, err)
		return
	}
	if _, err := app.grpc.StartSession(context.Background(), sgcID, false); err != nil {
		log.Printf("Background restart: error starting deployment %d: %v", sgcID, err)
		return
	}
	log.Printf("Background restart: deployment %d stopped and restarted successfully", sgcID)
}

// deploymentActionCtx returns a bounded context.WithTimeout derived from
// ctx for stopDeployment/restartDeployment's initial StopSession dispatch
// (#1664 defense-in-depth), falling back to defaultDeploymentActionTimeout
// when the App wasn't configured with an override -- tests inject a small
// value via App.deploymentActionTimeout so a simulated slow StopSession
// doesn't wait out the real 8s deadline. The caller owns the returned
// cancel func.
func (app *App) deploymentActionCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := app.deploymentActionTimeout
	if timeout <= 0 {
		timeout = defaultDeploymentActionTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// deploymentStopPoll returns the interval/timeout
// finishRestartInBackground's waitForNoLiveSession call should poll with,
// falling back to the production defaults when the App wasn't configured
// with overrides (tests inject small values via these fields so a
// simulated stuck stop doesn't wait out the real 15s deadline).
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

// deploymentStopErrorMessage turns a StopSession error into a
// human-readable inline message, distinguishing a bounded-context timeout
// (FR8's "command times out" trigger) from a generic failure. ControlClient
// wraps the underlying gRPC error with %w (grpc_client.go's StopSession),
// so errors.Is sees through to context.DeadlineExceeded when
// deploymentActionCtx's deadline is what actually stopped the call.
func deploymentStopErrorMessage(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "The stop command is taking longer than expected; check back shortly."
	}
	return "Failed to stop the running session."
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
