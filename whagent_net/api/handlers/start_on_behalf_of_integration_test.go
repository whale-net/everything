//go:build integration

// This file proves StartSession's on_behalf_of (delegated-start) path
// (FR9/FR11/FR12, NFR1, NFR4): only an allowlisted caller's own Keycloak
// client_id may set it, an empty allowlist fails closed, the persisted row
// keeps the authenticated caller in subject_* while the asserted identity
// lands in on_behalf_of_*, and leaving on_behalf_of unset is unchanged
// (on_behalf_of == subject).
//
// These tests call SessionServer.StartSession directly under a context built
// with grpcauth.ContextWithClaims rather than over a real gRPC/bufconn call,
// for the same reason service_account_integration_test.go does: the
// interceptor chain cannot inject a caller with a chosen client_id, and
// client_id is the entire subject of the allowlist gate. The gate reads
// grpcauth.Claims.ClientID out of ctx exactly as it would off a verified
// Keycloak token; the transport/interceptor plumbing is irrelevant to what is
// under test here.
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

// newOnBehalfOfTestServer is newServiceAccountTestServer's counterpart with a
// configurable on_behalf_of client_id allowlist: same real Postgres /
// migrations / fake Temporal plumbing, but allowedClientIDs is handed
// straight to NewSessionServer so a test can opt a client in (or leave the
// list empty to exercise the fail-closed default).
func newOnBehalfOfTestServer(t *testing.T, allowedClientIDs ...string) (*handlers.SessionServer, *session.Store) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	store := session.New(db.Pool, nil)
	srv := handlers.NewSessionServer(ctx, store, testIssuer, &fakeTemporalClient{}, "test-task-queue", nil, nil, allowedClientIDs)
	return srv, store
}

// fcmClientID stands in for friendly_computing_machine's Keycloak client_id,
// the one deployment the allowlist is configured with in production (FR12).
const fcmClientID = "friendly-computing-machine"

// assertedUser is the Slack user's Keycloak identity a delegated StartSession
// runs as.
var assertedUser = session.Subject{Iss: "https://slack-user-issuer.example.com", Sub: "slack-user-1", Kind: session.SubjectKindHuman}

// delegatingClaims builds a service-account caller whose own client_id is
// clientID -- distinct from its `sub`, which is what lets a test vary the
// allowlist decision while holding the caller's identity fixed. serviceClaims
// (service_account_integration_test.go) sets ClientID == Subject, so it cannot
// express a caller whose token was issued to a specific client on behalf of a
// separately-identified principal.
func delegatingClaims(sub, clientID string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{
		Subject:          sub,
		ClientID:         clientID,
		IsServiceAccount: true,
	})
}

// assertNoSessions proves a rejected delegated start wrote nothing (NFR1's
// fail-closed rule): the allowlist/validation check happens before any
// sessions row is created.
func assertNoSessions(t *testing.T, ctx context.Context, store *session.Store) {
	t.Helper()
	sessions, _, err := store.Sessions().List(ctx, session.SessionFilter{}, session.SessionPage{PageSize: 100})
	require.NoError(t, err)
	assert.Empty(t, sessions, "a rejected StartSession must not create a sessions row")
}

// subjectToTestProto maps a session.Subject onto the request's on_behalf_of
// wire shape.
func subjectToTestProto(s session.Subject) *pb.Subject {
	kind := pb.SubjectKind_SUBJECT_KIND_HUMAN
	if s.Kind == session.SubjectKindService {
		kind = pb.SubjectKind_SUBJECT_KIND_SERVICE
	}
	return &pb.Subject{Iss: s.Iss, Sub: s.Sub, Kind: kind}
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	require.NoError(t, err)
	return id
}

// TestStartSession_AllowlistedCaller_DelegatedStartRecordsAssertedOnBehalfOf
// proves case 1: an allowlisted caller setting on_behalf_of succeeds, the
// stored OnBehalfOf is the asserted subject, and Subject stays the
// authenticated caller (NFR4's distinct-columns audit).
func TestStartSession_AllowlistedCaller_DelegatedStartRecordsAssertedOnBehalfOf(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)

	// The caller is fcm's service identity; the session runs as the Slack user.
	resp, err := srv.StartSession(delegatingClaims("fcm-service", fcmClientID), &pb.StartSessionRequest{
		AgentId:    "agent-1",
		OnBehalfOf: subjectToTestProto(assertedUser),
	})
	require.NoError(t, err)

	// Read the persisted row back (not just the response mapping).
	sess, err := store.Sessions().GetByID(ctx, mustUUID(t, resp.Session.SessionId))
	require.NoError(t, err)

	assert.Equal(t, "fcm-service", sess.Subject.Sub, "subject_* must stay the authenticated caller")
	assert.Equal(t, session.SubjectKindService, sess.Subject.Kind)
	assert.Equal(t, assertedUser, sess.OnBehalfOf, "on_behalf_of_* must be the asserted subject")
	assert.NotEqual(t, sess.Subject, sess.OnBehalfOf, "a delegated start's two identity columns must differ")

	// The response exposes both halves distinctly too.
	assert.Equal(t, "fcm-service", resp.Session.Subject.Sub)
	assert.Equal(t, assertedUser.Sub, resp.Session.OnBehalfOf.Sub)
	assert.Equal(t, assertedUser.Iss, resp.Session.OnBehalfOf.Iss)
}

// TestStartSession_NonAllowlistedCaller_PermissionDenied proves case 2: a
// caller whose client_id is not on the allowlist is denied, and no session
// row is created.
func TestStartSession_NonAllowlistedCaller_PermissionDenied(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)

	// An authenticated caller, but a different client than the allowlisted one.
	_, err := srv.StartSession(delegatingClaims("some-other-service", "not-allowlisted"), &pb.StartSessionRequest{
		AgentId:    "agent-1",
		OnBehalfOf: subjectToTestProto(assertedUser),
	})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	assertNoSessions(t, ctx, store)
}

// TestStartSession_EmptyAllowlist_FailsClosed proves case 3: an unset/empty
// allowlist (the default deployment config) permits no delegated start at all,
// and writes nothing.
func TestStartSession_EmptyAllowlist_FailsClosed(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t) // no allowlist entries
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)

	_, err := srv.StartSession(delegatingClaims("fcm-service", fcmClientID), &pb.StartSessionRequest{
		AgentId:    "agent-1",
		OnBehalfOf: subjectToTestProto(assertedUser),
	})
	require.Error(t, err, "an empty allowlist must fail closed (NFR1)")
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	assertNoSessions(t, ctx, store)
}

// TestStartSession_OnBehalfOfUnset_UnchangedBehaviour proves case 4: a
// caller that leaves on_behalf_of unset gets exactly today's behaviour -- the
// session runs as itself, OnBehalfOf == Subject -- and this holds regardless
// of the allowlist (the gate is never consulted on the non-delegated path).
func TestStartSession_OnBehalfOfUnset_UnchangedBehaviour(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)

	// Even a NON-allowlisted caller succeeds when it does not ask to delegate.
	resp, err := srv.StartSession(delegatingClaims("plain-service", "not-allowlisted"), &pb.StartSessionRequest{
		AgentId: "agent-1",
	})
	require.NoError(t, err)

	sess, err := store.Sessions().GetByID(ctx, mustUUID(t, resp.Session.SessionId))
	require.NoError(t, err)
	assert.Equal(t, sess.Subject, sess.OnBehalfOf, "with on_behalf_of unset, on_behalf_of must equal subject")
}

// TestStartSession_AllowlistedCaller_MalformedOnBehalfOf_InvalidArgument
// proves case 5: an allowlisted caller asserting a malformed on_behalf_of (a
// missing sub or iss) is rejected with InvalidArgument, and writes nothing.
func TestStartSession_AllowlistedCaller_MalformedOnBehalfOf_InvalidArgument(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	seedServiceTestAgent(t, ctx, store, "agent-1", nil)

	caller := delegatingClaims("fcm-service", fcmClientID)

	t.Run("missing sub", func(t *testing.T) {
		_, err := srv.StartSession(caller, &pb.StartSessionRequest{
			AgentId:    "agent-1",
			OnBehalfOf: &pb.Subject{Iss: assertedUser.Iss, Kind: pb.SubjectKind_SUBJECT_KIND_HUMAN},
		})
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("missing iss", func(t *testing.T) {
		_, err := srv.StartSession(caller, &pb.StartSessionRequest{
			AgentId:    "agent-1",
			OnBehalfOf: &pb.Subject{Sub: assertedUser.Sub, Kind: pb.SubjectKind_SUBJECT_KIND_HUMAN},
		})
		require.Error(t, err)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	assertNoSessions(t, ctx, store)
}
