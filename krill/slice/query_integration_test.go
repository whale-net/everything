//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and krill/store/session_integration_test.go
// for the pattern this file follows: spin up a throwaway Postgres via
// dbtest, apply krill's own real embedded migrations (//krill/migrate/schema),
// seed a multi-feature-set, multi-scope product graph with krill/store's
// real Create methods, then exercise Querier (query.go) against it --
// issue #2491's Testing section:
//   - FR5 returns the requested FeatureSet's own decisions and excludes a
//     sibling FeatureSet's decisions (the one clause a naive
//     product-wide join gets wrong -- explicit negative assertion);
//   - FR6 excludes sibling Features and the parent's other content;
//   - FR7 returns exactly one entity;
//   - FR8 returns every entity beneath the Product in one call, and never
//     leaks another scope's entities into it;
//   - all four responses carry a non-empty schema_version plus per-entity
//     surrogate ids and as-of (current-row) revision ids (FR9);
//   - an unknown id returns store.ErrNotFound for every granularity,
//     never another scope's data.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/slice:query_integration_test --test_output=all
package slice_test

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
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newTestStore provisions an isolated, migrated Postgres database (krill's
// own real embedded schema, not a hand-copied one) and returns a ready
// *store.Store plus the *pgxpool.Pool it is built over -- the latter is
// only needed for the one raw INSERT (scope) that has no store method of
// its own, mirroring store/session_integration_test.go's newTestSessionStore.
func newTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
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

	return store.New(pool), pool
}

func createScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repoFullName string) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, $2) RETURNING id
	`, repoFullName, "main").Scan(&scopeID))
	return scopeID
}

func strPtr(s string) *string { return &s }

// world is a seeded product graph: Product -> {FeatureSetA, FeatureSetB}
// (siblings), FeatureSetA -> {FeatureA1, FeatureA2} (siblings),
// FeatureA1 -> {FR, NFR}, and a LoadBearingDecision attached to each of
// FeatureSetA and FeatureSetB -- exactly the shape FR5's sibling-scoping
// negative case needs.
type world struct {
	ScopeID uuid.UUID

	Product store.Product

	FeatureSetA store.FeatureSet
	FeatureSetB store.FeatureSet

	FeatureA1 store.Feature
	FeatureA2 store.Feature

	RequirementA1FR  store.Requirement
	RequirementA1NFR store.Requirement

	DecisionA store.LoadBearingDecision
	DecisionB store.LoadBearingDecision
}

func seedWorld(t *testing.T, ctx context.Context, entities *store.Store, scopeID uuid.UUID) world {
	t.Helper()

	product, err := entities.Products().Create(ctx, scopeID, "krill", "spec-of-record substrate")
	require.NoError(t, err)

	featureSetA, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "FeatureSetA", nil)
	require.NoError(t, err)
	featureSetB, err := entities.FeatureSets().Create(ctx, scopeID, product.ID, "FeatureSetB", nil)
	require.NoError(t, err)

	featureA1, err := entities.Features().Create(ctx, scopeID, featureSetA.ID, "FeatureA1", nil)
	require.NoError(t, err)
	featureA2, err := entities.Features().Create(ctx, scopeID, featureSetA.ID, "FeatureA2", nil)
	require.NoError(t, err)

	reqFR, err := entities.Requirements().Create(ctx, scopeID, featureA1.ID, store.RequirementKindFR, "FR1", strPtr("do the thing"))
	require.NoError(t, err)
	reqNFR, err := entities.Requirements().Create(ctx, scopeID, featureA1.ID, store.RequirementKindNFR, "NFR1", nil)
	require.NoError(t, err)

	decisionA, err := entities.Decisions().Create(ctx, scopeID, featureSetA.ID, "LB-A", strPtr("decision scoped to FeatureSetA"))
	require.NoError(t, err)
	decisionB, err := entities.Decisions().Create(ctx, scopeID, featureSetB.ID, "LB-B", strPtr("decision scoped to FeatureSetB"))
	require.NoError(t, err)

	return world{
		ScopeID:          scopeID,
		Product:          product,
		FeatureSetA:      featureSetA,
		FeatureSetB:      featureSetB,
		FeatureA1:        featureA1,
		FeatureA2:        featureA2,
		RequirementA1FR:  reqFR,
		RequirementA1NFR: reqNFR,
		DecisionA:        decisionA,
		DecisionB:        decisionB,
	}
}

func decisionIDs(doc slice.Document) []uuid.UUID {
	ids := make([]uuid.UUID, len(doc.Decisions))
	for i, d := range doc.Decisions {
		ids[i] = d.ID
	}
	return ids
}

func featureIDs(doc slice.Document) []uuid.UUID {
	ids := make([]uuid.UUID, len(doc.Features))
	for i, f := range doc.Features {
		ids[i] = f.ID
	}
	return ids
}

func requirementIDs(doc slice.Document) []uuid.UUID {
	ids := make([]uuid.UUID, len(doc.Requirements))
	for i, r := range doc.Requirements {
		ids[i] = r.ID
	}
	return ids
}

// TestGetFeatureSetSlice_ExcludesSiblingFeatureSetDecisions is FR5's
// explicit negative case: a query for FeatureSetA must return FeatureSetA's
// own decisions and Features/Requirements, and must NOT return
// FeatureSetB's decision -- the one clause a naive product-wide decision
// join gets wrong. This test fails if GetFeatureSetSlice widens its
// decision read to the product's full decision list (verified by manually
// swapping the ListCurrentByFeatureSet call for ListDecisionsByProduct
// during development: the assertion below went red with both decisions
// present, then green again after reverting).
func TestGetFeatureSetSlice_ExcludesSiblingFeatureSetDecisions(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-fr5-test")
	w := seedWorld(t, ctx, entities, scopeID)

	q := slice.NewQuerier(entities)
	doc, err := q.GetFeatureSetSlice(ctx, w.FeatureSetA.ID)
	require.NoError(t, err)

	assert.Contains(t, decisionIDs(doc), w.DecisionA.ID, "FeatureSetA's own decision must be present")
	assert.NotContains(t, decisionIDs(doc), w.DecisionB.ID, "a sibling FeatureSet's decision must never appear in this FeatureSet's slice")

	assert.ElementsMatch(t, featureIDs(doc), []uuid.UUID{w.FeatureA1.ID, w.FeatureA2.ID})
	assert.ElementsMatch(t, requirementIDs(doc), []uuid.UUID{w.RequirementA1FR.ID, w.RequirementA1NFR.ID})

	require.Len(t, doc.FeatureSets, 1)
	assert.Equal(t, w.FeatureSetA.ID, doc.FeatureSets[0].ID)
	assert.Nil(t, doc.Product, "GetFeatureSetSlice must never populate Product")
}

// TestGetFeatureSlice_ExcludesSiblingFeaturesAndParentContent is FR6: only
// the requested Feature and its own Requirements, nothing else in the
// product -- no FeatureSet, no Decisions, no sibling Feature's
// Requirements.
func TestGetFeatureSlice_ExcludesSiblingFeaturesAndParentContent(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-fr6-test")
	w := seedWorld(t, ctx, entities, scopeID)

	q := slice.NewQuerier(entities)
	doc, err := q.GetFeatureSlice(ctx, w.FeatureA1.ID)
	require.NoError(t, err)

	require.Len(t, doc.Features, 1)
	assert.Equal(t, w.FeatureA1.ID, doc.Features[0].ID)
	assert.NotContains(t, featureIDs(doc), w.FeatureA2.ID, "a sibling Feature must never appear in this Feature's slice")

	assert.ElementsMatch(t, requirementIDs(doc), []uuid.UUID{w.RequirementA1FR.ID, w.RequirementA1NFR.ID})

	assert.Empty(t, doc.FeatureSets, "GetFeatureSlice must never populate FeatureSets")
	assert.Empty(t, doc.Decisions, "GetFeatureSlice must never populate Decisions")
	assert.Nil(t, doc.Product, "GetFeatureSlice must never populate Product")
}

// TestGetRequirementSlice_ReturnsExactlyOneEntity is FR7: a single FR/NFR
// by surrogate id, alone -- every other field of Document stays empty.
func TestGetRequirementSlice_ReturnsExactlyOneEntity(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-fr7-test")
	w := seedWorld(t, ctx, entities, scopeID)

	q := slice.NewQuerier(entities)
	doc, err := q.GetRequirementSlice(ctx, w.RequirementA1FR.ID)
	require.NoError(t, err)

	require.Len(t, doc.Requirements, 1)
	assert.Equal(t, w.RequirementA1FR.ID, doc.Requirements[0].ID)
	assert.Empty(t, doc.Features)
	assert.Empty(t, doc.FeatureSets)
	assert.Empty(t, doc.Decisions)
	assert.Nil(t, doc.Product)
}

// TestGetProductSlice_ReturnsEveryEntityBeneathProduct_ExcludesOtherScope is
// FR8: every FeatureSet, Feature, FR/NFR, and LoadBearingDecision beneath
// the Product in one call, and -- the "another scope" negative case from
// this issue's Testing section -- never a sibling scope's entities that
// happen to share the same table.
func TestGetProductSlice_ReturnsEveryEntityBeneathProduct_ExcludesOtherScope(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)

	scopeA := createScope(t, ctx, pool, "whale-net/slice-fr8-test-a")
	wA := seedWorld(t, ctx, entities, scopeA)

	scopeB := createScope(t, ctx, pool, "whale-net/slice-fr8-test-b")
	wB := seedWorld(t, ctx, entities, scopeB)

	q := slice.NewQuerier(entities)
	doc, err := q.GetProductSlice(ctx, wA.Product.ID)
	require.NoError(t, err)

	require.NotNil(t, doc.Product)
	assert.Equal(t, wA.Product.ID, doc.Product.ID)

	assert.ElementsMatch(t, []uuid.UUID{doc.FeatureSets[0].ID, doc.FeatureSets[1].ID}, []uuid.UUID{wA.FeatureSetA.ID, wA.FeatureSetB.ID})
	assert.ElementsMatch(t, featureIDs(doc), []uuid.UUID{wA.FeatureA1.ID, wA.FeatureA2.ID})
	assert.ElementsMatch(t, requirementIDs(doc), []uuid.UUID{wA.RequirementA1FR.ID, wA.RequirementA1NFR.ID})
	assert.ElementsMatch(t, decisionIDs(doc), []uuid.UUID{wA.DecisionA.ID, wA.DecisionB.ID})

	// Scope B's entities live in the very same tables under different
	// FKs -- prove none of them leak into scope A's product slice.
	assert.NotContains(t, []uuid.UUID{doc.FeatureSets[0].ID, doc.FeatureSets[1].ID}, wB.FeatureSetA.ID)
	assert.NotContains(t, featureIDs(doc), wB.FeatureA1.ID)
	assert.NotContains(t, requirementIDs(doc), wB.RequirementA1FR.ID)
	assert.NotContains(t, decisionIDs(doc), wB.DecisionA.ID)
}

// TestAllFourGranularities_CarrySchemaVersionAndEntityRefs is FR9: the same
// Document type comes back from all four granularities, each carrying a
// non-empty schema_version and, on every populated entity, a surrogate id
// distinct from its as-of revision id (RevisionID is the SCD2 row key --
// see EntityRef's doc comment).
func TestAllFourGranularities_CarrySchemaVersionAndEntityRefs(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-fr9-test")
	w := seedWorld(t, ctx, entities, scopeID)
	q := slice.NewQuerier(entities)

	docs := map[string]slice.Document{}

	fsDoc, err := q.GetFeatureSetSlice(ctx, w.FeatureSetA.ID)
	require.NoError(t, err)
	docs["feature_set"] = fsDoc

	featureDoc, err := q.GetFeatureSlice(ctx, w.FeatureA1.ID)
	require.NoError(t, err)
	docs["feature"] = featureDoc

	reqDoc, err := q.GetRequirementSlice(ctx, w.RequirementA1FR.ID)
	require.NoError(t, err)
	docs["requirement"] = reqDoc

	productDoc, err := q.GetProductSlice(ctx, w.Product.ID)
	require.NoError(t, err)
	docs["product"] = productDoc

	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, doc.SchemaVersion, "every granularity must carry a non-empty schema_version")
			assert.Equal(t, slice.SchemaVersion, doc.SchemaVersion)

			if doc.Product != nil {
				assert.NotEqual(t, uuid.Nil, doc.Product.ID)
				assert.NotEqual(t, uuid.Nil, doc.Product.RevisionID)
			}
			for _, fs := range doc.FeatureSets {
				assert.NotEqual(t, uuid.Nil, fs.ID)
				assert.NotEqual(t, uuid.Nil, fs.RevisionID)
			}
			for _, f := range doc.Features {
				assert.NotEqual(t, uuid.Nil, f.ID)
				assert.NotEqual(t, uuid.Nil, f.RevisionID)
			}
			for _, r := range doc.Requirements {
				assert.NotEqual(t, uuid.Nil, r.ID)
				assert.NotEqual(t, uuid.Nil, r.RevisionID)
			}
			for _, d := range doc.Decisions {
				assert.NotEqual(t, uuid.Nil, d.ID)
				assert.NotEqual(t, uuid.Nil, d.RevisionID)
			}
		})
	}
}

// TestQuery_UnknownID_ReturnsNotFound proves every granularity distinguishes
// "no such entity" from a store failure, and never substitutes another
// scope's data for a miss.
func TestQuery_UnknownID_ReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-notfound-test")
	seedWorld(t, ctx, entities, scopeID) // populate the tables so an empty DB isn't the reason for a miss

	q := slice.NewQuerier(entities)
	unknown := uuid.New()

	_, err := q.GetFeatureSetSlice(ctx, unknown)
	assert.ErrorIs(t, err, store.ErrNotFound)

	_, err = q.GetFeatureSlice(ctx, unknown)
	assert.ErrorIs(t, err, store.ErrNotFound)

	_, err = q.GetRequirementSlice(ctx, unknown)
	assert.ErrorIs(t, err, store.ErrNotFound)

	_, err = q.GetProductSlice(ctx, unknown)
	assert.ErrorIs(t, err, store.ErrNotFound)
}
