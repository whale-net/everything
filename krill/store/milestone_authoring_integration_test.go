//go:build integration

// Real-Postgres coverage for MilestoneAuthoringStore
// (milestone_authoring.go, migration 010, issue #2683's Testing section,
// FR1/FR2/NFR1/NFR4): CreateMilestone/SetOutcome/SetFRBudget,
// AddDelivers/AddMustNotForeclose's LB6 partitioning and idempotency,
// AddDeferral's FR1 destination requirement, NFR1's identity-stability
// guarantee, and NFR4's subject-pair recording. See
// store_integration_test.go's package doc for why this file only builds
// under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:milestone_authoring_integration_test --test_output=all
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

func newMilestoneAuthoringTestStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
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

func newMilestoneAuthoringTestScope(t *testing.T, ctx context.Context, db *dbtest.Postgres) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "scope-authoring-"+uuid.NewString()).Scan(&id))
	return id
}

func milestoneAuthoringTestSubject(sub string) store.Subject {
	return store.Subject{Iss: "https://issuer.example.com", Sub: sub, Kind: store.SubjectKindService}
}

// TestMilestoneAuthoringStore_CreateMilestone_WithOutcomeAndBudget_GetMilestoneReturnsAllFieldsAndSubjectPair
// is issue #2683's Testing section item 1: creating a milestone with an
// outcome sentence and an FR budget round-trips both through GetMilestone,
// along with the recorded LB4 subject pair (NFR4).
func TestMilestoneAuthoringStore_CreateMilestone_WithOutcomeAndBudget_GetMilestoneReturnsAllFieldsAndSubjectPair(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	acting := milestoneAuthoringTestSubject("agent-1")
	onBehalfOf := milestoneAuthoringTestSubject("human-1")
	budget := 5

	ref, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship the authoring surface", &budget, acting, onBehalfOf)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, ref.ID)
	assert.Equal(t, store.MilestoneKindMilestone, ref.Kind)
	require.NotNil(t, ref.Outcome)
	assert.Equal(t, "ship the authoring surface", *ref.Outcome)
	require.NotNil(t, ref.FRBudget)
	assert.Equal(t, 5, *ref.FRBudget)
	require.NotNil(t, ref.CreatedByActing, "NFR4: the acting subject must be recorded")
	assert.Equal(t, acting, *ref.CreatedByActing)
	require.NotNil(t, ref.CreatedByOnBehalfOf, "NFR4: the on-behalf-of subject must be recorded")
	assert.Equal(t, onBehalfOf, *ref.CreatedByOnBehalfOf)

	gotRef, delivers, mustNotForeclose, deferrals, err := s.MilestoneAuthoring().GetMilestone(ctx, ref.ID)
	require.NoError(t, err)
	assert.Equal(t, ref.ID, gotRef.ID)
	require.NotNil(t, gotRef.Outcome)
	assert.Equal(t, "ship the authoring surface", *gotRef.Outcome)
	require.NotNil(t, gotRef.FRBudget)
	assert.Equal(t, 5, *gotRef.FRBudget)
	assert.Equal(t, acting, *gotRef.CreatedByActing)
	assert.Equal(t, onBehalfOf, *gotRef.CreatedByOnBehalfOf)
	assert.Empty(t, delivers)
	assert.Empty(t, mustNotForeclose)
	assert.Empty(t, deferrals)
}

// TestMilestoneAuthoringStore_CreateMilestone_UnknownProduct_ReturnsErrNotFound
// proves LB2 parentage: CreateMilestone rejects a product_id with no
// current `product` row and inserts no row.
func TestMilestoneAuthoringStore_CreateMilestone_UnknownProduct_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	self := milestoneAuthoringTestSubject("agent-1")
	_, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, uuid.New(), "M1", "", nil, self, self)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_ref`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected CreateMilestone must insert no row")
}

// TestMilestoneAuthoringStore_SetOutcomeAndSetFRBudget_TwiceCurrentIsLatest
// is issue #2683's Testing section item 2 (extended to SetOutcome, which
// the issue's own dispatch calls out alongside SetFRBudget): calling
// either revise method twice leaves the current value as the latest write,
// never the first (FR2).
func TestMilestoneAuthoringStore_SetOutcomeAndSetFRBudget_TwiceCurrentIsLatest(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	ref, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "first outcome", nil, self, self)
	require.NoError(t, err)

	require.NoError(t, s.MilestoneAuthoring().SetOutcome(ctx, ref.ID, "second outcome", self, self))
	require.NoError(t, s.MilestoneAuthoring().SetFRBudget(ctx, ref.ID, 3, self, self))
	require.NoError(t, s.MilestoneAuthoring().SetFRBudget(ctx, ref.ID, 7, self, self))

	got, _, _, _, err := s.MilestoneAuthoring().GetMilestone(ctx, ref.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Outcome)
	assert.Equal(t, "second outcome", *got.Outcome, "the current outcome must be the latest SetOutcome call's value")
	require.NotNil(t, got.FRBudget)
	assert.Equal(t, 7, *got.FRBudget, "the current FR budget must be the latest SetFRBudget call's value, not the first")
}

// TestMilestoneAuthoringStore_SetFRBudget_UnknownMilestone_ReturnsErrNotFound
// proves SetFRBudget (and by the same code shape, SetOutcome) rejects an
// id with no milestone_ref row rather than silently succeeding.
func TestMilestoneAuthoringStore_SetFRBudget_UnknownMilestone_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, _ := newMilestoneAuthoringTestStore(t)

	self := milestoneAuthoringTestSubject("agent-1")
	err := s.MilestoneAuthoring().SetFRBudget(ctx, uuid.New(), 1, self, self)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// TestMilestoneAuthoringStore_AddDeliversAndAddMustNotForeclose_PartitionedAndIdempotent
// is issue #2683's Testing section item 3: two Features added via
// AddDelivers and three LoadBearingDecisions added via
// AddMustNotForeclose against the same milestone produce five
// entity_milestone rows correctly partitioned by relation, and re-adding
// the same (entity, milestone, relation) pair is a no-op.
func TestMilestoneAuthoringStore_AddDeliversAndAddMustNotForeclose_PartitionedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	ref, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	f1, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	f2, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F2", nil)
	require.NoError(t, err)

	lb1, err := s.Decisions().Create(ctx, scopeID, featureSet.ID, "LB1", nil)
	require.NoError(t, err)
	lb2, err := s.Decisions().Create(ctx, scopeID, featureSet.ID, "LB2", nil)
	require.NoError(t, err)
	lb3, err := s.Decisions().Create(ctx, scopeID, featureSet.ID, "LB3", nil)
	require.NoError(t, err)

	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, ref.ID, f1.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, ref.ID, f2.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMustNotForeclose(ctx, scopeID, ref.ID, lb1.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMustNotForeclose(ctx, scopeID, ref.ID, lb2.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMustNotForeclose(ctx, scopeID, ref.ID, lb3.ID, self, self))

	_, delivers, mustNotForeclose, _, err := s.MilestoneAuthoring().GetMilestone(ctx, ref.ID)
	require.NoError(t, err)
	require.Len(t, delivers, 2, "AddDelivers must produce exactly the two Delivers rows")
	require.Len(t, mustNotForeclose, 3, "AddMustNotForeclose must produce exactly the three Must-not-foreclose rows")

	deliversEntities := map[uuid.UUID]bool{}
	for _, m := range delivers {
		assert.Equal(t, store.MilestoneRelationDelivers, m.Relation)
		deliversEntities[m.EntityID] = true
	}
	assert.True(t, deliversEntities[f1.ID])
	assert.True(t, deliversEntities[f2.ID])

	mustNotForecloseEntities := map[uuid.UUID]bool{}
	for _, m := range mustNotForeclose {
		assert.Equal(t, store.MilestoneRelationMustNotForeclose, m.Relation)
		mustNotForecloseEntities[m.EntityID] = true
	}
	assert.True(t, mustNotForecloseEntities[lb1.ID])
	assert.True(t, mustNotForecloseEntities[lb2.ID])
	assert.True(t, mustNotForecloseEntities[lb3.ID])

	var totalCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM entity_milestone WHERE milestone_id = $1`, ref.ID).Scan(&totalCount))
	require.Equal(t, 5, totalCount, "exactly five entity_milestone rows total")

	// Re-adding the same (entity, milestone) pair under the same relation
	// must be a no-op, not a duplicate or an error.
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, ref.ID, f1.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMustNotForeclose(ctx, scopeID, ref.ID, lb1.ID, self, self))

	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM entity_milestone WHERE milestone_id = $1`, ref.ID).Scan(&totalCount))
	assert.Equal(t, 5, totalCount, "re-adding an existing (entity, milestone, relation) pair must not insert a duplicate row")
}

// TestMilestoneAuthoringStore_AddDeferral_EmptyDestination_Rejected is
// issue #2683's Testing section item 4: FR1 requires every deferred entry
// to cite where it went -- an empty destination is rejected, and nothing
// is written.
func TestMilestoneAuthoringStore_AddDeferral_EmptyDestination_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	ref, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	_, err = s.MilestoneAuthoring().AddDeferral(ctx, scopeID, ref.ID, "cut for M1", "", self, self)
	require.Error(t, err, "an empty destination must be rejected -- FR1 requires every deferred entry to cite where it went")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_deferral WHERE milestone_id = $1`, ref.ID).Scan(&count))
	assert.Equal(t, 0, count, "a rejected AddDeferral must insert no row")
}

// TestMilestoneAuthoringStore_AddDeferral_RecordsSubjectPair proves NFR4
// for AddDeferral specifically: milestone_deferral's subject-pair columns
// are mandatory (unlike milestone_ref's own, per migration 010's LB4
// note), and both are populated and non-empty on every successful write.
func TestMilestoneAuthoringStore_AddDeferral_RecordsSubjectPair(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	acting := milestoneAuthoringTestSubject("agent-1")
	onBehalfOf := milestoneAuthoringTestSubject("human-1")
	ref, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, acting, onBehalfOf)
	require.NoError(t, err)

	deferral, err := s.MilestoneAuthoring().AddDeferral(ctx, scopeID, ref.ID, "milepebble breakdown", "M4", acting, onBehalfOf)
	require.NoError(t, err)
	assert.Equal(t, acting, deferral.CreatedByActing)
	assert.NotEmpty(t, deferral.CreatedByActing.Sub)
	assert.Equal(t, onBehalfOf, deferral.CreatedByOnBehalfOf)
	assert.NotEmpty(t, deferral.CreatedByOnBehalfOf.Sub)
	assert.Equal(t, "M4", deferral.Destination)

	deferrals, err := s.MilestoneAuthoring().ListDeferrals(ctx, ref.ID)
	require.NoError(t, err)
	require.Len(t, deferrals, 1)
	assert.Equal(t, deferral.ID, deferrals[0].ID)
}

// TestMilestoneAuthoringStore_NFR1_IdentityStability is issue #2683's
// Testing section item 5, verbatim: create M1, M2, M3; delete/rename a
// sibling; an id captured before the change still resolves to the same
// milestone with the same content. milestone_ref's identity is its
// surrogate id (LB2/NFR1), never `name` ("M2") or sibling position --
// renaming or removing M2 must never change what an id captured for M1
// resolves to.
func TestMilestoneAuthoringStore_NFR1_IdentityStability(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	m1, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship M1's outcome", nil, self, self)
	require.NoError(t, err)
	m2, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M2", "ship M2's outcome", nil, self, self)
	require.NoError(t, err)
	m3, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M3", "ship M3's outcome", nil, self, self)
	require.NoError(t, err)

	capturedM1ID := m1.ID

	// Rename M2 (a sibling) and delete M3 (another sibling) entirely.
	_, err = db.Pool.Exec(ctx, `UPDATE milestone_ref SET name = 'M2-renamed' WHERE id = $1`, m2.ID)
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `DELETE FROM milestone_ref WHERE id = $1`, m3.ID)
	require.NoError(t, err)

	got, _, _, _, err := s.MilestoneAuthoring().GetMilestone(ctx, capturedM1ID)
	require.NoError(t, err, "an id captured before a sibling's rename/removal must still resolve")
	assert.Equal(t, capturedM1ID, got.ID)
	assert.Equal(t, "M1", got.Name, "M1's own name must be untouched by a sibling's rename")
	require.NotNil(t, got.Outcome)
	assert.Equal(t, "ship M1's outcome", *got.Outcome, "M1's own content must be untouched by a sibling's rename/removal")

	// M2 itself must still resolve by its own captured id, just with the
	// new name -- renaming does not mint a new identity.
	gotM2, _, _, _, err := s.MilestoneAuthoring().GetMilestone(ctx, m2.ID)
	require.NoError(t, err)
	assert.Equal(t, m2.ID, gotM2.ID)
	assert.Equal(t, "M2-renamed", gotM2.Name)

	// M3 was deleted outright -- its id must no longer resolve.
	_, _, _, _, err = s.MilestoneAuthoring().GetMilestone(ctx, m3.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}
