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

// TestAmendRequirement_PreservesPositionRelativeToSiblings is this issue's
// Testing case 3 (FR7): amending a requirement does not change its position
// relative to its siblings -- a supersession is the same logical entity, so
// its sibling order must not move even though amend inserts a brand new
// physical row.
func TestAmendRequirement_PreservesPositionRelativeToSiblings(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	feature := newAmendTestFeature(t, ctx, s, scopeID)

	// Created in order C, A, B -- positions 0, 1, 2 respectively.
	c, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "C Requirement", nil)
	require.NoError(t, err)
	a, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "A Requirement", nil)
	require.NoError(t, err)
	b, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "B Requirement", nil)
	require.NoError(t, err)

	amendedA, err := s.Amend().AmendRequirement(ctx, a.ID, "A Requirement (amended)", nil)
	require.NoError(t, err)
	assert.Equal(t, a.Position, amendedA.Position, "amending must carry the prior revision's position through unchanged")

	got, err := s.Requirements().ListCurrentByFeature(ctx, feature.ID)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []uuid.UUID{c.ID, a.ID, b.ID}, []uuid.UUID{got[0].ID, got[1].ID, got[2].ID},
		"amending A must not move it relative to its siblings -- order must still be C, A, B")
}

// TestAmendRequirement_SiblingCreatedAfterAmend_PicksUpMaxPlusOneOverCurrentRowsOnly
// is this issue's Testing case 4 (FR7): creating a sibling after an amend
// picks up max+1 over CURRENT rows only -- the closed (superseded) revision
// must not inflate the max.
//
// Amend always carries a prior revision's position through unchanged, so an
// ordinary amend can never leave behind a closed row whose position exceeds
// its own replacement's -- the two are always equal, which would mask a
// missing/broken "valid_to IS NULL" filter in nextSiblingPosition (MAX() is
// insensitive to a duplicate value at the same magnitude). To make the
// filter's absence actually observable, this test manufactures a stale-high
// closed position directly via SQL after the amend, the same "position is
// freely rewritable" technique store_integration_test.go's
// TestStore_InsertBetweenSiblings_PreservesSiblingSurrogateIDs uses -- then
// proves the next Create ignores it.
func TestAmendRequirement_SiblingCreatedAfterAmend_PicksUpMaxPlusOneOverCurrentRowsOnly(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	feature := newAmendTestFeature(t, ctx, s, scopeID)

	a, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "A Requirement", nil)
	require.NoError(t, err)
	b, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "B Requirement", nil)
	require.NoError(t, err)
	require.Equal(t, a.Position+1, b.Position, "B must be one greater than A")

	_, err = s.Amend().AmendRequirement(ctx, a.ID, "A Requirement (amended)", nil)
	require.NoError(t, err)

	// Manufacture a stale-high position on A's now-closed original row --
	// a value no current row carries -- so a next-position query that
	// forgot to filter on valid_to IS NULL would wrongly pick it up.
	_, err = db.Pool.Exec(ctx, `UPDATE requirement SET position = 99 WHERE id = $1 AND valid_to IS NOT NULL`, a.ID)
	require.NoError(t, err)

	c, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "C Requirement", nil)
	require.NoError(t, err)
	assert.Equal(t, b.Position+1, c.Position,
		"a sibling created after an amend must pick up max+1 over CURRENT rows only, ignoring the closed original A row's stale position")
}

// ── every spec-axis kind, not just Requirement and LoadBearingDecision ──

// assertSuperseded proves the shape every Amend* method shares: exactly two
// physical rows behind the one immutable id, exactly one of them current,
// and the original's valid_to set. kind names the table for the message.
func assertSuperseded(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string, id uuid.UUID) {
	t.Helper()

	var totalRows, currentRows int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = $1`, id).Scan(&totalRows))
	assert.Equal(t, 2, totalRows, "%s: amend must leave the closed original and one new current row", table)

	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE id = $1 AND valid_to IS NULL`, id).Scan(&currentRows))
	assert.Equal(t, 1, currentRows, "%s: exactly one row must remain current after an amend", table)
}

// TestAmendProduct_SupersedesUnderTheSameID proves FR 8b2e87d1's core
// contract on a kind that had no amend path at all: the Product's current
// row is closed and a successor opens under the same immutable id, with its
// scope and position carried forward.
func TestAmendProduct_SupersedesUnderTheSameID(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)

	product, err := s.Products().Create(ctx, scopeID, "Krill", "the original vision")
	require.NoError(t, err)

	amended, err := s.Amend().AmendProduct(ctx, product.ID, "krill", "a revised vision")
	require.NoError(t, err)

	assert.Equal(t, product.ID, amended.ID, "amend must never mint a new surrogate id (LB2)")
	assert.NotEqual(t, product.RevisionID, amended.RevisionID, "amend must insert a distinct physical row")
	assert.Equal(t, "krill", amended.Name)
	assert.Equal(t, "a revised vision", amended.Vision)
	assert.Equal(t, product.ScopeID, amended.ScopeID)
	assert.Equal(t, product.Position, amended.Position, "position is never rewritten by an amend")
	assert.Nil(t, amended.ValidTo)
	assertSuperseded(t, ctx, db, "product", product.ID)
}

// TestAmendFeatureSet_SupersedesAndLeavesParentUnchanged proves the same
// for a FeatureSet, and that the parent Product an amend carries forward is
// the real one.
func TestAmendFeatureSet_SupersedesAndLeavesParentUnchanged(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	fs := newAmendTestFeatureSet(t, ctx, s, scopeID)

	amended, err := s.Amend().AmendFeatureSet(ctx, fs.ID, "Spec Entities (amended)", strPtrAmend("a description"))
	require.NoError(t, err)

	assert.Equal(t, fs.ID, amended.ID)
	assert.Equal(t, fs.ProductID, amended.ProductID, "amend carries the parent forward; it never reparents (FR f0f6bc18)")
	assert.Equal(t, fs.Position, amended.Position)
	require.NotNil(t, amended.Description)
	assert.Equal(t, "a description", *amended.Description)
	assertSuperseded(t, ctx, db, "feature_set", fs.ID)
}

// TestAmendFeature_CarriesDisplayNumberForward proves a Feature amend
// supersedes the row without ever renumbering it: the `Cn` a caller already
// cites (migration 017) is the same number before and after.
func TestAmendFeature_CarriesDisplayNumberForward(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	feature := newAmendTestFeature(t, ctx, s, scopeID)

	amended, err := s.Amend().AmendFeature(ctx, feature.ID, "SCD2 Store (amended)", strPtrAmend("a description"))
	require.NoError(t, err)

	assert.Equal(t, feature.ID, amended.ID)
	assert.Equal(t, feature.DisplayNumber, amended.DisplayNumber, "an amend must never renumber an entity (LB2)")
	assert.Equal(t, feature.FeatureSetID, amended.FeatureSetID)
	assertSuperseded(t, ctx, db, "feature", feature.ID)

	// The whole lineage, closed revision included, must still agree on the
	// one number -- otherwise a citation rendered against the closed row
	// could resolve to nothing.
	var numbers []int
	rows, err := db.Pool.Query(ctx, `SELECT display_number FROM feature WHERE id = $1`, feature.ID)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var n int
		require.NoError(t, rows.Scan(&n))
		numbers = append(numbers, n)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []int{feature.DisplayNumber, feature.DisplayNumber}, numbers)
}

// TestAmendPersona_SupersedesUnderTheSameID covers the Persona kind.
func TestAmendPersona_SupersedesUnderTheSameID(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)

	persona, err := s.Personas().Create(ctx, scopeID, product.ID, "A Requirement Contributor", nil)
	require.NoError(t, err)

	amended, err := s.Amend().AmendPersona(ctx, persona.ID, "A Requirement Contributor (amended)", strPtrAmend("who they are"))
	require.NoError(t, err)

	assert.Equal(t, persona.ID, amended.ID)
	assert.Equal(t, product.ID, amended.ProductID)
	require.NotNil(t, amended.Description)
	assert.Equal(t, "who they are", *amended.Description)
	assertSuperseded(t, ctx, db, "persona", persona.ID)
}

// TestAmendNonGoal_CarriesKindForward proves a NonGoal amend supersedes
// the row without re-kinding it: `deferred` stays `deferred`, because
// resolving it (to `permanent`, or to nothing) is its own verb, not an
// amend (FR f0f6bc18).
func TestAmendNonGoal_CarriesKindForward(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)

	deferred, err := s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindDeferred, "Multi-tenancy", nil)
	require.NoError(t, err)

	amended, err := s.Amend().AmendNonGoal(ctx, deferred.ID, "Multi-tenancy (amended)", strPtrAmend("later"))
	require.NoError(t, err)

	assert.Equal(t, deferred.ID, amended.ID)
	assert.Equal(t, store.NonGoalKindDeferred, amended.Kind, "amend never re-kinds; resolution does")
	assert.Equal(t, product.ID, amended.ProductID)
	assertSuperseded(t, ctx, db, "non_goal", deferred.ID)
}

// TestAmendMilestone_LeavesDeliveryAxisUntouched is FR 39373553's proof:
// a milestone carrying a full delivery axis -- status history, Delivers,
// must-not-foreclose, deferrals -- is amended without one row of any of
// those four tables changing.
func TestAmendMilestone_LeavesDeliveryAxisUntouched(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	acting := store.Subject{Iss: "test", Sub: "operator", Kind: store.SubjectKindHuman}

	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)
	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, fs.ID, "SCD2 Store", nil)
	require.NoError(t, err)
	decision, err := s.Decisions().Create(ctx, scopeID, fs.ID, "LB2 identity", nil)
	require.NoError(t, err)

	budget := 12
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M9", "the original outcome", &budget, acting, acting)
	require.NoError(t, err)

	onBehalf := acting
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, feature.ID, acting, onBehalf))
	require.NoError(t, s.MilestoneAuthoring().AddMustNotForeclose(ctx, scopeID, milestone.ID, decision.ID, acting, onBehalf))
	_, err = s.MilestoneAuthoring().AddDeferral(ctx, scopeID, milestone.ID, "the UI rewrite", "Later", nil, acting, onBehalf)
	require.NoError(t, err)
	_, err = s.MilestoneStatus().RecordTransition(ctx, scopeID, milestone.ID, store.MilestoneStatusInProgress, nil, acting, onBehalf)
	require.NoError(t, err)

	// Snapshot the whole delivery axis before the amend.
	countDeliveryAxisRows := func() map[string]int {
		counts := map[string]int{}
		for _, spec := range []struct{ table, column string }{
			{"entity_milestone", "milestone_id"},
			{"milestone_deferral", "milestone_id"},
			{"milestone_status_event", "milestone_id"},
			{"delivery_shipment", "milestone_id"},
		} {
			var n int
			require.NoError(t, db.Pool.QueryRow(ctx,
				`SELECT count(*) FROM `+spec.table+` WHERE `+spec.column+` = $1`, milestone.ID).Scan(&n))
			counts[spec.table] = n
		}
		return counts
	}
	before := countDeliveryAxisRows()
	require.Equal(t, map[string]int{
		"entity_milestone":       2, // one Delivers, one must-not-foreclose
		"milestone_deferral":     1,
		"milestone_status_event": 1,
		"delivery_shipment":      0,
	}, before)

	amended, err := s.Amend().AmendMilestone(ctx, milestone.ID, "M9 (renamed)", strPtrAmend("a revised outcome"))
	require.NoError(t, err)

	// Authoring content is the amended revision's.
	assert.Equal(t, milestone.ID, amended.ID, "amend must never mint a new surrogate id (LB2)")
	assert.Equal(t, "M9 (renamed)", amended.Name)
	require.NotNil(t, amended.Outcome)
	assert.Equal(t, "a revised outcome", *amended.Outcome)

	// Everything else about the milestone is carried forward untouched.
	assert.NotEqual(t, milestone.RevisionID, amended.RevisionID, "amend must insert a distinct physical row")
	assert.Equal(t, product.ID, amended.ProductID)
	assert.Equal(t, store.MilestoneKindMilestone, amended.Kind)
	assert.Equal(t, milestone.Position, amended.Position)
	require.NotNil(t, amended.FRBudget)
	assert.Equal(t, budget, *amended.FRBudget, "an amend never rewrites the FR budget")
	require.NotNil(t, amended.CreatedByActing)
	assert.Equal(t, acting.Iss, amended.CreatedByActing.Iss, "the LB4 subject pair is carried forward")
	assert.Nil(t, amended.ValidTo)
	assertSuperseded(t, ctx, db, "milestone_ref", milestone.ID)

	// The delivery axis is bit-for-bit what it was.
	assert.Equal(t, before, countDeliveryAxisRows(), "an authoring amend must not touch status history, Delivers, must-not-foreclose, or deferrals")

	status, err := s.MilestoneStatus().CurrentStatus(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusInProgress, status, "the milestone's status must survive the amend, still keyed on the same id")

	transitions, err := s.MilestoneStatus().ListTransitions(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Len(t, transitions, 1, "an amend must append no status event of its own")

	ref, delivers, mustNotForeclose, deferrals, err := s.MilestoneAuthoring().GetMilestone(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Equal(t, "M9 (renamed)", ref.Name, "GetMilestone must read the amended revision")
	assert.Len(t, delivers, 1, "the Delivers association must survive the amend")
	assert.Len(t, mustNotForeclose, 1, "the must-not-foreclose association must survive the amend")
	assert.Len(t, deferrals, 1, "the deferral must survive the amend")
}

// TestAmend_ReplacementNameCollidingWithLiveSibling_IsRejected is FR
// b2767a89: an amend validates its replacement name for sibling
// uniqueness exactly as create does, and a collision is refused rather
// than silently resolving to a second live entity.
func TestAmend_ReplacementNameCollidingWithLiveSibling_IsRejected(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	feature := newAmendTestFeature(t, ctx, s, scopeID)

	a, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "A Requirement", nil)
	require.NoError(t, err)
	_, err = s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "B Requirement", nil)
	require.NoError(t, err)

	_, err = s.Amend().AmendRequirement(ctx, a.ID, "B Requirement", nil)
	assert.ErrorIs(t, err, store.ErrNameConflict, "an amend must reject a name a live sibling already holds")

	// The rejected amend must be atomic: A keeps its own name and stays
	// current, with no half-closed row left behind.
	got, err := s.Requirements().GetCurrentByID(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, a.RevisionID, got.RevisionID, "a rejected amend must not close the row it was amending")
	assert.Equal(t, "A Requirement", got.Name)
	siblingID := currentRequirementID(t, ctx, s, feature.ID, "B Requirement")
	var siblingRows, siblingCurrent int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM requirement WHERE id = $1`, siblingID).Scan(&siblingRows))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM requirement WHERE id = $1 AND valid_to IS NULL`, siblingID).Scan(&siblingCurrent))
	assert.Equal(t, 1, siblingRows, "the sibling whose name collided must be left with exactly its one original row")
	assert.Equal(t, 1, siblingCurrent, "the sibling must still be current")
}

// currentRequirementID resolves the current Requirement named name under
// featureID -- a helper for the atomicity assertion above, which needs the
// sibling's id to prove the collision left it alone.
func currentRequirementID(t *testing.T, ctx context.Context, s *store.Store, featureID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	rows, err := s.Requirements().ListCurrentByFeature(ctx, featureID)
	require.NoError(t, err)
	for _, r := range rows {
		if r.Name == name {
			return r.ID
		}
	}
	t.Fatalf("no current requirement named %q under feature %s", name, featureID)
	return uuid.Nil
}

// TestAmend_KeepingItsOwnNameSucceeds is the other half of the same rule:
// the prior revision is closed before the successor is inserted, so an
// amend that does not rename anything does not collide with the row it just
// superseded.
func TestAmend_KeepingItsOwnNameSucceeds(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	feature := newAmendTestFeature(t, ctx, s, scopeID)

	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "A Requirement", nil)
	require.NoError(t, err)

	amended, err := s.Amend().AmendRequirement(ctx, fr.ID, fr.Name, strPtrAmend("only the body changed"))
	require.NoError(t, err)

	assert.Equal(t, fr.ID, amended.ID)
	assert.Equal(t, "A Requirement", amended.Name)
	require.NotNil(t, amended.Body)
	assert.Equal(t, "only the body changed", *amended.Body)
	assertSuperseded(t, ctx, db, "requirement", fr.ID)
}

// ── CurrentPlacement: the read the placement guard compares against ──

// TestCurrentPlacement_ReadsEachKindsOwnColumns is the per-kind column map
// against the real schema: each kind reports the parent and kind its own
// current row holds, and reports nothing at all for the placement fields it
// has no column for. Those absences are what make a submitted value for one
// of them differ, so getting one wrong here would either over-refuse a
// correct client or under-refuse a real move.
func TestCurrentPlacement_ReadsEachKindsOwnColumns(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	acting := store.Subject{Iss: "test", Sub: "operator", Kind: store.SubjectKindHuman}

	product, err := s.Products().Create(ctx, scopeID, "Krill", "the vision")
	require.NoError(t, err)
	otherSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "Other Set", nil)
	require.NoError(t, err)
	set, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, set.ID, "SCD2 Store", nil)
	require.NoError(t, err)
	otherFeature, err := s.Features().Create(ctx, scopeID, otherSet.ID, "Somewhere Else", nil)
	require.NoError(t, err)
	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindNFR, "Placement guard", nil)
	require.NoError(t, err)
	persona, err := s.Personas().Create(ctx, scopeID, product.ID, "A Requirement Contributor", nil)
	require.NoError(t, err)
	deferred, err := s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindDeferred, "Multi-tenancy", nil)
	require.NoError(t, err)
	decision, err := s.Decisions().Create(ctx, scopeID, set.ID, "LB2 identity", nil)
	require.NoError(t, err)
	budget := 12
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M9", "", &budget, acting, acting)
	require.NoError(t, err)
	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "M9.1", "", nil, acting, acting)
	require.NoError(t, err)

	for _, tc := range []struct {
		kind string
		id   uuid.UUID
		want store.AmendPlacementChange
	}{
		// A Product is scoped by scope_id alone and has no parent and no
		// kind column, so it reports nothing -- every field it could be
		// sent is absent.
		{"product", product.ID, store.AmendPlacementChange{}},
		{"feature set", set.ID, store.AmendPlacementChange{ProductID: strPtrAmend(product.ID.String())}},
		{"feature", feature.ID, store.AmendPlacementChange{FeatureSetID: strPtrAmend(set.ID.String())}},
		{
			"requirement", fr.ID,
			store.AmendPlacementChange{FeatureID: strPtrAmend(feature.ID.String()), Kind: strPtrAmend("NFR")},
		},
		{"persona", persona.ID, store.AmendPlacementChange{ProductID: strPtrAmend(product.ID.String())}},
		{
			"non-goal", deferred.ID,
			store.AmendPlacementChange{ProductID: strPtrAmend(product.ID.String()), Kind: strPtrAmend("deferred")},
		},
		{"load-bearing decision", decision.ID, store.AmendPlacementChange{FeatureSetID: strPtrAmend(set.ID.String())}},
		{
			"milestone", milestone.ID,
			store.AmendPlacementChange{ProductID: strPtrAmend(product.ID.String()), Kind: strPtrAmend("milestone")},
		},
		{
			"milestone", milepebble.ID,
			store.AmendPlacementChange{
				ProductID:         strPtrAmend(product.ID.String()),
				ParentMilestoneID: strPtrAmend(milestone.ID.String()),
				Kind:              strPtrAmend("milepebble"),
			},
		},
	} {
		t.Run(tc.kind+"/"+tc.id.String()[:8], func(t *testing.T) {
			got, err := s.Amend().CurrentPlacement(ctx, tc.kind, tc.id)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	// The read follows the id it was given, not some other row of the kind:
	// a second Feature under a different FeatureSet reports that set.
	other, err := s.Amend().CurrentPlacement(ctx, "feature", otherFeature.ID)
	require.NoError(t, err)
	require.NotNil(t, other.FeatureSetID)
	assert.Equal(t, otherSet.ID.String(), *other.FeatureSetID,
		"the read must return the row it was asked about, not a constant or a sibling's")
}

// TestCurrentPlacement_ReadsTheCurrentRevisionAfterASupersession proves the
// read filters on valid_to IS NULL like every other current-row read: after
// an amend the placement comes from the new current revision, which carries
// the parent forward unchanged.
func TestCurrentPlacement_ReadsTheCurrentRevisionAfterASupersession(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	feature := newAmendTestFeature(t, ctx, s, scopeID)

	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "Placement guard", nil)
	require.NoError(t, err)
	_, err = s.Amend().AmendRequirement(ctx, fr.ID, "Placement guard (amended)", nil)
	require.NoError(t, err)

	got, err := s.Amend().CurrentPlacement(ctx, "requirement", fr.ID)
	require.NoError(t, err)
	require.NotNil(t, got.FeatureID)
	assert.Equal(t, feature.ID.String(), *got.FeatureID)
	require.NotNil(t, got.Kind)
	assert.Equal(t, "FR", *got.Kind)
}

// TestCurrentPlacement_RealPlacementDrivesTheGuard closes the loop: the
// placement a client would have read is accepted, moving the entity to any
// other parent is refused by name, and a field the kind has no column for is
// refused whatever it holds.
func TestCurrentPlacement_RealPlacementDrivesTheGuard(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	feature := newAmendTestFeature(t, ctx, s, scopeID)

	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "Placement guard", nil)
	require.NoError(t, err)

	current, err := s.Amend().CurrentPlacement(ctx, "requirement", fr.ID)
	require.NoError(t, err)

	echoed := store.AmendPlacementChange{FeatureID: current.FeatureID, Kind: current.Kind}
	assert.NoError(t, echoed.Refuse("requirement", current), "a client echoing what it read must not be refused")

	moved := store.AmendPlacementChange{FeatureID: strPtrAmend(uuid.NewString())}
	err = moved.Refuse("requirement", current)
	require.ErrorIs(t, err, store.ErrPlacementChange)
	assert.Contains(t, err.Error(), "cannot change feature_id on amend")

	// A Requirement has no parent_milestone_id column, so its current value
	// is absent and anything sent for it differs -- an empty string
	// included, which is a submitted value.
	orphan := store.AmendPlacementChange{ParentMilestoneID: strPtrAmend("")}
	assert.ErrorIs(t, orphan.Refuse("requirement", current), store.ErrPlacementChange)
}

// TestCurrentPlacement_UnknownIDAndUnknownKind proves the read's two failure
// modes: an id with no current row is ErrNotFound, exactly as the write
// would report it, and a kind the placement map does not name is an error
// rather than a silent zero value.
func TestCurrentPlacement_UnknownIDAndUnknownKind(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	feature := newAmendTestFeature(t, ctx, s, scopeID)

	_, err := s.Amend().CurrentPlacement(ctx, "requirement", uuid.New())
	assert.ErrorIs(t, err, store.ErrNotFound)

	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "Placement guard", nil)
	require.NoError(t, err)
	_, err = s.Amend().CurrentPlacement(ctx, "task", fr.ID)
	assert.Error(t, err, "a kind the placement map does not name must be an error, not a silent zero value")
}

// ── AmendDeferral (migration 024, which made milestone_deferral SCD2) ──

// newAmendTestMilestone provisions a bare milestone to hang deferrals off,
// so each deferral test below states only what it is about.
func newAmendTestMilestone(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, scopeID uuid.UUID) store.MilestoneRef {
	t.Helper()
	acting := store.Subject{Iss: "test", Sub: "operator", Kind: store.SubjectKindHuman}

	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "the original outcome", nil, acting, acting)
	require.NoError(t, err)
	return milestone
}

// TestAmendDeferral_SupersedesUnderTheSameID is the core contract on the
// kind migration 024 created this method for: a deferral's body is
// correctable in place, under its own unchanged id, with the pre-correction
// text retained beside the current one rather than deleted.
func TestAmendDeferral_SupersedesUnderTheSameID(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	acting := store.Subject{Iss: "test", Sub: "operator", Kind: store.SubjectKindHuman}
	milestone := newAmendTestMilestone(t, ctx, s, db, scopeID)

	created, err := s.MilestoneAuthoring().AddDeferral(ctx, scopeID, milestone.ID, "C4 stays unbuilt until M2", "M2", nil, acting, acting)
	require.NoError(t, err)
	assert.Nil(t, created.ValidTo, "a freshly added deferral is its own first, current revision")

	amended, err := s.Amend().AmendDeferral(ctx, created.ID, "C4 is unbuilt until M2, which now also carries C5", "M2", nil)
	require.NoError(t, err)

	assert.Equal(t, created.ID, amended.ID, "amend must never mint a new surrogate id (LB2) -- a corrected deferral is the same deferral")
	assert.NotEqual(t, created.RevisionID, amended.RevisionID, "amend must insert a distinct physical row")
	assert.Equal(t, "C4 is unbuilt until M2, which now also carries C5", amended.Body)
	assert.Equal(t, "M2", amended.Destination)
	assert.Nil(t, amended.ValidTo, "the successor must be the current revision")

	// Everything else about the deferral is carried forward, not re-chosen.
	assert.Equal(t, created.Position, amended.Position, "an amend must not reorder the item against its siblings")
	assert.Equal(t, created.MilestoneID, amended.MilestoneID, "an amend never reparents (LB2)")
	assert.WithinDuration(t, created.CreatedAt, amended.CreatedAt, time.Second,
		"created_at names when the item was DEFERRED, which a wording correction does not change")
	assert.Equal(t, acting.Iss, amended.CreatedByActing.Iss,
		"the successor carries the original's subject pair forward -- overwriting it would make 'when and by whom was this deferred' unreadable, which is the provenance delete-and-recreate already destroyed")
	assert.Equal(t, acting.Sub, amended.CreatedByOnBehalfOf.Sub)
	assert.Equal(t, acting.Kind, amended.CreatedByOnBehalfOf.Kind)

	// The closed original keeps the pre-correction text in full: amend
	// supersedes, it never overwrites or deletes.
	var originalBody string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT body FROM milestone_deferral WHERE id = $1 AND valid_to IS NOT NULL
	`, created.ID).Scan(&originalBody))
	assert.Equal(t, "C4 stays unbuilt until M2", originalBody,
		"the superseded revision must still be readable -- that is the whole reason for closing it instead of updating in place")

	assertSuperseded(t, ctx, db, "milestone_deferral", created.ID)
}

// TestAmendDeferral_SuccessiveAmends_ReadsReturnExactlyOneRow is the
// read-path sweep's whole point: after two successive amends, every reader
// -- ListDeferrals, and GetMilestone, and through the latter the
// get_milestone MCP tool response and the renderer -- must return the
// latest amendment exactly once, never the superseded text alongside it.
func TestAmendDeferral_SuccessiveAmends_ReadsReturnExactlyOneRow(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	acting := store.Subject{Iss: "test", Sub: "operator", Kind: store.SubjectKindHuman}
	milestone := newAmendTestMilestone(t, ctx, s, db, scopeID)

	// A sibling deferral, so "exactly one row" is not satisfied trivially by
	// an over-aggressive filter that drops everything.
	other, err := s.MilestoneAuthoring().AddDeferral(ctx, scopeID, milestone.ID, "the UI rewrite", "Later", nil, acting, acting)
	require.NoError(t, err)

	created, err := s.MilestoneAuthoring().AddDeferral(ctx, scopeID, milestone.ID, "C4 stale", "M2", nil, acting, acting)
	require.NoError(t, err)

	_, err = s.Amend().AmendDeferral(ctx, created.ID, "C4 corrected once", "M2", nil)
	require.NoError(t, err)
	_, err = s.Amend().AmendDeferral(ctx, created.ID, "C4 corrected twice", "M3", nil)
	require.NoError(t, err)

	list, err := s.MilestoneAuthoring().ListDeferrals(ctx, milestone.ID)
	require.NoError(t, err)
	require.Len(t, list, 2, "two current deferrals, not four: every superseded revision must be filtered out")
	// Index by id rather than by slot: the assertion is about which rows
	// survive the filter, not about sibling ordering.
	byID := map[uuid.UUID]store.MilestoneDeferral{}
	for _, d := range list {
		byID[d.ID] = d
	}
	require.Contains(t, byID, created.ID, "the amended deferral must still be in the list, under its own id")
	assert.Equal(t, "C4 corrected twice", byID[created.ID].Body, "the reader must see the second amendment's text")
	assert.Equal(t, "M3", byID[created.ID].Destination)
	assert.Nil(t, byID[created.ID].ValidTo)
	require.Contains(t, byID, other.ID, "the un-amended sibling must be untouched and still present -- a filter that dropped everything would satisfy Len(2) too")
	assert.Equal(t, "the UI rewrite", byID[other.ID].Body)

	// GetMilestone hands its []MilestoneDeferral straight to the
	// get_milestone tool output, so it is the second place a superseded
	// revision would have surfaced.
	_, _, _, deferrals, err := s.MilestoneAuthoring().GetMilestone(ctx, milestone.ID)
	require.NoError(t, err)
	require.Len(t, deferrals, 2, "GetMilestone must return one deferral per current row, never one per revision")
	var amendedViaGet store.MilestoneDeferral
	for _, d := range deferrals {
		if d.ID == created.ID {
			amendedViaGet = d
		}
	}
	assert.Equal(t, "C4 corrected twice", amendedViaGet.Body, "get_milestone's tool output must carry the amended text")

	// Three physical rows now: the original plus two successors, of which
	// exactly one is current.
	var total, current int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_deferral WHERE id = $1`, created.ID).Scan(&total))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_deferral WHERE id = $1 AND valid_to IS NULL`, created.ID).Scan(&current))
	assert.Equal(t, 3, total, "an amend must supersede, never delete: all three revisions stay on record")
	assert.Equal(t, 1, current)
}

// TestAmendDeferral_AmendMilestoneLeavesItAlone is the separation AmendStore
// has to keep: FR 39373553's promise was that amending a milestone never
// reaches its delivery axis, and migration 024 did not reopen that -- a
// deferral got its own amend, it did not become something AmendMilestone
// touches.
func TestAmendDeferral_AmendMilestoneLeavesItAlone(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	acting := store.Subject{Iss: "test", Sub: "operator", Kind: store.SubjectKindHuman}
	milestone := newAmendTestMilestone(t, ctx, s, db, scopeID)

	created, err := s.MilestoneAuthoring().AddDeferral(ctx, scopeID, milestone.ID, "the UI rewrite", "Later", nil, acting, acting)
	require.NoError(t, err)

	_, err = s.Amend().AmendMilestone(ctx, milestone.ID, "M1 (renamed)", nil)
	require.NoError(t, err)

	var total, current int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_deferral WHERE id = $1`, created.ID).Scan(&total))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_deferral WHERE id = $1 AND valid_to IS NULL`, created.ID).Scan(&current))
	assert.Equal(t, 1, total, "amending a milestone must not supersede any of its deferrals (FR 39373553)")
	assert.Equal(t, 1, current)
}

// TestAmendDeferral_EmptyDestinationRefused is FR1's amendment-side half:
// the row shape AddDeferral would have refused must not be reachable by
// amending either, or "the destination is mandatory" would be a property of
// the create verb alone.
func TestAmendDeferral_EmptyDestinationRefused(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	acting := store.Subject{Iss: "test", Sub: "operator", Kind: store.SubjectKindHuman}
	milestone := newAmendTestMilestone(t, ctx, s, db, scopeID)

	created, err := s.MilestoneAuthoring().AddDeferral(ctx, scopeID, milestone.ID, "the UI rewrite", "Later", nil, acting, acting)
	require.NoError(t, err)

	_, err = s.Amend().AmendDeferral(ctx, created.ID, "the UI rewrite, restated", "", nil)
	assert.Error(t, err, "FR1: every deferred entry must cite where it went -- an amend may not write the row AddDeferral refuses")
	assert.Contains(t, err.Error(), "FR1", "the refusal must name the rule it enforces, as AddDeferral's does")

	var current int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM milestone_deferral WHERE id = $1 AND valid_to IS NULL
	`, created.ID).Scan(&current))
	assert.Equal(t, 1, current, "a refused amend must be atomic: the original stays current and no successor is written")
}

// TestAmendDeferral_UnknownIDIsNotFound matches every other Amend* method's
// error contract, and pins the second half of the row-lock: each amend
// supersedes the CURRENT revision only, so an id that has already been
// amended once still amends cleanly, against its successor.
func TestAmendDeferral_UnknownIDIsNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	acting := store.Subject{Iss: "test", Sub: "operator", Kind: store.SubjectKindHuman}
	milestone := newAmendTestMilestone(t, ctx, s, db, scopeID)

	_, err := s.Amend().AmendDeferral(ctx, uuid.New(), "never existed", "M2", nil)
	assert.ErrorIs(t, err, store.ErrNotFound, "amending an id that names no deferral row is ErrNotFound, as every other Amend* method reports")

	created, err := s.MilestoneAuthoring().AddDeferral(ctx, scopeID, milestone.ID, "the UI rewrite", "Later", nil, acting, acting)
	require.NoError(t, err)
	_, err = s.Amend().AmendDeferral(ctx, created.ID, "amended once", "Later", nil)
	require.NoError(t, err)
	_, err = s.Amend().AmendDeferral(ctx, created.ID, "amended twice", "Later", nil)
	require.NoError(t, err, "an already-amended id still names a current row, so it is still amendable")

	var total, current int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_deferral WHERE id = $1`, created.ID).Scan(&total))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_deferral WHERE id = $1 AND valid_to IS NULL`, created.ID).Scan(&current))
	assert.Equal(t, 3, total, "one original plus one successor per amend, none deleted")
	assert.Equal(t, 1, current, "the FOR UPDATE + valid_to IS NULL row-lock is what makes each amend close the current row rather than re-closing an already-closed one")
}

// TestAmendMilepebble_RevisesNameAndOutcome_LeavesBudgetHistoryAndDeliveryUntouched
// proves amend_milepebble: a new SCD2 revision under the same id, with the
// FR budget, status history and delivery axis unchanged; a milestone id is
// refused.
func TestAmendMilepebble_RevisesNameAndOutcome_LeavesBudgetHistoryAndDeliveryUntouched(t *testing.T) {
	ctx := context.Background()
	s, db := newAmendTestStore(t)
	scopeID := newAmendTestScope(t, ctx, db)
	acting := store.Subject{Iss: "test", Sub: "operator", Kind: store.SubjectKindHuman}

	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)
	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, fs.ID, "SCD2 Store", nil)
	require.NoError(t, err)

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M9", "outcome", nil, acting, acting)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, feature.ID, acting, acting))
	budget := 5
	pebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "P1", "pebble outcome", &budget, acting, acting)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, pebble.ID, feature.ID, acting, acting))
	_, err = s.MilestoneStatus().RecordTransition(ctx, scopeID, pebble.ID, store.MilestoneStatusInProgress, nil, acting, acting)
	require.NoError(t, err)

	count := func(table string) int {
		var n int
		require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE milestone_id = $1`, pebble.ID).Scan(&n))
		return n
	}
	deliversBefore, eventsBefore := count("entity_milestone"), count("milestone_status_event")
	require.Equal(t, 1, deliversBefore)
	require.Equal(t, 1, eventsBefore)

	amended, err := s.Amend().AmendMilepebble(ctx, pebble.ID, "P1 (renamed)", strPtrAmend("revised pebble outcome"))
	require.NoError(t, err)

	assert.Equal(t, pebble.ID, amended.ID)
	assert.NotEqual(t, pebble.RevisionID, amended.RevisionID)
	assert.Equal(t, "P1 (renamed)", amended.Name)
	require.NotNil(t, amended.Outcome)
	assert.Equal(t, "revised pebble outcome", *amended.Outcome)
	assert.Equal(t, store.MilestoneKindMilepebble, amended.Kind)
	require.NotNil(t, amended.FRBudget)
	assert.Equal(t, budget, *amended.FRBudget, "FR budget is carried forward")
	require.NotNil(t, amended.ParentMilestoneID)
	assert.Equal(t, milestone.ID, *amended.ParentMilestoneID)
	assertSuperseded(t, ctx, db, "milestone_ref", pebble.ID)

	assert.Equal(t, deliversBefore, count("entity_milestone"))
	assert.Equal(t, eventsBefore, count("milestone_status_event"))
	status, err := s.MilestoneStatus().CurrentStatus(ctx, pebble.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusInProgress, status)

	_, err = s.Amend().AmendMilepebble(ctx, milestone.ID, "nope", nil)
	require.ErrorIs(t, err, store.ErrNotFound, "a milestone id is not a milepebble")
}
