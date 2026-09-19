//go:build integration

// Real-Postgres coverage for TaskStore.DeclareDependency/ListDependencies/
// UnsatisfiedDependencies (task_dependency.go, migration 015, issue
// #2720's Testing section, FR2): declaring one dependency and several at
// once, idempotent re-declaration, self-dependency and cycle rejection,
// NFR1's cross-scope rejection, the terminal-Done claimability predicate,
// and NFR3's two-subject attribution. Shares task_integration_test.go's
// test-store/test-scope/test-world/subject helpers (issue #2719's own
// fixture), mirroring milepebble_integration_test.go's own choice to
// share milestone_authoring_integration_test.go's helpers rather than
// duplicating them. See store_integration_test.go's package doc for why
// this file only builds under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_dependency_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// createTestTask inserts a task scoped to milestoneID with a full
// five-lane sequence starting at Scaffold -- the shared shape every
// dependency test below needs, none of which cares about lane-sequence
// validation itself (that is task_integration_test.go's own job).
func createTestTask(t *testing.T, ctx context.Context, s *store.Store, scopeID, milestoneID uuid.UUID, title string, self store.Subject) store.Task {
	t.Helper()
	task, err := s.Tasks().CreateTask(ctx, store.CreateTaskParams{
		ScopeID:      scopeID,
		MilestoneID:  milestoneID,
		Title:        title,
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation, store.LaneTesting, store.LaneValidation, store.LaneDone},
		StartingLane: store.LaneScaffold,
		Acting:       self,
		OnBehalfOf:   self,
	})
	require.NoError(t, err)
	return task
}

// setTaskLane forces taskID's current_lane directly (no store method exists
// yet to advance a task through its lane sequence -- that lands in a later
// M4 task) so UnsatisfiedDependencies' terminal-Done predicate can be
// exercised against a task that has actually reached Done.
func setTaskLane(t *testing.T, ctx context.Context, db *dbtest.Postgres, taskID uuid.UUID, lane store.Lane) {
	t.Helper()
	_, err := db.Pool.Exec(ctx, `UPDATE task SET current_lane = $1 WHERE id = $2`, string(lane), taskID)
	require.NoError(t, err)
}

// dependencyIDs extracts DependsOnTaskID from each dep, for order-
// insensitive assertions.
func dependencyIDs(deps []store.TaskDependency) []uuid.UUID {
	ids := make([]uuid.UUID, len(deps))
	for i, d := range deps {
		ids[i] = d.DependsOnTaskID
	}
	return ids
}

// TestTaskDependencyStore_DeclareDependency_OneAndSeveral_Persisted is
// issue #2720's Testing section item 1: declaring one dependency, and
// declaring several in a single call, both persist all edges.
func TestTaskDependencyStore_DeclareDependency_OneAndSeveral_Persisted(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	taskA := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "A", self)
	taskB := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "B", self)
	taskC := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "C", self)
	taskD := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "D", self)

	require.NoError(t, s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID: scopeID, TaskID: taskA.ID, DependsOnTaskIDs: []uuid.UUID{taskB.ID},
		Acting: self, OnBehalfOf: self,
	}))
	deps, err := s.Tasks().ListDependencies(ctx, scopeID, taskA.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{taskB.ID}, dependencyIDs(deps), "declaring one dependency must persist exactly that edge")

	require.NoError(t, s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID: scopeID, TaskID: taskA.ID, DependsOnTaskIDs: []uuid.UUID{taskC.ID, taskD.ID},
		Acting: self, OnBehalfOf: self,
	}))
	deps, err = s.Tasks().ListDependencies(ctx, scopeID, taskA.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{taskB.ID, taskC.ID, taskD.ID}, dependencyIDs(deps), "declaring several dependencies in one call must persist every edge")
}

// TestTaskDependencyStore_DeclareDependency_Redeclare_IsNoOp is issue
// #2720's Testing section item 2: re-declaring an existing edge is
// idempotent, not an error, and does not insert a second row.
func TestTaskDependencyStore_DeclareDependency_Redeclare_IsNoOp(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	taskA := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "A", self)
	taskB := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "B", self)

	params := store.DeclareDependencyParams{
		ScopeID: scopeID, TaskID: taskA.ID, DependsOnTaskIDs: []uuid.UUID{taskB.ID},
		Acting: self, OnBehalfOf: self,
	}
	require.NoError(t, s.Tasks().DeclareDependency(ctx, params))
	require.NoError(t, s.Tasks().DeclareDependency(ctx, params), "re-declaring an existing edge must be absorbed, not an error")

	deps, err := s.Tasks().ListDependencies(ctx, scopeID, taskA.ID)
	require.NoError(t, err)
	require.Len(t, deps, 1, "a re-declared edge must not insert a second row")
	assert.Equal(t, taskB.ID, deps[0].DependsOnTaskID)
}

// TestTaskDependencyStore_DeclareDependency_SelfDependency_Rejected is
// issue #2720's Testing section item 3's first half: a task declaring a
// dependency on itself is rejected with the named error, and inserts no
// row.
func TestTaskDependencyStore_DeclareDependency_SelfDependency_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	taskA := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "A", self)

	err := s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID: scopeID, TaskID: taskA.ID, DependsOnTaskIDs: []uuid.UUID{taskA.ID},
		Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrSelfDependency)

	deps, err := s.Tasks().ListDependencies(ctx, scopeID, taskA.ID)
	require.NoError(t, err)
	assert.Empty(t, deps, "a rejected self-dependency must insert no row")
}

// TestTaskDependencyStore_DeclareDependency_Cycle_Rejected is issue
// #2720's Testing section item 3's second half: declaring A depends on B,
// then B depends on A, rejects the second call with the named cycle
// error, and inserts no row for the rejected edge.
func TestTaskDependencyStore_DeclareDependency_Cycle_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	taskA := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "A", self)
	taskB := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "B", self)

	require.NoError(t, s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID: scopeID, TaskID: taskA.ID, DependsOnTaskIDs: []uuid.UUID{taskB.ID},
		Acting: self, OnBehalfOf: self,
	}))

	err := s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID: scopeID, TaskID: taskB.ID, DependsOnTaskIDs: []uuid.UUID{taskA.ID},
		Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrDependencyCycle)

	deps, err := s.Tasks().ListDependencies(ctx, scopeID, taskB.ID)
	require.NoError(t, err)
	assert.Empty(t, deps, "a rejected cyclic edge must insert no row on the declaring task")
}

// TestTaskDependencyStore_DeclareDependency_CrossScopeTaskID_Rejected is
// issue #2720's Testing section item 4 (NFR1): a task id from another
// scope -- whether as TaskID or as a DependsOnTaskIDs entry -- is
// rejected, never silently accepted across a scope boundary.
func TestTaskDependencyStore_DeclareDependency_CrossScopeTaskID_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeA := newTaskTestScope(t, ctx, db)
	scopeB := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	worldA := newTaskTestWorld(t, ctx, s, scopeA, self)
	worldB := newTaskTestWorld(t, ctx, s, scopeB, self)

	taskA := createTestTask(t, ctx, s, scopeA, worldA.milepebbleID, "A", self)
	taskInScopeB := createTestTask(t, ctx, s, scopeB, worldB.milepebbleID, "B", self)

	t.Run("depends-on id from another scope", func(t *testing.T) {
		err := s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
			ScopeID: scopeA, TaskID: taskA.ID, DependsOnTaskIDs: []uuid.UUID{taskInScopeB.ID},
			Acting: self, OnBehalfOf: self,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, store.ErrNotFound, "a depends-on id belonging to another scope must be rejected as not found in the caller's scope")
	})

	t.Run("task id itself from another scope", func(t *testing.T) {
		err := s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
			ScopeID: scopeA, TaskID: taskInScopeB.ID, DependsOnTaskIDs: []uuid.UUID{taskA.ID},
			Acting: self, OnBehalfOf: self,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, store.ErrNotFound, "a task id belonging to another scope must be rejected as not found in the caller's scope")
	})

	deps, err := s.Tasks().ListDependencies(ctx, scopeA, taskA.ID)
	require.NoError(t, err)
	assert.Empty(t, deps, "a rejected cross-scope declaration must insert no row")
}

// TestTaskDependencyStore_UnsatisfiedDependencies_NonDoneThenDone is issue
// #2720's Testing section item 5: UnsatisfiedDependencies returns the
// dependency while its own task sits in any non-Done lane, and returns
// empty once that task reaches Done.
func TestTaskDependencyStore_UnsatisfiedDependencies_NonDoneThenDone(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	taskA := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "A", self)
	taskB := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "B", self)

	require.NoError(t, s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID: scopeID, TaskID: taskA.ID, DependsOnTaskIDs: []uuid.UUID{taskB.ID},
		Acting: self, OnBehalfOf: self,
	}))

	unsatisfied, err := s.Tasks().UnsatisfiedDependencies(ctx, scopeID, taskA.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{taskB.ID}, unsatisfied, "a dependency sitting in Scaffold (non-Done) must be reported unsatisfied")

	setTaskLane(t, ctx, db, taskB.ID, store.LaneImplementation)
	unsatisfied, err = s.Tasks().UnsatisfiedDependencies(ctx, scopeID, taskA.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{taskB.ID}, unsatisfied, "a dependency in any non-Done lane must still be reported unsatisfied")

	setTaskLane(t, ctx, db, taskB.ID, store.LaneDone)
	unsatisfied, err = s.Tasks().UnsatisfiedDependencies(ctx, scopeID, taskA.ID)
	require.NoError(t, err)
	assert.Empty(t, unsatisfied, "a dependency that has reached Done must no longer be reported unsatisfied")
}

// TestTaskDependencyStore_UnsatisfiedDependencies_TwoDeps_OneDoneOneNot is
// issue #2720's Testing section item 6: a task with two dependencies, one
// Done and one not, reports exactly the not-done one.
func TestTaskDependencyStore_UnsatisfiedDependencies_TwoDeps_OneDoneOneNot(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	taskA := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "A", self)
	doneDep := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "done-dep", self)
	notDoneDep := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "not-done-dep", self)

	require.NoError(t, s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID: scopeID, TaskID: taskA.ID, DependsOnTaskIDs: []uuid.UUID{doneDep.ID, notDoneDep.ID},
		Acting: self, OnBehalfOf: self,
	}))
	setTaskLane(t, ctx, db, doneDep.ID, store.LaneDone)

	unsatisfied, err := s.Tasks().UnsatisfiedDependencies(ctx, scopeID, taskA.ID)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{notDoneDep.ID}, unsatisfied, "exactly the not-done dependency must be reported, never the Done one")
}

// TestTaskDependencyStore_DeclareDependency_RecordsBothSubjectPairs is
// issue #2720's Testing section item 7 (NFR3): both subject pairs are
// persisted NOT NULL on each `task_dependency` edge row, and a distinct
// acting/on-behalf-of pair is recorded distinctly, never collapsed into
// one.
func TestTaskDependencyStore_DeclareDependency_RecordsBothSubjectPairs(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	acting := taskTestSubject("agent-1")
	onBehalfOf := taskTestSubject("human-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, acting)

	taskA := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "A", acting)
	taskB := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "B", acting)

	require.NoError(t, s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID: scopeID, TaskID: taskA.ID, DependsOnTaskIDs: []uuid.UUID{taskB.ID},
		Acting: acting, OnBehalfOf: onBehalfOf,
	}))

	deps, err := s.Tasks().ListDependencies(ctx, scopeID, taskA.ID)
	require.NoError(t, err)
	require.Len(t, deps, 1)
	dep := deps[0]

	require.NotEmpty(t, dep.CreatedByActing.Sub, "NFR3: the acting subject must be recorded")
	assert.Equal(t, acting, dep.CreatedByActing)
	require.NotEmpty(t, dep.CreatedByOnBehalfOf.Sub, "NFR3: the on-behalf-of subject must be recorded")
	assert.Equal(t, onBehalfOf, dep.CreatedByOnBehalfOf)
	assert.NotEqual(t, dep.CreatedByActing, dep.CreatedByOnBehalfOf, "a distinct acting/on-behalf-of pair must be recorded distinctly, never collapsed into one")

	// Both subject-pair column sets are NOT NULL at the DB layer too --
	// confirm no column silently accepted NULL.
	var actingSub, onBehalfOfSub string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT created_by_acting_sub, created_by_on_behalf_of_sub FROM task_dependency WHERE id = $1
	`, dep.ID).Scan(&actingSub, &onBehalfOfSub))
	assert.Equal(t, "agent-1", actingSub)
	assert.Equal(t, "human-1", onBehalfOfSub)
}
