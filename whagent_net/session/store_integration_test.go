//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and audience_score_system/store/
// store_integration_test.go for the pattern this file follows: spin up a
// throwaway Postgres via dbtest, apply the package's own real embedded
// migrations (//whagent_net/migrate/schema, not a hand-copied schema), then
// exercise the session store package's public API against it -- proving
// the unique/partial-unique index enforcement, the advisory-lock seq
// allocation, the UpdateStatus compare-and-swap, and the SCD2 close-and-
// open write path actually hit Postgres (issue #2109's Testing section).
package session_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
	"github.com/whale-net/everything/whagent_net/session"
)

// newStore provisions an isolated Postgres database via dbtest, applies
// every migration in whagent-net's own embedded schema
// (schema.Migrations, currently only 001_initial_schema, #2109), and
// returns a ready *session.Store plus the underlying dbtest.Postgres for
// tests that need to reach past the store's own API (e.g. to assert on
// constraint-rejection behavior directly via raw SQL).
func newStore(t *testing.T) (*session.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return session.New(db.Pool, nil), db
}

// newTestSession builds a Session fixture with subject and on_behalf_of
// deliberately identical -- M1's always-true invariant (issue #2109's
// schema section) -- ready to pass to SessionStore.Create. Callers that
// need to mutate a field do so on the returned pointer before calling
// Create.
func newTestSession() *session.Session {
	subject := session.Subject{
		Iss:  "https://issuer.example.com",
		Sub:  "user-" + uuid.NewString(),
		Kind: session.SubjectKindHuman,
	}
	return &session.Session{
		SessionID:  uuid.New(),
		Subject:    subject,
		OnBehalfOf: subject,
		AgentID:    "test-agent",
		Model:      "test-model",
		Status:     session.StatusRunning,
	}
}

// createTestSession is newTestSession plus an actual Create call against s,
// for tests (transcript/agentdef/usage/idempotency) that only need *some*
// valid session row to hang their own fixtures off of.
func createTestSession(t *testing.T, ctx context.Context, s *session.Store) *session.Session {
	t.Helper()
	sess := newTestSession()
	require.NoError(t, s.Sessions().Create(ctx, sess))
	return sess
}
