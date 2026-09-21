//go:build integration

// Real-Postgres coverage for TaskStore.ListCancelledTasks and
// TaskStore.ListEscalatedTasks (task_console.go).
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
