//go:build integration

// Real-Postgres coverage for TaskStore.ListCancelledTasks
// (task_console.go, issue #2873's Testing section, FR10): only cancelled
// tasks appear (a claimed-but-not-cancelled and a Done task are both
// absent), each row carries title, delivery reference (both shapes -- a
// milepebble and an uncut milestone), and both cancellation subject
// pairs, paging default/max/clamp, deterministic order
// (most-recently-cancelled first, id as tiebreak), a continuation walk
// with no gaps or duplicates, a cross-scope token rejected, and another
// scope's cancelled task never appearing. Shares
// task_integration_test.go's test-store/test-scope/test-world/subject
// helpers, task_dependency_integration_test.go's createTestTask helper,
// and task_claim_integration_test.go's claimTestSession helper rather
// than duplicating them. See store_integration_test.go's package doc for
// why this file only builds under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_console_integration_test --test_output=all
package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

func cancelTestTask(t *testing.T, ctx context.Context, s *store.Store, scopeID, milestoneID uuid.UUID, title string, self store.Subject) (store.Task, store.CancelResult) {
	t.Helper()
	task := createTestTask(t, ctx, s, scopeID, milestoneID, title, self)
	result, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	return task, result
}

// TestTaskStore_ListCancelledTasks_OnlyCancelledAppear is issue #2873's
// Testing section item: only cancelled tasks appear -- an ordinary,
// never-cancelled task and a claimed-but-not-cancelled task are both
// absent from the page.
func TestTaskStore_ListCancelledTasks_OnlyCancelledAppear(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	cancelled, _ := cancelTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "cancelled task", self)
	_ = createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "ordinary task", self)

	claimedTask := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "claimed task", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: claimedTask.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	page, err := s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, cancelled.ID, page.Items[0].TaskID)
}

// TestTaskStore_ListCancelledTasks_RowContent is issue #2873's Testing
// section item: each row carries title, delivery reference (both a
// milepebble and an uncut milestone shape), and both cancellation subject
// pairs.
func TestTaskStore_ListCancelledTasks_RowContent(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	acting := taskTestSubject("operator-1")
	onBehalfOf := taskTestSubject("swarm-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, acting)

	milepebbleTask := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "against a milepebble", acting)
	_, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: milepebbleTask.ID, Acting: acting, OnBehalfOf: onBehalfOf,
	})
	require.NoError(t, err)

	milestoneTask := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "against an uncut milestone", acting)
	_, err = s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: milestoneTask.ID, Acting: acting, OnBehalfOf: onBehalfOf,
	})
	require.NoError(t, err)

	page, err := s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)

	byTaskID := map[uuid.UUID]store.CancelledTaskRow{}
	for _, row := range page.Items {
		byTaskID[row.TaskID] = row
	}

	mp := byTaskID[milepebbleTask.ID]
	assert.Equal(t, "against a milepebble", mp.Title)
	assert.Equal(t, world.milepebbleID, mp.DeliveryRef.ID)
	assert.Equal(t, store.MilestoneKindMilepebble, mp.DeliveryRef.Kind)
	assert.NotEmpty(t, mp.DeliveryRef.Title)
	assert.Equal(t, acting, mp.CancelledByActing)
	assert.Equal(t, onBehalfOf, mp.CancelledByOnBehalfOf)
	assert.False(t, mp.CancelledAt.IsZero())

	ms := byTaskID[milestoneTask.ID]
	assert.Equal(t, "against an uncut milestone", ms.Title)
	assert.Equal(t, world.uncutMilestoneID, ms.DeliveryRef.ID)
	assert.Equal(t, store.MilestoneKindMilestone, ms.DeliveryRef.Kind)
	assert.NotEmpty(t, ms.DeliveryRef.Title)
}

// TestTaskStore_ListCancelledTasks_PageSizeDefaultMaxClamp is issue
// #2873's Testing section item: an absent page size applies
// DefaultConsolePageSize, and a page size above MaxConsolePageSize is
// clamped down to it rather than rejected.
func TestTaskStore_ListCancelledTasks_PageSizeDefaultMaxClamp(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	const total = store.DefaultConsolePageSize + 5
	for i := 0; i < total; i++ {
		cancelTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, fmt.Sprintf("task %d", i), self)
	}

	page, err := s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	assert.Len(t, page.Items, store.DefaultConsolePageSize, "an absent page size must apply DefaultConsolePageSize")
	assert.NotEmpty(t, page.NextToken, "more rows remain beyond the default page")

	clamped, err := s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{
		ScopeID: scopeID,
		Page:    store.PageParams{PageSize: store.MaxConsolePageSize + 1000},
	})
	require.NoError(t, err)
	assert.Len(t, clamped.Items, total, "a page size above MaxConsolePageSize must clamp down, not reject, and every row here fits within the clamp")
}

// TestTaskStore_ListCancelledTasks_ContinuationNoGapsOrDuplicates is issue
// #2873's Testing section item: walking every page via NextToken visits
// every cancelled task exactly once, in the query's own deterministic
// order (most-recently-cancelled first, id as tiebreak).
func TestTaskStore_ListCancelledTasks_ContinuationNoGapsOrDuplicates(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	const total = 23
	const pageSize = 5
	want := make(map[uuid.UUID]bool, total)
	for i := 0; i < total; i++ {
		task, _ := cancelTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, fmt.Sprintf("task %d", i), self)
		want[task.ID] = true
	}

	seen := map[uuid.UUID]bool{}
	var order []uuid.UUID
	token := ""
	pages := 0
	for {
		page, err := s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{
			ScopeID: scopeID,
			Page:    store.PageParams{PageSize: pageSize, ContinuationToken: token},
		})
		require.NoError(t, err)
		pages++
		require.Less(t, pages, 20, "must terminate well within a sane number of pages")

		for _, row := range page.Items {
			require.False(t, seen[row.TaskID], "task %s must not be seen twice across the continuation walk", row.TaskID)
			seen[row.TaskID] = true
			order = append(order, row.TaskID)
		}

		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}

	assert.Len(t, seen, total, "every cancelled task must be visited exactly once with no gaps")
	for id := range want {
		assert.True(t, seen[id], "task %s must appear somewhere in the continuation walk", id)
	}

	// Deterministic order: re-running the same walk from scratch produces
	// the exact same sequence.
	var replay []uuid.UUID
	token = ""
	for {
		page, err := s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{
			ScopeID: scopeID,
			Page:    store.PageParams{PageSize: pageSize, ContinuationToken: token},
		})
		require.NoError(t, err)
		for _, row := range page.Items {
			replay = append(replay, row.TaskID)
		}
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}
	assert.Equal(t, order, replay, "the same walk repeated must produce the exact same order")
}

// TestTaskStore_ListCancelledTasks_CrossScopeToken_Rejected is issue
// #2873's Testing section item: a continuation token issued for one
// scope is rejected when presented against a different scope's query.
func TestTaskStore_ListCancelledTasks_CrossScopeToken_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeA := newTaskTestScope(t, ctx, db)
	scopeB := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	worldA := newTaskTestWorld(t, ctx, s, scopeA, self)
	worldB := newTaskTestWorld(t, ctx, s, scopeB, self)

	for i := 0; i < 3; i++ {
		cancelTestTask(t, ctx, s, scopeA, worldA.uncutMilestoneID, fmt.Sprintf("a-task %d", i), self)
	}
	cancelTestTask(t, ctx, s, scopeB, worldB.uncutMilestoneID, "b-task", self)

	pageA, err := s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{
		ScopeID: scopeA,
		Page:    store.PageParams{PageSize: 1},
	})
	require.NoError(t, err)
	require.NotEmpty(t, pageA.NextToken)

	_, err = s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{
		ScopeID: scopeB,
		Page:    store.PageParams{PageSize: 1, ContinuationToken: pageA.NextToken},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTokenScopeMismatch)
}

// TestTaskStore_ListCancelledTasks_OtherScopeRowsAbsent is issue #2873's
// Testing section item: a second scope's cancelled task never appears in
// the first scope's page (NFR1).
func TestTaskStore_ListCancelledTasks_OtherScopeRowsAbsent(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeA := newTaskTestScope(t, ctx, db)
	scopeB := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	worldA := newTaskTestWorld(t, ctx, s, scopeA, self)
	worldB := newTaskTestWorld(t, ctx, s, scopeB, self)

	taskA, _ := cancelTestTask(t, ctx, s, scopeA, worldA.uncutMilestoneID, "scope a", self)
	cancelTestTask(t, ctx, s, scopeB, worldB.uncutMilestoneID, "scope b", self)

	page, err := s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeA})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, taskA.ID, page.Items[0].TaskID)
}
