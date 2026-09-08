//go:build integration

// This file only builds under the "integration" build tag (see
// //libs/go/dbtest's README and whagent_net/session/store_integration_test.go
// for the pattern) so `bazel test //...` on a Docker-less machine stays
// green. It proves GetSession and ReadTranscript (issue #2113's two read
// paths) end to end: a real gRPC server (bufconn transport, the exact
// interceptor chain whagent_net/api/main.go wires -- grpcauth's
// AuthModeNone dev-claims injection plus handlers.RequireClaimsUnaryInterceptor)
// in front of a *handlers.SessionServer backed by a real Postgres via
// //libs/go/dbtest, with the store's own embedded migrations
// (//whagent_net/migrate/schema) applied exactly as whagent_net/session's
// own integration tests do.
package handlers_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/api/handlers"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

const bufSize = 1024 * 1024

// testIssuer is the issuer NewSessionServer is configured with below --
// matches what WHAGENT_OIDC_ISSUER would hold in production (main.go), and
// is what SessionServer.callerSubject reconstructs the caller's identity
// against.
const testIssuer = "https://issuer.example.com"

// devSubject is the identity every RPC call in this file authenticates as:
// grpcauth.NewServerInterceptors' AuthModeNone always injects a fixed
// Claims{Subject: "dev-user"} (see grpcauth/interceptors.go) -- there is no
// way to authenticate as a different subject over a real call in
// AuthModeNone, so PERMISSION_DENIED coverage below instead creates a
// session owned by a *different* stored Subject and proves devSubject
// cannot read it.
var devSubject = session.Subject{Iss: testIssuer, Sub: "dev-user", Kind: session.SubjectKindHuman}

var otherSubject = session.Subject{Iss: testIssuer, Sub: "someone-else", Kind: session.SubjectKindHuman}

// newTestServer provisions an isolated Postgres via dbtest, applies
// whagent-net's real embedded migrations, wires a *handlers.SessionServer
// behind the same auth interceptor chain whagent_net/api/main.go uses (minus
// the logging interceptor, which has no bearing on auth/business-logic
// behavior), and returns a ready SessionServiceClient plus the underlying
// *session.Store for test fixtures to write through directly.
func newTestServer(t *testing.T) (pb.SessionServiceClient, *session.Store) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	store := session.New(db.Pool, nil)
	// temporalClient is nil: this file covers GetSession/ReadTranscript
	// only (issue #2113's two read paths), neither of which touches
	// SessionServer.temporalClient -- the write RPCs' own integration
	// coverage (issue #2117's Testing phase) constructs a real Temporal
	// test environment instead.
	sessionServer := handlers.NewSessionServer(store, testIssuer, nil, "")

	unaryAuth, streamAuth, err := grpcauth.NewServerInterceptors(ctx, grpcauth.ServerConfig{
		Mode: grpcauth.AuthModeNone,
	})
	require.NoError(t, err)

	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryAuth, handlers.RequireClaimsUnaryInterceptor),
		grpc.ChainStreamInterceptor(streamAuth, handlers.RequireClaimsStreamInterceptor),
	)
	pb.RegisterSessionServiceServer(grpcServer, sessionServer)

	go func() {
		_ = grpcServer.Serve(lis)
	}()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	t.Cleanup(func() {
		conn.Close() //nolint:errcheck
		grpcServer.Stop()
	})

	return pb.NewSessionServiceClient(conn), store
}

// createSession inserts a Session row owned by subject in status, ready for
// a test to read back through the gRPC surface.
func createSession(t *testing.T, ctx context.Context, store *session.Store, subject session.Subject, status session.Status) *session.Session {
	t.Helper()
	sess := &session.Session{
		SessionID:  uuid.New(),
		Subject:    subject,
		OnBehalfOf: subject,
		AgentID:    "test-agent",
		Model:      "test-model",
		Status:     status,
	}
	require.NoError(t, store.Sessions().Create(ctx, sess))
	return sess
}

// TestGetSession_NotFound proves an unknown session id is NOT_FOUND.
func TestGetSession_NotFound(t *testing.T) {
	client, _ := newTestServer(t)

	_, err := client.GetSession(context.Background(), &pb.GetSessionRequest{SessionId: uuid.NewString()})

	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// TestReadTranscript_NotFound proves an unknown session id is NOT_FOUND.
func TestReadTranscript_NotFound(t *testing.T) {
	client, _ := newTestServer(t)

	_, err := client.ReadTranscript(context.Background(), &pb.ReadTranscriptRequest{SessionId: uuid.NewString()})

	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// TestGetSession_PermissionDenied proves a session belonging to a different
// subject than the authenticated caller (devSubject, injected by
// AuthModeNone) is PERMISSION_DENIED, not NOT_FOUND -- the row exists, the
// caller just isn't allowed to see it.
func TestGetSession_PermissionDenied(t *testing.T) {
	client, store := newTestServer(t)
	sess := createSession(t, context.Background(), store, otherSubject, session.StatusRunning)

	_, err := client.GetSession(context.Background(), &pb.GetSessionRequest{SessionId: sess.SessionID.String()})

	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

// TestReadTranscript_PermissionDenied is GetSession's PERMISSION_DENIED
// proof, for ReadTranscript.
func TestReadTranscript_PermissionDenied(t *testing.T) {
	client, store := newTestServer(t)
	sess := createSession(t, context.Background(), store, otherSubject, session.StatusRunning)

	_, err := client.ReadTranscript(context.Background(), &pb.ReadTranscriptRequest{SessionId: sess.SessionID.String()})

	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

// TestGetSession_MapsEachStoredStatusToTheMatchingProtoEnum proves all six
// session.Status values round-trip through GetSession to the correct
// pb.SessionState (FR3). Terminal statuses (done/stopped/failed/capped) are
// reached via UpdateStatus's compare-and-swap from an initial running
// status, exactly as the real workflow would transition them -- Create
// itself never writes cap_kind/error_category (those columns aren't part of
// its INSERT), so a terminal row only exists via UpdateStatus.
func TestGetSession_MapsEachStoredStatusToTheMatchingProtoEnum(t *testing.T) {
	client, store := newTestServer(t)
	ctx := context.Background()

	cases := []struct {
		name      string
		terminal  session.Status
		reason    *session.TerminalReason
		wantState pb.SessionState
	}{
		{name: "running", terminal: "", wantState: pb.SessionState_SESSION_STATE_RUNNING},
		{name: "awaiting_input", terminal: session.StatusAwaitingInput, wantState: pb.SessionState_SESSION_STATE_AWAITING_INPUT},
		{name: "done", terminal: session.StatusDone, wantState: pb.SessionState_SESSION_STATE_DONE},
		{name: "stopped", terminal: session.StatusStopped, wantState: pb.SessionState_SESSION_STATE_STOPPED},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := createSession(t, ctx, store, devSubject, session.StatusRunning)
			if tc.terminal != "" {
				require.NoError(t, store.Sessions().UpdateStatus(ctx, sess.SessionID, tc.terminal, tc.reason))
			}

			resp, err := client.GetSession(ctx, &pb.GetSessionRequest{SessionId: sess.SessionID.String()})
			require.NoError(t, err)
			assert.Equal(t, tc.wantState, resp.Session.State)
		})
	}
}

// TestGetSession_CappedReportsCapKind proves a capped session's cap_kind is
// set on the wire (FR3: "which cap was hit"), and that error_category/
// error_detail stay unset (nil), not zero-valued.
func TestGetSession_CappedReportsCapKind(t *testing.T) {
	client, store := newTestServer(t)
	ctx := context.Background()
	sess := createSession(t, ctx, store, devSubject, session.StatusRunning)

	capKind := session.CapKindTurns
	require.NoError(t, store.Sessions().UpdateStatus(ctx, sess.SessionID, session.StatusCapped, &session.TerminalReason{CapKind: &capKind}))

	resp, err := client.GetSession(ctx, &pb.GetSessionRequest{SessionId: sess.SessionID.String()})
	require.NoError(t, err)

	assert.Equal(t, pb.SessionState_SESSION_STATE_CAPPED, resp.Session.State)
	if assert.NotNil(t, resp.Session.CapKind, "cap_kind must be set on a capped session") {
		assert.Equal(t, pb.CapKind_CAP_KIND_TURNS, *resp.Session.CapKind)
	}
	assert.Nil(t, resp.Session.ErrorCategory, "error_category must stay unset on a capped session")
	assert.Nil(t, resp.Session.ErrorDetail, "error_detail must stay unset on a capped session")
}

// TestGetSession_FailedReportsCategoryAndDetail proves a failed session
// reports both error_category and error_detail (FR3: "an error category of
// retryable or non_retryable, plus a short human-readable detail"), and that
// cap_kind stays unset.
func TestGetSession_FailedReportsCategoryAndDetail(t *testing.T) {
	client, store := newTestServer(t)
	ctx := context.Background()
	sess := createSession(t, ctx, store, devSubject, session.StatusRunning)

	category := session.ErrorCategoryRetryable
	detail := "upstream model provider timed out"
	require.NoError(t, store.Sessions().UpdateStatus(ctx, sess.SessionID, session.StatusFailed, &session.TerminalReason{ErrorCategory: &category, ErrorDetail: &detail}))

	resp, err := client.GetSession(ctx, &pb.GetSessionRequest{SessionId: sess.SessionID.String()})
	require.NoError(t, err)

	assert.Equal(t, pb.SessionState_SESSION_STATE_FAILED, resp.Session.State)
	if assert.NotNil(t, resp.Session.ErrorCategory, "error_category must be set on a failed session") {
		assert.Equal(t, pb.ErrorCategory_ERROR_CATEGORY_RETRYABLE, *resp.Session.ErrorCategory)
	}
	if assert.NotNil(t, resp.Session.ErrorDetail, "error_detail must be set on a failed session") {
		assert.Equal(t, detail, *resp.Session.ErrorDetail)
	}
	assert.Nil(t, resp.Session.CapKind, "cap_kind must stay unset on a failed session")
}

// TestGetSession_RunningReportsNeitherCapKindNorErrorCategory proves a
// non-terminal session's terminal-reason fields are left unset (not
// zero-valued/empty-string) -- the issue's explicit "fields unset, not
// empty strings" requirement.
func TestGetSession_RunningReportsNeitherCapKindNorErrorCategory(t *testing.T) {
	client, store := newTestServer(t)
	ctx := context.Background()
	sess := createSession(t, ctx, store, devSubject, session.StatusRunning)

	resp, err := client.GetSession(ctx, &pb.GetSessionRequest{SessionId: sess.SessionID.String()})
	require.NoError(t, err)

	assert.Equal(t, pb.SessionState_SESSION_STATE_RUNNING, resp.Session.State)
	assert.Nil(t, resp.Session.CapKind)
	assert.Nil(t, resp.Session.ErrorCategory)
	assert.Nil(t, resp.Session.ErrorDetail)
}

// TestReadTranscript_OrderedBySeqForInterleavedEventTypes proves
// ReadTranscript returns every event type FR2 names -- user turn, model
// message, tool call, tool result -- in commit (seq) order for a session
// that is still running, and that a tool-result event whose own payload
// carries isError=true is transcribed as an ordinary tool-result event
// (never rewritten to a failure event type). This is also the test the
// issue's red/green instruction targets: reversing transcript.go's `ORDER
// BY seq` clause turns the ordering assertion below red.
func TestReadTranscript_OrderedBySeqForInterleavedEventTypes(t *testing.T) {
	client, store := newTestServer(t)
	ctx := context.Background()
	sess := createSession(t, ctx, store, devSubject, session.StatusRunning)

	type fixture struct {
		eventType string
		payload   json.RawMessage
	}
	fixtures := []fixture{
		{eventType: "user_turn", payload: json.RawMessage(`{"text":"hello"}`)},
		{eventType: "model_message", payload: json.RawMessage(`{"text":"thinking..."}`)},
		{eventType: "tool_call", payload: json.RawMessage(`{"tool":"search","args":{}}`)},
		{eventType: "tool_result", payload: json.RawMessage(`{"isError":true,"output":"domain server reported an error"}`)},
		{eventType: "model_message", payload: json.RawMessage(`{"text":"done"}`)},
	}
	var wantSeqs []int64
	for _, f := range fixtures {
		ev, err := store.Transcript().Append(ctx, sess.SessionID, 1, f.eventType, f.payload)
		require.NoError(t, err)
		wantSeqs = append(wantSeqs, ev.Seq)
	}

	resp, err := client.ReadTranscript(ctx, &pb.ReadTranscriptRequest{SessionId: sess.SessionID.String()})
	require.NoError(t, err)
	require.Len(t, resp.Events, len(fixtures), "a still-running session must return exactly what has been committed so far")

	var gotSeqs []int64
	for i, ev := range resp.Events {
		gotSeqs = append(gotSeqs, ev.Seq)
		assert.Equal(t, fixtures[i].eventType, ev.Type, "event %d: type must round-trip unchanged", i)
		assert.JSONEq(t, string(fixtures[i].payload), string(ev.Payload), "event %d: payload must round-trip unchanged", i)
	}
	assert.Equal(t, wantSeqs, gotSeqs, "events must be returned in commit (seq) order")

	// FR2's explicit isError distinction: the tool-result event with its own
	// isError=true is still type "tool_result", never rewritten to a
	// session-failure event type.
	assert.Equal(t, "tool_result", resp.Events[3].Type, "a domain-server isError=true tool result must stay a tool-result event, not become a failure event")

	assert.Equal(t, wantSeqs[len(wantSeqs)-1]+1, resp.NextFromSeq, "next_from_seq must resume exactly after the last event returned")
}

// TestReadTranscript_Pagination_ResumesWithNoGapAndNoDuplicate proves
// from_seq/limit pagination covers every committed event exactly once
// across pages, in order, with next_from_seq always resuming exactly after
// the last event returned.
func TestReadTranscript_Pagination_ResumesWithNoGapAndNoDuplicate(t *testing.T) {
	client, store := newTestServer(t)
	ctx := context.Background()
	sess := createSession(t, ctx, store, devSubject, session.StatusRunning)

	const total = 5
	for i := 0; i < total; i++ {
		_, err := store.Transcript().Append(ctx, sess.SessionID, 1, "test.event", json.RawMessage(`{}`))
		require.NoError(t, err)
	}

	var allSeqs []int64
	fromSeq := int64(0)
	for page := 0; page < total+1; page++ { // +1 guards against an infinite loop if pagination regresses
		resp, err := client.ReadTranscript(ctx, &pb.ReadTranscriptRequest{SessionId: sess.SessionID.String(), FromSeq: fromSeq, Limit: 2})
		require.NoError(t, err)

		if len(resp.Events) == 0 {
			assert.Equal(t, fromSeq, resp.NextFromSeq, "next_from_seq must echo the request's from_seq back unchanged when there is nothing new")
			break
		}

		for _, ev := range resp.Events {
			allSeqs = append(allSeqs, ev.Seq)
		}
		assert.Equal(t, allSeqs[len(allSeqs)-1]+1, resp.NextFromSeq)
		fromSeq = resp.NextFromSeq
	}

	require.Len(t, allSeqs, total, "pagination must cover every committed event exactly once")
	seen := make(map[int64]bool, total)
	for i, seq := range allSeqs {
		require.False(t, seen[seq], "seq %d returned more than once across pages", seq)
		seen[seq] = true
		if i > 0 {
			assert.Equal(t, allSeqs[i-1]+1, seq, "no gap between consecutive pages")
		}
	}
}
