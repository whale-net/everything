//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and audience_score_system/store's
// store_integration_test.go for the pattern this mirrors: spin up a
// throwaway Postgres via dbtest, apply krill's own real embedded migrations
// (//krill/migrate/schema, not a hand-copied schema), then exercise this
// package's public API against it.
//
// This file holds the shared fixtures (newStore, newScope); per-entity
// Create/uniqueness/parentage coverage lives in the sibling
// *_integration_test.go files, following whagent_net/session's
// per-file-per-concern split.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:store_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newStore provisions an isolated Postgres database via dbtest, applies
// every migration in krill's own embedded schema (schema.Migrations --
// currently 001_scope plus 002_spec_entities, #2488), and returns a ready
// *store.Store plus the underlying dbtest.Postgres for tests that need to
// reach past the store's own API (e.g. to assert on row counts/columns
// directly, or to rewrite a `position` column the way a future
// insert-between-siblings caller would).
func newStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
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

// newScope inserts a fresh `scope` row directly (bypassing krill/migrate/seed
// -- this package's tests exercise the store, not the seeder) and returns
// its id. Every spec-axis Create call requires a scope_id (LB1), and a
// unique repo_full_name per call lets a single test create more than one
// scope to prove uniqueness constraints are scope-qualified.
func newScope(t *testing.T, ctx context.Context, db *dbtest.Postgres) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "scope-"+uuid.NewString()).Scan(&id))
	return id
}

// TestStore_CreateOneOfEach_ReturnsStableSurrogateIDAndScope walks the
// entire spec chain (Product -> FeatureSet -> Feature -> {FR, NFR}, and
// FeatureSet -> LoadBearingDecision, plus Product -> Persona and
// Product -> NonGoal) and proves every Create mints a non-nil surrogate id
// and writes the exact scope_id it was given (LB1/LB2), per issue #2488's
// Testing section.
func TestStore_CreateOneOfEach_ReturnsStableSurrogateIDAndScope(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)

	product, err := s.Products().Create(ctx, scopeID, "Krill", "Spec-of-record for agent swarms")
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, product.ID)
	require.Equal(t, scopeID, product.ScopeID)

	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", nil)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, featureSet.ID)
	require.Equal(t, scopeID, featureSet.ScopeID)

	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "SCD2 Store", nil)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, feature.ID)
	require.Equal(t, scopeID, feature.ScopeID)

	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "Create Product", nil)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, fr.ID)
	require.Equal(t, scopeID, fr.ScopeID)
	require.Equal(t, store.RequirementKindFR, fr.Kind)

	nfr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindNFR, "Store layer only", nil)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, nfr.ID)
	require.Equal(t, scopeID, nfr.ScopeID)
	require.Equal(t, store.RequirementKindNFR, nfr.Kind)

	decision, err := s.Decisions().Create(ctx, scopeID, featureSet.ID, "LB2 identity", nil)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, decision.ID)
	require.Equal(t, scopeID, decision.ScopeID)

	persona, err := s.Personas().Create(ctx, scopeID, product.ID, "Agent Swarm Operator", nil)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, persona.ID)
	require.Equal(t, scopeID, persona.ScopeID)

	nonGoal, err := s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindPermanent, "Multi-tenant scope", nil)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, nonGoal.ID)
	require.Equal(t, scopeID, nonGoal.ScopeID)
	require.Equal(t, store.NonGoalKindPermanent, nonGoal.Kind)

	// No two of these surrogate ids may collide.
	ids := map[uuid.UUID]string{
		product.ID:    "product",
		featureSet.ID: "feature_set",
		feature.ID:    "feature",
		fr.ID:         "fr",
		nfr.ID:        "nfr",
		decision.ID:   "decision",
		persona.ID:    "persona",
		nonGoal.ID:    "non_goal",
	}
	require.Len(t, ids, 8, "every Create above must mint a distinct surrogate id")
}

// TestStore_InsertBetweenSiblings_PreservesSiblingSurrogateIDs proves LB2's
// "position is not identity" contract: rewriting the sort order of a set of
// siblings (simulating an insert-between, which this task's Create-only
// store has no dedicated method for yet) changes no existing sibling's
// surrogate id, revision id, or resolvability by GetCurrentByID.
func TestStore_InsertBetweenSiblings_PreservesSiblingSurrogateIDs(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)

	product, err := s.Products().Create(ctx, scopeID, "Krill", "Spec-of-record")
	require.NoError(t, err)

	a, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "A", nil)
	require.NoError(t, err)
	b, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "B", nil)
	require.NoError(t, err)
	c, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "C", nil)
	require.NoError(t, err)

	// Simulate inserting a new sibling between A and B by renumbering
	// position directly (position is "freely rewritable", per
	// krill/ARCHITECTURE.md and migration 002's comment -- an ordinary
	// UPDATE, not an SCD2 supersession).
	_, err = db.Pool.Exec(ctx, `UPDATE feature_set SET position = 0 WHERE id = $1`, a.ID)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `UPDATE feature_set SET position = 20 WHERE id = $1`, b.ID)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `UPDATE feature_set SET position = 10 WHERE id = $1`, c.ID)
	require.NoError(t, err)

	gotA, err := s.FeatureSets().GetCurrentByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, gotA.ID)
	require.Equal(t, a.RevisionID, gotA.RevisionID, "rewriting a SIBLING's position must not touch this row's revision id")

	gotB, err := s.FeatureSets().GetCurrentByID(ctx, b.ID)
	require.NoError(t, err)
	require.Equal(t, b.ID, gotB.ID)
	require.Equal(t, b.RevisionID, gotB.RevisionID)

	gotC, err := s.FeatureSets().GetCurrentByID(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, c.ID, gotC.ID)
	require.Equal(t, c.RevisionID, gotC.RevisionID)

	ordered, err := s.FeatureSets().ListCurrentByProduct(ctx, product.ID)
	require.NoError(t, err)
	require.Len(t, ordered, 3)
	require.Equal(t, []uuid.UUID{a.ID, c.ID, b.ID}, []uuid.UUID{ordered[0].ID, ordered[1].ID, ordered[2].ID},
		"ListCurrentByProduct must reflect the rewritten position order (A, C, B)")
}
