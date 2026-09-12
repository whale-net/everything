//go:build integration

// Real-Postgres coverage for DesignSessionStore (migration 008, issue
// #2542, FR1/FR8) -- the longer-lived container a session's
// revision_event rounds accumulate under. See store_integration_test.go's
// package doc for why this file only builds under the "integration" build
// tag.
//
// This target also carries revision_event_integration_test.go
// (RevisionEventStore, FR2-FR4/NFR1): a RevisionEvent's sole parent is a
// DesignSession, so the two share this file's fixture helpers rather than
// duplicating scope/product/krill_session/design_session setup across two
// go_test targets.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:design_session_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newDesignSessionTestStore provisions an isolated, migrated Postgres
// database (krill's own real embedded schema through 008_design_session)
// and returns a ready *store.Store plus the underlying dbtest.Postgres.
func newDesignSessionTestStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return store.New(db.Pool), db
}

// newDesignSessionTestScope seeds a single scope row directly -- LB1's FK
// target -- mirroring pointer_integration_test.go's newPointerTestScope.
func newDesignSessionTestScope(t *testing.T, ctx context.Context, db *dbtest.Postgres, repoFullName string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, repoFullName).Scan(&id))
	return id
}

// dsTestSubject builds a Subject for these tests -- kept local to this
// go_test target (each go_test target in this package compiles only its
// own srcs, so helpers are not shared across targets).
func dsTestSubject(sub string) store.Subject {
	return store.Subject{Iss: "https://issuer.example.com", Sub: sub, Kind: store.SubjectKindService}
}

// mintKrillSession opens a real krill_session row via SessionStore.
// InitSession -- FR1's opened_by_krill_session_id provenance column
// requires a genuine krill_session id, not a fabricated uuid.
func mintKrillSession(t *testing.T, ctx context.Context, db *dbtest.Postgres, scopeID uuid.UUID, subject store.Subject) store.SessionID {
	t.Helper()
	id, err := store.NewSessionStore(db.Pool).InitSession(ctx, scopeID, subject, subject, nil)
	require.NoError(t, err)
	return id
}

// TestDesignSessionStore_Open_WritesRowAndGetByIDReadsItBack is this
// issue's Testing case 1: Open writes a row and returns an id; GetByID
// reads it back with opening_submission and opened_by_krill_session_id
// intact.
func TestDesignSessionStore_Open_WritesRowAndGetByIDReadsItBack(t *testing.T) {
	ctx := context.Background()
	s, db := newDesignSessionTestStore(t)
	scopeID := newDesignSessionTestScope(t, ctx, db, "whale-net/design-session-open-test")
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record substrate")
	require.NoError(t, err)
	krillSessionID := mintKrillSession(t, ctx, db, scopeID, dsTestSubject("human-1"))

	const submission = "As a Requirement Contributor, I want to open a design session with a plain-language idea."
	ds, err := s.DesignSessions().Open(ctx, scopeID, product.ID, submission, krillSessionID)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, ds.ID)
	assert.Equal(t, scopeID, ds.ScopeID)
	assert.Equal(t, product.ID, ds.ProductID)
	assert.Equal(t, submission, ds.OpeningSubmission)
	assert.Equal(t, krillSessionID, ds.OpenedByKrillSessionID)

	got, err := s.DesignSessions().GetByID(ctx, ds.ID)
	require.NoError(t, err)
	assert.Equal(t, ds, got, "GetByID must read back exactly what Open wrote")

	list, err := s.DesignSessions().ListByProduct(ctx, product.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, ds.ID, list[0].ID)
}

// TestDesignSessionStore_Open_UnknownProduct_ReturnsErrNotFoundAndWritesNoRow
// is this issue's Testing case 2: Open against a nonexistent product_id
// fails with a named parent error and writes no row (LB2 parentage).
func TestDesignSessionStore_Open_UnknownProduct_ReturnsErrNotFoundAndWritesNoRow(t *testing.T) {
	ctx := context.Background()
	s, db := newDesignSessionTestStore(t)
	scopeID := newDesignSessionTestScope(t, ctx, db, "whale-net/design-session-orphan-test")
	krillSessionID := mintKrillSession(t, ctx, db, scopeID, dsTestSubject("human-1"))

	_, err := s.DesignSessions().Open(ctx, scopeID, uuid.New(), "an idea", krillSessionID)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM design_session`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected Open must insert no row")
}
