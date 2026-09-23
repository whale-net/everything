//go:build integration

// Real-Postgres coverage for TaskStore.CreateTask (task.go, migration
// 015, issue #2719's Testing section, FR1): the two allowed creation
// shapes (milepebble, uncut milestone), the one rejected shape (a
// milestone with a milepebble already cut, ErrMilestoneHasMilepebbleCut),
// NFR7's "never a Feature/Requirement id" rejection, every lane_sequence
// validation branch (out of order, duplicate, empty, starting lane not a
// member, and a lane-skipping sequence accepted), and NFR1/NFR3's
// scope_id/two-subject attribution, plus ListTasksByMilestone (issue
// #2941). See store_integration_test.go's
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
	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, cut.ID, "MP1", "a slice of M-cut", self, self)
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
}
