//go:build integration

// See //whagent_net/session:session_integration_test's BUILD comment for
// why this file only builds under the "integration" tag (real Postgres
// via dbtest, requires Docker) and is excluded from `bazel test //...`.
package main

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
	"github.com/whale-net/everything/whagent_net/session"
)

// newTestStore provisions an isolated, migrated Postgres database (see
// //whagent_net/session:session_integration_test's identical newDB
// helper) and returns a ready *session.Store plus the underlying
// dbtest.Postgres for direct SQL assertions.
func newTestStore(t *testing.T) (*session.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	return session.New(db.Pool, nil), db
}

// newTestSessionRow inserts a minimal valid sessions row, matching
// //whagent_net/session:session_integration_test's newTestSession
// fixture shape.
func newTestSessionRow(t *testing.T, ctx context.Context, s *session.Store) *session.Session {
	t.Helper()
	subject := session.Subject{
		Iss:  "https://issuer.example.com",
		Sub:  "user-" + uuid.NewString(),
		Kind: session.SubjectKindHuman,
	}
	sess := &session.Session{
		SessionID:  uuid.New(),
		Subject:    subject,
		OnBehalfOf: subject,
		AgentID:    "test-agent",
		Model:      "test-model",
		Status:     session.StatusRunning,
	}
	require.NoError(t, s.Sessions().Create(ctx, sess))
	return sess
}

// TestActivities_BuildContext_PersistsEventIDListNotAssembledContext
// proves issue #2114's Testing phase: "Context build persists the exact
// ordered event-ID list into turn_context and never persists the
// assembled context itself." Two parts: (1) turn_context's stored
// event_ids exactly matches BuildContext's returned EventIDs, in the same
// order; (2) turn_context has no column that could hold an assembled
// message body -- a structural guarantee, not just an incidental one,
// checked directly against information_schema so a future migration
// widening the table would fail this test rather than silently
// regressing the contract.
func TestActivities_BuildContext_PersistsEventIDListNotAssembledContext(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	a := &Activities{Store: store}
	result, err := a.BuildContext(ctx, BuildContextInput{
		SessionID: sess.SessionID,
		Turn:      1,
		Input:     "hello from the user",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.EventIDs, "BuildContext must select at least the turn's own new user-input event")

	var storedIDs []uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT event_ids FROM turn_context WHERE session_id = $1 AND turn = $2
	`, sess.SessionID, 1).Scan(&storedIDs))
	assert.Equal(t, result.EventIDs, storedIDs, "turn_context must store the exact ordered event-ID list BuildContext returned")

	var columns []string
	rows, err := db.Pool.Query(ctx, `
		SELECT column_name FROM information_schema.columns WHERE table_name = 'turn_context'
	`)
	require.NoError(t, err)
	for rows.Next() {
		var col string
		require.NoError(t, rows.Scan(&col))
		columns = append(columns, col)
	}
	rows.Close()
	assert.ElementsMatch(t, []string{"session_id", "turn", "event_ids"}, columns,
		"turn_context must have no column capable of holding an assembled context body -- only the event-ID list")
}

// TestActivities_BuildContext_Retried_PersistsSameEventIDList proves
// BuildContext's retry-safety (activities.go's doc comment): a retried
// call for the same (session, turn, input) does not duplicate the
// user-input transcript event and recomputes the identical event-ID
// list.
func TestActivities_BuildContext_Retried_PersistsSameEventIDList(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	a := &Activities{Store: store}
	in := BuildContextInput{SessionID: sess.SessionID, Turn: 1, Input: "hello"}

	first, err := a.BuildContext(ctx, in)
	require.NoError(t, err)
	second, err := a.BuildContext(ctx, in)
	require.NoError(t, err)

	assert.Equal(t, first.EventIDs, second.EventIDs, "a retried BuildContext call must recompute the same event-ID list")

	var eventCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM transcript_event WHERE session_id = $1 AND turn = $2
	`, sess.SessionID, 1).Scan(&eventCount))
	assert.Equal(t, 1, eventCount, "a retried BuildContext call must not duplicate the user-input transcript event")
}
