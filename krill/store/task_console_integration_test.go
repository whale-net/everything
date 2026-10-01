//go:build integration

// Real-Postgres coverage for TaskStore.ListClaimedTasks,
// TaskStore.ListCancelledTasks, and TaskStore.ListEscalatedTasks
// (task_console.go).
//
// ListClaimedTasks (issue #2916's Testing section, FR4): only currently-
// claimed tasks appear (an ordinary and a cancelled task are both absent),
// each row carries title, delivery reference (both shapes -- a milepebble
// and an uncut milestone), the live claim's own claimant session id and
// both LB4 subject pairs, current lane, a non-zero lease expiry, and
// attempt count, paging default/max/clamp, deterministic order (soonest-
// lease-to-lapse first, id as tiebreak), a continuation walk with no gaps
// or duplicates, a cross-scope token rejected, and another scope's claimed
// task never appearing.
//
// ListCancelledTasks (issue #2873's Testing section, FR10): only cancelled
// tasks appear (a claimed-but-not-cancelled and a Done task are both
// absent), each row carries title, delivery reference (both shapes -- a
// milepebble and an uncut milestone), and both cancellation subject
// pairs, paging default/max/clamp, deterministic order
// (most-recently-cancelled first, id as tiebreak), a continuation walk
// with no gaps or duplicates, a cross-scope token rejected, and another
// scope's cancelled task never appearing.
//
// ListEscalatedTasks (issue #2875's Testing section, FR5): rows appear for
// a task escalated by each of the three EscalationReasons, produced
// through the real paths (tripThrashCap, task_complete_integration_test.go;
// escalateViaAttemptCap and EscalateTask below); the thrash-cap/
// attempt-cap rows carry the correct counter/cap and manual rows carry
// neither; MostRecentVerdict is VerdictFail exactly for thrash-cap and nil
// otherwise (EscalatedTaskRow's own doc comment); summary counts
// (attempt/failing-verdict/note) are correct including seeded notes and
// verdicts; EscalatedTaskRow carries no per-attempt/per-verdict/per-note
// array (FR5/NFR6/#2851 Assumption 11); a requeued task (simulated by
// clearing current_escalation_id directly -- RequeueTask, issue #2876, is
// not yet in this branch) disappears from the view, and a cancelled task
// (never escalated) is absent; the same paging/ordering/continuation/
// cross-scope proofs as ListCancelledTasks above; and a row-size proof
// that a long attempt/verdict/note history does not grow the row.
//
// Shares task_integration_test.go's test-store/test-scope/test-world/
// subject helpers, task_dependency_integration_test.go's createTestTask
// helper, task_claim_integration_test.go's claimTestSession helper, and
// task_complete_integration_test.go's tripThrashCap helper, rather than
// duplicating them. See store_integration_test.go's package doc for why
// this file only builds under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_console_integration_test --test_output=all
package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// claimTestTask creates a task and claims it from a fresh session -- the
// cheapest real path to a claimed row, mirroring cancelTestTask/
// escalateTestTask's own shape below.
func claimTestTask(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, scopeID, milestoneID uuid.UUID, title string, self store.Subject) (store.Task, store.Claim) {
	t.Helper()
	task := createTestTask(t, ctx, s, scopeID, milestoneID, title, self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	return task, claim
}

// TestTaskStore_ListClaimedTasks_OnlyClaimedAppear is issue #2916's Testing
// section item: only currently-claimed tasks appear -- an ordinary,
// never-claimed task and a cancelled task are both absent from the page.
func TestTaskStore_ListClaimedTasks_OnlyClaimedAppear(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	claimed, _ := claimTestTask(t, ctx, s, db, scopeID, world.uncutMilestoneID, "claimed task", self)
	_ = createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "ordinary task", self)
	cancelTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "cancelled task", self)

	page, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, claimed.ID, page.Items[0].TaskID)
}

// TestTaskStore_ListClaimedTasks_RowContent is issue #2916's Testing
// section item: each row carries title, delivery reference (both a
// milepebble and an uncut milestone shape), the live claim's own claimant
// session id, both LB4 subject pairs, the claim's own claimed-since,
// current lane, a non-zero lease expiry, and attempt count.
func TestTaskStore_ListClaimedTasks_RowContent(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	acting := taskTestSubject("operator-1")
	onBehalfOf := taskTestSubject("swarm-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, acting)

	milepebbleTask := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "against a milepebble", acting)
	sessionID := claimTestSession(t, ctx, db, scopeID, acting)
	milepebbleClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: milepebbleTask.ID, SessionID: sessionID, Acting: acting, OnBehalfOf: onBehalfOf,
	})
	require.NoError(t, err)

	milestoneTask, milestoneClaim := claimTestTask(t, ctx, s, db, scopeID, world.uncutMilestoneID, "against an uncut milestone", acting)

	page, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)

	byTaskID := map[uuid.UUID]store.ClaimedTaskRow{}
	for _, row := range page.Items {
		byTaskID[row.TaskID] = row
	}

	mp := byTaskID[milepebbleTask.ID]
	assert.Equal(t, "against a milepebble", mp.Title)
	assert.Equal(t, world.milepebbleID, mp.DeliveryRef.ID)
	assert.Equal(t, store.MilestoneKindMilepebble, mp.DeliveryRef.Kind)
	assert.NotEmpty(t, mp.DeliveryRef.Title)
	assert.Equal(t, sessionID, mp.ClaimantSessionID)
	assert.Equal(t, acting, mp.ClaimantActing)
	assert.Equal(t, onBehalfOf, mp.ClaimantOnBehalfOf)
	assert.False(t, mp.ClaimedAt.IsZero())
	assert.Equal(t, mp.ClaimedAt.UTC(), milepebbleClaim.ClaimedAt.UTC(), "claimed-since is the claim's own instant, not the task's created_at")
	assert.Equal(t, store.LaneScaffold, mp.CurrentLane)
	assert.False(t, mp.LeaseExpiresAt.IsZero())
	assert.Equal(t, mp.LeaseExpiresAt.UTC(), milepebbleClaim.InitialLeaseExpiresAt.UTC())
	assert.Equal(t, 0, mp.AttemptCount)

	ms := byTaskID[milestoneTask.ID]
	assert.Equal(t, "against an uncut milestone", ms.Title)
	assert.Equal(t, world.uncutMilestoneID, ms.DeliveryRef.ID)
	assert.Equal(t, store.MilestoneKindMilestone, ms.DeliveryRef.Kind)
	assert.NotEmpty(t, ms.DeliveryRef.Title)
	assert.Equal(t, milestoneClaim.SessionID, ms.ClaimantSessionID)
}

// TestTaskStore_ListClaimedTasks_PageSizeDefaultMaxClamp is issue #2916's
// Testing section item: an absent page size applies DefaultConsolePageSize,
// and a page size above MaxConsolePageSize is clamped down to it rather
// than rejected -- mirrors TestTaskStore_ListCancelledTasks_PageSizeDefaultMaxClamp.
func TestTaskStore_ListClaimedTasks_PageSizeDefaultMaxClamp(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	const total = store.DefaultConsolePageSize + 5
	for i := 0; i < total; i++ {
		claimTestTask(t, ctx, s, db, scopeID, world.uncutMilestoneID, fmt.Sprintf("task %d", i), self)
	}

	page, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	assert.Len(t, page.Items, store.DefaultConsolePageSize, "an absent page size must apply DefaultConsolePageSize")
	assert.NotEmpty(t, page.NextToken, "more rows remain beyond the default page")

	clamped, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID: scopeID,
		Page:    store.PageParams{PageSize: store.MaxConsolePageSize + 1000},
	})
	require.NoError(t, err)
	assert.Len(t, clamped.Items, total, "a page size above MaxConsolePageSize must clamp down, not reject, and every row here fits within the clamp")
}

// TestTaskStore_ListClaimedTasks_ContinuationNoGapsOrDuplicates is issue
// #2916's Testing section item: walking every page via NextToken visits
// every claimed task exactly once, in the query's own deterministic order
// (soonest-lease-to-lapse first, id as tiebreak), and the same walk
// repeated produces the exact same order -- mirrors
// TestTaskStore_ListCancelledTasks_ContinuationNoGapsOrDuplicates.
func TestTaskStore_ListClaimedTasks_ContinuationNoGapsOrDuplicates(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	const total = 23
	const pageSize = 5
	want := make(map[uuid.UUID]bool, total)
	for i := 0; i < total; i++ {
		task, _ := claimTestTask(t, ctx, s, db, scopeID, world.uncutMilestoneID, fmt.Sprintf("task %d", i), self)
		want[task.ID] = true
	}

	seen := map[uuid.UUID]bool{}
	var order []uuid.UUID
	token := ""
	pages := 0
	for {
		page, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
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

	assert.Len(t, seen, total, "every claimed task must be visited exactly once with no gaps")
	for id := range want {
		assert.True(t, seen[id], "task %s must appear somewhere in the continuation walk", id)
	}

	var replay []uuid.UUID
	token = ""
	for {
		page, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
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

// TestTaskStore_ListClaimedTasks_CrossScopeToken_Rejected is issue #2916's
// Testing section item: a continuation token issued for one scope is
// rejected when presented against a different scope's query -- mirrors
// TestTaskStore_ListCancelledTasks_CrossScopeToken_Rejected.
func TestTaskStore_ListClaimedTasks_CrossScopeToken_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeA := newTaskTestScope(t, ctx, db)
	scopeB := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	worldA := newTaskTestWorld(t, ctx, s, scopeA, self)
	worldB := newTaskTestWorld(t, ctx, s, scopeB, self)

	for i := 0; i < 3; i++ {
		claimTestTask(t, ctx, s, db, scopeA, worldA.uncutMilestoneID, fmt.Sprintf("a-task %d", i), self)
	}
	claimTestTask(t, ctx, s, db, scopeB, worldB.uncutMilestoneID, "b-task", self)

	pageA, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID: scopeA,
		Page:    store.PageParams{PageSize: 1},
	})
	require.NoError(t, err)
	require.NotEmpty(t, pageA.NextToken)

	_, err = s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID: scopeB,
		Page:    store.PageParams{PageSize: 1, ContinuationToken: pageA.NextToken},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTokenScopeMismatch)
}

// TestTaskStore_ListClaimedTasks_OtherScopeRowsAbsent is issue #2916's
// Testing section item: a second scope's claimed task never appears in the
// first scope's page (NFR1) -- mirrors
// TestTaskStore_ListCancelledTasks_OtherScopeRowsAbsent.
func TestTaskStore_ListClaimedTasks_OtherScopeRowsAbsent(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeA := newTaskTestScope(t, ctx, db)
	scopeB := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	worldA := newTaskTestWorld(t, ctx, s, scopeA, self)
	worldB := newTaskTestWorld(t, ctx, s, scopeB, self)

	taskA, _ := claimTestTask(t, ctx, s, db, scopeA, worldA.uncutMilestoneID, "scope a", self)
	claimTestTask(t, ctx, s, db, scopeB, worldB.uncutMilestoneID, "scope b", self)

	page, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeA})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, taskA.ID, page.Items[0].TaskID)
}

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

// escalateTestTask creates a task and escalates it manually via
// EscalateTask (FR9) -- ListEscalatedTasks' cheapest real path to an
// escalated row, used by the tests below that don't care which reason
// produced the row (paging/ordering/cross-scope).
func escalateTestTask(t *testing.T, ctx context.Context, s *store.Store, scopeID, milestoneID uuid.UUID, title string, self store.Subject) (store.Task, store.EscalateResult) {
	t.Helper()
	task := createTestTask(t, ctx, s, scopeID, milestoneID, title, self)
	result, err := s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	return task, result
}

// escalateViaAttemptCap drives taskID through DefaultAttemptCap abandoned
// claims (each preceded by a claim), tripping the attempt cap (FR3, issue
// #2871) through the real AbandonClaim path -- the attempt-cap counterpart
// to task_complete_integration_test.go's own tripThrashCap.
func escalateViaAttemptCap(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, scopeID, taskID uuid.UUID, self store.Subject) {
	t.Helper()
	for i := 0; i < store.DefaultAttemptCap; i++ {
		sessionID := claimTestSession(t, ctx, db, scopeID, self)
		claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
			ScopeID: scopeID, TaskID: taskID, SessionID: sessionID,
			Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
		_, err = s.Tasks().AbandonClaim(ctx, store.AbandonParams{
			ScopeID: scopeID, TaskID: taskID, ClaimID: claim.ID,
			Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
	}
}

// TestTaskStore_ListEscalatedTasks_AllThreeReasonsAppear is issue #2875's
// Testing section item: a row appears for a task escalated by each of the
// three EscalationReasons, produced through the real paths -- tripThrashCap
// (alternating pass/fail to the thrash cap, #2870), escalateViaAttemptCap
// (abandons to the attempt cap, #2871), and EscalateTask (a manual
// escalate, #2872).
func TestTaskStore_ListEscalatedTasks_AllThreeReasonsAppear(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	thrashTask := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "thrash-capped", self)
	tripThrashCap(t, ctx, s, db, scopeID, thrashTask.ID, self)

	attemptTask := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "attempt-capped", self)
	escalateViaAttemptCap(t, ctx, s, db, scopeID, attemptTask.ID, self)

	manualTask, _ := escalateTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "manually escalated", self)

	page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 3)

	byTaskID := map[uuid.UUID]store.EscalatedTaskRow{}
	for _, row := range page.Items {
		byTaskID[row.TaskID] = row
	}
	require.Contains(t, byTaskID, thrashTask.ID)
	require.Contains(t, byTaskID, attemptTask.ID)
	require.Contains(t, byTaskID, manualTask.ID)
	assert.Equal(t, store.EscalationReasonThrashCap, byTaskID[thrashTask.ID].Reason)
	assert.Equal(t, store.EscalationReasonAttemptCap, byTaskID[attemptTask.ID].Reason)
	assert.Equal(t, store.EscalationReasonManual, byTaskID[manualTask.ID].Reason)
}

// TestTaskStore_ListEscalatedTasks_CounterAndCapPerReason is issue #2875's
// Testing section item: thrash-cap and attempt-cap rows carry the correct
// counter value and cap; manual rows carry NULL for both. MostRecentVerdict
// is VerdictFail exactly for thrash-cap (FR2's own certainty, EscalatedTaskRow's
// doc comment) and nil for the other two reasons.
func TestTaskStore_ListEscalatedTasks_CounterAndCapPerReason(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	thrashTask := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "thrash-capped", self)
	tripThrashCap(t, ctx, s, db, scopeID, thrashTask.ID, self)

	attemptTask := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "attempt-capped", self)
	escalateViaAttemptCap(t, ctx, s, db, scopeID, attemptTask.ID, self)

	manualTask, _ := escalateTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "manually escalated", self)

	page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 3)

	byTaskID := map[uuid.UUID]store.EscalatedTaskRow{}
	for _, row := range page.Items {
		byTaskID[row.TaskID] = row
	}

	thrashRow := byTaskID[thrashTask.ID]
	require.NotNil(t, thrashRow.CounterValue)
	require.NotNil(t, thrashRow.CapValue)
	assert.Equal(t, store.DefaultThrashCap, *thrashRow.CounterValue)
	assert.Equal(t, store.DefaultThrashCap, *thrashRow.CapValue)
	require.NotNil(t, thrashRow.MostRecentVerdict, "a thrash-cap escalation is tripped by a failing verdict, so it must be knowable with certainty")
	assert.Equal(t, store.VerdictFail, *thrashRow.MostRecentVerdict)

	attemptRow := byTaskID[attemptTask.ID]
	require.NotNil(t, attemptRow.CounterValue)
	require.NotNil(t, attemptRow.CapValue)
	assert.Equal(t, store.DefaultAttemptCap, *attemptRow.CounterValue)
	assert.Equal(t, store.DefaultAttemptCap, *attemptRow.CapValue)
	assert.Nil(t, attemptRow.MostRecentVerdict, "an attempt-cap escalation's triggering event is never itself a verdict")

	manualRow := byTaskID[manualTask.ID]
	assert.Nil(t, manualRow.CounterValue, "manual escalation carries no counter")
	assert.Nil(t, manualRow.CapValue, "manual escalation carries no cap")
	assert.Nil(t, manualRow.MostRecentVerdict)
}

// TestTaskStore_ListEscalatedTasks_SummaryCounts is issue #2875's Testing
// section item: summary counts are correct -- attempt count, failing-
// verdict count, and note count -- seeded via real notes and verdicts, both
// kept below their own caps so the manual escalate at the end is the one
// that actually puts the task in front of an operator.
func TestTaskStore_ListEscalatedTasks_SummaryCounts(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)
	require.Greater(t, store.DefaultThrashCap, 2, "this scenario needs two failing verdicts to stay below the thrash cap")
	require.Greater(t, store.DefaultAttemptCap, 1, "this scenario needs one abandon to stay below the attempt cap")

	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "summary counts", self)

	for i := 0; i < 2; i++ {
		_, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
			ScopeID: scopeID, TaskID: &task.ID, Kind: store.NoteKindComment,
			Body: fmt.Sprintf("note %d", i), Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
	}

	// Two failing verdicts (thrash_count), below the cap.
	for i := 0; i < 2; i++ {
		sessionID := claimTestSession(t, ctx, db, scopeID, self)
		claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
			ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
		_, err = s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
			ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Verdict: store.VerdictFail,
			Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
	}

	// One abandon (attempt_count), below the cap.
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	_, err = s.Tasks().AbandonClaim(ctx, store.AbandonParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	row := page.Items[0]
	assert.Equal(t, 1, row.AttemptCount, "one abandon must be reflected in attempt_count")
	assert.Equal(t, 2, row.FailingVerdictCount, "two failing verdicts must be reflected in thrash_count")
	assert.Equal(t, 2, row.NoteCount, "both recorded notes must be counted")
	assert.Nil(t, row.MostRecentVerdict, "a manual escalation's triggering event is never itself a verdict, even though earlier failing verdicts exist")
}

// TestTaskStore_EscalatedTaskRow_NoInlineHistoryFields is issue #2875's
// Testing section item: the no-inline-history regression guard for FR5's
// list-not-record rule, at the struct-shape level -- EscalatedTaskRow
// carries no slice field (a variable-length list, the shape a per-attempt/
// per-verdict/per-note history would take), so such a list can never sneak
// into this row without this test catching it. Only reflect.Slice is
// checked, not reflect.Array -- uuid.UUID's own underlying [16]byte is a
// fixed-size array, an id, never a history list (see
// TestListEscalatedTasksHandler_NoInlineHistory_RegressionGuard,
// krill/api/handlers/console_test.go, for the same guard at the wire
// boundary).
func TestTaskStore_EscalatedTaskRow_NoInlineHistoryFields(t *testing.T) {
	rowType := reflect.TypeOf(store.EscalatedTaskRow{})
	for i := 0; i < rowType.NumField(); i++ {
		field := rowType.Field(i)
		assert.NotEqual(t, reflect.Slice, field.Type.Kind(), "EscalatedTaskRow.%s must not be a slice -- FR5/NFR6/#2851 Assumption 11 restrict this row to summary history only, never a per-attempt/per-verdict/per-note array", field.Name)
	}
}

// TestTaskStore_ListEscalatedTasks_RequeuedTaskDisappears is issue #2875's
// Testing section item: a requeued task disappears from the view.
// RequeueTask (issue #2876) is not yet in this branch's codebase (see this
// task's own dispatch notes), so its effect -- clearing
// task.current_escalation_id -- is simulated directly, the same technique
// task_complete_integration_test.go's own transactionality test already
// uses to force a particular escalation state.
func TestTaskStore_ListEscalatedTasks_RequeuedTaskDisappears(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task, _ := escalateTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "will be requeued", self)

	page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 1, "the task must appear while its escalation is active")

	_, err = db.Pool.Exec(ctx, `UPDATE task SET current_escalation_id = NULL WHERE id = $1`, task.ID)
	require.NoError(t, err)

	page, err = s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	assert.Empty(t, page.Items, "a requeued task (current_escalation_id cleared) must disappear from the escalated view")
}

// TestTaskStore_ListEscalatedTasks_CancelledTaskAbsent is issue #2875's
// Testing section item: a cancelled task is reported by FR10's
// ListCancelledTasks, not this view -- CancelTask and EscalateTask are
// distinct terminal/active states, so a cancelled, never-escalated task
// must never appear here.
func TestTaskStore_ListEscalatedTasks_CancelledTaskAbsent(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	cancelTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "cancelled, never escalated", self)

	page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	assert.Empty(t, page.Items, "a cancelled task is never escalated -- FR10's view reports it, not this one")
}

// TestTaskStore_ListEscalatedTasks_PageSizeDefaultMaxClamp is issue #2875's
// Testing section item: an absent page size applies DefaultConsolePageSize,
// and a page size above MaxConsolePageSize is clamped down to it rather
// than rejected -- mirrors TestTaskStore_ListCancelledTasks_PageSizeDefaultMaxClamp.
func TestTaskStore_ListEscalatedTasks_PageSizeDefaultMaxClamp(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	const total = store.DefaultConsolePageSize + 5
	for i := 0; i < total; i++ {
		escalateTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, fmt.Sprintf("task %d", i), self)
	}

	page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	assert.Len(t, page.Items, store.DefaultConsolePageSize, "an absent page size must apply DefaultConsolePageSize")
	assert.NotEmpty(t, page.NextToken, "more rows remain beyond the default page")

	clamped, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeID,
		Page:    store.PageParams{PageSize: store.MaxConsolePageSize + 1000},
	})
	require.NoError(t, err)
	assert.Len(t, clamped.Items, total, "a page size above MaxConsolePageSize must clamp down, not reject, and every row here fits within the clamp")
}

// TestTaskStore_ListEscalatedTasks_ContinuationNoGapsOrDuplicates is issue
// #2875's Testing section item: walking every page via NextToken visits
// every escalated task exactly once, in the query's own deterministic
// order (most-recently-escalated first, id as tiebreak), and the same walk
// repeated produces the exact same order -- mirrors
// TestTaskStore_ListCancelledTasks_ContinuationNoGapsOrDuplicates.
func TestTaskStore_ListEscalatedTasks_ContinuationNoGapsOrDuplicates(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	const total = 23
	const pageSize = 5
	want := make(map[uuid.UUID]bool, total)
	for i := 0; i < total; i++ {
		task, _ := escalateTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, fmt.Sprintf("task %d", i), self)
		want[task.ID] = true
	}

	seen := map[uuid.UUID]bool{}
	var order []uuid.UUID
	token := ""
	pages := 0
	for {
		page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
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

	assert.Len(t, seen, total, "every escalated task must be visited exactly once with no gaps")
	for id := range want {
		assert.True(t, seen[id], "task %s must appear somewhere in the continuation walk", id)
	}

	var replay []uuid.UUID
	token = ""
	for {
		page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
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

// TestTaskStore_ListEscalatedTasks_CrossScopeToken_Rejected is issue
// #2875's Testing section item: a continuation token issued for one scope
// is rejected when presented against a different scope's query -- mirrors
// TestTaskStore_ListCancelledTasks_CrossScopeToken_Rejected.
func TestTaskStore_ListEscalatedTasks_CrossScopeToken_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeA := newTaskTestScope(t, ctx, db)
	scopeB := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	worldA := newTaskTestWorld(t, ctx, s, scopeA, self)
	worldB := newTaskTestWorld(t, ctx, s, scopeB, self)

	for i := 0; i < 3; i++ {
		escalateTestTask(t, ctx, s, scopeA, worldA.uncutMilestoneID, fmt.Sprintf("a-task %d", i), self)
	}
	escalateTestTask(t, ctx, s, scopeB, worldB.uncutMilestoneID, "b-task", self)

	pageA, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeA,
		Page:    store.PageParams{PageSize: 1},
	})
	require.NoError(t, err)
	require.NotEmpty(t, pageA.NextToken)

	_, err = s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeB,
		Page:    store.PageParams{PageSize: 1, ContinuationToken: pageA.NextToken},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTokenScopeMismatch)
}

// TestTaskStore_ListEscalatedTasks_OtherScopeRowsAbsent is issue #2875's
// Testing section item: a second scope's escalated task never appears in
// the first scope's page (NFR1) -- mirrors
// TestTaskStore_ListCancelledTasks_OtherScopeRowsAbsent.
func TestTaskStore_ListEscalatedTasks_OtherScopeRowsAbsent(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeA := newTaskTestScope(t, ctx, db)
	scopeB := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	worldA := newTaskTestWorld(t, ctx, s, scopeA, self)
	worldB := newTaskTestWorld(t, ctx, s, scopeB, self)

	taskA, _ := escalateTestTask(t, ctx, s, scopeA, worldA.uncutMilestoneID, "scope a", self)
	escalateTestTask(t, ctx, s, scopeB, worldB.uncutMilestoneID, "scope b", self)

	page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeA})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, taskA.ID, page.Items[0].TaskID)
}

// TestTaskStore_ListEscalatedTasks_RowSizeDoesNotGrowWithHistory is issue
// #2875's Testing section item: with a task carrying a long attempt/
// verdict/note history, the row size does not grow with that history --
// only NoteCount's own digit width is allowed to differ; no per-note array
// is ever added as the underlying history grows (FR5, NFR6, #2851
// Assumption 11).
func TestTaskStore_ListEscalatedTasks_RowSizeDoesNotGrowWithHistory(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	shortTask, _ := escalateTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "short history", self)

	longTask := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "long history", self)
	const noteCount = 200
	for i := 0; i < noteCount; i++ {
		_, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
			ScopeID: scopeID, TaskID: &longTask.ID, Kind: store.NoteKindComment,
			Body:       fmt.Sprintf("note number %d, padded to a realistic note length so this is a fair comparison of row shape rather than of trivially short strings", i),
			Acting:     self,
			OnBehalfOf: self,
		})
		require.NoError(t, err)
	}
	_, err := s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: longTask.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)

	byTaskID := map[uuid.UUID]store.EscalatedTaskRow{}
	for _, row := range page.Items {
		byTaskID[row.TaskID] = row
	}
	shortRow := byTaskID[shortTask.ID]
	longRow := byTaskID[longTask.ID]
	assert.Equal(t, 0, shortRow.NoteCount)
	assert.Equal(t, noteCount, longRow.NoteCount, "the count itself must reflect the full history")

	shortJSON, err := json.Marshal(shortRow)
	require.NoError(t, err)
	longJSON, err := json.Marshal(longRow)
	require.NoError(t, err)
	assert.InDelta(t, len(shortJSON), len(longJSON), 10, "a task's row size must not grow with the size of its attempt/verdict/note history")
}

// consoleFilterTestProduct creates one Product with a single milestone and
// returns both ids -- the ConsoleFilter tests' fixture, which needs two
// products inside one scope and therefore cannot reuse
// task_integration_test.go's taskTestWorld (which seeds exactly one and
// does not hand back its product id).
func consoleFilterTestProduct(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID, name string, self store.Subject) (productID, milestoneID uuid.UUID) {
	t.Helper()
	product, err := s.Products().Create(ctx, scopeID, name, "a product to narrow against")
	require.NoError(t, err)
	return product.ID, consoleFilterTestMilestone(t, ctx, s, scopeID, product.ID, name, self)
}

// consoleFilterTestMilestone adds one more milestone to an existing
// product -- for the second container a single-product fixture needs.
func consoleFilterTestMilestone(t *testing.T, ctx context.Context, s *store.Store, scopeID, productID uuid.UUID, name string, self store.Subject) uuid.UUID {
	t.Helper()
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, productID, "M-"+name, "ship it", nil, self, self)
	require.NoError(t, err)
	return milestone.ID
}

// TestTaskStore_ConsoleQueueReads_ProductFilter_KeepsOnlyThatProduct is
// FR a6cd917f's product-narrowing half on the three task queue reads: a
// product filter keeps exactly the rows whose task's delivery container
// belongs to that product (a milepebble counts under the product its own
// milestone belongs to), and an unfiltered read still returns every
// product's rows.
func TestTaskStore_ConsoleQueueReads_ProductFilter_KeepsOnlyThatProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")

	productA, milestoneA := consoleFilterTestProduct(t, ctx, s, scopeID, "A", self)
	_, milestoneB := consoleFilterTestProduct(t, ctx, s, scopeID, "B", self)
	milepebbleA, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestoneA, "MP-A", "a slice of A", nil, self, self)
	require.NoError(t, err)
	// A second, uncut milestone in the same product -- a milestone that has
	// had a milepebble cut can no longer carry a task of its own
	// (ErrMilestoneHasMilepebbleCut), so the uncut shape needs its own.
	uncutA := consoleFilterTestMilestone(t, ctx, s, scopeID, productA, "A2", self)

	claimedA, _ := claimTestTask(t, ctx, s, db, scopeID, milepebbleA.ID, "claimed in A", self)
	claimTestTask(t, ctx, s, db, scopeID, milestoneB, "claimed in B", self)
	cancelledA, _ := cancelTestTask(t, ctx, s, scopeID, uncutA, "cancelled in A", self)
	cancelTestTask(t, ctx, s, scopeID, milestoneB, "cancelled in B", self)
	escalatedA, _ := escalateTestTask(t, ctx, s, scopeID, milepebbleA.ID, "escalated in A", self)
	escalateTestTask(t, ctx, s, scopeID, milestoneB, "escalated in B", self)

	narrowA := store.ConsoleFilter{ProductID: &productA}

	claimed, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID, ConsoleFilter: narrowA})
	require.NoError(t, err)
	require.Len(t, claimed.Items, 1, "a product filter must keep only that product's claimed tasks")
	assert.Equal(t, claimedA.ID, claimed.Items[0].TaskID, "a milepebble's tasks belong to the product its milestone belongs to")

	claimedAll, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	assert.Len(t, claimedAll.Items, 2, "an un-narrowed read must still return every product's rows")

	cancelled, err := s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID, ConsoleFilter: narrowA})
	require.NoError(t, err)
	require.Len(t, cancelled.Items, 1)
	assert.Equal(t, cancelledA.ID, cancelled.Items[0].TaskID)

	cancelledAll, err := s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	assert.Len(t, cancelledAll.Items, 2, "an un-narrowed cancelled read must still return every product's rows")

	escalated, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID, ConsoleFilter: narrowA})
	require.NoError(t, err)
	require.Len(t, escalated.Items, 1)
	assert.Equal(t, escalatedA.ID, escalated.Items[0].TaskID)

	escalatedAll, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	assert.Len(t, escalatedAll.Items, 2, "an un-narrowed escalated read must still return every product's rows")
}

// TestTaskStore_ConsoleQueueReads_MilestoneFilter_IncludesMilepebbles is
// FR a6cd917f's container-narrowing half: a milestone filter keeps that
// milestone's own tasks and its milepebbles' tasks, a milepebble filter
// keeps only that milepebble's, and a filter never reaches outside the
// container it names.
func TestTaskStore_ConsoleQueueReads_MilestoneFilter_IncludesMilepebbles(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")

	_, milestone := consoleFilterTestProduct(t, ctx, s, scopeID, "A", self)
	_, other := consoleFilterTestProduct(t, ctx, s, scopeID, "B", self)

	// The milestone's own task is created before the cut, since a
	// milestone that already has a milepebble can no longer carry one.
	onMilestone, _ := claimTestTask(t, ctx, s, db, scopeID, milestone, "on the milestone", self)
	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone, "MP1", "a slice", nil, self, self)
	require.NoError(t, err)
	onMilepebble, _ := claimTestTask(t, ctx, s, db, scopeID, milepebble.ID, "on the milepebble", self)
	claimTestTask(t, ctx, s, db, scopeID, other, "elsewhere", self)

	byMilestone, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{MilestoneID: &milestone},
	})
	require.NoError(t, err)
	ids := map[uuid.UUID]bool{}
	for _, row := range byMilestone.Items {
		ids[row.TaskID] = true
	}
	assert.Len(t, ids, 2, "a milestone filter must keep the milestone's own tasks and its milepebbles'")
	assert.True(t, ids[onMilestone.ID])
	assert.True(t, ids[onMilepebble.ID])

	byMilepebble, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{MilestoneID: &milepebble.ID},
	})
	require.NoError(t, err)
	require.Len(t, byMilepebble.Items, 1, "a milepebble filter must keep only that milepebble's rows")
	assert.Equal(t, onMilepebble.ID, byMilepebble.Items[0].TaskID)

	otherID := other
	byOther, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{MilestoneID: &otherID},
	})
	require.NoError(t, err)
	assert.Len(t, byOther.Items, 1, "a container filter must never reach outside the container it names")
}

// TestTaskStore_ConsoleQueueReads_FilterOutsideScope_Refused is FR
// a6cd917f's LB1 refusal on all three task queue reads: a product or
// container outside the caller's scope, and a container belonging to a
// different product than the product filter it is paired with, are all
// refused as a missing parent -- never answered with another scope's rows
// and never as a silently empty page.
func TestTaskStore_ConsoleQueueReads_FilterOutsideScope_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	otherScopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")

	productHere, milestoneHere := consoleFilterTestProduct(t, ctx, s, scopeID, "here", self)
	productThere, milestoneThere := consoleFilterTestProduct(t, ctx, s, otherScopeID, "there", self)

	claimTestTask(t, ctx, s, db, scopeID, milestoneHere, "a claimed row that must never leak", self)
	cancelTestTask(t, ctx, s, scopeID, milestoneHere, "a cancelled row that must never leak", self)
	escalateTestTask(t, ctx, s, scopeID, milestoneHere, "an escalated row that must never leak", self)

	_, milestoneElsewhere := consoleFilterTestProduct(t, ctx, s, scopeID, "elsewhere", self)

	t.Run("product_outside_scope", func(t *testing.T) {
		filter := store.ConsoleFilter{ProductID: &productThere}
		_, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID, ConsoleFilter: filter})
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID, ConsoleFilter: filter})
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID, ConsoleFilter: filter})
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID, ConsoleFilter: filter})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("container_outside_scope", func(t *testing.T) {
		filter := store.ConsoleFilter{MilestoneID: &milestoneThere}
		_, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID, ConsoleFilter: filter})
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID, ConsoleFilter: filter})
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID, ConsoleFilter: filter})
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID, ConsoleFilter: filter})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("container_of_another_product", func(t *testing.T) {
		filter := store.ConsoleFilter{ProductID: &productHere, MilestoneID: &milestoneElsewhere}
		_, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID, ConsoleFilter: filter})
		assert.ErrorIs(t, err, store.ErrNotFound, "a container of another product than the one filtered for is refused against the container, the id the caller got wrong")
		_, err = s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID, ConsoleFilter: filter})
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID, ConsoleFilter: filter})
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID, ConsoleFilter: filter})
		assert.ErrorIs(t, err, store.ErrNotFound)
	})
}

// TestTaskStore_ListEscalatedTasks_ReasonFilter_KeepsOnlyThatReason is FR
// a6cd917f's escalation-reason filter: each of the three reasons keeps only
// its own rows, an absent filter keeps all three, and a reason outside the
// enumeration is refused rather than answered as an empty page.
func TestTaskStore_ListEscalatedTasks_ReasonFilter_KeepsOnlyThatReason(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	_, milestone := consoleFilterTestProduct(t, ctx, s, scopeID, "A", self)

	thrash := createTestTask(t, ctx, s, scopeID, milestone, "thrash-capped", self)
	tripThrashCap(t, ctx, s, db, scopeID, thrash.ID, self)

	attempt := createTestTask(t, ctx, s, scopeID, milestone, "attempt-capped", self)
	escalateViaAttemptCap(t, ctx, s, db, scopeID, attempt.ID, self)

	manual, _ := escalateTestTask(t, ctx, s, scopeID, milestone, "manually escalated", self)

	want := map[store.EscalationReason]uuid.UUID{
		store.EscalationReasonThrashCap:  thrash.ID,
		store.EscalationReasonAttemptCap: attempt.ID,
		store.EscalationReasonManual:     manual.ID,
	}
	for reason, taskID := range want {
		page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
			ScopeID: scopeID,
			Reason:  &reason,
		})
		require.NoError(t, err)
		require.Len(t, page.Items, 1, "reason %s must keep only its own row", reason)
		assert.Equal(t, taskID, page.Items[0].TaskID)
		assert.Equal(t, reason, page.Items[0].Reason)
	}

	unfiltered, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	assert.Len(t, unfiltered.Items, 3, "an absent reason filter must keep every reason")

	unknown := store.EscalationReason("nonsense")
	_, err = s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID, Reason: &unknown})
	assert.ErrorIs(t, err, store.ErrUnknownEscalationReason, "a reason outside the enumeration is refused, never silently an empty page")
}

// TestTaskStore_ClaimedRow_CarriesCurrentClaimID proves the claimed row's
// new claim_id is the task's own live claim -- the id a release carrying
// an expected claim is checked against -- and that the escalated row type
// carries no claim id of its own.
func TestTaskStore_ClaimedRow_CarriesCurrentClaimID(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	_, milestone := consoleFilterTestProduct(t, ctx, s, scopeID, "A", self)

	_, claim := claimTestTask(t, ctx, s, db, scopeID, milestone, "claimed", self)

	page, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, claim.ID, page.Items[0].ClaimID, "the row must carry the task's current open claim id")
	assert.NotEqual(t, uuid.Nil, page.Items[0].ClaimID)

	assert.NotContains(t, escalatedTaskFieldNames(), "ClaimID", "an escalated row carries no claim id")
}

// TestTaskStore_EscalatedRow_CarriesCurrentEscalationID proves the
// escalated row's new escalation_id is the task's own active escalation --
// the id a requeue or cancel carrying an expected escalation is checked
// against -- and that the claimed row type carries no escalation id.
func TestTaskStore_EscalatedRow_CarriesCurrentEscalationID(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	_, milestone := consoleFilterTestProduct(t, ctx, s, scopeID, "A", self)

	_, result := escalateTestTask(t, ctx, s, scopeID, milestone, "escalated", self)

	page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, result.EscalationEvent.ID, page.Items[0].EscalationID, "the row must carry the task's current escalation id")
	assert.NotEqual(t, uuid.Nil, page.Items[0].EscalationID)

	assert.NotContains(t, claimedTaskFieldNames(), "EscalationID", "a claimed row carries no escalation id")
}

// claimedTaskFieldNames/escalatedTaskFieldNames list the two row types'
// own field names, so the "carries no id of the other kind" assertions
// above read as the one-liners they are.
func claimedTaskFieldNames() []string {
	t := reflect.TypeOf(store.ClaimedTaskRow{})
	names := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		names = append(names, t.Field(i).Name)
	}
	return names
}

func escalatedTaskFieldNames() []string {
	t := reflect.TypeOf(store.EscalatedTaskRow{})
	names := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		names = append(names, t.Field(i).Name)
	}
	return names
}

// TestTaskStore_ConsoleQueueReads_TokenBindsFilterSet is FR 5713b7da on
// the console queue reads: a token issued under one filter set is refused
// when presented under another (including under none), and an un-narrowed
// read keeps the scope-only token it issued before filters existed.
func TestTaskStore_ConsoleQueueReads_TokenBindsFilterSet(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")

	productA, milestoneA := consoleFilterTestProduct(t, ctx, s, scopeID, "A", self)
	productB, milestoneB := consoleFilterTestProduct(t, ctx, s, scopeID, "B", self)

	for i := 0; i < 3; i++ {
		claimTestTask(t, ctx, s, db, scopeID, milestoneA, fmt.Sprintf("a %d", i), self)
		claimTestTask(t, ctx, s, db, scopeID, milestoneB, fmt.Sprintf("b %d", i), self)
	}

	filtered, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{ProductID: &productA},
		Page:          store.PageParams{PageSize: 1},
	})
	require.NoError(t, err)
	require.NotEmpty(t, filtered.NextToken)

	same, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{ProductID: &productA},
		Page:          store.PageParams{PageSize: 1, ContinuationToken: filtered.NextToken},
	})
	require.NoError(t, err, "the same filter set must resume normally")
	assert.Len(t, same.Items, 1)

	_, err = s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{ProductID: &productB},
		Page:          store.PageParams{PageSize: 1, ContinuationToken: filtered.NextToken},
	})
	require.Error(t, err, "a token issued under one filter set must be refused under another, never answered as a wrong-filter page")
	assert.ErrorIs(t, err, store.ErrTokenFilterMismatch)

	_, err = s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID: scopeID,
		Page:    store.PageParams{PageSize: 1, ContinuationToken: filtered.NextToken},
	})
	assert.ErrorIs(t, err, store.ErrTokenFilterMismatch, "a filtered token is never a valid unfiltered resume")

	_, err = s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID: scopeID,
		Page:    store.PageParams{PageSize: 1, ContinuationToken: filtered.NextToken},
	})
	assert.ErrorIs(t, err, store.ErrTokenFilterMismatch, "the escalation-reason filter binds into ListEscalatedTasks' own filter set too, so a claimed token never resumes it")

	_, err = s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeID,
		Page:    store.PageParams{PageSize: 1, ContinuationToken: filtered.NextToken},
	})
	assert.ErrorIs(t, err, store.ErrTokenFilterMismatch)

	unfiltered, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID: scopeID,
		Page:    store.PageParams{PageSize: 1},
	})
	require.NoError(t, err)
	require.NotEmpty(t, unfiltered.NextToken)

	_, err = store.DecodeContinuationToken(scopeID, unfiltered.NextToken)
	assert.NoError(t, err, "an un-narrowed read must keep the scope-only token it issued before the filters existed")

	_, err = s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{ProductID: &productA},
		Page:          store.PageParams{PageSize: 1, ContinuationToken: unfiltered.NextToken},
	})
	assert.ErrorIs(t, err, store.ErrTokenFilterMismatch, "a scope-only token never resumes a filtered request")
}

// TestTaskStore_ConsoleQueueReads_FilterSurvivesContinuation is FR
// a6cd917f's narrowing meeting FR 5713b7da's paging: a narrowed read walks
// its own narrowed set page by page, so every page -- not just the first
// -- still excludes the rows outside the filter, and the walk terminates
// having visited each of the filtered rows exactly once.
//
// The first page alone is what
// TestTaskStore_ConsoleQueueReads_ProductFilter_KeepsOnlyThatProduct
// proves, so a read that applied its filter only when no cursor was
// present would pass that test and then leak another product's rows onto
// page two. This walks all three task queue reads past their first page
// to close that.
func TestTaskStore_ConsoleQueueReads_FilterSurvivesContinuation(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")

	productA, milestoneA := consoleFilterTestProduct(t, ctx, s, scopeID, "A", self)
	_, milestoneB := consoleFilterTestProduct(t, ctx, s, scopeID, "B", self)

	// Five rows inside the filter and three outside it, at a page size of
	// two: the walk must cross a page boundary, and a page that quietly
	// dropped the filter would then serve product B's rows rather than
	// merely duplicating A's.
	const inFilter, outOfFilter = 5, 3
	claimedWant, cancelledWant, escalatedWant := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for i := 0; i < inFilter; i++ {
		task, _ := claimTestTask(t, ctx, s, db, scopeID, milestoneA, fmt.Sprintf("claimed a %d", i), self)
		claimedWant[task.ID] = true
		task, _ = cancelTestTask(t, ctx, s, scopeID, milestoneA, fmt.Sprintf("cancelled a %d", i), self)
		cancelledWant[task.ID] = true
		task, _ = escalateTestTask(t, ctx, s, scopeID, milestoneA, fmt.Sprintf("escalated a %d", i), self)
		escalatedWant[task.ID] = true
	}
	for i := 0; i < outOfFilter; i++ {
		claimTestTask(t, ctx, s, db, scopeID, milestoneB, fmt.Sprintf("claimed b %d", i), self)
		cancelTestTask(t, ctx, s, scopeID, milestoneB, fmt.Sprintf("cancelled b %d", i), self)
		escalateTestTask(t, ctx, s, scopeID, milestoneB, fmt.Sprintf("escalated b %d", i), self)
	}

	narrow := store.ConsoleFilter{ProductID: &productA}
	const pageSize = 2

	t.Run("claimed", func(t *testing.T) {
		seen, pages := map[uuid.UUID]bool{}, 0
		token := ""
		for {
			page, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
				ScopeID: scopeID, ConsoleFilter: narrow,
				Page: store.PageParams{PageSize: pageSize, ContinuationToken: token},
			})
			require.NoError(t, err)
			pages++
			require.Less(t, pages, 20, "the walk must terminate well within a sane number of pages")
			for _, row := range page.Items {
				require.False(t, seen[row.TaskID], "task %s seen twice across the filtered walk", row.TaskID)
				seen[row.TaskID] = true
			}
			if page.NextToken == "" {
				break
			}
			token = page.NextToken
		}
		assert.Greater(t, pages, 1, "the fixture must cross a page boundary, or this proves nothing about continuation under a filter")
		assert.Equal(t, claimedWant, seen, "the filtered walk must visit exactly product A's claimed tasks -- no gaps, and no product B row leaking onto a later page")
	})

	t.Run("cancelled", func(t *testing.T) {
		seen, pages := map[uuid.UUID]bool{}, 0
		token := ""
		for {
			page, err := s.Tasks().ListCancelledTasks(ctx, store.ListCancelledTasksParams{
				ScopeID: scopeID, ConsoleFilter: narrow,
				Page: store.PageParams{PageSize: pageSize, ContinuationToken: token},
			})
			require.NoError(t, err)
			pages++
			require.Less(t, pages, 20, "the walk must terminate well within a sane number of pages")
			for _, row := range page.Items {
				require.False(t, seen[row.TaskID], "task %s seen twice across the filtered walk", row.TaskID)
				seen[row.TaskID] = true
			}
			if page.NextToken == "" {
				break
			}
			token = page.NextToken
		}
		assert.Greater(t, pages, 1, "the fixture must cross a page boundary, or this proves nothing about continuation under a filter")
		assert.Equal(t, cancelledWant, seen, "the filtered walk must visit exactly product A's cancelled tasks")
	})

	t.Run("escalated", func(t *testing.T) {
		seen, pages := map[uuid.UUID]bool{}, 0
		token := ""
		for {
			page, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
				ScopeID: scopeID, ConsoleFilter: narrow,
				Page: store.PageParams{PageSize: pageSize, ContinuationToken: token},
			})
			require.NoError(t, err)
			pages++
			require.Less(t, pages, 20, "the walk must terminate well within a sane number of pages")
			for _, row := range page.Items {
				require.False(t, seen[row.TaskID], "task %s seen twice across the filtered walk", row.TaskID)
				seen[row.TaskID] = true
			}
			if page.NextToken == "" {
				break
			}
			token = page.NextToken
		}
		assert.Greater(t, pages, 1, "the fixture must cross a page boundary, or this proves nothing about continuation under a filter")
		assert.Equal(t, escalatedWant, seen, "the filtered walk must visit exactly product A's escalated tasks")
	})
}

// TestTaskStore_ListEscalatedTasks_ReasonBindsContinuationToken is FR
// 5713b7da on the one filter the escalated read has that the other queue
// reads do not: the reason is part of the request's filter set, so a token
// issued under one reason is refused under another (and under none), never
// answered as a page of the wrong reason's rows.
//
// The reason never appears in a ConsoleFilter, so the other three reads'
// token proofs cannot reach this: they would all still pass with the
// reason left out of ListEscalatedTasksParams.Filters entirely.
func TestTaskStore_ListEscalatedTasks_ReasonBindsContinuationToken(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	_, milestone := consoleFilterTestProduct(t, ctx, s, scopeID, "A", self)

	manual := store.EscalationReasonManual
	for i := 0; i < 3; i++ {
		escalateTestTask(t, ctx, s, scopeID, milestone, fmt.Sprintf("manual %d", i), self)
	}
	attempt := store.EscalationReasonAttemptCap
	escalateViaAttemptCapTask(t, ctx, s, db, scopeID, milestone, "attempt-capped", self)

	first, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeID,
		Reason:  &manual,
		Page:    store.PageParams{PageSize: 1},
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.NextToken)
	require.Equal(t, manual, first.Items[0].Reason, "the fixture's first manual row must be the manual one, or the resume below proves nothing")

	same, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeID,
		Reason:  &manual,
		Page:    store.PageParams{PageSize: 1, ContinuationToken: first.NextToken},
	})
	require.NoError(t, err, "the same reason must resume normally")
	require.Len(t, same.Items, 1)
	assert.Equal(t, manual, same.Items[0].Reason)

	_, err = s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeID,
		Reason:  &attempt,
		Page:    store.PageParams{PageSize: 1, ContinuationToken: first.NextToken},
	})
	assert.ErrorIs(t, err, store.ErrTokenFilterMismatch, "a token issued under one reason must be refused under another, never answered as a wrong-reason page")

	_, err = s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeID,
		Page:    store.PageParams{PageSize: 1, ContinuationToken: first.NextToken},
	})
	assert.ErrorIs(t, err, store.ErrTokenFilterMismatch, "a reason-filtered token is never a valid unfiltered resume")

	// The refusal must name no reason value and no cursor: a wrong-reason
	// page is a scope-crossing-shaped leak if the token's payload escapes
	// in the error.
	_, err = s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeID,
		Reason:  &attempt,
		Page:    store.PageParams{PageSize: 1, ContinuationToken: first.NextToken},
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), first.NextToken, "the refusal must expose no cursor")
}

// escalateViaAttemptCapTask creates a task under milestoneID and drives it
// to the attempt cap -- escalateViaAttemptCap's own fixture, kept separate
// so the reason test does not have to thread a create call inline.
func escalateViaAttemptCapTask(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, scopeID, milestoneID uuid.UUID, title string, self store.Subject) store.Task {
	t.Helper()
	task := createTestTask(t, ctx, s, scopeID, milestoneID, title, self)
	escalateViaAttemptCap(t, ctx, s, db, scopeID, task.ID, self)
	return task
}

// TestTaskStore_ListEscalatedTasks_ReasonComposesWithContainerFilter is the
// two-filter intersection on the escalated read: a reason and a delivery
// container named together keep exactly the rows satisfying both, so
// neither filter is quietly dropped when the other is present -- the shape
// a console filter plus a reason chip produces in one request.
func TestTaskStore_ListEscalatedTasks_ReasonComposesWithContainerFilter(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	_, milestoneA := consoleFilterTestProduct(t, ctx, s, scopeID, "A", self)
	_, milestoneB := consoleFilterTestProduct(t, ctx, s, scopeID, "B", self)

	manualA, _ := escalateTestTask(t, ctx, s, scopeID, milestoneA, "manual in A", self)
	escalateViaAttemptCapTask(t, ctx, s, db, scopeID, milestoneA, "attempt-capped in A", self)
	manualB, _ := escalateTestTask(t, ctx, s, scopeID, milestoneB, "manual in B", self)

	manual := store.EscalationReasonManual
	byContainer := store.ConsoleFilter{MilestoneID: &milestoneA}

	both, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeID, ConsoleFilter: byContainer, Reason: &manual,
	})
	require.NoError(t, err)
	require.Len(t, both.Items, 1, "a container filter and a reason filter must intersect, not either one alone")
	assert.Equal(t, manualA.ID, both.Items[0].TaskID)

	attempt := store.EscalationReasonAttemptCap
	other, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeID, ConsoleFilter: byContainer, Reason: &attempt,
	})
	require.NoError(t, err)
	require.Len(t, other.Items, 1, "the attempt-capped row in A must still be reachable under A plus attempt-cap")
	assert.NotEqual(t, manualA.ID, other.Items[0].TaskID)

	containerOnly, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeID, ConsoleFilter: byContainer,
	})
	require.NoError(t, err)
	assert.Len(t, containerOnly.Items, 2, "with no reason named, the container filter alone keeps every reason in that container")

	reasonOnly, err := s.Tasks().ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID: scopeID, Reason: &manual,
	})
	require.NoError(t, err)
	require.Len(t, reasonOnly.Items, 2, "with no container named, the reason filter alone keeps every container's manual rows")
	assert.Contains(t, []uuid.UUID{reasonOnly.Items[0].TaskID, reasonOnly.Items[1].TaskID}, manualB.ID)
}

// TestTaskStore_ConsoleQueueReads_BothFilters_Intersect is the two-filter
// shape neither single-filter test can reach: a product and one of its own
// milestones named together. The pair must intersect -- the milestone's
// rows, and only those -- rather than behave as though either one alone
// were enough.
//
// The product test and the milestone test each pass one filter, so a
// ConsoleFilter that treated the two clauses as alternatives (widening to
// the union) would satisfy both of them and fail only here, where the
// product holds a second milestone the caller did not name.
func TestTaskStore_ConsoleQueueReads_BothFilters_Intersect(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")

	productA, milestoneA := consoleFilterTestProduct(t, ctx, s, scopeID, "A", self)
	// A second, uncut milestone in the same product: the row a union-
	// instead-of-intersection filter would wrongly add.
	sibling := consoleFilterTestMilestone(t, ctx, s, scopeID, productA, "A-sibling", self)
	_, milestoneB := consoleFilterTestProduct(t, ctx, s, scopeID, "B", self)

	onA, _ := claimTestTask(t, ctx, s, db, scopeID, milestoneA, "on A's first milestone", self)
	onSibling, _ := claimTestTask(t, ctx, s, db, scopeID, sibling, "on A's other milestone", self)
	claimTestTask(t, ctx, s, db, scopeID, milestoneB, "on B", self)

	page, err := s.Tasks().ListClaimedTasks(ctx, store.ListClaimedTasksParams{
		ScopeID: scopeID,
		ConsoleFilter: store.ConsoleFilter{
			ProductID:   &productA,
			MilestoneID: &milestoneA,
		},
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 1, "a product filter and one of its milestones must intersect: the named milestone's rows and nothing else")
	assert.Equal(t, onA.ID, page.Items[0].TaskID)
	assert.NotEqual(t, onSibling.ID, page.Items[0].TaskID, "the product's un-named sibling milestone must not survive the pair")
}
