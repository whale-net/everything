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
	_, err = s.MilestoneAuthoring().AddDeferral(ctx, scopeID, milestone.ID, "the UI rewrite", "Later", acting, onBehalf)
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
