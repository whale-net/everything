//go:build integration

// Real-Postgres coverage for MilestoneStore (milestone.go, migration 004,
// issue #2492; kind-filtering and the entity_milestone.relation column
// added by migration 010, issue #2683) -- the bare importer-facing
// milestone_ref/entity_milestone surface, as opposed to
// milestone_authoring_integration_test.go's MilestoneAuthoringStore
// coverage. See store_integration_test.go's package doc for why this file
// only builds under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:milestone_integration_test --test_output=all
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

func newMilestoneTestStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
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

func newMilestoneTestScope(t *testing.T, ctx context.Context, db *dbtest.Postgres) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "scope-milestone-"+uuid.NewString()).Scan(&id))
	return id
}

// TestMilestoneStore_GetOrCreateRef_Idempotent proves the importer's own
// idempotency contract at the store layer directly (mirroring
// krill/importer's end-to-end coverage): two calls with the same (scope,
// product, name) resolve to the same row, never a duplicate, and the
// resulting row defaults to kind="milestone" with no authoring fields set.
func TestMilestoneStore_GetOrCreateRef_Idempotent(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneTestStore(t)
	scopeID := newMilestoneTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	first, err := s.Milestones().GetOrCreateRef(ctx, scopeID, product.ID, "M1")
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneKindMilestone, first.Kind, "an importer-created row must default to kind=\"milestone\"")
	assert.Nil(t, first.Outcome)
	assert.Nil(t, first.FRBudget)
	assert.Nil(t, first.CreatedByActing, "GetOrCreateRef has no session to attribute to -- both subjects must be nil")
	assert.Nil(t, first.CreatedByOnBehalfOf)

	second, err := s.Milestones().GetOrCreateRef(ctx, scopeID, product.ID, "M1")
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID, "a second GetOrCreateRef call for the same (scope, product, name) must resolve the same row")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_ref WHERE scope_id = $1 AND product_id = $2 AND name = 'M1'`, scopeID, product.ID).Scan(&count))
	assert.Equal(t, 1, count, "two GetOrCreateRef calls for the same identifier must never insert a duplicate row")
}

// TestMilestoneStore_AddAssociation_IdempotentAndONCONFLICTRegression is
// this task's explicit regression test for the AddAssociation ON CONFLICT
// fix (issue #2683's Implementation phase): migration 010 widened
// entity_milestone's unique index from (entity_id, milestone_id) to
// (entity_id, milestone_id, relation), which made AddAssociation's old
// `ON CONFLICT (entity_id, milestone_id)` target reference a
// no-longer-unique column pair -- Postgres rejects that at the SQL layer
// with "no unique or exclusion constraint matching the ON CONFLICT
// specification" on the very first re-insert, not merely on a duplicate
// key. Re-running AddAssociation for the same (entityID, milestoneID) pair
// a second time is exactly the idempotency contract the importer relies
// on (re-importing a document must not duplicate an association) and
// exactly the call shape that surfaces the stale-ON-CONFLICT-target bug if
// it regresses.
//
// Verified red/green during development: reverting
// MilestoneStore.AddAssociation's ON CONFLICT target back to
// `(entity_id, milestone_id)` makes this test fail with exactly that
// Postgres error on the second call; restoring the current
// `(entity_id, milestone_id, relation)` target makes it pass again.
func TestMilestoneStore_AddAssociation_IdempotentAndONCONFLICTRegression(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneTestStore(t)
	scopeID := newMilestoneTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F", nil)
	require.NoError(t, err)
	ref, err := s.Milestones().GetOrCreateRef(ctx, scopeID, product.ID, "M1")
	require.NoError(t, err)

	require.NoError(t, s.Milestones().AddAssociation(ctx, scopeID, feature.ID, ref.ID), "first AddAssociation must succeed")

	err = s.Milestones().AddAssociation(ctx, scopeID, feature.ID, ref.ID)
	require.NoError(t, err, "a second AddAssociation for the same (entity, milestone) pair must be a idempotent no-op, not a Postgres ON CONFLICT error")

	associations, err := s.Milestones().ListAssociationsByMilestone(ctx, ref.ID)
	require.NoError(t, err)
	require.Len(t, associations, 1, "the duplicate AddAssociation call must not have inserted a second row")
	assert.Equal(t, store.MilestoneRelationDelivers, associations[0].Relation, "the importer's AddAssociation always writes a Delivers row")
}

// TestMilestoneStore_ListRefsByProduct_FiltersToMilestoneKind proves
// migration 010's kind-aware expand-contract step (issue #2683's
// Implementation section), now with kind="milepebble" rows actually
// present for the first time (migration 011, issue #2684's "Kind-awareness
// follow-through" note): two real milestones and two real milepebbles cut
// from one of them all exist in the same product, and
// ListRefsByProduct returns only the two milestones -- never a milepebble,
// even though both share the same product_id and (for one pair) a
// milestone/milepebble could otherwise collide on name.
func TestMilestoneStore_ListRefsByProduct_FiltersToMilestoneKind(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneTestStore(t)
	scopeID := newMilestoneTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	m1, err := s.Milestones().GetOrCreateRef(ctx, scopeID, product.ID, "M1")
	require.NoError(t, err)
	_, err = s.Milestones().GetOrCreateRef(ctx, scopeID, product.ID, "M2")
	require.NoError(t, err)

	self := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-1", Kind: store.SubjectKindService}
	_, err = s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, m1.ID, "cut 1", "", nil, self, self)
	require.NoError(t, err)
	_, err = s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, m1.ID, "cut 2", "", nil, self, self)
	require.NoError(t, err)

	refs, err := s.Milestones().ListRefsByProduct(ctx, scopeID, product.ID)
	require.NoError(t, err)
	require.Len(t, refs, 2, "ListRefsByProduct must return exactly the two milestones, never either milepebble")
	for _, ref := range refs {
		assert.Equal(t, store.MilestoneKindMilestone, ref.Kind, "ListRefsByProduct must only ever return kind=\"milestone\" rows")
	}
}
