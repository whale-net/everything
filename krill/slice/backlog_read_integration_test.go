//go:build integration

// This file is FR5's backlog-bucket read coverage (issue #2687's
// //krill/slice half): //krill/slice.Querier.GetBacklog returns the
// bucket's own milestone_ref id alongside its typed entities, so a
// producer holding no session can hand that id to move_delivery_scope as
// `to`. The cases that matter are the ones a caller is actually in the
// first time they retract anything -- a product whose bucket has never
// been created, and one whose bucket exists but is empty -- plus the
// attribution the read writes when it creates the row (LB4: a permanent
// append-only row must never name a creator that does not exist, and an
// ungated read has no caller identity to name). Reuses
// query_integration_test.go's newTestStore/createScope/seedWorld harness
// rather than duplicating it -- both files build under package slice_test
// with the "integration" gotag, so they compile as one test binary.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/slice:backlog_read_integration_test --test_output=all
package slice_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// backlogRowCount counts the current `kind='backlog'` milestone_ref rows
// for one product -- the "exactly one row, no matter how many reads"
// half of the contract, which a returned id alone cannot prove.
func backlogRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scopeID, productID uuid.UUID) int {
	t.Helper()
	var count int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM milestone_ref
		WHERE scope_id = $1 AND product_id = $2 AND kind = $3 AND valid_to IS NULL
	`, scopeID, productID, string(store.MilestoneKindBacklog)).Scan(&count))
	return count
}

// backlogRowSubject reads the six created_by_* columns off a bucket row,
// so a test can assert what the read attributed the row to rather than
// only that an id came back.
func backlogRowSubject(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (acting, onBehalfOf store.Subject) {
	t.Helper()
	var actingIss, actingSub, actingKind string
	var onBehalfIss, onBehalfSub, onBehalfKind string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
		       created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		FROM milestone_ref WHERE id = $1
	`, id).Scan(&actingIss, &actingSub, &actingKind, &onBehalfIss, &onBehalfSub, &onBehalfKind))
	return store.Subject{Iss: actingIss, Sub: actingSub, Kind: store.SubjectKind(actingKind)},
		store.Subject{Iss: onBehalfIss, Sub: onBehalfSub, Kind: store.SubjectKind(onBehalfKind)}
}

// TestGetBacklog_FreshProduct_NamesBucketOnce is the case the read exists
// for: a product that has never had a bucket created, read for the first
// time. It must return a nameable id, create exactly one row, attribute
// that row to the one shared krill service subject in both LB4 slots (not
// to zeros, and not to the first reader that happened to arrive), and
// resolve the same id with no second row on a second read.
func TestGetBacklog_FreshProduct_NamesBucketOnce(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/backlog-fresh-test")
	w := seedWorld(t, ctx, entities, scopeID)
	q := slice.NewQuerier(entities)

	require.Equal(t, 0, backlogRowCount(t, ctx, pool, scopeID, w.Product.ID), "nothing has been abandoned or moved into this product's bucket yet")

	first, err := q.GetBacklog(ctx, w.Product.ID)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, first.MilestoneRefID, "the first read of a never-created bucket must still return a nameable id")
	assert.Equal(t, slice.Document{SchemaVersion: slice.SchemaVersion}, first.Document, "an empty bucket resolves to an empty document, not an error")
	require.Equal(t, 1, backlogRowCount(t, ctx, pool, scopeID, w.Product.ID), "the first read creates exactly one bucket row")

	acting, onBehalfOf := backlogRowSubject(t, ctx, pool, first.MilestoneRefID)
	assert.Equal(t, slice.BacklogBucketSubject, acting, "a permanent row the read created must name krill, never an empty creator")
	assert.Equal(t, slice.BacklogBucketSubject, onBehalfOf, "LB4: both slots populated, with on_behalf_of = acting")

	second, err := q.GetBacklog(ctx, w.Product.ID)
	require.NoError(t, err)
	assert.Equal(t, first.MilestoneRefID, second.MilestoneRefID, "the read is idempotent: the same product always resolves the same bucket")
	assert.Equal(t, 1, backlogRowCount(t, ctx, pool, scopeID, w.Product.ID), "a second read must create no second row")
}

// TestGetBacklog_NeverCreatedAndEmpty_AreTheSameRead pins the two states a
// caller cannot otherwise tell apart onto ONE response shape: a product
// whose bucket the read has to create, and one whose bucket already exists
// and is empty, return the same document and both return an id.
func TestGetBacklog_NeverCreatedAndEmpty_AreTheSameRead(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/backlog-two-states-test")
	q := slice.NewQuerier(entities)

	neverCreated, err := entities.Products().Create(ctx, scopeID, "krill-never", "never had a bucket")
	require.NoError(t, err)
	existing, err := entities.Products().Create(ctx, scopeID, "krill-existing", "bucket already resolved")
	require.NoError(t, err)

	caller := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
	_, err = entities.Recut().GetOrCreateBacklog(ctx, scopeID, existing.ID, caller, caller)
	require.NoError(t, err)

	fresh, err := q.GetBacklog(ctx, neverCreated.ID)
	require.NoError(t, err)
	empty, err := q.GetBacklog(ctx, existing.ID)
	require.NoError(t, err)

	assert.Equal(t, fresh.Document, empty.Document, "never-created and existing-but-empty are one read, not a 404 on one and an empty list on the other")
	assert.NotEqual(t, uuid.Nil, fresh.MilestoneRefID)
	assert.NotEqual(t, uuid.Nil, empty.MilestoneRefID)
	assert.NotEqual(t, fresh.MilestoneRefID, empty.MilestoneRefID, "the bucket is per product, not shared")
}

// TestGetBacklog_PopulatedBucket_ReturnsSameIdAndEntities is the other
// half: once scope has been moved into the bucket, the read returns the
// SAME id the fresh case returned, plus that scope's entities. The two
// states differ only in the bucket's contents, never in whether an id
// comes back.
func TestGetBacklog_PopulatedBucket_ReturnsSameIdAndEntities(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/backlog-populated-test")
	w := seedWorld(t, ctx, entities, scopeID)
	q := slice.NewQuerier(entities)

	fresh, err := q.GetBacklog(ctx, w.Product.ID)
	require.NoError(t, err)

	caller := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
	milestone, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, "M1", "ship it", nil, caller, caller)
	require.NoError(t, err)
	require.NoError(t, entities.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, w.FeatureA1.ID, caller, caller))
	require.NoError(t, entities.Recut().MoveScope(ctx, scopeID, []uuid.UUID{w.FeatureA1.ID}, milestone.ID, fresh.MilestoneRefID, caller, caller))

	populated, err := q.GetBacklog(ctx, w.Product.ID)
	require.NoError(t, err)
	assert.Equal(t, fresh.MilestoneRefID, populated.MilestoneRefID, "populating the bucket never renames it")
	assert.Equal(t, []uuid.UUID{w.FeatureA1.ID}, featureIDs(populated.Document))
	assert.Equal(t, 1, backlogRowCount(t, ctx, pool, scopeID, w.Product.ID))

	acting, _ := backlogRowSubject(t, ctx, pool, populated.MilestoneRefID)
	assert.Equal(t, slice.BacklogBucketSubject, acting, "a bucket first created by the read keeps its original attribution: the insert's ON CONFLICT leaves the row alone")
}

// TestGetBacklog_UnknownProduct_ReturnsErrNotFound proves the surface
// still refuses an id that names no product (the handler's 404), rather
// than creating a bucket under a product that does not exist (LB2).
func TestGetBacklog_UnknownProduct_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	entities, _ := newTestStore(t)
	q := slice.NewQuerier(entities)

	_, err := q.GetBacklog(ctx, uuid.New())
	assert.ErrorIs(t, err, store.ErrNotFound)
}
