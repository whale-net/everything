//go:build integration

// This file only builds under the "integration" build tag -- see
// session_integration_test.go's doc comment for the full rationale. It
// covers issue #2490's (FR1/FR2/FR4) store-layer Testing bullets against a
// real, migrated Postgres (//krill/migrate/schema), not a hand-copied
// schema:
//   - create Product -> FeatureSet -> Feature -> FR -> NFR chain end to
//     end; every entity carries a surrogate id and the caller's scope_id;
//   - a child create under a nonexistent parent is rejected (ErrNotFound);
//   - a child create under a parent that belongs to a DIFFERENT scope is
//     rejected -- LB1's "scope_id ... never client-supplied as an
//     unchecked value" would otherwise let one scope's write silently
//     attach under another scope's entity;
//   - attaching a Load-Bearing Decision to a FeatureSet, then reading that
//     FeatureSet's decisions back, returns the decision;
//   - scope-qualified uniqueness (a duplicate name in the same scope)
//     surfaces as a Postgres unique-constraint violation (SQLSTATE 23505),
//     the error shape api/handlers/types.go's isUniqueViolation maps to a
//     409, never a bare unwrapped failure.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:entities_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// testEntityStore provisions an isolated, migrated Postgres database off
// krill's real embedded schema and returns a ready *store.Store plus a
// helper to mint additional `scope` rows (newScope) -- this file's
// cross-scope coverage needs more than one.
func testEntityStore(t *testing.T) (*store.Store, *pgxpool.Pool, func(t *testing.T) uuid.UUID) {
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

	newScope := func(t *testing.T) uuid.UUID {
		t.Helper()
		var scopeID uuid.UUID
		require.NoError(t, pool.QueryRow(ctx, `
			INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
		`, "whale-net/entities-test-"+uuid.NewString(), "main").Scan(&scopeID))
		return scopeID
	}

	return store.New(pool), pool, newScope
}

// TestCreateChain_ProductToNFR_CarriesSurrogateIDAndScope proves FR1/FR2's
// full create chain: Product -> FeatureSet -> Feature -> FR -> NFR, with
// every entity minting its own surrogate id (LB2) and every row carrying
// the caller's scope_id (LB1).
func TestCreateChain_ProductToNFR_CarriesSurrogateIDAndScope(t *testing.T) {
	ctx := context.Background()
	entities, _, newScope := testEntityStore(t)
	scopeID := newScope(t)

	product, err := entities.Products().Create(ctx, scopeID, "Krill", "A spec-of-record substrate")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, product.ID)
	assert.Equal(t, scopeID, product.ScopeID)

	featureSet, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "M1", nil)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, featureSet.ID)
	assert.Equal(t, scopeID, featureSet.ScopeID)
	assert.Equal(t, product.ID, featureSet.ProductID)

	feature, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "Entity write API", nil)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, feature.ID)
	assert.Equal(t, scopeID, feature.ScopeID)
	assert.Equal(t, featureSet.ID, feature.FeatureSetID)

	fr, err := entities.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "Create a Product", nil)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, fr.ID)
	assert.Equal(t, scopeID, fr.ScopeID)
	assert.Equal(t, feature.ID, fr.FeatureID)
	assert.Equal(t, store.RequirementKindFR, fr.Kind)

	nfr, err := entities.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindNFR, "Writes complete under 500ms", nil)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, nfr.ID)
	assert.Equal(t, scopeID, nfr.ScopeID)
	assert.Equal(t, feature.ID, nfr.FeatureID)
	assert.Equal(t, store.RequirementKindNFR, nfr.Kind)

	assert.NotEqual(t, fr.ID, nfr.ID, "an FR and an NFR under the same Feature must mint distinct surrogate ids")
}

// TestCreateFeatureSet_NonexistentProduct_ReturnsErrNotFound proves LB2
// parentage: a child create referencing a Product id that never existed is
// rejected, not silently inserted.
func TestCreateFeatureSet_NonexistentProduct_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	entities, _, newScope := testEntityStore(t)
	scopeID := newScope(t)

	_, err := entities.FeatureSets().Create(ctx, scopeID, uuid.New(), "Orphaned", nil)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// TestCreateFeatureSet_CrossScopeProduct_IsRejected proves the other half
// of this task's parentage acceptance criterion: a child create referencing
// a real, currently-valid parent that belongs to a DIFFERENT scope must
// still be rejected. LB1 requires scope_id to never be an unchecked value --
// if a parent's own scope isn't checked against the caller's scope, one
// scope's session could silently graft a child onto another scope's entity
// tree.
func TestCreateFeatureSet_CrossScopeProduct_IsRejected(t *testing.T) {
	ctx := context.Background()
	entities, _, newScope := testEntityStore(t)

	ownerScope := newScope(t)
	otherScope := newScope(t)

	product, err := entities.Products().Create(ctx, ownerScope, "Owner's Product", "vision")
	require.NoError(t, err)

	_, err = entities.FeatureSets().Create(ctx, otherScope, product.ID, "Cross-scope FeatureSet", nil)
	assert.ErrorIs(t, err, store.ErrNotFound, "a parent belonging to a different scope must be rejected exactly like a nonexistent parent")
}

// TestAttachLoadBearingDecision_ReadableBackFromFeatureSet proves FR4: a
// Load-Bearing Decision attached to a FeatureSet is readable back from that
// FeatureSet (never a global list, per C2).
func TestAttachLoadBearingDecision_ReadableBackFromFeatureSet(t *testing.T) {
	ctx := context.Background()
	entities, _, newScope := testEntityStore(t)
	scopeID := newScope(t)

	product, err := entities.Products().Create(ctx, scopeID, "Krill", "vision")
	require.NoError(t, err)
	featureSet, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "M1", nil)
	require.NoError(t, err)

	decision, err := entities.Decisions().Create(ctx, scopeID, featureSet.ID, "LB1", strPtr("scope indirection"))
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, decision.ID)
	assert.Equal(t, featureSet.ID, decision.FeatureSetID)

	decisions, err := entities.Decisions().ListCurrentByFeatureSet(ctx, featureSet.ID)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.Equal(t, decision.ID, decisions[0].ID)
	assert.Equal(t, "LB1", decisions[0].Name)
}

// TestCreateProduct_DuplicateNameInScope_IsUniqueViolation proves
// scope-qualified uniqueness (LB1) surfaces as a genuine Postgres
// unique-constraint violation (SQLSTATE 23505) -- the shape
// api/handlers/types.go's isUniqueViolation maps onto a 409, not a bare
// unwrapped failure a caller can't distinguish from a 500-worthy bug.
func TestCreateProduct_DuplicateNameInScope_IsUniqueViolation(t *testing.T) {
	ctx := context.Background()
	entities, _, newScope := testEntityStore(t)
	scopeID := newScope(t)

	_, err := entities.Products().Create(ctx, scopeID, "Krill", "vision")
	require.NoError(t, err)

	_, err = entities.Products().Create(ctx, scopeID, "Krill", "a different vision")
	require.Error(t, err)

	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "expected a *pgconn.PgError in the error chain, got %v", err)
	assert.Equal(t, "23505", pgErr.Code, "a scope-qualified duplicate name must be a unique-constraint violation")
}

// strPtr mirrors session_integration_test.go's helper of the same name --
// each is its own go_test target (separate srcs, BUILD.bazel), so each
// compiles as an independent package and needs its own copy.
func strPtr(s string) *string { return &s }
