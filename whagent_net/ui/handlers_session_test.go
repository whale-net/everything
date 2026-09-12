package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/whale-net/everything/libs/go/htmxauth"
	whagentpb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/ui/components"
)

// This file guards issue #2242's Testing section: handleSessionDetail's
// FR2 read-only gating (isSessionOwner) end to end through the real
// handler and rendered HTML -- not merely isSessionOwner's own unit
// behavior -- so a regression in how IsOwner is threaded into
// components.SessionDetail is caught the same way a regression in
// isSessionOwner's comparison itself would be.

// fakeUISessionServer is a real whagentpb.SessionServiceServer -- reached
// over a real (in-memory, bufconn) gRPC connection, mirroring
// grpc_client_test.go's fakeSessionServer -- that serves a single fixed
// Session/transcript for GetSession/ReadTranscript. Any other RPC panics
// on the embedded UnimplementedSessionServiceServer, deliberately: these
// tests only exercise handleSessionDetail's call graph.
type fakeUISessionServer struct {
	whagentpb.UnimplementedSessionServiceServer

	session *whagentpb.Session
	events  []*whagentpb.TranscriptEvent
}

func (f *fakeUISessionServer) GetSession(ctx context.Context, req *whagentpb.GetSessionRequest) (*whagentpb.GetSessionResponse, error) {
	return &whagentpb.GetSessionResponse{Session: f.session}, nil
}

func (f *fakeUISessionServer) ReadTranscript(ctx context.Context, req *whagentpb.ReadTranscriptRequest) (*whagentpb.ReadTranscriptResponse, error) {
	var out []*whagentpb.TranscriptEvent
	next := req.GetFromSeq()
	for _, ev := range f.events {
		if ev.GetSeq() < req.GetFromSeq() {
			continue
		}
		out = append(out, ev)
		if ev.GetSeq()+1 > next {
			next = ev.GetSeq() + 1
		}
	}
	return &whagentpb.ReadTranscriptResponse{Events: out, NextFromSeq: next}, nil
}

// GetSessionUsage is a fixed non-zero-cap stub (issue #2248's FR4 panel):
// handleSessionDetail now reads usage on every render, so this fake must
// implement the RPC for this file's owner-gating tests to reach handler
// code at all -- this file's tests do not otherwise assert on the usage
// panel's own rendered values.
func (f *fakeUISessionServer) GetSessionUsage(ctx context.Context, req *whagentpb.GetSessionUsageRequest) (*whagentpb.GetSessionUsageResponse, error) {
	return &whagentpb.GetSessionUsageResponse{Usage: &whagentpb.SessionUsage{TurnCap: 100, CostCapUsd: 1}}, nil
}

// newBufconnUISessionClient dials server over an in-memory bufconn
// listener and wraps it in a *SessionClient, the same shape
// grpc_client_test.go's newBufconnSessionClient uses -- generalized to
// any whagentpb.SessionServiceServer so both handlers_session_test.go and
// handlers_session_live_test.go can share it without duplicating the
// bufconn/grpc.NewServer plumbing.
func newBufconnUISessionClient(t *testing.T, server whagentpb.SessionServiceServer) *SessionClient {
	t.Helper()
	ctx := context.Background()

	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	whagentpb.RegisterSessionServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	client, err := NewSessionClient(ctx, "passthrough:///bufnet", grpc.WithContextDialer(dialer))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// devModeAuthenticator mirrors main_test.go's OIDC fixture but in
// AuthModeNone: RequireAuthFunc auto-authenticates every request as the
// fixed dev-user (Sub "dev-user"), matching htmxauth.Authenticator's own
// documented AuthModeNone contract. Used so these tests exercise the real
// RequireAuthFunc -> htmxauth.GetUser(ctx) -> isSessionOwner path, not a
// hand-rolled context injection (userContextKey is package-private to
// htmxauth -- see tools/app_registry/ui/handlers_promote_rollback_test.go's
// devUserAuth for the same constraint).
func devModeAuthenticator(t *testing.T) *htmxauth.Authenticator {
	t.Helper()
	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "session-detail-test-secret-at-least-32-bytes",
		SessionName:   "whagent_net_ui_session_detail_test_session",
	})
	require.NoError(t, err)
	return auth
}

// renderSessionDetail drives handleSessionDetail through the real
// RequireAuthFunc/WithAccessToken wrapping setupRoutes uses, so the
// dev-user context and the outbound gRPC token both flow exactly like
// production.
func renderSessionDetail(t *testing.T, app *App, sessionID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	wrapped := app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSessionDetail))

	req := httptest.NewRequest(http.MethodGet, "/sessions/"+sessionID.String(), nil)
	req.SetPathValue("id", sessionID.String())
	w := httptest.NewRecorder()
	wrapped(w, req)
	return w
}

// composerSlotMarker is the exact owner-only markup components.SessionDetail
// emits (session.templ's `id="composer-slot"` div) -- absence of this
// string is what proves a non-owner's response contains no composer/stop
// control markup at all, per issue #2242's Implementation section ("not
// merely disabled CSS").
const composerSlotMarker = `id="composer-slot"`

func TestHandleSessionDetail_OwnerGating(t *testing.T) {
	sessionID := uuid.New()
	now := timestamppb.New(time.Now())

	tests := []struct {
		name       string
		oidcIssuer string
		onBehalfOf *whagentpb.Subject
		wantOwner  bool
	}{
		{
			name:       "owner: iss and sub both match",
			oidcIssuer: "",
			onBehalfOf: &whagentpb.Subject{Iss: "", Sub: "dev-user", Kind: whagentpb.SubjectKind_SUBJECT_KIND_HUMAN},
			wantOwner:  true,
		},
		{
			name:       "non-owner: different sub",
			oidcIssuer: "",
			onBehalfOf: &whagentpb.Subject{Iss: "", Sub: "someone-else", Kind: whagentpb.SubjectKind_SUBJECT_KIND_HUMAN},
			wantOwner:  false,
		},
		{
			name:       "non-owner: same sub, different iss (LB2 -- matched on the pair, never sub alone)",
			oidcIssuer: "https://keycloak.example.com/realms/whagent",
			onBehalfOf: &whagentpb.Subject{Iss: "https://a-different-issuer.example.com", Sub: "dev-user", Kind: whagentpb.SubjectKind_SUBJECT_KIND_HUMAN},
			wantOwner:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := &fakeUISessionServer{
				session: &whagentpb.Session{
					SessionId:  sessionID.String(),
					State:      whagentpb.SessionState_SESSION_STATE_RUNNING,
					Subject:    tc.onBehalfOf,
					OnBehalfOf: tc.onBehalfOf,
					CreatedAt:  now,
					UpdatedAt:  now,
				},
			}
			app := &App{
				auth:       devModeAuthenticator(t),
				session:    newBufconnUISessionClient(t, server),
				oidcIssuer: tc.oidcIssuer,
			}

			w := renderSessionDetail(t, app, sessionID)
			require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

			body := w.Body.String()
			hasComposerSlot := strings.Contains(body, composerSlotMarker)
			if tc.wantOwner {
				require.True(t, hasComposerSlot, "expected the owner-only composer slot in the rendered page, got %q", body)
			} else {
				require.False(t, hasComposerSlot, "expected no composer/stop control markup at all for a non-owner, got %q", body)
			}
		})
	}
}

// TestIsSessionOwner_MatchesOnPairNeverSubAlone is a direct unit test of
// isSessionOwner (LB2): a duplicate of the sub-only collision case
// TestHandleSessionDetail_OwnerGating exercises at the handler level, kept
// here too because isSessionOwner is the one place that comparison is
// made and a future refactor could otherwise change it without any
// handler-level test noticing (e.g. if a second call site is added).
func TestIsSessionOwner_MatchesOnPairNeverSubAlone(t *testing.T) {
	app := &App{oidcIssuer: "https://keycloak.example.com"}
	user := &htmxauth.UserInfo{Sub: "dev-user"}

	sameIssSameSub := components.SubjectView{Iss: "https://keycloak.example.com", Sub: "dev-user"}
	if !app.isSessionOwner(user, sameIssSameSub) {
		t.Errorf("expected owner when (iss, sub) both match")
	}

	sameSubDifferentIss := components.SubjectView{Iss: "https://a-different-issuer.example.com", Sub: "dev-user"}
	if app.isSessionOwner(user, sameSubDifferentIss) {
		t.Errorf("expected non-owner when sub matches but iss does not (LB2)")
	}

	differentSubSameIss := components.SubjectView{Iss: "https://keycloak.example.com", Sub: "someone-else"}
	if app.isSessionOwner(user, differentSubSameIss) {
		t.Errorf("expected non-owner when sub does not match")
	}

	if app.isSessionOwner(nil, sameIssSameSub) {
		t.Errorf("expected non-owner for a nil user")
	}
}
