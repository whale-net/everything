//go:build integration

// Real-Postgres coverage for HistoryStore (history.go, issue #2493's
// Testing section): as-of reads and version lists for Requirement and
// LoadBearingDecision. See store_integration_test.go's package doc for why
// this file only builds under the "integration" build tag, and
// amend_integration_test.go's doc comment for why this file provisions its
// own self-contained fixture rather than sharing one with other
// *_integration_test.go files.
//
// Every as-of boundary in this file is read off Postgres's own clock (via
// dbNow, a bare `SELECT NOW()`), never the Go test process's wall clock --
// valid_from/valid_to are both written with Postgres's NOW() too, so
// comparing against a Go-side timestamp would be vulnerable to clock skew
// between the test process and the (possibly containerized) database. A
// short sleep between each write and its bracketing dbNow() call keeps
// consecutive revisions from landing on the same microsecond, which would
// otherwise make the boundary read ambiguous.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:history_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newHistoryTestStore mirrors amend_integration_test.go's
// newAmendTestStore -- its own self-contained fixture, since this file
// compiles as its own go_test target.
func newHistoryTestStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
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

func newHistoryTestScope(t *testing.T, ctx context.Context, db *dbtest.Postgres) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "scope-"+uuid.NewString()).Scan(&id))
	return id
}

func newHistoryTestFeature(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID) store.Feature {
	t.Helper()
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)
	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", nil)
	require.NoError(t, err)
	f, err := s.Features().Create(ctx, scopeID, fs.ID, "SCD2 Store", nil)
	require.NoError(t, err)
	return f
}

func newHistoryTestFeatureSet(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID) store.FeatureSet {
	t.Helper()
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)
	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", nil)
	require.NoError(t, err)
	return fs
}

// dbNow reads the target database's own current time, per this file's doc
// comment on why every as-of boundary here goes through Postgres's clock
// rather than Go's.
func dbNow(t *testing.T, ctx context.Context, db *dbtest.Postgres) time.Time {
	t.Helper()
	var now time.Time
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT NOW()`).Scan(&now))
	return now
}

const historyStepDelay = 20 * time.Millisecond

// TestGetRequirementAsOf_BetweenTwoAmendments_ReturnsVersionCurrentAtThatTime
// proves the core FR11 contract: an as-of read at a point between two
// amendments returns the version that was current AT that point, not the
// latest.
func TestGetRequirementAsOf_BetweenTwoAmendments_ReturnsVersionCurrentAtThatTime(t *testing.T) {
	ctx := context.Background()
	s, db := newHistoryTestStore(t)
	scopeID := newHistoryTestScope(t, ctx, db)
	feature := newHistoryTestFeature(t, ctx, s, scopeID)

	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "v1", nil)
	require.NoError(t, err)

	time.Sleep(historyStepDelay)
	tBetweenV1AndV2 := dbNow(t, ctx, db)
	time.Sleep(historyStepDelay)

	_, err = s.Amend().AmendRequirement(ctx, fr.ID, "v2", nil)
	require.NoError(t, err)

	time.Sleep(historyStepDelay)
	tBetweenV2AndV3 := dbNow(t, ctx, db)
	time.Sleep(historyStepDelay)

	_, err = s.Amend().AmendRequirement(ctx, fr.ID, "v3", nil)
	require.NoError(t, err)

	gotV1, err := s.History().GetRequirementAsOf(ctx, fr.ID, tBetweenV1AndV2)
	require.NoError(t, err)
	assert.Equal(t, "v1", gotV1.Name, "an as-of read between v1 and v2 must return v1, not the latest revision")

	gotV2, err := s.History().GetRequirementAsOf(ctx, fr.ID, tBetweenV2AndV3)
	require.NoError(t, err)
	assert.Equal(t, "v2", gotV2.Name, "an as-of read between v2 and v3 must return v2, not the latest revision")

	gotCurrent, err := s.History().GetRequirementAsOf(ctx, fr.ID, dbNow(t, ctx, db))
	require.NoError(t, err)
	assert.Equal(t, "v3", gotCurrent.Name, "an as-of read at the present must return the current revision")
}

// TestGetRequirementAsOf_BeforeEntityExisted_ReturnsErrNotFound proves an
// as-of read strictly before an entity's first revision is not-found, not
// (say) the first revision anyway.
func TestGetRequirementAsOf_BeforeEntityExisted_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newHistoryTestStore(t)
	scopeID := newHistoryTestScope(t, ctx, db)
	feature := newHistoryTestFeature(t, ctx, s, scopeID)

	tBeforeCreate := dbNow(t, ctx, db)
	time.Sleep(historyStepDelay)

	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "v1", nil)
	require.NoError(t, err)

	_, err = s.History().GetRequirementAsOf(ctx, fr.ID, tBeforeCreate)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// TestGetRequirementAsOf_UnknownID_ReturnsErrNotFound proves an id that
// never existed at all is not-found too, not a different error shape.
func TestGetRequirementAsOf_UnknownID_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newHistoryTestStore(t)

	_, err := s.History().GetRequirementAsOf(ctx, uuid.New(), dbNow(t, ctx, db))
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// TestListRequirementVersions_ReturnsEveryVersionInOrderWithSupersessionTimestamps
// proves the version-list contract: every revision, oldest first, each
// non-current revision carrying the ValidTo it was superseded at, and only
// the last carrying a nil ValidTo.
func TestListRequirementVersions_ReturnsEveryVersionInOrderWithSupersessionTimestamps(t *testing.T) {
	ctx := context.Background()
	s, db := newHistoryTestStore(t)
	scopeID := newHistoryTestScope(t, ctx, db)
	feature := newHistoryTestFeature(t, ctx, s, scopeID)

	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "v1", nil)
	require.NoError(t, err)
	time.Sleep(historyStepDelay)
	v2, err := s.Amend().AmendRequirement(ctx, fr.ID, "v2", nil)
	require.NoError(t, err)
	time.Sleep(historyStepDelay)
	v3, err := s.Amend().AmendRequirement(ctx, fr.ID, "v3", nil)
	require.NoError(t, err)

	versions, err := s.History().ListRequirementVersions(ctx, fr.ID)
	require.NoError(t, err)
	require.Len(t, versions, 3)

	assert.Equal(t, "v1", versions[0].Name)
	assert.Equal(t, "v2", versions[1].Name)
	assert.Equal(t, "v3", versions[2].Name)

	require.NotNil(t, versions[0].ValidTo, "a superseded revision must carry the timestamp it was superseded at")
	require.NotNil(t, versions[1].ValidTo)
	assert.Nil(t, versions[2].ValidTo, "only the current revision may have a nil ValidTo")

	assert.True(t, versions[0].ValidFrom.Before(versions[1].ValidFrom) || versions[0].ValidFrom.Equal(versions[1].ValidFrom))
	assert.True(t, versions[1].ValidFrom.Before(versions[2].ValidFrom) || versions[1].ValidFrom.Equal(versions[2].ValidFrom))

	assert.Equal(t, v2.RevisionID, versions[1].RevisionID)
	assert.Equal(t, v3.RevisionID, versions[2].RevisionID)

	// Every version must carry the SAME surrogate id (LB2) -- an amend
	// never mints a new one.
	for _, v := range versions {
		assert.Equal(t, fr.ID, v.ID)
	}
}

// TestListRequirementVersions_UnknownID_ReturnsErrNotFound proves an id
// with no revision at all is rejected, not an empty (but successful) list.
func TestListRequirementVersions_UnknownID_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, _ := newHistoryTestStore(t)

	_, err := s.History().ListRequirementVersions(ctx, uuid.New())
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// TestGetRequirementAsOf_NFRKind_Works proves FR11's as-of read is not
// FR-only -- an NFR (same table, different Kind) behaves identically.
func TestGetRequirementAsOf_NFRKind_Works(t *testing.T) {
	ctx := context.Background()
	s, db := newHistoryTestStore(t)
	scopeID := newHistoryTestScope(t, ctx, db)
	feature := newHistoryTestFeature(t, ctx, s, scopeID)

	nfr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindNFR, "Store layer only", nil)
	require.NoError(t, err)

	time.Sleep(historyStepDelay)
	tBetween := dbNow(t, ctx, db)
	time.Sleep(historyStepDelay)

	_, err = s.Amend().AmendRequirement(ctx, nfr.ID, "Store layer only (amended)", nil)
	require.NoError(t, err)

	got, err := s.History().GetRequirementAsOf(ctx, nfr.ID, tBetween)
	require.NoError(t, err)
	assert.Equal(t, "Store layer only", got.Name)
	assert.Equal(t, store.RequirementKindNFR, got.Kind)
}

// TestGetLoadBearingDecisionAsOf_BetweenTwoAmendments_ReturnsVersionCurrentAtThatTime
// mirrors the Requirement coverage above for LoadBearingDecision.
func TestGetLoadBearingDecisionAsOf_BetweenTwoAmendments_ReturnsVersionCurrentAtThatTime(t *testing.T) {
	ctx := context.Background()
	s, db := newHistoryTestStore(t)
	scopeID := newHistoryTestScope(t, ctx, db)
	fs := newHistoryTestFeatureSet(t, ctx, s, scopeID)

	decision, err := s.Decisions().Create(ctx, scopeID, fs.ID, "v1", nil)
	require.NoError(t, err)

	time.Sleep(historyStepDelay)
	tBetween := dbNow(t, ctx, db)
	time.Sleep(historyStepDelay)

	_, err = s.Amend().AmendLoadBearingDecision(ctx, decision.ID, "v2", nil)
	require.NoError(t, err)

	got, err := s.History().GetLoadBearingDecisionAsOf(ctx, decision.ID, tBetween)
	require.NoError(t, err)
	assert.Equal(t, "v1", got.Name)

	current, err := s.History().GetLoadBearingDecisionAsOf(ctx, decision.ID, dbNow(t, ctx, db))
	require.NoError(t, err)
	assert.Equal(t, "v2", current.Name)
}

// TestListLoadBearingDecisionVersions_ReturnsEveryVersionInOrderWithSupersessionTimestamps
// mirrors TestListRequirementVersions_ReturnsEveryVersionInOrderWithSupersessionTimestamps
// for LoadBearingDecision.
func TestListLoadBearingDecisionVersions_ReturnsEveryVersionInOrderWithSupersessionTimestamps(t *testing.T) {
	ctx := context.Background()
	s, db := newHistoryTestStore(t)
	scopeID := newHistoryTestScope(t, ctx, db)
	fs := newHistoryTestFeatureSet(t, ctx, s, scopeID)

	decision, err := s.Decisions().Create(ctx, scopeID, fs.ID, "v1", nil)
	require.NoError(t, err)
	time.Sleep(historyStepDelay)
	_, err = s.Amend().AmendLoadBearingDecision(ctx, decision.ID, "v2", nil)
	require.NoError(t, err)

	versions, err := s.History().ListLoadBearingDecisionVersions(ctx, decision.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, "v1", versions[0].Name)
	assert.Equal(t, "v2", versions[1].Name)
	require.NotNil(t, versions[0].ValidTo)
	assert.Nil(t, versions[1].ValidTo)
	for _, v := range versions {
		assert.Equal(t, decision.ID, v.ID)
	}
}

// TestListLoadBearingDecisionVersions_UnknownID_ReturnsErrNotFound mirrors
// TestListRequirementVersions_UnknownID_ReturnsErrNotFound.
func TestListLoadBearingDecisionVersions_UnknownID_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, _ := newHistoryTestStore(t)

	_, err := s.History().ListLoadBearingDecisionVersions(ctx, uuid.New())
	assert.ErrorIs(t, err, store.ErrNotFound)
}
