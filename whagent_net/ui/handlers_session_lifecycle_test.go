package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	whagentpb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/ui/components"
)

// This file guards issue #2246's Testing section: the three lifecycle
// controls (start, send turn, stop) reach the right `api` call with the
// right arguments for the owner, are refused -- both at the UI and by
// never invoking the underlying `api` call -- for a non-owner, SendTurn's
// asynchrony (no read-back/poll), and PermissionDenied's inline-message
// mapping. Rendering/red-green coverage for the ownership gate itself
// lives alongside it below.

// fakeLifecycleSessionServer is a real whagentpb.SessionServiceServer,
// reached over bufconn (mirrors handlers_session_test.go's
// fakeUISessionServer), that records every StartSession/SendTurn/
// StopSession call it receives plus a GetSession/ReadTranscript call
// count -- the latter is how TestHandleSendTurn_ReturnsWithoutWaiting
// proves handleSendTurn never reads the session back after SendTurn.
type fakeLifecycleSessionServer struct {
	whagentpb.UnimplementedSessionServiceServer

	mu sync.Mutex

	session *whagentpb.Session

	getSessionCalls     int
	readTranscriptCalls int

	startReq  *whagentpb.StartSessionRequest
	startErr  error
	startResp *whagentpb.StartSessionResponse

	sendTurnCalled bool
	sendTurnReq    *whagentpb.SendTurnRequest
	sendTurnErr    error

	stopCalled bool
	stopReq    *whagentpb.StopSessionRequest
	stopErr    error
}

func (f *fakeLifecycleSessionServer) GetSession(ctx context.Context, req *whagentpb.GetSessionRequest) (*whagentpb.GetSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getSessionCalls++
	return &whagentpb.GetSessionResponse{Session: f.session}, nil
}

func (f *fakeLifecycleSessionServer) ReadTranscript(ctx context.Context, req *whagentpb.ReadTranscriptRequest) (*whagentpb.ReadTranscriptResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readTranscriptCalls++
	return &whagentpb.ReadTranscriptResponse{}, nil
}

func (f *fakeLifecycleSessionServer) StartSession(ctx context.Context, req *whagentpb.StartSessionRequest) (*whagentpb.StartSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startReq = req
	if f.startErr != nil {
		return nil, f.startErr
	}
	if f.startResp != nil {
		return f.startResp, nil
	}
	return &whagentpb.StartSessionResponse{Session: f.session}, nil
}

func (f *fakeLifecycleSessionServer) SendTurn(ctx context.Context, req *whagentpb.SendTurnRequest) (*whagentpb.SendTurnResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendTurnCalled = true
	f.sendTurnReq = req
	if f.sendTurnErr != nil {
		return nil, f.sendTurnErr
	}
	return &whagentpb.SendTurnResponse{Session: f.session}, nil
}

func (f *fakeLifecycleSessionServer) StopSession(ctx context.Context, req *whagentpb.StopSessionRequest) (*whagentpb.StopSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalled = true
	f.stopReq = req
	if f.stopErr != nil {
		return nil, f.stopErr
	}
	return &whagentpb.StopSessionResponse{Session: f.session}, nil
}

// GetSessionUsage is a fixed non-zero-cap stub (mirrors
// handlers_session_test.go's fakeUISessionServer): renderSessionDetail
// (via handlers_session.go's readUsage) now always fetches usage for the
// live panel, and this fake otherwise has no notion of usage at all.
func (f *fakeLifecycleSessionServer) GetSessionUsage(ctx context.Context, req *whagentpb.GetSessionUsageRequest) (*whagentpb.GetSessionUsageResponse, error) {
	return &whagentpb.GetSessionUsageResponse{Usage: &whagentpb.SessionUsage{TurnCap: 100, CostCapUsd: 1}}, nil
}

// newLifecycleTestApp builds an *App wired to server over bufconn, in
// AuthModeNone (dev-user, Sub "dev-user"), mirroring
// handlers_session_test.go's setup so ownership can be driven purely by
// onBehalfOf's (iss, sub).
func newLifecycleTestApp(t *testing.T, server *fakeLifecycleSessionServer, oidcIssuer string) *App {
	t.Helper()
	return &App{
		auth:       devModeAuthenticator(t),
		session:    newBufconnUISessionClient(t, server),
		oidcIssuer: oidcIssuer,
	}
}

func ownerSubject() *whagentpb.Subject {
	return &whagentpb.Subject{Iss: "", Sub: "dev-user", Kind: whagentpb.SubjectKind_SUBJECT_KIND_HUMAN}
}

func nonOwnerSubject() *whagentpb.Subject {
	return &whagentpb.Subject{Iss: "", Sub: "someone-else", Kind: whagentpb.SubjectKind_SUBJECT_KIND_HUMAN}
}

func newFixtureSession(sessionID uuid.UUID, state whagentpb.SessionState, onBehalfOf *whagentpb.Subject) *whagentpb.Session {
	now := timestamppb.New(time.Now())
	return &whagentpb.Session{
		SessionId:  sessionID.String(),
		State:      state,
		Subject:    onBehalfOf,
		OnBehalfOf: onBehalfOf,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// --- handleStartSession ---------------------------------------------------

// postForm drives handler through the real RequireAuthFunc/WithAccessToken
// wrapping, exactly like renderSessionDetail (handlers_session_test.go),
// with a application/x-www-form-urlencoded body and (when pathID != "")
// the {id} path value set.
func postForm(t *testing.T, app *App, handler http.HandlerFunc, path string, pathID string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	wrapped := app.auth.RequireAuthFunc(app.auth.WithAccessToken(handler))

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if pathID != "" {
		req.SetPathValue("id", pathID)
	}
	w := httptest.NewRecorder()
	wrapped(w, req)
	return w
}

// TestHandleStartSession_CallsStartSessionWithFormArgs proves the owner's
// submit reaches StartSession with the agent_id/model_override read
// verbatim off the form, and redirects to /sessions/{id} on success.
func TestHandleStartSession_CallsStartSessionWithFormArgs(t *testing.T) {
	newSessionID := uuid.New()
	server := &fakeLifecycleSessionServer{
		startResp: &whagentpb.StartSessionResponse{
			Session: newFixtureSession(newSessionID, whagentpb.SessionState_SESSION_STATE_RUNNING, ownerSubject()),
		},
	}
	app := newLifecycleTestApp(t, server, "")

	form := url.Values{"agent_id": {"code-review"}, "model_override": {"opus"}}
	w := postForm(t, app, app.handleStartSession, "/sessions", "", form)

	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/sessions/"+newSessionID.String(), w.Header().Get("Location"))

	require.NotNil(t, server.startReq, "StartSession must be called")
	require.Equal(t, "code-review", server.startReq.GetAgentId())
	require.Equal(t, "opus", server.startReq.GetModelOverride())
}

// TestHandleStartSession_MissingAgentID proves a blank agent_id is refused
// before StartSession is ever called (client-side validation still guarded
// server-side, since a plain POST bypasses the form's `required` attr).
func TestHandleStartSession_MissingAgentID(t *testing.T) {
	server := &fakeLifecycleSessionServer{}
	app := newLifecycleTestApp(t, server, "")

	w := postForm(t, app, app.handleStartSession, "/sessions", "", url.Values{"agent_id": {"  "}})

	require.Equal(t, http.StatusOK, w.Code)
	require.Nil(t, server.startReq, "StartSession must never be called for a missing agent id")
	require.Contains(t, w.Body.String(), "Agent ID is required.")
}

// TestHandleStartSession_PermissionDeniedRendersInlineMessage is FR1's
// "Error surfacing": a PermissionDenied from `api` (e.g. C8's missing-role
// rejection) renders inline on the form, never a raw gRPC status string
// and never a 500.
func TestHandleStartSession_PermissionDeniedRendersInlineMessage(t *testing.T) {
	server := &fakeLifecycleSessionServer{
		startErr: status.Error(codes.PermissionDenied, `caller lacks required role "code-review" for agent "code-review"`),
	}
	app := newLifecycleTestApp(t, server, "")

	w := postForm(t, app, app.handleStartSession, "/sessions", "", url.Values{"agent_id": {"code-review"}})

	require.Equal(t, http.StatusOK, w.Code)
	require.NotEqual(t, http.StatusInternalServerError, w.Code)
	// The rendered HTML escapes quotes (&#34;), so match the unquoted core
	// of the message rather than the raw Go string literal.
	require.Contains(t, w.Body.String(), "caller lacks required role")
	require.Contains(t, w.Body.String(), "code-review")
	require.NotContains(t, w.Body.String(), "rpc error", "must never leak a raw gRPC status string")
}

// --- handleSendTurn --------------------------------------------------------

// TestHandleSendTurn_Owner_CallsSendTurnWithSessionAndInput proves the
// owner's submit reaches SendTurn with the right session id and input.
func TestHandleSendTurn_Owner_CallsSendTurnWithSessionAndInput(t *testing.T) {
	sessionID := uuid.New()
	server := &fakeLifecycleSessionServer{
		session: newFixtureSession(sessionID, whagentpb.SessionState_SESSION_STATE_RUNNING, ownerSubject()),
	}
	app := newLifecycleTestApp(t, server, "")

	w := postForm(t, app, app.handleSendTurn, "/sessions/"+sessionID.String()+"/turns", sessionID.String(), url.Values{"input": {"hello there"}})

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, server.sendTurnCalled)
	require.Equal(t, sessionID.String(), server.sendTurnReq.GetSessionId())
	require.Equal(t, "hello there", server.sendTurnReq.GetInput())
}

// TestHandleSendTurn_NonOwner_RefusedAndAPINotCalled is issue #2246's
// Testing section: "non-owner POST to turns/stop is refused by the UI
// *and* the underlying `api` call is never made."
func TestHandleSendTurn_NonOwner_RefusedAndAPINotCalled(t *testing.T) {
	sessionID := uuid.New()
	server := &fakeLifecycleSessionServer{
		session: newFixtureSession(sessionID, whagentpb.SessionState_SESSION_STATE_RUNNING, nonOwnerSubject()),
	}
	app := newLifecycleTestApp(t, server, "")

	w := postForm(t, app, app.handleSendTurn, "/sessions/"+sessionID.String()+"/turns", sessionID.String(), url.Values{"input": {"hello there"}})

	require.Equal(t, http.StatusForbidden, w.Code)
	require.False(t, server.sendTurnCalled, "SendTurn must never be invoked for a non-owner's replayed POST")
}

// TestHandleSendTurn_ReturnsWithoutWaitingForTurn is FR1's asynchrony
// requirement (issue #2246's Testing section): "assert the handler does
// not call any read-back/poll path." controlSessionOwner's own ownership
// check is the one expected GetSession call; anything beyond that --
// another GetSession, or any ReadTranscript at all -- would mean
// handleSendTurn is reading the session/transcript back rather than
// relying purely on the live SSE feed.
func TestHandleSendTurn_ReturnsWithoutWaitingForTurn(t *testing.T) {
	sessionID := uuid.New()
	server := &fakeLifecycleSessionServer{
		session: newFixtureSession(sessionID, whagentpb.SessionState_SESSION_STATE_RUNNING, ownerSubject()),
	}
	app := newLifecycleTestApp(t, server, "")

	w := postForm(t, app, app.handleSendTurn, "/sessions/"+sessionID.String()+"/turns", sessionID.String(), url.Values{"input": {"hello there"}})

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, server.sendTurnCalled)
	require.Equal(t, 1, server.getSessionCalls, "expected exactly one GetSession call (the ownership check), no read-back after SendTurn")
	require.Equal(t, 0, server.readTranscriptCalls, "handleSendTurn must never poll/read the transcript back")

	// The success response is the empty, enabled composer fragment, not a
	// spinner or partial transcript re-render.
	require.Contains(t, w.Body.String(), `id="turn-composer"`)
}

// TestHandleSendTurn_PermissionDeniedRendersInlineMessage proves a
// terminal-session FailedPrecondition (or any PermissionDenied) from
// SendTurn renders inline on the re-shown composer, never a 500.
func TestHandleSendTurn_PermissionDeniedRendersInlineMessage(t *testing.T) {
	sessionID := uuid.New()
	server := &fakeLifecycleSessionServer{
		session:     newFixtureSession(sessionID, whagentpb.SessionState_SESSION_STATE_DONE, ownerSubject()),
		sendTurnErr: status.Error(codes.FailedPrecondition, "session "+sessionID.String()+" is already done"),
	}
	app := newLifecycleTestApp(t, server, "")

	w := postForm(t, app, app.handleSendTurn, "/sessions/"+sessionID.String()+"/turns", sessionID.String(), url.Values{"input": {"hi"}})

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "session "+sessionID.String()+" is already done")
	require.NotContains(t, w.Body.String(), "rpc error")
}

// --- handleStopSession ------------------------------------------------------

// TestHandleStopSession_Owner_CallsStopSessionWithSessionID proves the
// owner's submit reaches StopSession with the right session id, and swaps
// in the absent-control fragment on success.
func TestHandleStopSession_Owner_CallsStopSessionWithSessionID(t *testing.T) {
	sessionID := uuid.New()
	server := &fakeLifecycleSessionServer{
		session: newFixtureSession(sessionID, whagentpb.SessionState_SESSION_STATE_RUNNING, ownerSubject()),
	}
	app := newLifecycleTestApp(t, server, "")

	w := postForm(t, app, app.handleStopSession, "/sessions/"+sessionID.String()+"/stop", sessionID.String(), nil)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, server.stopCalled)
	require.Equal(t, sessionID.String(), server.stopReq.GetSessionId())

	body := w.Body.String()
	require.Contains(t, body, `id="stop-control"`)
	require.NotContains(t, body, "<form", "success swap must be StopControlAbsent -- no button left to act on")
}

// TestHandleStopSession_NonOwner_RefusedAndAPINotCalled mirrors
// TestHandleSendTurn_NonOwner_RefusedAndAPINotCalled for the stop control.
func TestHandleStopSession_NonOwner_RefusedAndAPINotCalled(t *testing.T) {
	sessionID := uuid.New()
	server := &fakeLifecycleSessionServer{
		session: newFixtureSession(sessionID, whagentpb.SessionState_SESSION_STATE_RUNNING, nonOwnerSubject()),
	}
	app := newLifecycleTestApp(t, server, "")

	w := postForm(t, app, app.handleStopSession, "/sessions/"+sessionID.String()+"/stop", sessionID.String(), nil)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.False(t, server.stopCalled, "StopSession must never be invoked for a non-owner's replayed POST")
}

// TestHandleStopSession_PermissionDeniedRendersInlineMessage proves a
// StopSession failure renders inline on the re-shown stop control, never
// a 500.
func TestHandleStopSession_PermissionDeniedRendersInlineMessage(t *testing.T) {
	sessionID := uuid.New()
	server := &fakeLifecycleSessionServer{
		session: newFixtureSession(sessionID, whagentpb.SessionState_SESSION_STATE_RUNNING, ownerSubject()),
		stopErr: status.Error(codes.PermissionDenied, "caller lacks required role"),
	}
	app := newLifecycleTestApp(t, server, "")

	w := postForm(t, app, app.handleStopSession, "/sessions/"+sessionID.String()+"/stop", sessionID.String(), nil)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "caller lacks required role")
	require.NotContains(t, w.Body.String(), "rpc error")
}

// --- Rendering: composer + stop for owner, neither for non-owner, stop -----
// --- hidden for a terminal session even for the owner (issue #2246's     ---
// --- Testing section).                                                   ---

const stopControlFormMarker = `hx-post="/sessions/`

func TestSessionDetailRendering_OwnerVsNonOwnerControls(t *testing.T) {
	tests := []struct {
		name         string
		state        whagentpb.SessionState
		onBehalfOf   *whagentpb.Subject
		wantComposer bool
		wantStopForm bool
	}{
		{
			name:         "owner, running: composer and stop both present",
			state:        whagentpb.SessionState_SESSION_STATE_RUNNING,
			onBehalfOf:   ownerSubject(),
			wantComposer: true,
			wantStopForm: true,
		},
		{
			name:         "owner, terminal (done): composer present, stop control hidden",
			state:        whagentpb.SessionState_SESSION_STATE_DONE,
			onBehalfOf:   ownerSubject(),
			wantComposer: true,
			wantStopForm: false,
		},
		{
			name:         "non-owner, running: neither composer nor stop present",
			state:        whagentpb.SessionState_SESSION_STATE_RUNNING,
			onBehalfOf:   nonOwnerSubject(),
			wantComposer: false,
			wantStopForm: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sessionID := uuid.New()
			server := &fakeLifecycleSessionServer{
				session: newFixtureSession(sessionID, tc.state, tc.onBehalfOf),
			}
			app := newLifecycleTestApp(t, server, "")

			w := renderSessionDetail(t, app, sessionID)
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

			body := w.Body.String()
			require.Equal(t, tc.wantComposer, strings.Contains(body, `id="turn-composer"`), "composer presence, body: %s", body)
			require.Equal(t, tc.wantStopForm, strings.Contains(body, stopControlFormMarker+sessionID.String()+"/stop"), "stop-control form presence, body: %s", body)
		})
	}
}

// TestOwnerControls_TerminalStateOmitsStopControl is a direct component-
// level duplicate of the "owner, terminal" case above (isTerminalSessionState,
// session.templ), kept for the same reason
// TestIsSessionOwner_MatchesOnPairNeverSubAlone duplicates the handler-level
// ownership check: OwnerControls is the one place the omission decision is
// made, and a future refactor could change it without any handler-level
// rendering test noticing if a second call site were ever added.
func TestOwnerControls_TerminalStateOmitsStopControl(t *testing.T) {
	var sb strings.Builder
	require.NoError(t, components.OwnerControls("sess-1", "done").Render(context.Background(), &sb))

	body := sb.String()
	require.Contains(t, body, `id="turn-composer"`)
	require.NotContains(t, body, stopControlFormMarker+"sess-1/stop")
}

// TestOwnerControls_NonTerminalStateIncludesStopControl is the positive
// control for TestOwnerControls_TerminalStateOmitsStopControl.
func TestOwnerControls_NonTerminalStateIncludesStopControl(t *testing.T) {
	var sb strings.Builder
	require.NoError(t, components.OwnerControls("sess-1", "running").Render(context.Background(), &sb))

	body := sb.String()
	require.Contains(t, body, `id="turn-composer"`)
	require.Contains(t, body, stopControlFormMarker+"sess-1/stop")
}
