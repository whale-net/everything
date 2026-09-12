//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and krill/slice/query_integration_test.go
// for the pattern this file follows: spin up a throwaway Postgres via
// dbtest, apply krill's own real embedded migrations
// (//krill/migrate/schema), seed a full product graph -- FeatureSet,
// Features, Decisions, a Persona, both NonGoal kinds, and a milestone with
// associations -- with krill/store's real Create methods, then render it
// through render.NewStoreSource (store_source.go), the one file in this
// package that ever imports krill/store directly.
//
// This is issue #2495's Testing section's real-Postgres half:
//   - rendering a seeded product produces the four-file layout end to end
//     through StoreSource + slice.Querier + MilestoneStore, not just
//     against a hand-built fakeSource (render_test.go's job);
//   - FR15's read-only-role proof: Render succeeds against a Postgres
//     connection pool whose every session has had write privileges
//     revoked via default_transaction_read_only, empirically proving the
//     render path issues no writes -- not merely inspecting render.go's
//     import graph.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/render:render_integration_test --test_output=all
package render_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/render"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newTestStore mirrors krill/slice/query_integration_test.go's helper of
// the same name: an isolated, migrated Postgres database seeded with
// krill's own real embedded schema, plus the *pgxpool.Pool underneath it
// for the one raw INSERT (scope) that has no store method of its own.
func newTestStore(t *testing.T) (*store.Store, *pgxpool.Pool, string) {
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

	return store.New(pool), pool, db.ConnString
}

func createScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoFullName string) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, repoFullName, "main").Scan(&scopeID))
	return scopeID
}

func strPtr2(s string) *string { return &s }

// seededProduct is a full product graph seeded via real store.Store Create
// calls: one FeatureSet with two Features, two LoadBearingDecisions, one
// Persona, one NonGoal of each kind, and a milestone (M1) that delivers the
// first Feature and must-not-foreclose the first Decision -- enough to
// exercise every renderer section against real rows.
type seededProduct struct {
	ScopeID   uuid.UUID
	ProductID uuid.UUID
}

func seedProduct(t *testing.T, ctx context.Context, entities *store.Store, scopeID uuid.UUID) seededProduct {
	t.Helper()

	product, err := entities.Products().Create(ctx, scopeID, "Widgets", "Make great widgets.")
	require.NoError(t, err)

	featureSet, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "Core", nil)
	require.NoError(t, err)

	f1, err := entities.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	_, err = entities.Features().Create(ctx, scopeID, featureSet.ID, "F2", nil)
	require.NoError(t, err)

	lb1, err := entities.Decisions().Create(ctx, scopeID, featureSet.ID, "Keep it simple", nil)
	require.NoError(t, err)
	_, err = entities.Decisions().Create(ctx, scopeID, featureSet.ID, "Ship fast", strPtr2("velocity over polish"))
	require.NoError(t, err)

	_, err = entities.Personas().Create(ctx, scopeID, product.ID, "Operator", strPtr2("runs the fleet"))
	require.NoError(t, err)

	_, err = entities.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindPermanent, "Rendering other domains' docs", nil)
	require.NoError(t, err)
	_, err = entities.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindDeferred, "Multi-tenant scopes", strPtr2("later, not now"))
	require.NoError(t, err)

	milestone, err := entities.Milestones().GetOrCreateRef(ctx, scopeID, product.ID, "M1")
	require.NoError(t, err)
	require.NoError(t, entities.Milestones().AddAssociation(ctx, scopeID, f1.ID, milestone.ID))
	require.NoError(t, entities.Milestones().AddAssociation(ctx, scopeID, lb1.ID, milestone.ID))

	return seededProduct{ScopeID: scopeID, ProductID: product.ID}
}

// TestRender_SeededProduct_ProducesFourFileLayout is #2495's "rendering a
// seeded product produces exactly the four files in the specified layout"
// case, exercised through the real store path (render.NewStoreSource),
// including the milestone's Delivers/Must-not-foreclose reconstruction
// from real entity_milestone rows.
func TestRender_SeededProduct_ProducesFourFileLayout(t *testing.T) {
	ctx := context.Background()
	entities, pool, _ := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/render-fr13-test")
	seeded := seedProduct(t, ctx, entities, scopeID)

	files, err := render.Render(ctx, render.NewStoreSource(entities), seeded.ScopeID, seeded.ProductID)
	require.NoError(t, err)

	assert.Contains(t, files.ProductMD, "# Widgets — Product brief")
	assert.Contains(t, files.ProductMD, "## Vision\n\nMake great widgets.")
	assert.Contains(t, files.ProductMD, "- **Operator** — runs the fleet")
	assert.Contains(t, files.ProductMD, "LB1 — Keep it simple")
	assert.Contains(t, files.ProductMD, "LB2 — Ship fast")
	assert.Contains(t, files.ProductMD, "- **Rendering other domains' docs.**")
	assert.Contains(t, files.ProductMD, "- **Multi-tenant scopes.** later, not now")

	assert.Contains(t, files.CapabilityMapMD, "## Core")
	assert.Contains(t, files.CapabilityMapMD, "- **C1** — F1")
	assert.Contains(t, files.CapabilityMapMD, "- **C2** — F2")

	assert.Contains(t, files.RoadmapMD, "### M1")
	assert.Contains(t, files.RoadmapMD, "Delivers: C1")
	assert.Contains(t, files.RoadmapMD, "Must not foreclose: LB1")

	for name, content := range files.FileMap() {
		assert.Contains(t, content, render.GeneratedMarker, "file %s must carry the non-hand-editable marker", name)
		assert.Contains(t, content, `Product "Widgets"`, "file %s must name the product it was rendered from", name)
	}
}

// TestRender_ReadOnlyDatabaseHandle_Succeeds is FR15's proof: Render
// succeeds against a Postgres connection pool whose every session has had
// default_transaction_read_only forced on -- i.e. any write the render
// path attempted would itself fail at the database, not merely "happen not
// to be called" this run. The second assertion (attempting a real INSERT
// through the same pool) is what makes this an empirical proof rather than
// a trivial one: it confirms the harness genuinely blocks writes, so
// Render's success above cannot be explained by AfterConnect silently
// failing to take effect.
func TestRender_ReadOnlyDatabaseHandle_Succeeds(t *testing.T) {
	ctx := context.Background()
	entities, pool, connString := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/render-fr15-test")
	seeded := seedProduct(t, ctx, entities, scopeID)

	roCfg, err := pgxpool.ParseConfig(connString)
	require.NoError(t, err)
	roCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET default_transaction_read_only = on")
		return err
	}
	roPool, err := pgxpool.NewWithConfig(ctx, roCfg)
	require.NoError(t, err)
	t.Cleanup(roPool.Close)

	// Prove the harness itself: this pool must reject a write. If this
	// assertion ever went green on a pool that in fact still permitted
	// writes, the success assertion below would prove nothing.
	_, writeErr := roPool.Exec(ctx, `INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2)`, "whale-net/should-never-be-written", "main")
	require.Error(t, writeErr, "the read-only pool must reject a write")
	assert.Contains(t, writeErr.Error(), "read-only transaction")

	roStore := store.New(roPool)
	files, err := render.Render(ctx, render.NewStoreSource(roStore), seeded.ScopeID, seeded.ProductID)
	require.NoError(t, err, "Render must succeed reading through a connection that cannot write")
	assert.Contains(t, files.ProductMD, "# Widgets — Product brief")
}
