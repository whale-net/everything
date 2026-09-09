package main

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	whagentpb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/ui/components"
)

// readTranscriptPageSize is the page size handleSessionDetail's initial
// full-page read and handleSessionEvents' seed/incremental reads
// (handlers_session_live.go) pass as ReadTranscriptRequest.Limit. A
// session's transcript is typically small (single-digit to low-hundreds
// of events, ARCHITECTURE.md "Guardrails": default caps 100 turns), so a
// single generously-sized page covers the common case; true pagination
// (a "load older events" affordance) is out of this task's scope.
const readTranscriptPageSize = 500

// readTranscript reads sessionID's transcript starting at fromSeq through
// the api client, converting the response to
// []components.TranscriptEventView (../convert.go). Shared by
// handleSessionDetail (fromSeq=0, the initial full-page render) and
// handlers_session_live.go's seed/incremental reads (fromSeq=cursor+1) so
// the two paths can never disagree about how a TranscriptEvent is
// projected for rendering.
func (app *App) readTranscript(ctx context.Context, sessionID uuid.UUID, fromSeq int64) ([]components.TranscriptEventView, int64, error) {
	resp, err := app.session.Client().ReadTranscript(ctx, &whagentpb.ReadTranscriptRequest{
		SessionId: sessionID.String(),
		FromSeq:   fromSeq,
		Limit:     readTranscriptPageSize,
	})
	if err != nil {
		return nil, fromSeq, err
	}

	views := make([]components.TranscriptEventView, len(resp.GetEvents()))
	for i, ev := range resp.GetEvents() {
		views[i] = transcriptEventToView(ev)
	}
	return views, resp.GetNextFromSeq(), nil
}

// isSessionOwner reports whether user is the signed-in operator who may
// control the session onBehalfOf identifies (FR2's read-only gating):
// user's (iss, sub) equals onBehalfOf's, matched on the pair -- never sub
// alone (LB2). user's iss is always app.oidcIssuer, the UI's own single
// configured Keycloak issuer -- never a claim read off user.RawClaims --
// mirroring whagent_net/api/handlers/session.go's callerSubject, which
// hardcodes the deployment's configured issuer the same way rather than
// trusting a per-token iss claim; M1 has no delegated-caller/multi-issuer
// path yet (LB2). A nil user (unreachable behind RequireAuthFunc in
// practice) is never an owner.
func (app *App) isSessionOwner(user *htmxauth.UserInfo, onBehalfOf components.SubjectView) bool {
	if user == nil {
		return false
	}
	return app.oidcIssuer == onBehalfOf.Iss && user.Sub == onBehalfOf.Sub
}

// handleSessionDetail is the session detail page (FR2, NFR2, NFR3, issue
// #2242): any signed-in operator may open any session -- GetSession/
// ReadTranscript both have no ownership check (session.proto's doc
// comments, #2237) -- and watches it live over the SSE stream
// handlers_session_live.go's handleSessionEvents serves. The composer/
// stop-control slot renders only for the session's on-behalf-of subject
// (isSessionOwner); every other signed-in operator gets the exact same
// page with that slot entirely absent.
func (app *App) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()

	sessionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return
	}

	sessResp, err := app.session.Client().GetSession(ctx, &whagentpb.GetSessionRequest{SessionId: sessionID.String()})
	if err != nil {
		logger.Error("failed to get session", "session_id", sessionID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	transcriptEvents, _, err := app.readTranscript(ctx, sessionID, 0)
	if err != nil {
		logger.Error("failed to read transcript", "session_id", sessionID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	user := htmxauth.GetUser(ctx)
	sessionView := sessionToView(sessResp.GetSession())

	layoutData := components.LayoutData{
		Title:  "Session " + sessionID.String(),
		Active: "Sessions",
		User:   user,
	}

	data := components.SessionDetailData{
		Layout:  layoutData,
		Session: sessionView,
		Events:  transcriptEvents,
		IsOwner: app.isSessionOwner(user, sessionView.OnBehalfOf),
		Topics:  sessionEventTopics(sessionID, transcriptEvents),
	}

	if err := RenderTempl(w, r, layoutData.Title, components.SessionDetail(data)); err != nil {
		logger.Error("failed to render session detail page", "session_id", sessionID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
