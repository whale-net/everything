// FR1 (issue #2246): the three session lifecycle controls the standalone
// UI gives an operator -- start, send a turn, stop -- the same three
// M1 already gave Claude Code and gRPC. All three go through the same
// `api` client every other handler in this binary uses, as the signed-in
// operator's own forwarded token (grpcauth, main.go's NewApp) -- never a
// shared service account.
package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	whagentpb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/ui/components"
	"github.com/whale-net/everything/whagent_net/ui/pages"
)

// mapAPIError converts an `api` gRPC error into FR1's "Error surfacing"
// inline message: never a raw gRPC status string, never a silent no-op.
// PermissionDenied/FailedPrecondition/NotFound/InvalidArgument pass the
// status message straight through -- `api`'s own handlers already write
// human-readable text for exactly these codes (e.g. start.go's "caller
// lacks required role %q for agent %q" for FR9's C8 role rejection,
// send.go's "session %s is already %s" for a terminal-session turn).
// Every other code (Internal, Unavailable, Unknown, ...) collapses to a
// generic message rather than ever leaking an internal error's detail to
// the browser.
func mapAPIError(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return "Something went wrong. Please try again."
	}
	switch st.Code() {
	case codes.PermissionDenied, codes.FailedPrecondition, codes.NotFound, codes.InvalidArgument:
		return st.Message()
	default:
		return "Something went wrong. Please try again."
	}
}

// isUnexpectedAPIError reports whether err represents a genuine failure
// worth an ERROR log (AGENTS.md "Logging Levels") rather than expected,
// handled control flow (a role check or terminal-session rejection the
// operator can act on, already surfaced inline via mapAPIError).
func isUnexpectedAPIError(err error) bool {
	switch status.Code(err) {
	case codes.Internal, codes.Unknown, codes.Unavailable:
		return true
	default:
		return false
	}
}

// listAgentOptions reads the current agents for the new-session dropdown.
func (app *App) listAgentOptions(ctx context.Context) ([]pages.AgentOption, error) {
	resp, err := app.session.Client().ListAgents(ctx, &whagentpb.ListAgentsRequest{})
	if err != nil {
		return nil, err
	}
	opts := make([]pages.AgentOption, 0, len(resp.GetAgents()))
	for _, a := range resp.GetAgents() {
		v := agentToView(a)
		opts = append(opts, pages.AgentOption{AgentID: v.AgentID, Model: v.Model})
	}
	return opts, nil
}

// newSessionData builds the form's page data with the agent dropdown filled.
func (app *App) newSessionData(ctx context.Context) pages.SessionNewData {
	data := pages.SessionNewData{
		Layout: components.LayoutData{
			Title:  "Start a session",
			Active: "New session",
			User:   htmxauth.GetUser(ctx),
		},
	}
	agents, err := app.listAgentOptions(ctx)
	if err != nil {
		logging.Get("main").Error("failed to list agents for new session form", "error", err)
		data.AgentsError = "Could not load agents: the whagent-net api is unavailable."
		return data
	}
	data.Agents = agents
	return data
}

// handleNewSession is GET /sessions/new.
func (app *App) handleNewSession(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	data := app.newSessionData(r.Context())

	if err := RenderTempl(w, r, data.Layout.Title, pages.SessionNew(data)); err != nil {
		logger.Error("failed to render session new page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleStartSession is POST /sessions. agent_id must be one of the
// current ListAgents entries; anything else is rejected with a 400.
func (app *App) handleStartSession(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	agentID := strings.TrimSpace(r.FormValue("agent_id"))
	modelOverride := strings.TrimSpace(r.FormValue("model_override"))

	renderForm := func(code int, errMsg string) {
		data := app.newSessionData(ctx)
		data.AgentID = agentID
		data.ModelOverride = modelOverride
		data.Error = errMsg
		w.WriteHeader(code)
		if err := RenderTempl(w, r, data.Layout.Title, pages.SessionNew(data)); err != nil {
			logger.Error("failed to render session new page", "error", err)
		}
	}

	agents, err := app.listAgentOptions(ctx)
	if err != nil {
		logger.Error("failed to list agents to validate start session", "error", err)
		renderForm(http.StatusBadGateway, "Could not validate the agent: the whagent-net api is unavailable.")
		return
	}
	known := false
	for _, a := range agents {
		if a.AgentID == agentID {
			known = true
			break
		}
	}
	if !known {
		renderForm(http.StatusBadRequest, "Select an agent from the list.")
		return
	}

	req := &whagentpb.StartSessionRequest{AgentId: agentID}
	if modelOverride != "" {
		req.ModelOverride = &modelOverride
	}

	resp, err := app.session.Client().StartSession(ctx, req)
	if err != nil {
		if isUnexpectedAPIError(err) {
			logger.Error("start session failed", "agent_id", agentID, "error", err)
		}
		renderForm(http.StatusOK, mapAPIError(err))
		return
	}

	http.Redirect(w, r, "/sessions/"+resp.GetSession().GetSessionId(), http.StatusSeeOther)
}

// controlSessionOwner reads sessionID's current on_behalf_of subject and
// reports whether the signed-in request's user matches it (isSessionOwner,
// handlers_session.go). Shared by handleSendTurn/handleStopSession (FR1's
// control-scope check, C13): the underlying api call must never be made
// for a non-owner, so this always runs -- and is evaluated -- before
// either handler reaches SendTurn/StopSession. ok is false both when the
// read itself fails (already written a 500 to w) and when the caller is
// not the owner (already written a 403 to w); either way the caller must
// return immediately without invoking the api's write RPC.
func (app *App) controlSessionOwner(w http.ResponseWriter, r *http.Request, sessionID uuid.UUID) (sessionView components.SessionView, ok bool) {
	logger := logging.Get("main")

	sessionView, err := app.readSession(r.Context(), sessionID)
	if err != nil {
		logger.Error("failed to read session for control ownership check", "session_id", sessionID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return components.SessionView{}, false
	}

	if !app.isSessionOwner(htmxauth.GetUser(r.Context()), sessionView.OnBehalfOf) {
		// FR1/C13: control is scoped to the session's on-behalf-of subject,
		// mirroring `api`'s own canControl check
		// (whagent_net/api/handlers/session.go) -- refused here, before
		// SendTurn/StopSession is ever called, so a non-owner's replayed
		// POST is rejected independently at both layers (issue #2246's
		// Validation section).
		http.Error(w, "forbidden: you are not this session's owner", http.StatusForbidden)
		return components.SessionView{}, false
	}

	return sessionView, true
}

// handleSendTurn is FR1's turn composer submit (POST /sessions/{id}/turns,
// issue #2246): calls SendTurn and returns immediately -- no read-back, no
// poll loop -- swapping the composer back to an empty, enabled state on
// success (Composer's doc comment). The turn's actual output surfaces
// later through the live transcript (handlers_session_live.go), which
// this handler never touches.
func (app *App) handleSendTurn(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()

	sessionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	input := r.FormValue("input")

	if _, ok := app.controlSessionOwner(w, r, sessionID); !ok {
		return
	}

	var errMsg string
	if _, err := app.session.Client().SendTurn(ctx, &whagentpb.SendTurnRequest{
		SessionId: sessionID.String(),
		Input:     input,
	}); err != nil {
		if isUnexpectedAPIError(err) {
			logger.Error("send turn failed", "session_id", sessionID, "error", err)
		}
		errMsg = mapAPIError(err)
	}

	renderComposerFragment(ctx, w, sessionID, errMsg)
}

// handleStopSession is FR1's stop control submit (POST
// /sessions/{id}/stop, issue #2246): calls StopSession, swapping the
// control to StopControlAbsent on success -- absent, not merely disabled,
// mirroring isTerminalSessionState's stance (session.templ). StopSession
// itself is idempotent for an already-terminal session
// (whagent_net/api/handlers/stop.go's doc comment), so this almost never
// actually reaches the error branch below in practice.
func (app *App) handleStopSession(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()

	sessionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}

	if _, ok := app.controlSessionOwner(w, r, sessionID); !ok {
		return
	}

	_, err = app.session.Client().StopSession(ctx, &whagentpb.StopSessionRequest{SessionId: sessionID.String()})

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	if err != nil {
		if isUnexpectedAPIError(err) {
			logger.Error("stop session failed", "session_id", sessionID, "error", err)
		}
		if renderErr := components.StopControl(sessionID.String(), mapAPIError(err)).Render(ctx, w); renderErr != nil {
			logger.Error("failed to render stop-control error fragment", "session_id", sessionID, "error", renderErr)
		}
		return
	}
	if err := components.StopControlAbsent().Render(ctx, w); err != nil {
		logger.Error("failed to render stop-control fragment", "session_id", sessionID, "error", err)
	}
}

// renderComposerFragment writes components.Composer(sessionID, errMsg) as
// a bare HTML fragment (text/html, 200) -- handleSendTurn's whole
// response body, matched to htmx's hx-target="this"/hx-swap="outerHTML"
// on the form that posted here (Composer's doc comment).
func renderComposerFragment(ctx context.Context, w http.ResponseWriter, sessionID uuid.UUID, errMsg string) {
	logger := logging.Get("main")
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	if err := components.Composer(sessionID.String(), errMsg).Render(ctx, w); err != nil {
		logger.Error("failed to render composer fragment", "session_id", sessionID, "error", err)
	}
}
