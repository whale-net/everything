//go:build integration

// Real-Postgres coverage for AmendStore (amend.go, issue #2493's Testing
// section): the SCD2 close-and-open write path for Requirement and
// LoadBearingDecision. See store_integration_test.go's package doc for why
// this file only builds under the "integration" build tag, and
// entities_integration_test.go for why this file provisions its own
// self-contained fixture (newAmendTestStore) rather than sharing newStore/
// newScope with other *_integration_test.go files -- each go_test target
// here only compiles the srcs BUILD.bazel lists for it.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:amend_integration_test --test_output=all
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

// newAmendTestStore provisions an isolated, migrated Postgres database off
// krill's real embedded schema (mirrors store_integration_test.go's
// newStore) and returns a ready *store.Store plus the underlying
// dbtest.Postgres for row-count assertions AmendStore's own API cannot
// make (e.g. "exactly one row was closed").
func newAmendTestStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up())

	return store.New(db.Pool), db
}

func newAmendTestScope(t *testing.T, ctx context.Context, db *dbtest.Postgres) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "scope-"+uuid.NewString()).Scan(&id))
	return id
}

// newAmendTestFeature walks Product -> FeatureSet -> Feature so a test can
// attach a Requirement under a real parent.
func newAmendTestFeature(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID) store.Feature {
	t.Helper()
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)
	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", nil)
	require.NoError(t, err)
	f, err := s.Features().Create(ctx, scopeID, fs.ID, "SCD2 Store", nil)
	require.NoError(t, err)
	return f
}

// newAmendTestFeatureSet walks Product -> FeatureSet so a test can attach a
// LoadBearingDecision under a real parent.
func newAmendTestFeatureSet(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID) store.FeatureSet {
	t.Helper()
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)
	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", nil)
	require.NoError(t, err)
	return fs
}

func strPtrAmend(s string) *string { return &s }

// TestAmendRequirement_ClosesExactlyOneRowAndInsertsExactlyOneNewCurrentRow
// proves FR12's core contract: amending a Requirement closes exactly one
// row (`valid_to` set) and inserts exactly one new current row, both under
// the SAME surrogate id (LB2) -- never a new id, never more than one row
// touched.
func TestAmendRequirement_ClosesExactlyOneRowAndInsertsExactlyOneNewCurrentRow(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	feature := newAmendTestFeature(t, ctx, s, scopeID)

	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "Create Product", strPtrAmend("v1 body"))
	require.NoError(t, err)

	amended, err := s.Amend().AmendRequirement(ctx, fr.ID, "Create Product (amended)", strPtrAmend("v2 body"))
	require.NoError(t, err)

	assert.Equal(t, fr.ID, amended.ID, "amend must never mint a new surrogate id (LB2)")
	assert.NotEqual(t, fr.RevisionID, amended.RevisionID, "amend must insert a distinct physical row")
	assert.Equal(t, "Create Product (amended)", amended.Name)
	assert.Equal(t, "v2 body", *amended.Body)
	assert.Equal(t, fr.ScopeID, amended.ScopeID)
	assert.Equal(t, fr.FeatureID, amended.FeatureID)
	assert.Equal(t, fr.Kind, amended.Kind)
	assert.Nil(t, amended.ValidTo, "the newly-opened row must be current")

	var totalRows int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM requirement WHERE id = $1`, fr.ID).Scan(&totalRows))
	assert.Equal(t, 2, totalRows, "amend must leave exactly two physical rows behind: the closed original and the new current one")

	var currentRows int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM requirement WHERE id = $1 AND valid_to IS NULL`, fr.ID).Scan(&currentRows))
	assert.Equal(t, 1, currentRows, "exactly one row must remain current after an amend")

	var closedValidTo sql.NullTime
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT valid_to FROM requirement WHERE revision_id = $1`, fr.RevisionID).Scan(&closedValidTo))
	assert.True(t, closedValidTo.Valid, "the original revision's valid_to must be set once superseded")

	// The original row's data columns must be untouched -- amend closes it,
	// it never rewrites it.
	got, err := s.History().GetRequirementAsOf(ctx, fr.ID, fr.ValidFrom)
	require.NoError(t, err)
	assert.Equal(t, "Create Product", got.Name, "the prior revision's name must survive unmodified")
}

// TestAmendRequirement_SiblingRequirement_Untouched proves an amendment
// never rewrites any row belonging to a different logical entity, even a
// sibling under the exact same Feature.
func TestAmendRequirement_SiblingRequirement_Untouched(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	feature := newAmendTestFeature(t, ctx, s, scopeID)

	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "Create Product", nil)
	require.NoError(t, err)
	sibling, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindNFR, "Store layer only", nil)
	require.NoError(t, err)

	_, err = s.Amend().AmendRequirement(ctx, fr.ID, "Create Product (amended)", nil)
	require.NoError(t, err)

	gotSibling, err := s.Requirements().GetCurrentByID(ctx, sibling.ID)
	require.NoError(t, err)
	assert.Equal(t, sibling.RevisionID, gotSibling.RevisionID, "amending a sibling must not touch this entity's revision id")
	assert.Equal(t, sibling.Name, gotSibling.Name)
	assert.Nil(t, gotSibling.ValidTo, "an untouched sibling must remain current")

	var siblingRowCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM requirement WHERE id = $1`, sibling.ID).Scan(&siblingRowCount))
	assert.Equal(t, 1, siblingRowCount, "an untouched sibling must still have exactly its one original row")
}

// TestAmendRequirement_UnknownID_ReturnsErrNotFoundAndInsertsNoRow proves
// amend rejects an id with no current row, and does so without writing
// anything.
func TestAmendRequirement_UnknownID_ReturnsErrNotFoundAndInsertsNoRow(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)

	_, err := s.Amend().AmendRequirement(ctx, uuid.New(), "Orphan amend", nil)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM requirement`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected amend must insert no row")
}

// TestAmendLoadBearingDecision_ClosesExactlyOneRowAndInsertsExactlyOneNewCurrentRow
// mirrors the Requirement coverage above for LoadBearingDecision.
func TestAmendLoadBearingDecision_ClosesExactlyOneRowAndInsertsExactlyOneNewCurrentRow(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	fs := newAmendTestFeatureSet(t, ctx, s, scopeID)

	decision, err := s.Decisions().Create(ctx, scopeID, fs.ID, "LB2 identity", strPtrAmend("id is stable"))
	require.NoError(t, err)

	amended, err := s.Amend().AmendLoadBearingDecision(ctx, decision.ID, "LB2 identity (amended)", strPtrAmend("id is stable, always"))
	require.NoError(t, err)

	assert.Equal(t, decision.ID, amended.ID, "amend must never mint a new surrogate id (LB2)")
	assert.NotEqual(t, decision.RevisionID, amended.RevisionID)
	assert.Equal(t, "LB2 identity (amended)", amended.Name)
	assert.Equal(t, decision.FeatureSetID, amended.FeatureSetID)
	assert.Nil(t, amended.ValidTo)

	var totalRows int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM load_bearing_decision WHERE id = $1`, decision.ID).Scan(&totalRows))
	assert.Equal(t, 2, totalRows)

	var currentRows int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM load_bearing_decision WHERE id = $1 AND valid_to IS NULL`, decision.ID).Scan(&currentRows))
	assert.Equal(t, 1, currentRows)
}

// TestAmendLoadBearingDecision_SiblingDecision_Untouched mirrors
// TestAmendRequirement_SiblingRequirement_Untouched for LoadBearingDecision.
func TestAmendLoadBearingDecision_SiblingDecision_Untouched(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	fs := newAmendTestFeatureSet(t, ctx, s, scopeID)

	decision, err := s.Decisions().Create(ctx, scopeID, fs.ID, "LB2 identity", nil)
	require.NoError(t, err)
	sibling, err := s.Decisions().Create(ctx, scopeID, fs.ID, "A different decision", nil)
	require.NoError(t, err)

	_, err = s.Amend().AmendLoadBearingDecision(ctx, decision.ID, "LB2 identity (amended)", nil)
	require.NoError(t, err)

	gotSibling, err := s.Decisions().GetCurrentByID(ctx, sibling.ID)
	require.NoError(t, err)
	assert.Equal(t, sibling.RevisionID, gotSibling.RevisionID, "amending a sibling must not touch this entity's revision id")
}

// TestAmendLoadBearingDecision_UnknownID_ReturnsErrNotFoundAndInsertsNoRow
// mirrors TestAmendRequirement_UnknownID_ReturnsErrNotFoundAndInsertsNoRow.
func TestAmendLoadBearingDecision_UnknownID_ReturnsErrNotFoundAndInsertsNoRow(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)

	_, err := s.Amend().AmendLoadBearingDecision(ctx, uuid.New(), "Orphan amend", nil)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM load_bearing_decision`).Scan(&count))
	assert.Equal(t, 0, count)
}
