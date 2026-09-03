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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// defaultDeploymentStopPollInterval/Timeout govern how restart's
// background stop-then-start waits for the live session to actually
// disappear before starting a new one (FR6). They are package-level
// defaults rather than hardcoded inline so tests can override
// App.deploymentStopPollInterval/deploymentStopTimeout with small values
// and finish quickly instead of waiting out the real 15s deadline.
//
// defaultDeploymentActionTimeout bounds Stop/Restart/Start's own outbound
// StopSession/StartSession RPC call (#1664, extended to Start by #1668):
// defense-in-depth so a slow action degrades to a rendered inline error
// (FR8) well under main.go's 15s http.Server.WriteTimeout, rather than
// ever risking a dropped connection again if the host/API layer regresses
// on the fast-ack behavior #1663 establishes.
const (
	defaultDeploymentStopPollInterval = 500 * time.Millisecond
	defaultDeploymentStopTimeout      = 15 * time.Second
	defaultDeploymentActionTimeout    = 8 * time.Second
)

// #1668 root-cause investigation (read before touching the bound below):
//
// #1664 wrapped stopDeployment/restartDeployment's StopSession call in
// context.WithTimeout(ctx, 8s) and it passed its own fake-gRPC-client unit
// tests, but #1667's live Tilt re-validation against a genuinely
// unreachable host still reproduced the original symptom: curl saw
// HTTP_CODE=000 after 60+ seconds, matching the API's own StopSession
// handler duration almost to the millisecond, with zero UI log lines in
// between -- i.e. the UI's 8s bound never fired at all, rather than firing
// late.
//
// To find out whether context.WithTimeout can even be trusted to bound
// this call path, this investigation built a standalone real-transport
// reproduction (not the fake-client unit tests, which stub the RPC in
// memory and so cannot exercise this): a real gRPC server on a real
// loopback TCP socket, wired through the *exact* production dial chain --
// grpcclient.NewClient + grpcauth.NewUserTokenDialOption as a
// grpc.WithPerRPCCredentials DialOption, precisely matching
// manmanv2/ui/main.go's NewControlClient call -- with a StopSession
// handler that either (a) selects on ctx.Done() before an artificial 30s
// sleep, or (b) ignores ctx entirely and calls time.Sleep(10s)
// unconditionally, simulating a downstream (host manager) that never
// answers and a handler that doesn't even bother checking its own
// context. In both cases, a context.WithTimeout(parent, 2s) passed to the
// generated ManManAPIClient.StopSession call returned in ~2.0s with a
// DeadlineExceeded-flavored error -- proving the client-side deadline
// *does* correctly bound the call over the real transport, through the
// real PerRPCCredentials dial option, independent of whether the server
// ever notices its own context was cancelled. So neither
// grpc.WithPerRPCCredentials nor libs/go/grpcclient's dial options were
// substituting or ignoring the per-call context -- that mechanism is
// sound.
//
// Two concrete bugs were found and fixed instead of that hypothesis:
//
//  1. deploymentStopErrorMessage/deploymentStartErrorMessage used
//     errors.Is(err, context.DeadlineExceeded) to detect the bound firing,
//     but a real gRPC client wraps a client-side-cancelled call as a
//     *status.Error with Code() == codes.DeadlineExceeded via
//     status.FromContextError -- NOT as a bare context.DeadlineExceeded --
//     and status.Error does not implement an Is() that errors.Is can see
//     through. errors.Is(err, context.DeadlineExceeded) against such an
//     error is always false (confirmed directly against
//     status.FromContextError(context.DeadlineExceeded).Err()), so FR8's
//     specific "taking longer than expected" copy could never render for
//     a real (non-fake-client) timeout -- it would silently fall through
//     to the generic failure message. isDeploymentActionTimeout below
//     checks both errors.Is and status.Code(err) == codes.DeadlineExceeded
//     so a genuine gRPC-transport deadline is classified correctly.
//
//  2. Only Stop/Restart's StopSession call was ever given a bound; Start
//     (both the plain "start" case and restart's own degenerate
//     no-live-session start) called StartSession on the raw, unbounded
//     request context, so a hung Start could still block the handler for
//     the backend's full unbounded duration.
//
// Given #1667 is a *second* round on this exact symptom despite code that
// reads correctly and passing fake-client tests, boundDeploymentRPC below
// deliberately does not rely on context.WithTimeout as the *only* thing
// standing between the handler and an indefinite hang, even though the
// investigation above found no fault in it: it races the RPC call (run in
// a background goroutine, still given the same bounded context so the
// underlying call is cancelled and the goroutine can exit) against an
// explicit time.After(timeout) selected in the calling goroutine. This
// makes the handler's own responsiveness a function of a single local
// wall-clock timer that started in this process, not of however grpc-go's
// internal deadline propagation behaves once it leaves this file --
// closing off the possibility of a third round with the same "the code
// looks right but doesn't fire live" shape, regardless of what
// version-skew, proxy, or dial-option quirk might exist in a live
// environment this vantage point can't directly observe.
//
// See handlers_deployment_actions_realtransport_test.go for the permanent
// regression test built from this investigation's reproduction: a real
// loopback gRPC server through the same production dial chain, with a
// handler that ignores its own context and blocks well past the bound.

// isDeploymentActionTimeout reports whether err represents
// boundDeploymentRPC's bound firing, whether that's this file's own
// explicit time.After race or the underlying gRPC call's context deadline
// expiring on its own. Real gRPC errors surface a context deadline as a
// *status.Error (status.Code(err) == codes.DeadlineExceeded), which
// errors.Is(err, context.DeadlineExceeded) alone cannot see through (see
// the investigation note above) -- so this checks both that and a plain
// wrapped context.DeadlineExceeded (boundDeploymentRPC's own time.After
// branch, and the fake gRPC client used in unit tests, both produce the
// latter).
func isDeploymentActionTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded
}

// boundDeploymentRPC calls fn with a context.WithTimeout(ctx, timeout)
// bound, but -- per the investigation note above -- does not treat that
// context bound as sufficient on its own to guarantee the caller gets
// control back within timeout. fn runs in a background goroutine; the
// calling goroutine races receiving fn's result against an independent
// time.After(timeout), returning as soon as either happens. The
// background goroutine is still handed the bounded context (not
// context.Background()), so if fn does honor context cancellation (as
// confirmed it does for the real StopSession/StartSession RPC path), it
// exits at essentially the same moment anyway; if it doesn't, the bounded
// context still fires eventually and lets the goroutine exit rather than
// leaking forever, it just no longer gates the caller's own return.
func boundDeploymentRPC[T any](ctx context.Context, timeout time.Duration, fn func(context.Context) (T, error)) (T, error) {
	boundedCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type outcome struct {
		val T
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		val, err := fn(boundedCtx)
		ch <- outcome{val, err}
	}()

	select {
	case o := <-ch:
		return o.val, o.err
	case <-time.After(timeout):
		var zero T
		return zero, fmt.Errorf("deployment action did not complete within %s: %w", timeout, context.DeadlineExceeded)
	}
}

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
		if _, err := boundDeploymentRPC(ctx, app.deploymentActionBound(), func(c context.Context) (*manmanpb.Session, error) {
			return app.grpc.StartSession(c, sgcID, false)
		}); err != nil {
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
// The StopSession call itself is wrapped in boundDeploymentRPC (#1664
// defense-in-depth, hardened by #1668 -- see the investigation note above
// deploymentActionBound) so a slow/hung command surfaces as an inline
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
	if _, err := boundDeploymentRPC(ctx, app.deploymentActionBound(), func(c context.Context) (*manmanpb.Session, error) {
		return app.grpc.StopSession(c, live.SessionId)
	}); err != nil {
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
		if _, err := boundDeploymentRPC(ctx, app.deploymentActionBound(), func(c context.Context) (*manmanpb.Session, error) {
			return app.grpc.StartSession(c, sgcID, false)
		}); err != nil {
			log.Printf("Error starting deployment %d during restart: %v", sgcID, err)
			return deploymentStartErrorMessage(err)
		}
		return ""
	}

	if _, err := boundDeploymentRPC(ctx, app.deploymentActionBound(), func(c context.Context) (*manmanpb.Session, error) {
		return app.grpc.StopSession(c, live.SessionId)
	}); err != nil {
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

// deploymentActionBound returns the timeout boundDeploymentRPC should race
// Stop/Restart/Start's own outbound StopSession/StartSession RPC call
// against (#1664 defense-in-depth, hardened and extended to Start by
// #1668 -- see the investigation note above), falling back to
// defaultDeploymentActionTimeout when the App wasn't configured with an
// override -- tests inject a small value via App.deploymentActionTimeout
// so a simulated slow/hung call doesn't wait out the real 8s bound.
func (app *App) deploymentActionBound() time.Duration {
	timeout := app.deploymentActionTimeout
	if timeout <= 0 {
		timeout = defaultDeploymentActionTimeout
	}
	return timeout
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
// human-readable inline message, distinguishing boundDeploymentRPC's bound
// firing (FR8's "command times out" trigger, #1668) from
// handleSessionStart's existing "active session" special case
// (handlers_sessions.go) and a generic failure, rather than leaking raw
// gRPC status text into the row when a clearer message exists. See
// isDeploymentActionTimeout's doc comment for why this checks both
// errors.Is and a gRPC status code instead of errors.Is alone.
func deploymentStartErrorMessage(err error) string {
	if isDeploymentActionTimeout(err) {
		return "The start command is taking longer than expected; check back shortly."
	}
	if strings.Contains(err.Error(), "active session") {
		return "A session is already active for this deployment."
	}
	return "Failed to start the deployment."
}

// deploymentStopErrorMessage turns a StopSession error into a
// human-readable inline message, distinguishing boundDeploymentRPC's bound
// firing (FR8's "command times out" trigger) from a generic failure. See
// isDeploymentActionTimeout's doc comment (#1668) for why this checks both
// errors.Is and a gRPC status code rather than errors.Is(err,
// context.DeadlineExceeded) alone, which never matched a real gRPC
// transport deadline.
func deploymentStopErrorMessage(err error) string {
	if isDeploymentActionTimeout(err) {
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
