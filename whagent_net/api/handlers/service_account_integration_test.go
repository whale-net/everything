//go:build integration

// This file proves FR6/C10 (issue #2243): a Keycloak client-credentials
// caller (a service account, no human present) can StartSession/SendTurn/
// StopSession exactly as a human operator can, passing the same
// required-role check (C8), and with StartSession recording subject/
// on_behalf_of as kind = service (on_behalf_of = subject, since the
// service acts for itself), scoping control (#2237's canControl) to that
// same on_behalf_of exactly as it does for a human.
//
// Unlike session_integration_test.go's newTestServer (which authenticates
// every call as AuthModeNone's single fixed Claims{Subject: "dev-user"},
// with no IsServiceAccount field to set), these tests need a caller whose
// grpcauth.Claims.IsServiceAccount is true. AuthModeNone's dev-claims
// injection has no such knob (KEYCLOAK.md § "Service accounts": "there is
// no dev-mode way to locally exercise a service-account-classified call
// short of running against a real oidc-mode Keycloak"), so these tests
// bypass NewServerInterceptors entirely and call SessionServer's exported
// RPC methods directly against a context built with
// grpcauth.ContextWithClaims -- exactly the pattern that function's own
// doc comment describes ("exercise authorization logic against handlers
// called directly... without duplicating the unexported context key"),
// already used by tools/app_registry/server/auth's tests. The gRPC/
// bufconn/interceptor-chain plumbing session_integration_test.go's
// newTestServer sets up is irrelevant here: RequireClaimsUnaryInterceptor
// only checks that claims are present, which ContextWithClaims already
// guarantees, and callerSubject (session.go) reads claims out of ctx the
// same way regardless of how they got there.
package handlers_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/api/handlers"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

// newServiceAccountTestServer is newTestServer's (session_integration_test.go)
// counterpart for this file: same real Postgres/migrations plumbing, but
// returns the raw *handlers.SessionServer (not a gRPC client) so a test can
// call StartSession/SendTurn/StopSession directly under whatever
// grpcauth.Claims it builds, plus a fakeTemporalClient (fake_temporal_test.go)
// StartSession/SendTurn/StopSession drive instead of a live Temporal
// frontend. catalog is always nil: no test in this file sets
// ModelOverride, so StartSession never reaches the catalogue-gated branch
// (start.go step 4) that would need one.
func newServiceAccountTestServer(t *testing.T) (*handlers.SessionServer, *session.Store, *fakeTemporalClient) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	store := session.New(db.Pool, nil)
	temporal := &fakeTemporalClient{}
	srv := handlers.NewSessionServer(ctx, store, testIssuer, temporal, "test-task-queue", nil, nil)
	return srv, store, temporal
}

// serviceClaims builds the grpcauth.Claims a Keycloak client-credentials
// caller's verified token would produce (KEYCLOAK.md § "Service
// accounts"): IsServiceAccount true, ClientID set (unused by callerSubject
// -- see auth.go's doc comment on why Kind branches on IsServiceAccount
// alone, never ClientID).
func serviceClaims(sub string, roles ...string) *grpcauth.Claims {
	return &grpcauth.Claims{Subject: sub, ClientID: sub, IsServiceAccount: true, Roles: roles}
}

// humanClaims mirrors serviceClaims for a human caller -- IsServiceAccount
// false, but still carrying a ClientID (the client the human authenticated
// through), proving callerSubject's Kind derivation never keys off
// ClientID presence.
func humanClaims(sub string, roles ...string) *grpcauth.Claims {
	return &grpcauth.Claims{Subject: sub, ClientID: "whagent-net-ui", IsServiceAccount: false, Roles: roles}
}

func ctxAs(claims *grpcauth.Claims) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), claims)
}

// seedServiceTestAgent upserts a version-1 agent_definition, optionally
// gated by requiredRole (nil == runnable by any authenticated caller).
func seedServiceTestAgent(t *testing.T, ctx context.Context, store *session.Store, agentID string, requiredRole *string) {
	t.Helper()
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, &session.AgentDefinition{
		AgentID:      agentID,
		Version:      1,
		Model:        strPtr2("test-model"),
		ToolSet:      []session.ToolServerRef{},
		MaxTurns:     100,
		MaxCostUSD:   1.0,
		RequiredRole: requiredRole,
	}))
}

func strPtr2(s string) *string { return &s }

// TestStartSession_ServiceAccountCaller_RecordsServiceKindOnBothSubjectAndOnBehalfOf
// proves the core FR6 claim: a client-credentials caller's StartSession
// writes subject_kind = 'service' and on_behalf_of_kind = 'service' with
// identical (iss, sub) -- on_behalf_of = subject, since a service account
// acts for itself (M1/M2 has no delegated-caller path).
func TestStartSession_ServiceAccountCaller_RecordsServiceKindOnBothSubjectAndOnBehalfOf(t *testing.T) {
	srv, store, _ := newServiceAccountTestServer(t)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)

	resp, err := srv.StartSession(ctxAs(serviceClaims("svc-1")), &pb.StartSessionRequest{AgentId: "agent-1"})
	require.NoError(t, err)

	assert.Equal(t, pb.SubjectKind_SUBJECT_KIND_SERVICE, resp.Session.Subject.Kind)
	assert.Equal(t, pb.SubjectKind_SUBJECT_KIND_SERVICE, resp.Session.OnBehalfOf.Kind)
	assert.Equal(t, resp.Session.Subject.Iss, resp.Session.OnBehalfOf.Iss)
	assert.Equal(t, resp.Session.Subject.Sub, resp.Session.OnBehalfOf.Sub)

	sess, err := store.Sessions().GetByID(ctx, uuid.MustParse(resp.Session.SessionId))
	require.NoError(t, err)
	assert.Equal(t, session.SubjectKindService, sess.Subject.Kind)
	assert.Equal(t, session.SubjectKindService, sess.OnBehalfOf.Kind)
	assert.Equal(t, sess.Subject, sess.OnBehalfOf, "on_behalf_of must equal subject exactly -- a service account acts for itself")
}

// TestStartSession_HumanCallerWithClientIDSet_StillRecordsHumanKind proves
// callerSubject's Kind derivation branches on IsServiceAccount alone: a
// human caller whose token also carries a ClientID (every real token does)
// must not be misclassified as a service account.
func TestStartSession_HumanCallerWithClientIDSet_StillRecordsHumanKind(t *testing.T) {
	srv, store, _ := newServiceAccountTestServer(t)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)

	resp, err := srv.StartSession(ctxAs(humanClaims("alice")), &pb.StartSessionRequest{AgentId: "agent-1"})
	require.NoError(t, err)

	assert.Equal(t, pb.SubjectKind_SUBJECT_KIND_HUMAN, resp.Session.Subject.Kind)
	assert.Equal(t, pb.SubjectKind_SUBJECT_KIND_HUMAN, resp.Session.OnBehalfOf.Kind)
}

// TestServiceAccountCaller_MaySendTurnAndStopSessionItStarted proves a
// service account's StartSession-recorded on_behalf_of is exactly what
// canControl checks: the same service account may both SendTurn and
// StopSession on a session it started, with no human token anywhere in
// this test (FR6's Testing section, "start -> send turn -> stop... with a
// client-credentials token and no human token anywhere in the test").
func TestServiceAccountCaller_MaySendTurnAndStopSessionItStarted(t *testing.T) {
	srv, store, temporal := newServiceAccountTestServer(t)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)
	callerCtx := ctxAs(serviceClaims("svc-1"))

	start, err := srv.StartSession(callerCtx, &pb.StartSessionRequest{AgentId: "agent-1"})
	require.NoError(t, err)
	require.Len(t, temporal.startedSnapshot(), 1)

	sendResp, err := srv.SendTurn(callerCtx, &pb.SendTurnRequest{SessionId: start.Session.SessionId, Input: "hello"})
	require.NoError(t, err, "the service account that started the session must be able to send it a turn")
	assert.Equal(t, start.Session.SessionId, sendResp.Session.SessionId)

	stopResp, err := srv.StopSession(callerCtx, &pb.StopSessionRequest{SessionId: start.Session.SessionId})
	require.NoError(t, err, "the service account that started the session must be able to stop it")
	assert.Equal(t, start.Session.SessionId, stopResp.Session.SessionId)

	signals := temporal.signalsSnapshot()
	require.Len(t, signals, 2, "one SendTurn signal, one Stop signal")
	assert.Equal(t, "SendTurn", signals[0].SignalName)
	assert.Equal(t, "Stop", signals[1].SignalName)
}

// TestSendTurn_DifferentServiceAccount_PermissionDenied proves a *different*
// service account -- not the one whose on_behalf_of the session carries --
// is denied control, exactly as a different human is.
func TestSendTurn_DifferentServiceAccount_PermissionDenied(t *testing.T) {
	srv, store, _ := newServiceAccountTestServer(t)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)

	start, err := srv.StartSession(ctxAs(serviceClaims("svc-1")), &pb.StartSessionRequest{AgentId: "agent-1"})
	require.NoError(t, err)

	_, err = srv.SendTurn(ctxAs(serviceClaims("svc-2")), &pb.SendTurnRequest{SessionId: start.Session.SessionId, Input: "hi"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	_, err = srv.StopSession(ctxAs(serviceClaims("svc-2")), &pb.StopSessionRequest{SessionId: start.Session.SessionId})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

// TestSendTurn_UnrelatedHuman_PermissionDenied_OnServiceAccountSession
// proves the converse: an unrelated human is denied control of a session a
// service account started -- ownership is scoped to on_behalf_of, and
// neither identity kind gets special treatment there.
func TestSendTurn_UnrelatedHuman_PermissionDenied_OnServiceAccountSession(t *testing.T) {
	srv, store, _ := newServiceAccountTestServer(t)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)

	start, err := srv.StartSession(ctxAs(serviceClaims("svc-1")), &pb.StartSessionRequest{AgentId: "agent-1"})
	require.NoError(t, err)

	_, err = srv.SendTurn(ctxAs(humanClaims("alice")), &pb.SendTurnRequest{SessionId: start.Session.SessionId, Input: "hi"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

// TestStartSession_RequiredRoleParity proves C8: a definition's
// required_role check applies identically to a service account and a
// human -- both denied when lacking the role, both allowed when holding
// it. Run as one test with subtests (rather than duplicated top-level
// tests) so the parity claim itself -- same rule, either caller kind -- is
// visible in the test structure, not just asserted twice independently.
func TestStartSession_RequiredRoleParity(t *testing.T) {
	srv, store, _ := newServiceAccountTestServer(t)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "restricted-agent", strPtr2("ops"))

	t.Run("service account lacking the required role is denied", func(t *testing.T) {
		_, err := srv.StartSession(ctxAs(serviceClaims("svc-1", "unrelated-role")), &pb.StartSessionRequest{AgentId: "restricted-agent"})
		require.Error(t, err)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("human lacking the required role is denied", func(t *testing.T) {
		_, err := srv.StartSession(ctxAs(humanClaims("alice", "unrelated-role")), &pb.StartSessionRequest{AgentId: "restricted-agent"})
		require.Error(t, err)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("service account holding the required role succeeds", func(t *testing.T) {
		resp, err := srv.StartSession(ctxAs(serviceClaims("svc-1", "ops")), &pb.StartSessionRequest{AgentId: "restricted-agent"})
		require.NoError(t, err)
		assert.NotEmpty(t, resp.Session.SessionId)
	})

	t.Run("human holding the required role succeeds", func(t *testing.T) {
		resp, err := srv.StartSession(ctxAs(humanClaims("alice", "ops")), &pb.StartSessionRequest{AgentId: "restricted-agent"})
		require.NoError(t, err)
		assert.NotEmpty(t, resp.Session.SessionId)
	})
}

// TestSessionLifecycle_ServiceAccountCaller_StartSendStopWithNoHumanTokenAnywhere
// is FR6's named end-to-end integration test: start -> send turn -> stop,
// entirely under a single service account's claims, over a real Postgres
// via dbtest -- no human-classified Claims value is constructed anywhere in
// this test.
func TestSessionLifecycle_ServiceAccountCaller_StartSendStopWithNoHumanTokenAnywhere(t *testing.T) {
	srv, store, temporal := newServiceAccountTestServer(t)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)
	caller := ctxAs(serviceClaims("scheduler-1"))

	start, err := srv.StartSession(caller, &pb.StartSessionRequest{AgentId: "agent-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.SessionState_SESSION_STATE_RUNNING, start.Session.State)
	assert.Equal(t, pb.SubjectKind_SUBJECT_KIND_SERVICE, start.Session.Subject.Kind)

	sendResp, err := srv.SendTurn(caller, &pb.SendTurnRequest{SessionId: start.Session.SessionId, Input: "do the thing"})
	require.NoError(t, err)
	assert.Equal(t, start.Session.SessionId, sendResp.Session.SessionId)

	// StopSession only signals the workflow (fire-and-forget, like
	// SendTurn) -- the session's status column flips to `stopped` only once
	// a real SessionWorkflow processes that signal (worker/workflow.go,
	// exercised by worker/workflow_test.go, not by this hermetic fake), so
	// stopResp/final below still report `running` here. That is itself part
	// of what this test proves: StopSession's control check and signal path
	// run identically for a service-account caller, with no synchronous
	// status side effect to (mis)report.
	stopResp, err := srv.StopSession(caller, &pb.StopSessionRequest{SessionId: start.Session.SessionId})
	require.NoError(t, err)
	assert.Equal(t, start.Session.SessionId, stopResp.Session.SessionId)

	final, err := srv.GetSession(caller, &pb.GetSessionRequest{SessionId: start.Session.SessionId})
	require.NoError(t, err)
	assert.Equal(t, start.Session.SessionId, final.Session.SessionId)
	assert.Equal(t, pb.SubjectKind_SUBJECT_KIND_SERVICE, final.Session.OnBehalfOf.Kind)

	signals := temporal.signalsSnapshot()
	require.Len(t, signals, 2)
	assert.Equal(t, "SendTurn", signals[0].SignalName)
	assert.Equal(t, "Stop", signals[1].SignalName)
}
