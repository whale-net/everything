//go:build integration

// Real-Postgres coverage for TaskStore.CreateTask (task.go, migration
// 015, issue #2719's Testing section, FR1): the two allowed creation
// shapes (milepebble, uncut milestone), the one rejected shape (a
// milestone with a milepebble already cut, ErrMilestoneHasMilepebbleCut),
// NFR7's "never a Feature/Requirement id" rejection, every lane_sequence
// validation branch (out of order, duplicate, empty, starting lane not a
// member, and a lane-skipping sequence accepted), and NFR1/NFR3's
// scope_id/two-subject attribution, plus ListTasksByMilestone (issue
// #2941) and the product-wide reads over them -- ListProductTasks (FR
// cfcd1104) and SummarizeProductTaskProgress (FR 59f664ff: per-container
// totals, per-lane counts, cancelled counting, the backlog-bucket
// exclusion, the incomplete-container predicate, and the aggregate's
// empty-vs-unknown-product disambiguation). See store_integration_test.go's
// package doc for why this file only builds under the "integration" build
// tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"fmt"
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

func newTaskTestStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
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

func newTaskTestScope(t *testing.T, ctx context.Context, db *dbtest.Postgres) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "scope-task-"+uuid.NewString()).Scan(&id))
	return id
}

func taskTestSubject(sub string) store.Subject {
	return store.Subject{Iss: "https://issuer.example.com", Sub: sub, Kind: store.SubjectKindService}
}

// taskTestWorld seeds a Product, a milestone with no milepebble cut
// (uncut), a milepebble cut from a second milestone (cut), and a
// Feature+Requirement pair -- the fixture shared by every CreateTask test
// below.
type taskTestWorld struct {
	scopeID          uuid.UUID
	uncutMilestoneID uuid.UUID
	cutMilestoneID   uuid.UUID
	milepebbleID     uuid.UUID
	featureID        uuid.UUID
	requirementID    uuid.UUID
}

func newTaskTestWorld(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID, self store.Subject) taskTestWorld {
	t.Helper()

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	uncut, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M-uncut", "ship it", nil, self, self)
	require.NoError(t, err)

	cut, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M-cut", "ship it too", nil, self, self)
	require.NoError(t, err)
	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, cut.ID, "MP1", "a slice of M-cut", nil, self, self)
	require.NoError(t, err)

	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	requirement, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "FR1", nil)
	require.NoError(t, err)

	return taskTestWorld{
		scopeID:          scopeID,
		uncutMilestoneID: uncut.ID,
		cutMilestoneID:   cut.ID,
		milepebbleID:     milepebble.ID,
		featureID:        feature.ID,
		requirementID:    requirement.ID,
	}
}

func defaultLaneParams(scopeID, milestoneID uuid.UUID, acting, onBehalfOf store.Subject) store.CreateTaskParams {
	return store.CreateTaskParams{
		ScopeID:      scopeID,
		MilestoneID:  milestoneID,
		Title:        "do the thing",
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation, store.LaneTesting, store.LaneValidation, store.LaneDone},
		StartingLane: store.LaneScaffold,
		Acting:       acting,
		OnBehalfOf:   onBehalfOf,
	}
}

// TestTaskStore_CreateTask_AgainstMilepebble_Persisted is issue #2719's
// Testing section item 1: a task scoped to a milepebble is persisted with
// the given lane sequence and starting lane.
func TestTaskStore_CreateTask_AgainstMilepebble_Persisted(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	params := defaultLaneParams(scopeID, world.milepebbleID, self, self)
	task, err := s.Tasks().CreateTask(ctx, params)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, task.ID)
	assert.Equal(t, world.milepebbleID, task.MilestoneID)
	assert.Equal(t, params.LaneSequence, task.LaneSequence)
	assert.Equal(t, store.LaneScaffold, task.CurrentLane)
	assert.Equal(t, 0, task.AttemptCount)
	assert.Nil(t, task.CurrentClaimID)
	assert.Nil(t, task.LeaseExpiresAt)
}

// TestTaskStore_CreateTask_AgainstUncutMilestone_Succeeds is issue #2719's
// Testing section item 2: a task scoped directly to a milestone with no
// milepebble cut succeeds (FR1's second allowed shape).
func TestTaskStore_CreateTask_AgainstUncutMilestone_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	params := defaultLaneParams(scopeID, world.uncutMilestoneID, self, self)
	task, err := s.Tasks().CreateTask(ctx, params)
	require.NoError(t, err)
	assert.Equal(t, world.uncutMilestoneID, task.MilestoneID)
}

// TestTaskStore_CreateTask_AgainstMilestoneWithMilepebbleCut_Rejected is
// issue #2719's Testing section item 3: a task scoped directly to a
// milestone that already has a milepebble cut is rejected with the named
// error (FR1), and inserts no row.
func TestTaskStore_CreateTask_AgainstMilestoneWithMilepebbleCut_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	params := defaultLaneParams(scopeID, world.cutMilestoneID, self, self)
	_, err := s.Tasks().CreateTask(ctx, params)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrMilestoneHasMilepebbleCut, "a milestone with a milepebble cut must be rejected, directing the caller to scope to the milepebble instead")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task WHERE scope_id = $1`, scopeID).Scan(&count))
	assert.Equal(t, 0, count, "a rejected CreateTask must insert no row")
}

// TestTaskStore_CreateTask_FeatureOrRequirementID_Rejected is issue
// #2719's Testing section item 4 (NFR7): a Feature or Requirement id
// passed as MilestoneID names no milestone_ref row at all, so it is
// rejected as ErrNotFound -- the delivery-axis reference must always be a
// milestone or milepebble, never a spec-axis entity.
func TestTaskStore_CreateTask_FeatureOrRequirementID_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	for name, id := range map[string]uuid.UUID{"feature": world.featureID, "requirement": world.requirementID} {
		t.Run(name, func(t *testing.T) {
			params := defaultLaneParams(scopeID, id, self, self)
			_, err := s.Tasks().CreateTask(ctx, params)
			require.Error(t, err)
			assert.ErrorIs(t, err, store.ErrNotFound, "a %s id must be rejected as ErrNotFound -- NFR7 forbids a task pointing at the spec chain directly", name)
		})
	}

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task WHERE scope_id = $1`, scopeID).Scan(&count))
	assert.Equal(t, 0, count, "a rejected CreateTask must insert no row")
}

// TestTaskStore_CreateTask_InvalidLaneSequence_Rejected is issue #2719's
// Testing section item 5: an out-of-order sequence, a duplicate, an empty
// sequence, and a starting lane that is not a member of the sequence are
// each rejected, and none inserts a row.
func TestTaskStore_CreateTask_InvalidLaneSequence_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	cases := map[string]struct {
		seq       []store.Lane
		starting  store.Lane
		wantErrIs error
	}{
		"out of canonical order": {
			seq:       []store.Lane{store.LaneTesting, store.LaneImplementation},
			starting:  store.LaneTesting,
			wantErrIs: store.ErrInvalidLaneSequence,
		},
		"duplicate lane": {
			seq:       []store.Lane{store.LaneScaffold, store.LaneScaffold, store.LaneTesting},
			starting:  store.LaneScaffold,
			wantErrIs: store.ErrInvalidLaneSequence,
		},
		"empty sequence": {
			seq:       []store.Lane{},
			starting:  store.LaneScaffold,
			wantErrIs: store.ErrInvalidLaneSequence,
		},
		"starting lane not in sequence": {
			seq:       []store.Lane{store.LaneScaffold, store.LaneImplementation},
			starting:  store.LaneTesting,
			wantErrIs: store.ErrStartingLaneNotInSequence,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			params := store.CreateTaskParams{
				ScopeID:      scopeID,
				MilestoneID:  world.uncutMilestoneID,
				Title:        "do the thing",
				LaneSequence: tc.seq,
				StartingLane: tc.starting,
				Acting:       self,
				OnBehalfOf:   self,
			}
			_, err := s.Tasks().CreateTask(ctx, params)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.wantErrIs)
		})
	}

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task WHERE scope_id = $1`, scopeID).Scan(&count))
	assert.Equal(t, 0, count, "every rejected CreateTask call above must insert no row")
}

// TestTaskStore_CreateTask_LaneSkippingSequence_Accepted is issue #2719's
// Testing section item 6: a lane-skipping sequence starting mid-sequence
// (e.g. a docs-only task that starts at Testing) is accepted.
func TestTaskStore_CreateTask_LaneSkippingSequence_Accepted(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	params := store.CreateTaskParams{
		ScopeID:      scopeID,
		MilestoneID:  world.uncutMilestoneID,
		Title:        "docs-only change",
		LaneSequence: []store.Lane{store.LaneTesting, store.LaneValidation, store.LaneDone},
		StartingLane: store.LaneTesting,
		Acting:       self,
		OnBehalfOf:   self,
	}
	task, err := s.Tasks().CreateTask(ctx, params)
	require.NoError(t, err)
	assert.Equal(t, store.LaneTesting, task.CurrentLane)
	assert.Equal(t, params.LaneSequence, task.LaneSequence)
}

// TestTaskStore_CreateTask_RecordsBothSubjectPairs is issue #2719's
// Testing section item 7 (NFR3): both subject pairs are persisted NOT
// NULL on the `task` row, and a distinct acting/on-behalf-of pair is
// recorded distinctly, never collapsed into one.
func TestTaskStore_CreateTask_RecordsBothSubjectPairs(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	acting := taskTestSubject("agent-1")
	onBehalfOf := taskTestSubject("human-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, acting)

	params := defaultLaneParams(scopeID, world.uncutMilestoneID, acting, onBehalfOf)
	task, err := s.Tasks().CreateTask(ctx, params)
	require.NoError(t, err)

	require.NotEmpty(t, task.CreatedByActing.Sub, "NFR3: the acting subject must be recorded")
	assert.Equal(t, acting, task.CreatedByActing)
	require.NotEmpty(t, task.CreatedByOnBehalfOf.Sub, "NFR3: the on-behalf-of subject must be recorded")
	assert.Equal(t, onBehalfOf, task.CreatedByOnBehalfOf)
	assert.NotEqual(t, task.CreatedByActing, task.CreatedByOnBehalfOf, "a distinct acting/on-behalf-of pair must be recorded distinctly, never collapsed into one")

	// Both subject-pair column sets are NOT NULL at the DB layer too --
	// confirm no column silently accepted NULL.
	var actingSub, onBehalfOfSub string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT created_by_acting_sub, created_by_on_behalf_of_sub FROM task WHERE id = $1
	`, task.ID).Scan(&actingSub, &onBehalfOfSub))
	assert.Equal(t, "agent-1", actingSub)
	assert.Equal(t, "human-1", onBehalfOfSub)
}

// TestTaskStore_CreateTask_ScopeIDRecorded is issue #2719's Testing
// section item 7 (NFR1): the created task's scope_id is exactly the
// caller's scope, and a second scope's task never appears in a query
// filtered by the first.
func TestTaskStore_CreateTask_ScopeIDRecorded(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeA := newTaskTestScope(t, ctx, db)
	scopeB := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	worldA := newTaskTestWorld(t, ctx, s, scopeA, self)
	_ = newTaskTestWorld(t, ctx, s, scopeB, self)

	params := defaultLaneParams(scopeA, worldA.uncutMilestoneID, self, self)
	task, err := s.Tasks().CreateTask(ctx, params)
	require.NoError(t, err)
	assert.Equal(t, scopeA, task.ScopeID)

	var countInScopeB int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task WHERE scope_id = $1`, scopeB).Scan(&countInScopeB))
	assert.Equal(t, 0, countInScopeB, "a task created in scope A must never show up when querying scope B")
}

// TestTaskStore_ListTasksByMilestone covers issue #2941's discovery read:
// an empty milestone lists as an empty slice (not an error), tasks list
// oldest-created first under their own milestone_id only (milepebble vs
// uncut milestone), and HasLiveClaim flips once a task is claimed.
func TestTaskStore_ListTasksByMilestone(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	t.Run("no tasks lists empty", func(t *testing.T) {
		got, err := s.Tasks().ListTasksByMilestone(ctx, world.milepebbleID)
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Empty(t, got)

		got, err = s.Tasks().ListTasksByMilestone(ctx, uuid.New())
		require.NoError(t, err)
		assert.Empty(t, got, "an unknown milestone id lists empty, not an error")
	})

	var pebbleTaskIDs []uuid.UUID
	for _, title := range []string{"first", "second", "third"} {
		params := defaultLaneParams(scopeID, world.milepebbleID, self, self)
		params.Title = title
		task, err := s.Tasks().CreateTask(ctx, params)
		require.NoError(t, err)
		pebbleTaskIDs = append(pebbleTaskIDs, task.ID)
	}
	uncutParams := defaultLaneParams(scopeID, world.uncutMilestoneID, self, self)
	uncutParams.Title = "uncut"
	uncutTask, err := s.Tasks().CreateTask(ctx, uncutParams)
	require.NoError(t, err)

	t.Run("milepebble tasks in creation order, unclaimed", func(t *testing.T) {
		got, err := s.Tasks().ListTasksByMilestone(ctx, world.milepebbleID)
		require.NoError(t, err)
		require.Len(t, got, 3)
		for i, summary := range got {
			assert.Equal(t, pebbleTaskIDs[i], summary.ID)
			assert.Equal(t, []string{"first", "second", "third"}[i], summary.Title)
			assert.Equal(t, store.LaneScaffold, summary.CurrentLane)
			assert.Equal(t, 0, summary.AttemptCount)
			assert.False(t, summary.HasLiveClaim)
		}
	})

	t.Run("uncut milestone lists only its own task", func(t *testing.T) {
		got, err := s.Tasks().ListTasksByMilestone(ctx, world.uncutMilestoneID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, uncutTask.ID, got[0].ID)

		got, err = s.Tasks().ListTasksByMilestone(ctx, world.cutMilestoneID)
		require.NoError(t, err)
		assert.Empty(t, got, "a milepebble's tasks never list under its parent milestone")
	})

	t.Run("claim sets HasLiveClaim", func(t *testing.T) {
		sessionID, err := store.NewSessionStore(db.Pool).InitSession(ctx, scopeID, self, self, nil)
		require.NoError(t, err)
		_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
			ScopeID: scopeID, TaskID: pebbleTaskIDs[1], SessionID: sessionID,
			Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)

		got, err := s.Tasks().ListTasksByMilestone(ctx, world.milepebbleID)
		require.NoError(t, err)
		require.Len(t, got, 3)
		assert.False(t, got[0].HasLiveClaim)
		assert.True(t, got[1].HasLiveClaim)
		assert.Equal(t, 0, got[1].AttemptCount, "a claim never moves attempt_count -- only lapsed/abandoned attempts count")
		assert.False(t, got[2].HasLiveClaim)
	})

	t.Run("claim, lease, escalation and cancel state", func(t *testing.T) {
		escalationID := uuid.New()
		_, err := db.Pool.Exec(ctx, `UPDATE task SET current_escalation_id = $1 WHERE id = $2`, escalationID, pebbleTaskIDs[2])
		require.NoError(t, err)
		_, err = db.Pool.Exec(ctx, `UPDATE task SET cancelled_at = NOW() WHERE id = $1`, pebbleTaskIDs[0])
		require.NoError(t, err)

		got, err := s.Tasks().ListTasksByMilestone(ctx, world.milepebbleID)
		require.NoError(t, err)
		require.Len(t, got, 3)

		// Unclaimed, cancelled task.
		assert.Nil(t, got[0].CurrentClaimID)
		assert.Nil(t, got[0].LeaseExpiresAt)
		assert.Nil(t, got[0].CurrentEscalationID)
		require.NotNil(t, got[0].CancelledAt)

		// Claimed task carries the row's claim id and lease expiry.
		var rowClaimID uuid.UUID
		var rowLease time.Time
		require.NoError(t, db.Pool.QueryRow(ctx,
			`SELECT current_claim_id, lease_expires_at FROM task WHERE id = $1`, pebbleTaskIDs[1]).Scan(&rowClaimID, &rowLease))
		require.NotNil(t, got[1].CurrentClaimID)
		assert.Equal(t, rowClaimID, *got[1].CurrentClaimID)
		require.NotNil(t, got[1].LeaseExpiresAt)
		assert.True(t, rowLease.Equal(*got[1].LeaseExpiresAt))
		assert.Nil(t, got[1].CurrentEscalationID)
		assert.Nil(t, got[1].CancelledAt)

		// Escalated task.
		assert.Nil(t, got[2].CurrentClaimID)
		assert.Nil(t, got[2].LeaseExpiresAt)
		require.NotNil(t, got[2].CurrentEscalationID)
		assert.Equal(t, escalationID, *got[2].CurrentEscalationID)
		assert.Nil(t, got[2].CancelledAt)
	})
}

// ============================================================================
// ListProductTasks (task_product_list.go, FR cfcd1104)
// ============================================================================

// productTaskFixture is the delivery world ListProductTasks' tests below
// share: one product holding an uncut milestone and a cut milestone (with
// its milepebble), plus a second product in the same scope so the
// cross-product refusal can be exercised without inventing a new scope.
//
// Milestones are created in fixture order, so positions ascend with it --
// the read orders position DESCENDING, so the cut milestone's rows come
// before the uncut milestone's even though the uncut one is older.
type productTaskFixture struct {
	productID      uuid.UUID
	otherProductID uuid.UUID

	uncutID  uuid.UUID
	cutID    uuid.UUID
	pebbleID uuid.UUID

	otherMilestoneID uuid.UUID
}

func newProductTaskFixture(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID, self store.Subject) productTaskFixture {
	t.Helper()

	product, err := s.Products().Create(ctx, scopeID, "Console Product", "vision")
	require.NoError(t, err)
	other, err := s.Products().Create(ctx, scopeID, "Other Product", "another vision")
	require.NoError(t, err)

	uncut, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "uncut", nil, self, self)
	require.NoError(t, err)
	cut, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M2", "cut", nil, self, self)
	require.NoError(t, err)
	pebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, cut.ID, "MP1", "a slice", nil, self, self)
	require.NoError(t, err)
	otherMilestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, other.ID, "M1", "theirs", nil, self, self)
	require.NoError(t, err)

	return productTaskFixture{
		productID:        product.ID,
		otherProductID:   other.ID,
		uncutID:          uncut.ID,
		cutID:            cut.ID,
		pebbleID:         pebble.ID,
		otherMilestoneID: otherMilestone.ID,
	}
}

// createProductTask is this file's own task-creation helper. It is
// deliberately NOT task_dependency_integration_test.go's createTestTask:
// each bazel target compiles one test file against its own srcs, so a
// helper defined in that file is simply absent from this target's
// compilation unit -- and defining createTestTask here too would collide
// the moment the package is built whole.
func createProductTask(t *testing.T, ctx context.Context, s *store.Store, scopeID, milestoneID uuid.UUID, title string, self store.Subject) store.Task {
	t.Helper()
	return createLaneTask(t, ctx, s, scopeID, milestoneID, title, store.LaneScaffold, self)
}

// createLaneTask creates a task starting in a caller-chosen lane, so the
// lane filter has more than one lane to choose between.
func createLaneTask(t *testing.T, ctx context.Context, s *store.Store, scopeID, milestoneID uuid.UUID, title string, lane store.Lane, self store.Subject) store.Task {
	t.Helper()
	task, err := s.Tasks().CreateTask(ctx, store.CreateTaskParams{
		ScopeID:      scopeID,
		MilestoneID:  milestoneID,
		Title:        title,
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation, store.LaneTesting, store.LaneValidation, store.LaneDone},
		StartingLane: lane,
		Acting:       self,
		OnBehalfOf:   self,
	})
	require.NoError(t, err)
	return task
}

// setContainerStatus records one real status transition for a delivery
// container -- the only way a container's current status is ever set, and
// exactly the append-only history ListProductTasks derives from.
func setContainerStatus(t *testing.T, ctx context.Context, s *store.Store, scopeID, containerID uuid.UUID, status store.MilestoneStatus, self store.Subject) {
	t.Helper()
	_, err := s.MilestoneStatus().RecordTransition(ctx, scopeID, containerID, status, nil, self, self)
	require.NoError(t, err)
}

// claimInFreshSession claims a task from a brand-new session, the cheapest
// real path to an open claim with a lease.
func claimInFreshSession(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, scopeID, taskID uuid.UUID, self store.Subject) store.Claim {
	t.Helper()
	sessionID, err := store.NewSessionStore(db.Pool).InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: taskID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	return claim
}

// cancelTask / escalateTask are this file's own local counterparts to
// task_console_integration_test.go's cancelTestTask/escalateTestTask --
// which live in a different bazel target's compilation unit and so are not
// importable here.
func cancelTask(t *testing.T, ctx context.Context, s *store.Store, scopeID, taskID uuid.UUID, self store.Subject) {
	t.Helper()
	_, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: taskID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
}

func escalateTask(t *testing.T, ctx context.Context, s *store.Store, scopeID, taskID uuid.UUID, self store.Subject) {
	t.Helper()
	_, err := s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: taskID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
}

func listIncomplete(t *testing.T, ctx context.Context, s *store.Store, scopeID, productID uuid.UUID) store.Page[store.ProductTaskRow] {
	t.Helper()
	page, err := s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
		ScopeID:   scopeID,
		ProductID: productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
	})
	require.NoError(t, err)
	return page
}

func rowTaskIDs(page store.Page[store.ProductTaskRow]) []uuid.UUID {
	ids := make([]uuid.UUID, len(page.Items))
	for i, row := range page.Items {
		ids[i] = row.TaskID
	}
	return ids
}

func findRow(t *testing.T, page store.Page[store.ProductTaskRow], taskID uuid.UUID) store.ProductTaskRow {
	t.Helper()
	for _, row := range page.Items {
		if row.TaskID == taskID {
			return row
		}
	}
	t.Fatalf("task %s is absent from the page", taskID)
	return store.ProductTaskRow{}
}

// TestTaskStore_ListProductTasks_IncompleteScope_SpansContainersAndNamesThem
// is FR2's first shape: the product-wide default returns one page across
// every incomplete container of the product, newest-positioned milestone
// first, each row naming its milestone -- and its milepebble when the task
// was scoped to one.
func TestTaskStore_ListProductTasks_IncompleteScope_SpansContainersAndNamesThem(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	uncutTask := createProductTask(t, ctx, s, scopeID, fx.uncutID, "on the uncut milestone", self)
	pebbleTask := createProductTask(t, ctx, s, scopeID, fx.pebbleID, "on the milepebble", self)
	// Another product's task, which this product's read must never see.
	createProductTask(t, ctx, s, scopeID, fx.otherMilestoneID, "on the other product", self)

	page := listIncomplete(t, ctx, s, scopeID, fx.productID)
	require.Len(t, page.Items, 2)
	assert.Equal(t, []uuid.UUID{pebbleTask.ID, uncutTask.ID}, rowTaskIDs(page),
		"position DESCENDING puts the newer milestone's rows first, and another product's task is absent")

	pebbleRow := page.Items[0]
	assert.Equal(t, "on the milepebble", pebbleRow.Title)
	assert.Equal(t, fx.cutID, pebbleRow.Milestone.ID)
	assert.Equal(t, "M2", pebbleRow.Milestone.Name)
	assert.Equal(t, store.MilestoneStatusNotStarted, pebbleRow.Milestone.Status,
		"a container with no status event is 'not started' by derivation, never a seeded row")
	require.NotNil(t, pebbleRow.Milepebble)
	assert.Equal(t, fx.pebbleID, pebbleRow.Milepebble.ID)
	assert.Equal(t, "MP1", pebbleRow.Milepebble.Name)

	uncutRow := page.Items[1]
	assert.Equal(t, fx.uncutID, uncutRow.Milestone.ID)
	assert.Nil(t, uncutRow.Milepebble, "an uncut milestone's task names no milepebble")
}

// TestTaskStore_ListProductTasks_RowCarriesLaneStateAttemptsAndClaim is the
// row-content half of FR2: lane, derived state with its escalation reason,
// attempt count against the package cap, and the lease expiry plus the id
// of the claim that expiry belongs to -- both absent when nothing holds
// the task.
func TestTaskStore_ListProductTasks_RowCarriesLaneStateAttemptsAndClaim(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	testing_ := createLaneTask(t, ctx, s, scopeID, fx.uncutID, "in testing", store.LaneTesting, self)
	claimed := createProductTask(t, ctx, s, scopeID, fx.uncutID, "claimed", self)
	claim := claimInFreshSession(t, ctx, s, db, scopeID, claimed.ID, self)
	escalated := createProductTask(t, ctx, s, scopeID, fx.uncutID, "escalated", self)
	escalateTask(t, ctx, s, scopeID, escalated.ID, self)
	cancelled := createProductTask(t, ctx, s, scopeID, fx.uncutID, "cancelled", self)
	cancelTask(t, ctx, s, scopeID, cancelled.ID, self)

	page := listIncomplete(t, ctx, s, scopeID, fx.productID)
	require.Len(t, page.Items, 4)

	inTesting := findRow(t, page, testing_.ID)
	assert.Equal(t, store.LaneTesting, inTesting.CurrentLane)
	assert.Equal(t, store.TaskStateActive, inTesting.State)
	assert.Nil(t, inTesting.EscalationReason)
	assert.Equal(t, store.DefaultAttemptCap, inTesting.AttemptCap)
	assert.Nil(t, inTesting.ClaimID, "no open claim means no claim id")
	assert.Nil(t, inTesting.LeaseExpiresAt, "and no lease expiry")
	assert.Nil(t, inTesting.CancelledAt)

	claimedRow := findRow(t, page, claimed.ID)
	require.NotNil(t, claimedRow.ClaimID)
	assert.Equal(t, claim.ID, *claimedRow.ClaimID, "the row names the claim its lease expiry belongs to")
	require.NotNil(t, claimedRow.LeaseExpiresAt)
	assert.Equal(t, 0, claimedRow.AttemptCount,
		"attempt_count counts lapses, not claims -- a live claim has not spent one yet")

	escalatedRow := findRow(t, page, escalated.ID)
	assert.Equal(t, store.TaskStateEscalated, escalatedRow.State)
	require.NotNil(t, escalatedRow.EscalationReason)
	assert.Equal(t, store.EscalationReasonManual, *escalatedRow.EscalationReason,
		"an escalated row names why it escalated")

	cancelledRow := findRow(t, page, cancelled.ID)
	require.NotNil(t, cancelledRow.CancelledAt, "a cancelled task still appears in the ordinary read, flagged by its own timestamp")
	assert.Equal(t, store.TaskStateActive, cancelledRow.State, "cancelled is not the same axis as escalated")
}

// TestTaskStore_ListProductTasks_IncompletePredicate covers FR2's per-
// container "incomplete" rule: judged on each container's own derived
// status, shipped and abandoned drop out, partially complete stays in, and
// a shipped milestone whose milepebble is still in design keeps that
// milepebble's tasks while its own direct tasks leave.
func TestTaskStore_ListProductTasks_IncompletePredicate(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	// A milestone whose direct task is created BEFORE the cut (CreateTask
	// refuses a milestone once it is cut), then shipped, with a milepebble
	// left in design underneath it.
	shipped, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, fx.productID, "M3", "ships", nil, self, self)
	require.NoError(t, err)
	shippedDirect := createProductTask(t, ctx, s, scopeID, shipped.ID, "shipped milestone direct task", self)
	shippedPebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, shipped.ID, "MP3", "still designing", nil, self, self)
	require.NoError(t, err)
	setContainerStatus(t, ctx, s, scopeID, shipped.ID, store.MilestoneStatusShipped, self)
	setContainerStatus(t, ctx, s, scopeID, shippedPebble.ID, store.MilestoneStatusInDesign, self)
	inDesignTask := createProductTask(t, ctx, s, scopeID, shippedPebble.ID, "in-design milepebble task", self)

	abandoned, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, fx.productID, "M4", "dropped", nil, self, self)
	require.NoError(t, err)
	setContainerStatus(t, ctx, s, scopeID, abandoned.ID, store.MilestoneStatusAbandoned, self)
	abandonedTask := createProductTask(t, ctx, s, scopeID, abandoned.ID, "abandoned task", self)

	partial, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, fx.productID, "M5", "half done", nil, self, self)
	require.NoError(t, err)
	setContainerStatus(t, ctx, s, scopeID, partial.ID, store.MilestoneStatusPartiallyComplete, self)
	partialTask := createProductTask(t, ctx, s, scopeID, partial.ID, "partially complete task", self)

	page := listIncomplete(t, ctx, s, scopeID, fx.productID)
	ids := rowTaskIDs(page)

	assert.Contains(t, ids, inDesignTask.ID,
		"a shipped milestone's milepebble is judged on its OWN status, so its tasks stay in scope")
	assert.NotContains(t, ids, shippedDirect.ID,
		"the shipped milestone's own direct tasks leave scope with the milestone")
	assert.NotContains(t, ids, abandonedTask.ID, "an abandoned container is not incomplete")
	assert.Contains(t, ids, partialTask.ID, "partially complete counts as incomplete")

	row := findRow(t, page, inDesignTask.ID)
	assert.Equal(t, store.MilestoneStatusShipped, row.Milestone.Status,
		"the row still reports the milestone's own status even though the milestone itself is out of scope")
	require.NotNil(t, row.Milepebble)
	assert.Equal(t, store.MilestoneStatusInDesign, row.Milepebble.Status)
}

// TestTaskStore_ListProductTasks_MilestoneScope_IncludesMilepebbleTasks is
// FR2's single-milestone scope: whatever the milestone's own status, and
// including every milepebble's tasks beneath it, each row naming the
// milepebble it came from.
func TestTaskStore_ListProductTasks_MilestoneScope_IncludesMilepebbleTasks(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	// A shipped milestone still answers a milestone-scoped read: "whatever
	// its status" is the point of the single-container scopes.
	shipped, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, fx.productID, "M6", "ships", nil, self, self)
	require.NoError(t, err)
	direct := createProductTask(t, ctx, s, scopeID, shipped.ID, "direct", self)
	pebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, shipped.ID, "MP6", "a slice", nil, self, self)
	require.NoError(t, err)
	pebbled := createProductTask(t, ctx, s, scopeID, pebble.ID, "pebbled", self)
	secondPebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, shipped.ID, "MP7", "another slice", nil, self, self)
	require.NoError(t, err)
	otherPebbled := createProductTask(t, ctx, s, scopeID, secondPebble.ID, "other pebbled", self)
	setContainerStatus(t, ctx, s, scopeID, shipped.ID, store.MilestoneStatusShipped, self)

	// A task on a different milestone of the same product must not leak in.
	outside := createProductTask(t, ctx, s, scopeID, fx.uncutID, "elsewhere", self)

	page, err := s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
		ScopeID:   scopeID,
		ProductID: fx.productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeMilestone, ContainerID: shipped.ID},
	})
	require.NoError(t, err)
	ids := rowTaskIDs(page)
	assert.ElementsMatch(t, []uuid.UUID{direct.ID, pebbled.ID, otherPebbled.ID}, ids,
		"a milestone scope returns its own direct tasks plus every milepebble's, and nothing else")

	assert.Nil(t, findRow(t, page, direct.ID).Milepebble)
	assert.Equal(t, pebble.ID, findRow(t, page, pebbled.ID).Milepebble.ID)
	assert.Equal(t, secondPebble.ID, findRow(t, page, otherPebbled.ID).Milepebble.ID)
	assert.NotContains(t, ids, outside.ID)

	assert.Equal(t, store.MilestoneStatusShipped, findRow(t, page, direct.ID).Milestone.Status,
		"a shipped milestone's tasks are still returned by a milestone-scoped read")
}

// TestTaskStore_ListProductTasks_MilepebbleScope_IsJustThatMilepebble is
// FR2's single-milepebble scope: one container's tasks, whatever that
// container's status.
func TestTaskStore_ListProductTasks_MilepebbleScope_IsJustThatMilepebble(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	sibling, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, fx.cutID, "MP2", "a sibling slice", nil, self, self)
	require.NoError(t, err)
	mine := createProductTask(t, ctx, s, scopeID, fx.pebbleID, "mine", self)
	theirs := createProductTask(t, ctx, s, scopeID, sibling.ID, "theirs", self)
	setContainerStatus(t, ctx, s, scopeID, fx.pebbleID, store.MilestoneStatusShipped, self)

	page, err := s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
		ScopeID:   scopeID,
		ProductID: fx.productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeMilepebble, ContainerID: fx.pebbleID},
	})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{mine.ID}, rowTaskIDs(page))
	assert.NotContains(t, rowTaskIDs(page), theirs.ID, "a sibling milepebble is not this scope")
}

// TestTaskStore_ListProductTasks_BacklogBucketNeverAMilestone is FR2's
// backlog rule. CreateTask refuses to scope a task to the bucket, so the
// row is inserted directly -- proving the read's own exclusion rather than
// relying on that write-path refusal alone.
func TestTaskStore_ListProductTasks_BacklogBucketNeverAMilestone(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	backlog, err := s.Recut().GetOrCreateBacklog(ctx, scopeID, fx.productID, self, self)
	require.NoError(t, err)
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task (
			scope_id, milestone_id, title, lane_sequence, current_lane,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, 'stranded in the backlog', ARRAY['Scaffold'], 'Scaffold',
			$3, $3, $4, $3, $3, $4)
		RETURNING id
	`, scopeID, backlog.ID, self.Iss, string(self.Kind)).Scan(new(uuid.UUID)))

	ours := createProductTask(t, ctx, s, scopeID, fx.uncutID, "real work", self)
	page := listIncomplete(t, ctx, s, scopeID, fx.productID)
	assert.Equal(t, []uuid.UUID{ours.ID}, rowTaskIDs(page),
		"the backlog bucket is never a milestone in the product-wide result")
}

// TestTaskStore_ListProductTasks_OnlyStuck is FR2's only-stuck predicate:
// exactly expired-lease, at-the-cap, escalated, or cancelled -- and
// nothing else.
func TestTaskStore_ListProductTasks_OnlyStuck(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	ordinary := createProductTask(t, ctx, s, scopeID, fx.uncutID, "ordinary", self)

	liveClaim := createProductTask(t, ctx, s, scopeID, fx.uncutID, "claimed, lease still live", self)
	claimInFreshSession(t, ctx, s, db, scopeID, liveClaim.ID, self)

	expired := createProductTask(t, ctx, s, scopeID, fx.uncutID, "lease expired", self)
	claimInFreshSession(t, ctx, s, db, scopeID, expired.ID, self)
	require.NoError(t, db.Pool.QueryRow(ctx,
		`UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1 RETURNING id`,
		expired.ID).Scan(new(uuid.UUID)))

	// Seeded straight to the cap rather than driven there: every real path
	// to DefaultAttemptCap also escalates, so only a direct write isolates
	// this one arm of the predicate.
	atCap := createProductTask(t, ctx, s, scopeID, fx.uncutID, "at the attempt cap", self)
	require.NoError(t, db.Pool.QueryRow(ctx,
		`UPDATE task SET attempt_count = $1 WHERE id = $2 RETURNING id`,
		store.DefaultAttemptCap, atCap.ID).Scan(new(uuid.UUID)))

	escalated := createProductTask(t, ctx, s, scopeID, fx.uncutID, "escalated", self)
	escalateTask(t, ctx, s, scopeID, escalated.ID, self)

	cancelled := createProductTask(t, ctx, s, scopeID, fx.uncutID, "cancelled", self)
	cancelTask(t, ctx, s, scopeID, cancelled.ID, self)

	page, err := s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
		ScopeID:   scopeID,
		ProductID: fx.productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
		OnlyStuck: true,
	})
	require.NoError(t, err)
	ids := rowTaskIDs(page)
	assert.ElementsMatch(t, []uuid.UUID{expired.ID, atCap.ID, escalated.ID, cancelled.ID}, ids,
		"only-stuck keeps exactly expired-lease, at-the-cap, escalated, or cancelled")
	assert.NotContains(t, ids, ordinary.ID)
	assert.NotContains(t, ids, liveClaim.ID, "a live lease is not stuck")
}

// TestTaskStore_ListProductTasks_LaneFilter is FR2's optional lane filter:
// an absent lane means every lane, never no lanes.
func TestTaskStore_ListProductTasks_LaneFilter(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	scaffold := createLaneTask(t, ctx, s, scopeID, fx.uncutID, "scaffold work", store.LaneScaffold, self)
	testing_ := createLaneTask(t, ctx, s, scopeID, fx.uncutID, "testing work", store.LaneTesting, self)

	implementation := store.LaneImplementation
	page, err := s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
		ScopeID:   scopeID,
		ProductID: fx.productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
		Lane:      &implementation,
	})
	require.NoError(t, err)
	assert.Empty(t, page.Items, "nothing in that lane is an empty page, not a broken filter")

	testingLane := store.LaneTesting
	page, err = s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
		ScopeID:   scopeID,
		ProductID: fx.productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
		Lane:      &testingLane,
	})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{testing_.ID}, rowTaskIDs(page))
	assert.NotContains(t, rowTaskIDs(page), scaffold.ID)

	unfiltered := listIncomplete(t, ctx, s, scopeID, fx.productID)
	assert.Len(t, unfiltered.Items, 2, "an absent lane filter means every lane")
}

// TestTaskStore_ListProductTasks_PageWalkIsStableUnderCompositeOrder is
// NFR6's paging half: the composite order (milestone position descending,
// then milestone id, then task creation time, then task id) holds across
// pages of one, with no gap and no duplicate, and the final page carries
// no token.
func TestTaskStore_ListProductTasks_PageWalkIsStableUnderCompositeOrder(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	// Two tasks on the cut milestone's milepebble and two on the older,
	// lower-positioned uncut milestone -- so the walk has to cross a
	// milestone boundary mid-page-sequence to prove the composite order.
	newer := createProductTask(t, ctx, s, scopeID, fx.pebbleID, "newer-1", self)
	_ = createProductTask(t, ctx, s, scopeID, fx.pebbleID, "newer-2", self)
	older := createProductTask(t, ctx, s, scopeID, fx.uncutID, "older-1", self)
	_ = createProductTask(t, ctx, s, scopeID, fx.uncutID, "older-2", self)

	unpaged := listIncomplete(t, ctx, s, scopeID, fx.productID)
	require.Len(t, unpaged.Items, 4)
	expected := rowTaskIDs(unpaged)
	assert.Equal(t, newer.ID, expected[0], "the higher-positioned milestone's tasks come first")

	var walked []uuid.UUID
	seen := map[uuid.UUID]bool{}
	token := ""
	for i := 0; i < 10; i++ {
		res, err := s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
			ScopeID:   scopeID,
			ProductID: fx.productID,
			Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
			Page:      store.PageParams{PageSize: 1, ContinuationToken: token},
		})
		require.NoError(t, err)
		require.LessOrEqual(t, len(res.Items), 1)
		for _, row := range res.Items {
			assert.False(t, seen[row.TaskID], "task %s appeared on two pages", row.TaskID)
			seen[row.TaskID] = true
			walked = append(walked, row.TaskID)
		}
		if res.NextToken == "" {
			break
		}
		token = res.NextToken
	}
	assert.Equal(t, expected, walked, "a one-row-at-a-time walk reproduces the unpaged order exactly")
	assert.Len(t, walked, 4)
	assert.Contains(t, walked, older.ID, "the walk reached the older milestone too")
}

// TestTaskStore_ListProductTasks_ContinuationTokenBindsFilters is FR3: a
// token issued under one filter set is refused under another, rather than
// answered as a wrong-filter page or an empty one.
func TestTaskStore_ListProductTasks_ContinuationTokenBindsFilters(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	createProductTask(t, ctx, s, scopeID, fx.pebbleID, "pebbled", self)
	createLaneTask(t, ctx, s, scopeID, fx.uncutID, "scaffold work", store.LaneScaffold, self)

	first, err := s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
		ScopeID:   scopeID,
		ProductID: fx.productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
		Page:      store.PageParams{PageSize: 1},
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.NextToken)

	testingLane := store.LaneTesting
	_, err = s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
		ScopeID:   scopeID,
		ProductID: fx.productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
		Lane:      &testingLane,
		Page:      store.PageParams{PageSize: 1, ContinuationToken: first.NextToken},
	})
	require.ErrorIs(t, err, store.ErrTokenFilterMismatch)

	_, err = s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
		ScopeID:   scopeID,
		ProductID: fx.productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
		OnlyStuck: true,
		Page:      store.PageParams{PageSize: 1, ContinuationToken: first.NextToken},
	})
	require.ErrorIs(t, err, store.ErrTokenFilterMismatch)

	// The same filter set resumes cleanly.
	resumed, err := s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
		ScopeID:   scopeID,
		ProductID: fx.productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
		Page:      store.PageParams{PageSize: 1, ContinuationToken: first.NextToken},
	})
	require.NoError(t, err)
	assert.Len(t, resumed.Items, 1)
	assert.NotEqual(t, first.Items[0].TaskID, resumed.Items[0].TaskID)
}

// TestTaskStore_ListProductTasks_ContainerOutsideProduct is FR2's distinct
// refusal (LB1): a container belonging to another product -- or to no
// product at all -- is refused outright, never answered as an empty page
// and never answered with the other product's tasks.
func TestTaskStore_ListProductTasks_ContainerOutsideProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	// The other product's task is created before its milepebble is cut, since
	// CreateTask refuses a milestone once it is cut.
	createProductTask(t, ctx, s, scopeID, fx.otherMilestoneID, "theirs", self)
	otherPebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, fx.otherMilestoneID, "MP-theirs", "theirs", nil, self, self)
	require.NoError(t, err)
	createProductTask(t, ctx, s, scopeID, fx.pebbleID, "ours", self)

	for _, scope := range []store.ProductTaskScope{
		{Kind: store.ProductTaskScopeMilestone, ContainerID: fx.otherMilestoneID},
		{Kind: store.ProductTaskScopeMilepebble, ContainerID: otherPebble.ID},
		{Kind: store.ProductTaskScopeMilestone, ContainerID: uuid.New()},
	} {
		page, err := s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
			ScopeID:   scopeID,
			ProductID: fx.productID,
			Scope:     scope,
		})
		require.ErrorIs(t, err, store.ErrMilestoneOutsideProduct, "scope %+v", scope)
		assert.Empty(t, page.Items, "a refused scope yields no rows at all")
	}
}

// summarizeIncomplete reads the whole per-container progress aggregate for
// fx.productID's default (incomplete) scope.
func summarizeIncomplete(t *testing.T, ctx context.Context, s *store.Store, scopeID, productID uuid.UUID) store.ProductTaskProgress {
	t.Helper()
	progress, err := s.Tasks().SummarizeProductTaskProgress(ctx, store.ProductTaskProgressParams{
		ScopeID:   scopeID,
		ProductID: productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
	})
	require.NoError(t, err)
	return progress
}

// containerRow finds the row for one container: the milepebble named by
// milepebbleID when that is not uuid.Nil, else the milestone's own row.
func containerRow(t *testing.T, progress store.ProductTaskProgress, milestoneID, milepebbleID uuid.UUID) store.ContainerTaskProgress {
	t.Helper()
	for _, row := range progress.Containers {
		switch {
		case milepebbleID != uuid.Nil:
			if row.Milepebble != nil && row.Milepebble.ID == milepebbleID {
				return row
			}
		case row.Milepebble == nil && row.Milestone.ID == milestoneID:
			return row
		}
	}
	t.Fatalf("container %s/%s is absent from the progress aggregate", milestoneID, milepebbleID)
	return store.ContainerTaskProgress{}
}

func containerIDs(progress store.ProductTaskProgress) []uuid.UUID {
	ids := make([]uuid.UUID, len(progress.Containers))
	for i, row := range progress.Containers {
		if row.Milepebble != nil {
			ids[i] = row.Milepebble.ID
		} else {
			ids[i] = row.Milestone.ID
		}
	}
	return ids
}

// TestTaskStore_SummarizeProductTaskProgress_MilestoneRowIncludesItsMilepebbles
// is FR 59f664ff's nesting rule: a milestone's counts are the whole cut's
// -- its own direct tasks plus every one of its milepebbles' -- while the
// milepebble reports its own slice, and one call returns both rather than
// one read per container.
func TestTaskStore_SummarizeProductTaskProgress_MilestoneRowIncludesItsMilepebbles(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	// The cut milestone has no direct task (CreateTask refuses a milestone
	// once it is cut), so its whole count comes from its milepebble.
	createProductTask(t, ctx, s, scopeID, fx.pebbleID, "on the milepebble", self)
	createProductTask(t, ctx, s, scopeID, fx.pebbleID, "also on the milepebble", self)
	createProductTask(t, ctx, s, scopeID, fx.uncutID, "on the uncut milestone", self)
	createProductTask(t, ctx, s, scopeID, fx.otherMilestoneID, "theirs", self)

	progress := summarizeIncomplete(t, ctx, s, scopeID, fx.productID)
	assert.Equal(t, fx.productID, progress.ProductID)
	assert.Equal(t, []uuid.UUID{fx.uncutID, fx.cutID, fx.pebbleID}, containerIDs(progress),
		"roadmap order: M1, then M2 immediately followed by its own milepebble; another product's milestone is absent")

	cutRow := containerRow(t, progress, fx.cutID, uuid.Nil)
	assert.Equal(t, 2, cutRow.Total(),
		"the milestone's total is the whole cut's, not just its own (empty) direct slice")

	pebbleRow := containerRow(t, progress, fx.cutID, fx.pebbleID)
	assert.Equal(t, 2, pebbleRow.Total(), "the milepebble reports its own slice")
	require.NotNil(t, pebbleRow.Milepebble)
	assert.Equal(t, "MP1", pebbleRow.Milepebble.Name)
	assert.Equal(t, fx.cutID, pebbleRow.Milestone.ID, "a milepebble row also names its parent milestone")

	assert.Equal(t, 1, containerRow(t, progress, fx.uncutID, uuid.Nil).Total())
}

// TestTaskStore_SummarizeProductTaskProgress_PerLaneCountsPartitionTheTotal
// is FR 59f664ff's arithmetic: every task lands in exactly one of the five
// lane counts, they sum to the container's total, and Done IS the Done
// lane's own count -- so the breakdown and the "N of M" figure rendered
// from it cannot disagree.
func TestTaskStore_SummarizeProductTaskProgress_PerLaneCountsPartitionTheTotal(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	for _, lane := range store.CanonicalLaneOrder {
		createLaneTask(t, ctx, s, scopeID, fx.pebbleID, "in "+string(lane), lane, self)
	}
	createLaneTask(t, ctx, s, scopeID, fx.uncutID, "one more", store.LaneTesting, self)

	progress := summarizeIncomplete(t, ctx, s, scopeID, fx.productID)

	pebbleRow := containerRow(t, progress, fx.cutID, fx.pebbleID)
	assert.Equal(t, store.TaskLaneCounts{Scaffold: 1, Implementation: 1, Testing: 1, Validation: 1, Done: 1}, pebbleRow.PerLane)
	assert.Equal(t, 5, pebbleRow.Total(), "one task per lane, and the total is their sum")
	assert.Equal(t, 1, pebbleRow.Done(), "Done is the Done lane's own count")

	uncutRow := containerRow(t, progress, fx.uncutID, uuid.Nil)
	assert.Equal(t, store.TaskLaneCounts{Testing: 1}, uncutRow.PerLane)
	assert.Equal(t, 1, uncutRow.Total())
	assert.Equal(t, 0, uncutRow.Done())

	for _, row := range progress.Containers {
		sum := row.PerLane.Scaffold + row.PerLane.Implementation + row.PerLane.Testing +
			row.PerLane.Validation + row.PerLane.Done
		assert.Equal(t, row.Total(), sum, "per-lane counts partition the total for container %s", row.Milestone.Name)
	}
}

// TestTaskStore_SummarizeProductTaskProgress_EmptyContainerReportsZeroTotal
// is FR 59f664ff's zero-task case: a milestone with nothing on it is still
// a row, carrying a zero total -- which the surfaces render as "No tasks
// yet", never "0 of 0" -- rather than being dropped from the listing.
func TestTaskStore_SummarizeProductTaskProgress_EmptyContainerReportsZeroTotal(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	empty, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, fx.productID, "M3", "nothing on it yet", nil, self, self)
	require.NoError(t, err)
	createProductTask(t, ctx, s, scopeID, fx.pebbleID, "some work", self)

	progress := summarizeIncomplete(t, ctx, s, scopeID, fx.productID)
	require.Contains(t, containerIDs(progress), empty.ID, "a container with no tasks is still reported")

	row := containerRow(t, progress, empty.ID, uuid.Nil)
	assert.Equal(t, 0, row.Total())
	assert.Equal(t, 0, row.Done())
	assert.Equal(t, store.TaskLaneCounts{}, row.PerLane)
	assert.Equal(t, 0, row.Cancelled)
}

// TestTaskStore_SummarizeProductTaskProgress_CancelledCountsInTotalNotDone
// is the CancelledTaskCounting rule against real rows: a cancelled task
// counts in its container's total and in the lane it was left in, is
// reported in Cancelled, and is never counted as Done -- while the five
// per-lane counts still sum to the total.
func TestTaskStore_SummarizeProductTaskProgress_CancelledCountsInTotalNotDone(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	cancelled := createLaneTask(t, ctx, s, scopeID, fx.uncutID, "cancelled in testing", store.LaneTesting, self)
	cancelTask(t, ctx, s, scopeID, cancelled.ID, self)
	createLaneTask(t, ctx, s, scopeID, fx.uncutID, "still testing", store.LaneTesting, self)

	progress := summarizeIncomplete(t, ctx, s, scopeID, fx.productID)
	row := containerRow(t, progress, fx.uncutID, uuid.Nil)

	assert.Equal(t, store.TaskLaneCounts{Testing: 2}, row.PerLane,
		"the cancelled task stays in the lane it was left in")
	assert.Equal(t, 2, row.Total(), "a cancelled task counts in the total: the container was given that work")
	assert.Equal(t, 0, row.Done(), "cancelled is never Done")
	assert.Equal(t, 1, row.Cancelled)
	assert.Equal(t, row.Total(), row.PerLane.Total(), "the partition still sums to the total with a cancelled task in it")
}

// TestTaskStore_SummarizeProductTaskProgress_AgreesWithTheProductTaskRead is
// FR 59f664ff's cross-read agreement: both reads select containers by the
// same predicate, so each container's progress total equals the number of
// task rows ListProductTasks returns for that same container.
func TestTaskStore_SummarizeProductTaskProgress_AgreesWithTheProductTaskRead(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	createProductTask(t, ctx, s, scopeID, fx.pebbleID, "pebble one", self)
	createLaneTask(t, ctx, s, scopeID, fx.pebbleID, "pebble two", store.LaneDone, self)
	createProductTask(t, ctx, s, scopeID, fx.uncutID, "uncut", self)
	createProductTask(t, ctx, s, scopeID, fx.otherMilestoneID, "theirs", self)

	progress := summarizeIncomplete(t, ctx, s, scopeID, fx.productID)

	countFor := func(scope store.ProductTaskScope) int {
		page, err := s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
			ScopeID: scopeID, ProductID: fx.productID, Scope: scope,
		})
		require.NoError(t, err)
		return len(page.Items)
	}

	for _, row := range progress.Containers {
		var scope store.ProductTaskScope
		if row.Milepebble != nil {
			scope = store.ProductTaskScope{Kind: store.ProductTaskScopeMilepebble, ContainerID: row.Milepebble.ID}
		} else {
			scope = store.ProductTaskScope{Kind: store.ProductTaskScopeMilestone, ContainerID: row.Milestone.ID}
		}
		assert.Equal(t, countFor(scope), row.Total(),
			"container %s: the progress total and the task read's row count must agree",
			row.Milestone.Name)
	}

	// There is deliberately no whole-product sum to compare: a milestone's
	// row covers its milepebbles' tasks, so summing every row would count
	// those twice. Agreement is per container, which is what the loop above
	// checks -- a caller renders one progress bar per row, never a product
	// total derived from overlapping rows.
}

// TestTaskStore_SummarizeProductTaskProgress_ContainerOutsideProduct is
// FR 59f664ff's refusal: a container of another product is refused with the
// same distinct error the product task read uses, never answered as an
// empty aggregate that would read as "this product has no milestones".
func TestTaskStore_SummarizeProductTaskProgress_ContainerOutsideProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	otherPebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, fx.otherMilestoneID, "MP-theirs", "theirs", nil, self, self)
	require.NoError(t, err)

	for _, scope := range []store.ProductTaskScope{
		{Kind: store.ProductTaskScopeMilestone, ContainerID: fx.otherMilestoneID},
		{Kind: store.ProductTaskScopeMilepebble, ContainerID: otherPebble.ID},
		{Kind: store.ProductTaskScopeMilestone, ContainerID: uuid.New()},
	} {
		progress, err := s.Tasks().SummarizeProductTaskProgress(ctx, store.ProductTaskProgressParams{
			ScopeID: scopeID, ProductID: fx.productID, Scope: scope,
		})
		require.ErrorIs(t, err, store.ErrMilestoneOutsideProduct, "scope %+v", scope)
		assert.Empty(t, progress.Containers, "a refused scope yields no rows at all")
	}
}

// TestTaskStore_SummarizeProductTaskProgress_IncompletePredicate is the
// scope half of the cross-read agreement: the default scope selects
// containers by the same IsIncompleteContainerStatus rule the task read
// uses -- shipped and abandoned drop out (row and all), partially complete
// stays, and a milepebble is judged on its OWN status even when its
// milestone is shipped.
func TestTaskStore_SummarizeProductTaskProgress_IncompletePredicate(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	// Shipped milestone with its own direct task (created before the cut)
	// and a milepebble left in design underneath it.
	shipped, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, fx.productID, "M3", "ships", nil, self, self)
	require.NoError(t, err)
	shippedDirect := createProductTask(t, ctx, s, scopeID, shipped.ID, "shipped milestone direct task", self)
	shippedPebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, shipped.ID, "MP3", "still designing", nil, self, self)
	require.NoError(t, err)
	setContainerStatus(t, ctx, s, scopeID, shipped.ID, store.MilestoneStatusShipped, self)
	setContainerStatus(t, ctx, s, scopeID, shippedPebble.ID, store.MilestoneStatusInDesign, self)
	createProductTask(t, ctx, s, scopeID, shippedPebble.ID, "in-design milepebble task", self)

	abandoned, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, fx.productID, "M4", "dropped", nil, self, self)
	require.NoError(t, err)
	setContainerStatus(t, ctx, s, scopeID, abandoned.ID, store.MilestoneStatusAbandoned, self)

	partial, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, fx.productID, "M5", "half done", nil, self, self)
	require.NoError(t, err)
	setContainerStatus(t, ctx, s, scopeID, partial.ID, store.MilestoneStatusPartiallyComplete, self)
	createProductTask(t, ctx, s, scopeID, partial.ID, "partially complete task", self)

	progress := summarizeIncomplete(t, ctx, s, scopeID, fx.productID)
	ids := containerIDs(progress)

	assert.Contains(t, ids, shippedPebble.ID,
		"a shipped milestone's milepebble is judged on its OWN status, so it stays in scope")
	assert.NotContains(t, ids, shipped.ID, "the shipped milestone itself leaves scope")
	assert.NotContains(t, ids, abandoned.ID, "an abandoned container is not incomplete")
	assert.Contains(t, ids, partial.ID, "partially complete counts as incomplete")

	// A milepebble row still names its out-of-scope parent and that
	// parent's own status, so a progress list can badge it.
	pebbleRow := containerRow(t, progress, shipped.ID, shippedPebble.ID)
	assert.Equal(t, store.MilestoneStatusShipped, pebbleRow.Milestone.Status)
	require.NotNil(t, pebbleRow.Milepebble)
	assert.Equal(t, store.MilestoneStatusInDesign, pebbleRow.Milepebble.Status)

	assert.Equal(t, 1, containerRow(t, progress, partial.ID, uuid.Nil).Total())
	assert.Equal(t, 1, pebbleRow.Total(), "the in-design milepebble counts its own task")

	// The shipped milestone's direct task is counted by no in-scope row:
	// its own milestone is out of scope, and the milepebble never covered
	// it. The task read agrees -- that task is in neither listing.
	assert.NotContains(t, rowTaskIDs(listIncomplete(t, ctx, s, scopeID, fx.productID)), shippedDirect.ID)
}

// TestTaskStore_SummarizeProductTaskProgress_BacklogBucketIsNeverAContainer
// is the backlog half of the scope rule. The bucket is a milestone_ref row
// like any other, so the read's own kind filter is the only thing keeping
// it out -- a progress list must never show a "Backlog" progress bar for
// work that is not being delivered. CreateTask refuses to scope a task to
// the bucket, so the row is inserted directly: this proves the read's
// exclusion rather than leaning on the write-path refusal.
func TestTaskStore_SummarizeProductTaskProgress_BacklogBucketIsNeverAContainer(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	backlog, err := s.Recut().GetOrCreateBacklog(ctx, scopeID, fx.productID, self, self)
	require.NoError(t, err)
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task (
			scope_id, milestone_id, title, lane_sequence, current_lane,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, 'stranded in the backlog', ARRAY['Scaffold'], 'Scaffold',
			$3, $3, $4, $3, $3, $4)
		RETURNING id
	`, scopeID, backlog.ID, self.Iss, string(self.Kind)).Scan(new(uuid.UUID)))

	ours := createProductTask(t, ctx, s, scopeID, fx.uncutID, "real work", self)
	progress := summarizeIncomplete(t, ctx, s, scopeID, fx.productID)

	assert.NotContains(t, containerIDs(progress), backlog.ID,
		"the backlog bucket is never a container in the progress aggregate")
	assert.Equal(t, 1, containerRow(t, progress, fx.uncutID, uuid.Nil).Total(),
		"the real milestone's count is unaffected by the bucket")

	// The task read agrees, so no caller can find the stranded task under
	// one read and not the other.
	assert.Equal(t, []uuid.UUID{ours.ID}, rowTaskIDs(listIncomplete(t, ctx, s, scopeID, fx.productID)))
}

// TestTaskStore_SummarizeProductTaskProgress_NoContainers_IsNotAnUnknownProduct
// covers the aggregate's one probe. An empty Containers slice is ambiguous
// -- a product whose every container is shipped looks exactly like a
// product id that does not exist -- so the read resolves it: the former is
// an empty success, the latter ErrNotFound. A caller must never read "no
// tasks yet" where the truth is "no such product".
func TestTaskStore_SummarizeProductTaskProgress_NoContainers_IsNotAnUnknownProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	// Every container shipped, so nothing is in the incomplete scope.
	setContainerStatus(t, ctx, s, scopeID, fx.uncutID, store.MilestoneStatusShipped, self)
	setContainerStatus(t, ctx, s, scopeID, fx.cutID, store.MilestoneStatusShipped, self)
	setContainerStatus(t, ctx, s, scopeID, fx.pebbleID, store.MilestoneStatusShipped, self)

	t.Run("an existing product with no in-scope container is an empty success", func(t *testing.T) {
		progress := summarizeIncomplete(t, ctx, s, scopeID, fx.productID)
		assert.Empty(t, progress.Containers)
		assert.NotNil(t, progress.Containers, "never nil: a caller indexes it without a check")
		assert.Equal(t, fx.productID, progress.ProductID)
	})

	t.Run("a product with no current row is ErrNotFound", func(t *testing.T) {
		progress, err := s.Tasks().SummarizeProductTaskProgress(ctx, store.ProductTaskProgressParams{
			ScopeID:   scopeID,
			ProductID: uuid.New(),
			Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
		})
		require.ErrorIs(t, err, store.ErrNotFound)
		assert.Empty(t, progress.Containers, "a refused product yields no rows at all")
	})
}

// TestTaskStore_SummarizeProductTaskProgress_SingleContainerScopes is the
// two non-default scopes: a milestone scope reports the milestone AND its
// milepebbles (the whole cut, the same rows the product-wide scope would
// report for that milestone), and a milepebble scope reports only that
// milepebble. Both answer whatever the container's status is, and both leave
// the product's other containers out entirely.
func TestTaskStore_SummarizeProductTaskProgress_SingleContainerScopes(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	createProductTask(t, ctx, s, scopeID, fx.pebbleID, "pebble work", self)
	createLaneTask(t, ctx, s, scopeID, fx.pebbleID, "pebble done", store.LaneDone, self)
	createProductTask(t, ctx, s, scopeID, fx.uncutID, "elsewhere", self)
	// Shipped or not, a named container is still reported.
	setContainerStatus(t, ctx, s, scopeID, fx.cutID, store.MilestoneStatusShipped, self)
	setContainerStatus(t, ctx, s, scopeID, fx.pebbleID, store.MilestoneStatusShipped, self)

	read := func(scope store.ProductTaskScope) store.ProductTaskProgress {
		t.Helper()
		progress, err := s.Tasks().SummarizeProductTaskProgress(ctx, store.ProductTaskProgressParams{
			ScopeID: scopeID, ProductID: fx.productID, Scope: scope,
		})
		require.NoError(t, err)
		return progress
	}

	t.Run("milestone scope is the whole cut", func(t *testing.T) {
		progress := read(store.ProductTaskScope{Kind: store.ProductTaskScopeMilestone, ContainerID: fx.cutID})
		assert.Equal(t, []uuid.UUID{fx.cutID, fx.pebbleID}, containerIDs(progress))
		assert.Equal(t, 2, containerRow(t, progress, fx.cutID, uuid.Nil).Total(),
			"the milestone's own row covers its milepebbles' tasks")
		assert.Equal(t, 2, containerRow(t, progress, fx.cutID, fx.pebbleID).Total())
		assert.Equal(t, 1, containerRow(t, progress, fx.cutID, fx.pebbleID).Done())
	})

	t.Run("milepebble scope is that milepebble alone", func(t *testing.T) {
		progress := read(store.ProductTaskScope{Kind: store.ProductTaskScopeMilepebble, ContainerID: fx.pebbleID})
		assert.Equal(t, []uuid.UUID{fx.pebbleID}, containerIDs(progress))
		row := containerRow(t, progress, fx.cutID, fx.pebbleID)
		assert.Equal(t, 2, row.Total())
		require.NotNil(t, row.Milepebble)
		assert.Equal(t, store.MilestoneStatusShipped, row.Milepebble.Status, "a named container is reported whatever its status")
		assert.Equal(t, store.MilestoneStatusShipped, row.Milestone.Status)
	})

	t.Run("an unknown scope kind is refused", func(t *testing.T) {
		_, err := s.Tasks().SummarizeProductTaskProgress(ctx, store.ProductTaskProgressParams{
			ScopeID:   scopeID,
			ProductID: fx.productID,
			Scope:     store.ProductTaskScope{Kind: "everything"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown product task scope kind")
	})
}

// ============================================================================
// CountProductTasks (task_product_list.go, FR c4ab6c68)
// ============================================================================

// walkProductTasks drains every page of ListProductTasks for params and
// returns how many rows the unpaged list holds -- the "Y" that
// CountProductTasks has to report for the same params.
func walkProductTasks(t *testing.T, ctx context.Context, s *store.Store, params store.ListProductTasksParams) int {
	t.Helper()
	total, token := 0, ""
	for {
		params.Page = store.PageParams{PageSize: 2, ContinuationToken: token}
		page, err := s.Tasks().ListProductTasks(ctx, params)
		require.NoError(t, err)
		total += len(page.Items)
		if page.NextToken == "" {
			return total
		}
		token = page.NextToken
	}
}

// TestTaskStore_CountProductTasks_MatchesList_AcrossScopesAndFilters is FR
// c4ab6c68's agreement criterion for the product task read: over each of
// the three container scopes and each optional filter, the total behind
// "Showing X of Y tasks" equals the rows the unpaged list holds. The
// fixture carries more rows than one page, so a count that answered with
// the page length would fail rather than pass.
func TestTaskStore_CountProductTasks_MatchesList_AcrossScopesAndFilters(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)

	for i := 0; i < 4; i++ {
		createProductTask(t, ctx, s, scopeID, fx.uncutID, fmt.Sprintf("uncut %d", i), self)
	}
	// fx.cutID has its milepebble already cut, so CreateTask refuses a
	// task against it (task.go's NFR7); a second uncut milestone of the
	// same product carries the rows the milestone-scoped cases need.
	secondUncut, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, fx.productID, "M3", "a second uncut", nil, self, self)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		createProductTask(t, ctx, s, scopeID, secondUncut.ID, fmt.Sprintf("second %d", i), self)
	}
	createProductTask(t, ctx, s, scopeID, fx.pebbleID, "under the milepebble", self)
	// A task in another product: it must be in no figure below.
	createProductTask(t, ctx, s, scopeID, fx.otherMilestoneID, "theirs", self)
	// One lane-filtered row in a lane no other task occupies.
	createLaneTask(t, ctx, s, scopeID, fx.uncutID, "in testing", store.LaneTesting, self)
	// And one stuck row (cancelled), so only_stuck is not the whole set.
	cancelTask(t, ctx, s, scopeID, createProductTask(t, ctx, s, scopeID, secondUncut.ID, "cancelled", self).ID, self)
	// A shipped milestone's tasks leave the product-wide incomplete scope.
	setContainerStatus(t, ctx, s, scopeID, fx.otherMilestoneID, store.MilestoneStatusShipped, self)

	testingLane := store.LaneTesting
	for name, params := range map[string]store.ListProductTasksParams{
		"incomplete": {ScopeID: scopeID, ProductID: fx.productID, Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}},
		"milestone":  {ScopeID: scopeID, ProductID: fx.productID, Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeMilestone, ContainerID: secondUncut.ID}},
		"milepebble": {ScopeID: scopeID, ProductID: fx.productID, Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeMilepebble, ContainerID: fx.pebbleID}},
		"lane":       {ScopeID: scopeID, ProductID: fx.productID, Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, Lane: &testingLane},
		"only_stuck": {ScopeID: scopeID, ProductID: fx.productID, Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, OnlyStuck: true},
		"milestone_lane": {
			ScopeID: scopeID, ProductID: fx.productID,
			Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeMilestone, ContainerID: fx.uncutID},
			Lane:  &testingLane,
		},
	} {
		t.Run(name, func(t *testing.T) {
			count, err := s.Tasks().CountProductTasks(ctx, params)
			require.NoError(t, err)
			assert.Equal(t, walkProductTasks(t, ctx, s, params), count,
				"the count and the list it describes must agree for the same filters")
		})
	}

	// And the figure is a total, not a page: ten rows (4 on the uncut
	// milestone, 4 on the second -- one of them cancelled -- 1 under the
	// milepebble, 1 in another lane) against a two-row page, with the
	// other product's now-shipped task in neither.
	total, err := s.Tasks().CountProductTasks(ctx, store.ListProductTasksParams{
		ScopeID: scopeID, ProductID: fx.productID,
		Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
	})
	require.NoError(t, err)
	assert.Equal(t, 10, total)
	page, err := s.Tasks().ListProductTasks(ctx, store.ListProductTasksParams{
		ScopeID: scopeID, ProductID: fx.productID,
		Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
		Page:  store.PageParams{PageSize: 2},
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.NotEqual(t, len(page.Items), total, "the total behind 'Showing X of Y' must never be the page length")
}

// TestTaskStore_CountProductTasks_ContainerOutsideProduct is the count
// read's half of the same LB1 refusal the list makes: a container outside
// the product is refused, not answered as a total of zero -- a zero would
// read as "this milestone has no work" for a milestone the caller simply
// named wrongly.
func TestTaskStore_CountProductTasks_ContainerOutsideProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)
	createProductTask(t, ctx, s, scopeID, fx.otherMilestoneID, "theirs", self)
	// A milepebble of the other product, created before its own task
	// since CreateTask refuses a milestone once it is cut.
	otherPebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, fx.otherMilestoneID, "MP-theirs", "theirs", nil, self, self)
	require.NoError(t, err)

	for _, scope := range []store.ProductTaskScope{
		{Kind: store.ProductTaskScopeMilestone, ContainerID: fx.otherMilestoneID},
		{Kind: store.ProductTaskScopeMilepebble, ContainerID: otherPebble.ID},
		{Kind: store.ProductTaskScopeMilepebble, ContainerID: uuid.New()},
	} {
		count, err := s.Tasks().CountProductTasks(ctx, store.ListProductTasksParams{
			ScopeID: scopeID, ProductID: fx.productID, Scope: scope,
		})
		assert.ErrorIs(t, err, store.ErrMilestoneOutsideProduct, "scope %+v", scope)
		assert.Zero(t, count, "a refused scope yields no figure at all")
	}
}

// TestTaskStore_CountProductTasks_FailedQueryIsAnError is FR c4ab6c68's
// fail-don't-degrade rule on this read: a count that cannot be computed
// returns its error, so no console renders a failed total as "0 tasks".
func TestTaskStore_CountProductTasks_FailedQueryIsAnError(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	fx := newProductTaskFixture(t, ctx, s, scopeID, self)
	createProductTask(t, ctx, s, scopeID, fx.uncutID, "ours", self)

	params := store.ListProductTasksParams{
		ScopeID: scopeID, ProductID: fx.productID,
		Scope: store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
	}
	count, err := s.Tasks().CountProductTasks(ctx, params)
	require.NoError(t, err)
	require.Equal(t, 1, count, "the fixture must hold a row, or a later failure proves nothing")

	_, err = db.Pool.Exec(ctx, `ALTER TABLE task RENAME TO task_moved`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `ALTER TABLE task_moved RENAME TO task`)
	})

	_, err = s.Tasks().CountProductTasks(ctx, params)
	require.Error(t, err, "a count that could not run must fail, never answer 0")
}
