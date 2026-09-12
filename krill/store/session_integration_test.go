//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and
// whagent_net/session/store_integration_test.go for the pattern this file
// follows: spin up a throwaway Postgres via dbtest, apply krill's own real
// embedded migrations (//krill/migrate/schema), then exercise SessionStore
// (session.go) against it -- issue #2489's Testing section:
//   - self-acting init writes identical acting and on-behalf-of triples;
//   - agent-on-behalf-of-agent init writes two distinct triples;
//   - init with a whagent claim records whagent_session_id and still mints
//     a distinct krill session id;
//   - init without a whagent claim (human/OAuth2 shape) succeeds with
//     whagent_session_id NULL;
//   - GetSession found/not-found.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:session_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newTestSessionStore provisions an isolated, migrated Postgres database
// (krill's own real embedded schema, not a hand-copied one), seeds a single
// scope row directly (LB1's FK target -- session.go's own test doesn't need
// krill/migrate/seed's forge-coordinate specifics, just a valid scope_id),
// and returns a ready store.SessionStore plus that scope's id.
func newTestSessionStore(t *testing.T) (store.SessionStore, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	pool, err := pgxpool.New(ctx, db.ConnString)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, "whale-net/session-test", "main").Scan(&scopeID))

	return store.NewSessionStore(pool), scopeID
}

func strPtr(s string) *string { return &s }

// TestInitSession_SelfActing_WritesIdenticalTriples proves LB4's "when a
// caller acts for itself, on_behalf_of is written identical to acting" case.
func TestInitSession_SelfActing_WritesIdenticalTriples(t *testing.T) {
	ctx := context.Background()
	sessions, scopeID := newTestSessionStore(t)

	self := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-1", Kind: store.SubjectKindService}

	id, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)

	sess, err := sessions.GetSession(ctx, id)
	require.NoError(t, err)

	assert.Equal(t, self, sess.Acting)
	assert.Equal(t, self, sess.OnBehalfOf, "self-acting init must write on_behalf_of identical to acting")
	assert.Equal(t, scopeID, sess.ScopeID)
	assert.Nil(t, sess.WhagentSessionID)
}

// TestInitSession_AgentOnBehalfOfAgent_WritesDistinctTriples proves LB4's
// other case: acting and on-behalf-of differ and are recorded distinctly,
// neither one silently overwriting or defaulting to the other.
func TestInitSession_AgentOnBehalfOfAgent_WritesDistinctTriples(t *testing.T) {
	ctx := context.Background()
	sessions, scopeID := newTestSessionStore(t)

	acting := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-caller", Kind: store.SubjectKindService}
	onBehalfOf := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-owner", Kind: store.SubjectKindService}

	id, err := sessions.InitSession(ctx, scopeID, acting, onBehalfOf, nil)
	require.NoError(t, err)

	sess, err := sessions.GetSession(ctx, id)
	require.NoError(t, err)

	assert.Equal(t, acting, sess.Acting)
	assert.Equal(t, onBehalfOf, sess.OnBehalfOf)
	assert.NotEqual(t, sess.Acting, sess.OnBehalfOf, "acting and on_behalf_of must be recorded distinctly when they differ")
}

// TestInitSession_WithWhagentClaim_RecordsCorrelationAndMintsDistinctID
// proves whagent_session_id is correlation-only: it is recorded verbatim,
// but the krill session id InitSession mints is never equal to it and is
// its own independently-generated identifier.
func TestInitSession_WithWhagentClaim_RecordsCorrelationAndMintsDistinctID(t *testing.T) {
	ctx := context.Background()
	sessions, scopeID := newTestSessionStore(t)

	self := store.Subject{Iss: "https://whagent.example.com", Sub: "agent-42", Kind: store.SubjectKindService}
	whagentSessionID := uuid.NewString()

	id, err := sessions.InitSession(ctx, scopeID, self, self, strPtr(whagentSessionID))
	require.NoError(t, err)

	require.NotEqual(t, whagentSessionID, id.String(), "the krill session id must never equal Claim.WhagentSessionID")

	sess, err := sessions.GetSession(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, sess.WhagentSessionID)
	assert.Equal(t, whagentSessionID, *sess.WhagentSessionID)
}

// TestInitSession_WithoutWhagentClaim_SucceedsWithNullCorrelation proves the
// human/OAuth2 caller shape: no whagent claim at all, InitSession still
// mints a krill session, and whagent_session_id reads back NULL.
func TestInitSession_WithoutWhagentClaim_SucceedsWithNullCorrelation(t *testing.T) {
	ctx := context.Background()
	sessions, scopeID := newTestSessionStore(t)

	human := store.Subject{Iss: "https://issuer.example.com", Sub: "human-1", Kind: store.SubjectKindHuman}

	id, err := sessions.InitSession(ctx, scopeID, human, human, nil)
	require.NoError(t, err)

	sess, err := sessions.GetSession(ctx, id)
	require.NoError(t, err)
	assert.Nil(t, sess.WhagentSessionID, "a human/OAuth2 caller with no whagent claim must read back a NULL whagent_session_id")
}

// TestGetSession_UnknownID_ReturnsErrSessionNotFound proves the gate's sole
// read path can distinguish "no such session" from a store failure.
func TestGetSession_UnknownID_ReturnsErrSessionNotFound(t *testing.T) {
	ctx := context.Background()
	sessions, _ := newTestSessionStore(t)

	_, err := sessions.GetSession(ctx, store.SessionID(uuid.New()))
	assert.ErrorIs(t, err, store.ErrSessionNotFound)
}
