//go:build integration

// This file only builds under the "integration" build tag -- see
// session_integration_test.go's doc comment for the full rationale. It
// covers issue #2548's (FR12, NFR3) ImportCompletionStore Testing bullets
// against a real, migrated Postgres (//krill/migrate/schema), not a
// hand-copied schema:
//   - MarkComplete writes a row; IsComplete returns it; a second
//     MarkComplete for the same (scope, product) errors
//     (store.ErrAlreadyComplete) and does not overwrite the first row's
//     CompletedAt or SourceRevision;
//   - IsComplete for a product that was never imported returns
//     (zero value, false, nil), not an error;
//   - MarkComplete against an unknown product_id errors
//     (store.ErrNotFound, LB2 parentage) and writes nothing.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:import_completion_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// testImportCompletionStore provisions an isolated, migrated Postgres
// database off krill's real embedded schema, plus a scope and a Product
// already created in it (import_completion always needs a real Product to
// key against, per LB2 parentage) -- mirrors entities_integration_test.go's
// testEntityStore helper of the same shape.
func testImportCompletionStore(t *testing.T) (*store.Store, uuid.UUID, uuid.UUID) {
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
	`, "whale-net/import-completion-test-"+uuid.NewString(), "main").Scan(&scopeID))

	entities := store.New(pool)
	product, err := entities.Products().Create(ctx, scopeID, "Test Product", "vision")
	require.NoError(t, err)

	return entities, scopeID, product.ID
}

// TestMarkComplete_ThenIsComplete_RoundTrips proves the write-then-read
// half of Testing item 1: MarkComplete writes a row carrying the caller's
// sourcePath/sourceRevision, and IsComplete resolves it back.
func TestMarkComplete_ThenIsComplete_RoundTrips(t *testing.T) {
	ctx := context.Background()
	entities, scopeID, productID := testImportCompletionStore(t)

	completion, err := entities.ImportCompletions().MarkComplete(ctx, scopeID, productID, "whagent_net/", "abc123")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, completion.ID)
	assert.Equal(t, scopeID, completion.ScopeID)
	assert.Equal(t, productID, completion.ProductID)
	assert.Equal(t, "whagent_net/", completion.SourcePath)
	assert.Equal(t, "abc123", completion.SourceRevision)
	assert.False(t, completion.CompletedAt.IsZero())

	got, ok, err := entities.ImportCompletions().IsComplete(ctx, scopeID, productID)
	require.NoError(t, err)
	require.True(t, ok, "expected IsComplete to report true after MarkComplete")
	assert.Equal(t, completion.ID, got.ID)
	assert.Equal(t, completion.SourcePath, got.SourcePath)
	assert.Equal(t, completion.SourceRevision, got.SourceRevision)
	assert.Equal(t, completion.CompletedAt, got.CompletedAt)
}

// TestMarkComplete_SecondCallSamePair_ErrorsWithoutOverwriting proves the
// second half of Testing item 1: a second MarkComplete for the same
// (scope, product) pair errors -- there is no un-complete verb, so this
// must never silently overwrite the first row's CompletedAt or
// SourceRevision (store.ImportCompletion's own doc comment).
func TestMarkComplete_SecondCallSamePair_ErrorsWithoutOverwriting(t *testing.T) {
	ctx := context.Background()
	entities, scopeID, productID := testImportCompletionStore(t)

	first, err := entities.ImportCompletions().MarkComplete(ctx, scopeID, productID, "whagent_net/", "abc123")
	require.NoError(t, err)

	_, err = entities.ImportCompletions().MarkComplete(ctx, scopeID, productID, "whagent_net/", "should-not-be-recorded")
	require.Error(t, err, "a second MarkComplete for the same (scope, product) must be rejected")
	assert.ErrorIs(t, err, store.ErrAlreadyComplete)

	got, ok, err := entities.ImportCompletions().IsComplete(ctx, scopeID, productID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, first.CompletedAt, got.CompletedAt, "a rejected second MarkComplete must not overwrite CompletedAt")
	assert.Equal(t, "abc123", got.SourceRevision, "a rejected second MarkComplete must not overwrite SourceRevision")

	completions, err := entities.ImportCompletions().ListByScope(ctx, scopeID)
	require.NoError(t, err)
	assert.Len(t, completions, 1, "a rejected second MarkComplete must not insert a second row")
}

// TestIsComplete_UnimportedProduct_ReturnsFalseNoError proves Testing item
// 2: IsComplete for a (scope, product) pair that was never marked complete
// returns the zero value and false, not an error -- "not yet imported" is
// not a failure mode.
func TestIsComplete_UnimportedProduct_ReturnsFalseNoError(t *testing.T) {
	ctx := context.Background()
	entities, scopeID, productID := testImportCompletionStore(t)

	got, ok, err := entities.ImportCompletions().IsComplete(ctx, scopeID, productID)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, store.ImportCompletion{}, got)
}

// TestMarkComplete_UnknownProductID_ErrorsAndWritesNothing proves Testing
// item 3: MarkComplete against a product_id with no current `product` row
// in scopeID is rejected (LB2 parentage, same enforcement as every other
// Create in this package) and leaves no row behind.
func TestMarkComplete_UnknownProductID_ErrorsAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	entities, scopeID, _ := testImportCompletionStore(t)

	unknownProductID := uuid.New()
	_, err := entities.ImportCompletions().MarkComplete(ctx, scopeID, unknownProductID, "whagent_net/", "abc123")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNotFound)

	completions, err := entities.ImportCompletions().ListByScope(ctx, scopeID)
	require.NoError(t, err)
	assert.Empty(t, completions, "a MarkComplete rejected for an unknown parent must write nothing")
}
