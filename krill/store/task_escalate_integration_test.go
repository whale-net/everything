//go:build integration

// Real-Postgres coverage for TaskStore.EscalateTask (task_escalate.go,
// migration 016, issue #2872's Testing section, FR9): a manual escalation
// on an unclaimed, uncapped task (one 'manual' event, NULL counter/cap, no
// claim touched, no attempt recorded), a manual escalation on a claimed
// task (the claim force-closed, attempt_count+1, the claimant's later
// heartbeat/complete/abandon rejected), FR9's single-event exception at
// the cap (the single most important assertion in this task: exactly one
// task_escalation_event exists, reason 'manual', even when the force-close
// itself crosses DefaultAttemptCap), escalating an already-escalated task
// (this task's own documented choice: reject by reusing ErrTaskEscalated,
// no second active escalation), and escalating a cancelled task refused.
// Shares
// task_integration_test.go's test-store/test-scope/test-world/subject
// helpers, task_dependency_integration_test.go's createTestTask helper,
// and task_claim_integration_test.go's claimTestSession/countRows
// helpers, mirroring task_release_integration_test.go's own choice to
// share rather than duplicate fixture helpers. See
// store_integration_test.go's package doc for why this file only builds
// under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_escalate_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// TestTaskStore_EscalateTask_UnclaimedUncappedTask_RecordsManualEvent is
// issue #2872's Testing section "escalate" item 1: on an unclaimed,
// uncapped task, EscalateTask records exactly one 'manual' event with
// NULL counter_value/cap_value, leaves the task unclaimable, touches no
// claim, and records no task_attempt row (nothing was force-closed).
func TestTaskStore_EscalateTask_UnclaimedUncappedTask_RecordsManualEvent(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	acting := taskTestSubject("operator-1")
	onBehalfOf := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, acting)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "manual-escalate", acting)

	result, err := s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: acting, OnBehalfOf: onBehalfOf,
	})
	require.NoError(t, err)
	assert.False(t, result.ClaimForceClosed)
	assert.False(t, result.CapExhausted)
	assert.Equal(t, store.EscalationReasonManual, result.EscalationEvent.Reason)
	assert.Nil(t, result.EscalationEvent.CounterValue)
	assert.Nil(t, result.EscalationEvent.CapValue)
	assert.Equal(t, store.LaneScaffold, result.EscalationEvent.LaneAtEscalation)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CurrentEscalationID)
	assert.Equal(t, result.EscalationEvent.ID, *got.CurrentEscalationID)
	assert.Nil(t, got.CurrentClaimID, "no claim exists to touch")
	assert.Equal(t, 0, got.AttemptCount, "a manual escalation with no open claim must not move attempt_count")

	assert.Equal(t, 0, countRows(t, ctx, db, "task_attempt", task.ID), "no task_attempt row is written when nothing is force-closed")

	var escalationEvents int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_escalation_event WHERE task_id = $1`, task.ID).Scan(&escalationEvents))
	assert.Equal(t, 1, escalationEvents)

	var interventionCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_intervention_event WHERE task_id = $1`, task.ID).Scan(&interventionCount))
	assert.Equal(t, 1, interventionCount)
	assert.Equal(t, store.InterventionActionEscalate, result.InterventionEvent.Action)
	assert.Equal(t, acting, result.InterventionEvent.CreatedByActing)
	assert.Equal(t, onBehalfOf, result.InterventionEvent.CreatedByOnBehalfOf)

	otherSession := claimTestSession(t, ctx, db, scopeID, acting)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: otherSession, Acting: acting, OnBehalfOf: acting,
	})
	require.Error(t, err, "an escalated task must be unclaimable")
	assert.ErrorIs(t, err, store.ErrTaskEscalated)
}

// TestTaskStore_EscalateTask_ClaimedTask_ForceClosesAndCountsAttempt is
// issue #2872's Testing section "escalate" item 2: on a claimed task, the
// claim is force-closed, attempt_count is incremented by one, and the
// claimant's later heartbeat/complete/abandon are each rejected the same
// way a reclaim already rejects one.
func TestTaskStore_EscalateTask_ClaimedTask_ForceClosesAndCountsAttempt(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "escalate-claimed", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	result, err := s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.True(t, result.ClaimForceClosed)
	assert.False(t, result.CapExhausted)
	assert.Equal(t, store.EscalationReasonManual, result.EscalationEvent.Reason)

	closed, err := s.Tasks().GetClaimByID(ctx, claim.ID)
	require.NoError(t, err)
	require.NotNil(t, closed.ReleasedAt)
	require.NotNil(t, closed.ReleaseReason)
	assert.Equal(t, "escalate", *closed.ReleaseReason)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Nil(t, got.CurrentClaimID)
	assert.Equal(t, 1, got.AttemptCount, "the force-close counts as an attempt against the same run-attempt cap")

	var forceClosedCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_attempt WHERE task_id = $1 AND outcome = 'force-closed'`, task.ID).Scan(&forceClosedCount))
	assert.Equal(t, 1, forceClosedCount)

	_, err = s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent, "the claimant's later heartbeat must be rejected")
}

// TestTaskStore_EscalateTask_CapCrossing_SingleEventException is issue
// #2872's Testing section "escalate" item 3 -- FR9's exception, "the
// single most important assertion in this task": a task at
// DefaultAttemptCap-1, escalated with a live claim so the force-close
// crosses the cap, must end with exactly ONE task_escalation_event, whose
// reason is 'manual', not 'attempt-cap' -- FR8's "cap crossed => a second
// attempt-cap escalation" rule must NOT fire here.
func TestTaskStore_EscalateTask_CapCrossing_SingleEventException(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "escalate-at-cap", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `UPDATE task SET attempt_count = $1 WHERE id = $2`, store.DefaultAttemptCap-1, task.ID)
	require.NoError(t, err)

	result, err := s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.True(t, result.ClaimForceClosed)
	assert.True(t, result.CapExhausted, "the force-close must be reported as having crossed the cap")
	assert.Equal(t, store.EscalationReasonManual, result.EscalationEvent.Reason, "FR9's own manual event stands as the single record -- it must not be a second, attempt-cap-reasoned event")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DefaultAttemptCap, got.AttemptCount)
	require.NotNil(t, got.CurrentEscalationID)
	assert.Equal(t, result.EscalationEvent.ID, *got.CurrentEscalationID)

	var escalationEvents int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_escalation_event WHERE task_id = $1`, task.ID).Scan(&escalationEvents))
	assert.Equal(t, 1, escalationEvents, "exactly one escalation event must exist -- a single task never carries two concurrently-active escalation events")

	var reason string
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT reason FROM task_escalation_event WHERE task_id = $1`, task.ID).Scan(&reason))
	assert.Equal(t, "manual", reason)
}

// TestTaskStore_EscalateTask_AlreadyEscalatedTask_Refused proves this
// task's own documented choice for the open ambiguity #2851's design notes
// flag (see task_escalate.go's package doc comment): escalating an
// already-escalated task is rejected -- reusing store.ErrTaskEscalated,
// the same rejection ClaimTask gives, rather than a second, redundant
// sentinel -- not a silent no-op, and in neither case does a second
// active escalation ever exist.
func TestTaskStore_EscalateTask_AlreadyEscalatedTask_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "double-escalate", self)
	first, err := s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "escalating an already-escalated task must be rejected")
	assert.ErrorIs(t, err, store.ErrTaskEscalated)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CurrentEscalationID)
	assert.Equal(t, first.EscalationEvent.ID, *got.CurrentEscalationID, "the first escalation must remain the task's one active escalation")

	var escalationEvents int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_escalation_event WHERE task_id = $1`, task.ID).Scan(&escalationEvents))
	assert.Equal(t, 1, escalationEvents, "a rejected second escalate must write no second event")

	var interventionCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_intervention_event WHERE task_id = $1`, task.ID).Scan(&interventionCount))
	assert.Equal(t, 1, interventionCount, "a rejected second escalate must write no second intervention event either")
}

// TestTaskStore_EscalateTask_CancelledTask_Refused is issue #2872's
// Testing section "Both verbs refuse a cancelled task": escalate against a
// cancelled task is refused with ErrTaskCancelled, and writes nothing.
func TestTaskStore_EscalateTask_CancelledTask_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "cancelled-escalate", self)
	_, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTaskCancelled)

	assert.Equal(t, 0, countRows(t, ctx, db, "task_escalation_event", task.ID), "a refused escalate must write no task_escalation_event row")
}
