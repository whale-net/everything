//go:build integration

// Real-Postgres coverage for TaskStore.RequeueTask (task_requeue.go,
// issue #2876's Testing section, FR6): the per-reason counter-reset
// matrix (thrash-cap, attempt-cap via lapse, attempt-cap via abandon,
// manual at the cap -- FR9's no-strand exception, manual below the cap),
// the task becoming claimable again immediately at the exact lane it held
// when escalated, no task_attempt row written and attempt_count never
// increasing, task_escalation_event left byte-for-byte unchanged with
// exactly one task_intervention_event naming the resolved escalation,
// refusal on a non-escalated task and a cancelled task, the escalate ->
// requeue -> claim round trip (the claim payload, work.Assembler's own
// document, carries every note recorded during the escalation at its
// current lifecycle status -- see krill/work/payload_integration_test.go
// for that half, since work.Assembler lives in a different package), the
// requeued task satisfying task_claimable_idx's predicate directly (FR5's
// console view, #2875, is not yet mergeable into this branch -- see this
// file's own tests for why the assertion is made directly against the
// predicate rather than a ListEscalatedTasks call), and double requeue
// refused rather than silently re-resetting counters. Shares
// task_integration_test.go's test-store/test-scope/test-world/subject
// helpers, task_dependency_integration_test.go's createTestTask/
// setTaskLane helpers, and task_claim_integration_test.go's
// claimTestSession/countRows helpers, mirroring
// task_escalate_integration_test.go's own choice to share rather than
// duplicate fixture helpers. See store_integration_test.go's package doc
// for why this file only builds under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_requeue_integration_test --test_output=all
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

// requeueTestComplete claims taskID and completes it with verdict, in one
// step -- the shared building block driveThrashCapHeldAtImplementation
// below composes into the exact alternating pass/fail sequence
// task_complete_integration_test.go's own
// TestTaskStore_CompleteTask_AlternatingPassFail_TripsThrashCapOnThirdFail
// uses, duplicated here (rather than sharing that file) so this target's
// srcs list stays the same minimal set task_escalate_integration_test.go
// already established.
func requeueTestComplete(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, scopeID, taskID uuid.UUID, self store.Subject, verdict store.Verdict) store.TaskLaneResult {
	t.Helper()
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: taskID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	result, err := s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: scopeID, TaskID: taskID, ClaimID: claim.ID, Verdict: verdict, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	return result
}

// driveThrashCapHeldAtImplementation drives taskID through
// pass/fail/pass/fail/pass/fail -- DefaultThrashCap (3) failing verdicts,
// each preceded by a pass, so the thrash cap trips while the task is held
// at Implementation, not Scaffold (the starting lane) -- proving
// RequeueTask's "at the lane it held when escalated" is a real,
// non-trivial lane, not simply the task's own starting lane. Returns the
// resulting store.TaskLaneResult from the capping completion.
func driveThrashCapHeldAtImplementation(t *testing.T, ctx context.Context, s *store.Store, db *dbtest.Postgres, scopeID, taskID uuid.UUID, self store.Subject) store.TaskLaneResult {
	t.Helper()
	require.Equal(t, 3, store.DefaultThrashCap, "this fixture's fixed pass/fail sequence assumes DefaultThrashCap == 3")
	requeueTestComplete(t, ctx, s, db, scopeID, taskID, self, store.VerdictPass) // Scaffold -> Implementation, thrash 0
	requeueTestComplete(t, ctx, s, db, scopeID, taskID, self, store.VerdictFail) // Implementation -> Scaffold, thrash 1
	requeueTestComplete(t, ctx, s, db, scopeID, taskID, self, store.VerdictPass) // Scaffold -> Implementation, thrash 1
	requeueTestComplete(t, ctx, s, db, scopeID, taskID, self, store.VerdictFail) // Implementation -> Scaffold, thrash 2
	requeueTestComplete(t, ctx, s, db, scopeID, taskID, self, store.VerdictPass) // Scaffold -> Implementation, thrash 2
	return requeueTestComplete(t, ctx, s, db, scopeID, taskID, self, store.VerdictFail) // held at Implementation, thrash 3 (capped)
}

// assertClaimableUnderIndexPredicate is this file's stand-in for FR5's
// escalated console view (#2875), which is not yet mergeable into this
// branch: it asserts the exact three-column predicate
// 016_escalation_axis.up.sql's task_claimable_idx enforces
// (current_claim_id IS NULL AND current_escalation_id IS NULL AND
// cancelled_at IS NULL) directly against the row, rather than calling a
// ListEscalatedTasks function that does not exist in this branch's
// codebase.
func assertClaimableUnderIndexPredicate(t *testing.T, ctx context.Context, db *dbtest.Postgres, taskID uuid.UUID) {
	t.Helper()
	var claimable bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT current_claim_id IS NULL AND current_escalation_id IS NULL AND cancelled_at IS NULL
		FROM task WHERE id = $1
	`, taskID).Scan(&claimable))
	assert.True(t, claimable, "the task must satisfy task_claimable_idx's own predicate directly -- current_claim_id, current_escalation_id, and cancelled_at must all be NULL")
}

// TestTaskStore_RequeueTask_ThrashCapEscalation_ResetsThrashCountOnly is
// issue #2876's Testing section, per-reason matrix item 1: requeuing a
// thrash-cap escalation resets thrash_count to 0 and leaves attempt_count
// completely untouched (NFR4) -- and the task returns to claimable at
// Implementation, the lane it was actually held at, not reverted to
// Scaffold.
func TestTaskStore_RequeueTask_ThrashCapEscalation_ResetsThrashCountOnly(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "requeue-thrash-cap", self)
	capResult := driveThrashCapHeldAtImplementation(t, ctx, s, db, scopeID, task.ID, self)
	require.Equal(t, store.TaskStateEscalated, capResult.State)
	require.NotNil(t, capResult.EscalationReason)
	require.Equal(t, store.EscalationReasonThrashCap, *capResult.EscalationReason)

	capped, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, store.LaneImplementation, capped.CurrentLane, "the fixture must actually hold the task at Implementation before requeue, or this test proves nothing about reversion")
	require.Equal(t, store.DefaultThrashCap, capped.ThrashCount)
	require.Equal(t, 0, capped.AttemptCount, "a thrash-cap escalation must never have touched attempt_count in the first place")
	require.NotNil(t, capped.CurrentEscalationID)
	escalationID := *capped.CurrentEscalationID

	attemptRowsBefore := countRows(t, ctx, db, "task_attempt", task.ID)

	result, err := s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.Equal(t, escalationID, result.EscalationEventID)
	assert.Equal(t, store.EscalationReasonThrashCap, result.EscalationReason)
	assert.Equal(t, store.ResetCounterThrash, result.CounterReset)
	assert.Equal(t, store.LaneImplementation, result.ResultingLane, "the resulting lane must be Implementation -- the lane the task actually held, not reverted to Scaffold")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, got.ThrashCount, "requeuing a thrash-cap escalation must reset thrash_count to 0")
	assert.Equal(t, 0, got.AttemptCount, "requeuing a thrash-cap escalation must leave attempt_count at its pre-requeue value (0), never touching the unrelated counter")
	assert.Nil(t, got.CurrentEscalationID)
	assert.Equal(t, store.LaneImplementation, got.CurrentLane)

	assert.Equal(t, attemptRowsBefore, countRows(t, ctx, db, "task_attempt", task.ID), "requeue must write no task_attempt row")
	assertClaimableUnderIndexPredicate(t, ctx, db, task.ID)

	otherSession := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: otherSession, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err, "the task must be immediately claimable again after requeue")
	require.NotEqual(t, uuid.Nil, claim.ID)

	afterClaim, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.LaneImplementation, afterClaim.CurrentLane, "the claim must be against the lane the task was requeued at")
}

// TestTaskStore_RequeueTask_AttemptCapViaLapse_ResetsAttemptCountOnly is
// issue #2876's Testing section, per-reason matrix item 2 (the lapse
// half): requeuing an attempt-cap escalation triggered by ReclaimExpired
// resets attempt_count to 0 and leaves thrash_count untouched.
func TestTaskStore_RequeueTask_AttemptCapViaLapse_ResetsAttemptCountOnly(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "requeue-attempt-cap-lapse", self)
	setTaskLane(t, ctx, db, task.ID, store.LaneTesting)
	// A prior below-cap failing verdict at Testing, recorded directly, so
	// this scenario also proves thrash_count (nonzero, but below its own
	// cap) is left completely alone by an attempt-cap requeue.
	_, err := db.Pool.Exec(ctx, `UPDATE task SET thrash_count = 1 WHERE id = $1`, task.ID)
	require.NoError(t, err)

	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `UPDATE task SET attempt_count = $1, lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $2`, store.DefaultAttemptCap-1, task.ID)
	require.NoError(t, err)

	reclaimResult, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeID, Acting: self, OnBehalfOf: self})
	require.NoError(t, err)
	require.Len(t, reclaimResult.Reclaimed, 1)
	require.True(t, reclaimResult.Reclaimed[0].CapExhausted)

	capped, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, store.DefaultAttemptCap, capped.AttemptCount)
	require.Equal(t, 1, capped.ThrashCount)
	escalationID := *capped.CurrentEscalationID

	result, err := s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.Equal(t, escalationID, result.EscalationEventID)
	assert.Equal(t, store.EscalationReasonAttemptCap, result.EscalationReason)
	assert.Equal(t, store.ResetCounterAttempt, result.CounterReset)
	assert.Equal(t, store.LaneTesting, result.ResultingLane)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, got.AttemptCount, "requeuing an attempt-cap escalation must reset attempt_count to 0")
	assert.Equal(t, 1, got.ThrashCount, "requeuing an attempt-cap escalation must leave thrash_count at its pre-requeue value, never touching the unrelated counter")
	assert.Nil(t, got.CurrentEscalationID)

	assertClaimableUnderIndexPredicate(t, ctx, db, task.ID)
}

// TestTaskStore_RequeueTask_AttemptCapViaAbandon_ResetsAttemptCountOnly is
// issue #2876's Testing section, per-reason matrix item 2 (the abandon
// half): requeuing an attempt-cap escalation triggered by AbandonClaim
// resets attempt_count to 0 and leaves thrash_count untouched.
func TestTaskStore_RequeueTask_AttemptCapViaAbandon_ResetsAttemptCountOnly(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "requeue-attempt-cap-abandon", self)
	setTaskLane(t, ctx, db, task.ID, store.LaneValidation)

	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `UPDATE task SET attempt_count = $1 WHERE id = $2`, store.DefaultAttemptCap-1, task.ID)
	require.NoError(t, err)

	abandonResult, err := s.Tasks().AbandonClaim(ctx, store.AbandonParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	require.True(t, abandonResult.CapExhausted)
	require.NotNil(t, abandonResult.EscalationID)

	capped, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, store.DefaultAttemptCap, capped.AttemptCount)
	require.Equal(t, 0, capped.ThrashCount)

	attemptRowsBefore := countRows(t, ctx, db, "task_attempt", task.ID)

	result, err := s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.Equal(t, *abandonResult.EscalationID, result.EscalationEventID)
	assert.Equal(t, store.EscalationReasonAttemptCap, result.EscalationReason)
	assert.Equal(t, store.ResetCounterAttempt, result.CounterReset)
	assert.Equal(t, store.LaneValidation, result.ResultingLane)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, got.AttemptCount, "requeuing an attempt-cap escalation must reset attempt_count to 0")
	assert.Equal(t, 0, got.ThrashCount, "requeuing an attempt-cap escalation must leave the untouched thrash_count exactly where it was")
	assert.Nil(t, got.CurrentEscalationID)
	assert.Equal(t, attemptRowsBefore, countRows(t, ctx, db, "task_attempt", task.ID), "requeue must write no task_attempt row")

	assertClaimableUnderIndexPredicate(t, ctx, db, task.ID)
}

// TestTaskStore_RequeueTask_ManualEscalationAtCap_ResetsAttemptCount_NoStrand
// is issue #2876's Testing section, per-reason matrix item 3 -- the
// no-strand assertion, "the single most important assertion in this
// task's own Implementation section": a manual escalation whose FR9
// force-close reached DefaultAttemptCap must, on requeue, ALSO reset
// attempt_count (FR9's stated exception), or the task would return to
// claimable but ClaimTask's own ErrAttemptCapExhausted check would
// immediately refuse it -- stranded, requeued but unclaimable. This test
// proves both the counter reset and that the task is actually claimable
// afterward, not merely that CounterReset reports attempt.
func TestTaskStore_RequeueTask_ManualEscalationAtCap_ResetsAttemptCount_NoStrand(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "requeue-manual-at-cap", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `UPDATE task SET attempt_count = $1 WHERE id = $2`, store.DefaultAttemptCap-1, task.ID)
	require.NoError(t, err)

	escalateResult, err := s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	require.True(t, escalateResult.CapExhausted, "the fixture must actually cross the cap via the force-close, or this test proves nothing about FR9's exception")
	require.Equal(t, store.EscalationReasonManual, escalateResult.EscalationEvent.Reason)

	capped, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, store.DefaultAttemptCap, capped.AttemptCount)

	result, err := s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.Equal(t, store.EscalationReasonManual, result.EscalationReason)
	assert.Equal(t, store.ResetCounterAttempt, result.CounterReset, "FR9's exception: a manual escalation whose force-close reached the cap must reset attempt_count on requeue")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, got.AttemptCount, "attempt_count must be reset to 0 -- otherwise the task would be requeued but immediately unclaimable again")
	assert.Nil(t, got.CurrentEscalationID)

	assertClaimableUnderIndexPredicate(t, ctx, db, task.ID)

	otherSession := claimTestSession(t, ctx, db, scopeID, self)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: otherSession, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err, "the no-strand assertion: a manual-at-cap requeue must leave the task genuinely, immediately claimable, not merely reporting a reset that ClaimTask's own cap check would then contradict")
}

// TestTaskStore_RequeueTask_ManualEscalationBelowCap_ResetsNoCounter is
// issue #2876's Testing section, per-reason matrix item 4: a manual
// escalation recorded on a task whose attempt_count never reached the
// cap resets neither counter (FR6's documented below-cap behaviour) --
// CounterReset reports "none", attempt_count is left exactly where it
// was, and the task is still claimable.
func TestTaskStore_RequeueTask_ManualEscalationBelowCap_ResetsNoCounter(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "requeue-manual-below-cap", self)
	_, err := db.Pool.Exec(ctx, `UPDATE task SET attempt_count = 1, thrash_count = 1 WHERE id = $1`, task.ID)
	require.NoError(t, err)

	escalateResult, err := s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	require.False(t, escalateResult.CapExhausted)
	require.False(t, escalateResult.ClaimForceClosed, "an unclaimed task's manual escalation must force-close nothing")

	result, err := s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.Equal(t, store.EscalationReasonManual, result.EscalationReason)
	assert.Equal(t, store.ResetCounterNone, result.CounterReset, "a below-cap manual escalation must reset neither counter")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.AttemptCount, "attempt_count must be left exactly where it was -- nothing to forgive below the cap")
	assert.Equal(t, 1, got.ThrashCount, "thrash_count must likewise be left untouched -- a manual escalation names no triggering counter")
	assert.Nil(t, got.CurrentEscalationID)

	assertClaimableUnderIndexPredicate(t, ctx, db, task.ID)
}

// TestTaskStore_RequeueTask_EscalationEventUnchanged_ExactlyOneIntervention
// is issue #2876's Testing section: task_escalation_event rows are never
// rewritten or deleted by requeue (NFR2, NFR5) -- byte-for-byte unchanged,
// read back via GetEscalationEventByID before and after -- and exactly one
// task_intervention_event names the resolved escalation afterward,
// action='requeue' (a manual EscalateTask call also appends its own
// action='escalate' intervention event with no escalation_event_id at
// all, task_escalate.go's own doc comment -- so this test counts rows
// naming the escalation specifically, not every intervention row on the
// task).
func TestTaskStore_RequeueTask_EscalationEventUnchanged_ExactlyOneIntervention(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "requeue-event-unchanged", self)
	escalateResult, err := s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	escalationID := escalateResult.EscalationEvent.ID

	before, err := s.Tasks().GetEscalationEventByID(ctx, escalationID)
	require.NoError(t, err)

	requeueReason := "investigated, safe to resume"
	result, err := s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: task.ID, Reason: &requeueReason, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	after, err := s.Tasks().GetEscalationEventByID(ctx, escalationID)
	require.NoError(t, err)
	assert.Equal(t, before, after, "task_escalation_event must be byte-for-byte unchanged by requeue -- resolution is recorded exclusively by the intervention event")

	var namingCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_intervention_event WHERE task_id = $1 AND escalation_event_id = $2`, task.ID, escalationID).Scan(&namingCount))
	assert.Equal(t, 1, namingCount, "exactly one task_intervention_event must name the resolved escalation")

	assert.Equal(t, store.InterventionActionRequeue, result.InterventionEvent.Action)
	require.NotNil(t, result.InterventionEvent.EscalationEventID)
	assert.Equal(t, escalationID, *result.InterventionEvent.EscalationEventID, "the intervention event must name the escalation it resolved")
	require.NotNil(t, result.InterventionEvent.Reason)
	assert.Equal(t, requeueReason, *result.InterventionEvent.Reason)
	assert.Equal(t, self, result.InterventionEvent.CreatedByActing)
	assert.Equal(t, self, result.InterventionEvent.CreatedByOnBehalfOf)
}

// TestTaskStore_RequeueTask_NonEscalatedTask_Refused is issue #2876's
// Testing section: requeuing a task with no active escalation is refused
// with ErrTaskNotEscalated, and nothing is written.
func TestTaskStore_RequeueTask_NonEscalatedTask_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "requeue-not-escalated", self)

	_, err := s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTaskNotEscalated)

	var interventionCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_intervention_event WHERE task_id = $1`, task.ID).Scan(&interventionCount))
	assert.Equal(t, 0, interventionCount, "a refused requeue must write no task_intervention_event row")
}

// TestTaskStore_RequeueTask_CancelledTask_Refused is issue #2876's Testing
// section: requeuing a cancelled task is refused with ErrTaskCancelled --
// even one with no active escalation, proving the cancelled check is
// independent of, and takes priority over, the not-escalated check --
// FR7's dead-letter state is one requeue can never reopen.
func TestTaskStore_RequeueTask_CancelledTask_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "requeue-cancelled", self)
	_, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	// CancelTask itself already appended one action='cancel' intervention
	// event (task_cancel.go) -- this test's own "nothing written" claim is
	// about the refused requeue call, so it must compare against this
	// baseline, not zero.
	interventionRowsBeforeRequeue := countRows(t, ctx, db, "task_intervention_event", task.ID)

	_, err = s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTaskCancelled, "a cancelled task must be refused with ErrTaskCancelled, not ErrTaskNotEscalated")

	assert.Equal(t, interventionRowsBeforeRequeue, countRows(t, ctx, db, "task_intervention_event", task.ID), "a refused requeue must write no additional task_intervention_event row")
}

// TestTaskStore_RequeueTask_DoubleRequeue_Refused is issue #2876's Testing
// section: a second requeue call after the first succeeded is refused,
// not a silent no-op that would re-reset counters that are already at
// their reset value -- since the first requeue already cleared
// current_escalation_id, the second call is refused with
// ErrTaskNotEscalated, and writes no additional task_intervention_event.
func TestTaskStore_RequeueTask_DoubleRequeue_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "requeue-double", self)
	_, err := s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	first, err := s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	require.Equal(t, store.ResetCounterNone, first.CounterReset)

	// The manual escalate call above already appended its own
	// action='escalate' intervention event; the first requeue appended a
	// second, action='requeue' -- this baseline is what the refused second
	// requeue below must leave untouched.
	interventionRowsAfterFirstRequeue := countRows(t, ctx, db, "task_intervention_event", task.ID)

	_, err = s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "a second requeue against an already-requeued task must be refused")
	assert.ErrorIs(t, err, store.ErrTaskNotEscalated)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, got.AttemptCount, "the refused second requeue must not have touched attempt_count")
	assert.Equal(t, 0, got.ThrashCount, "the refused second requeue must not have touched thrash_count")

	assert.Equal(t, interventionRowsAfterFirstRequeue, countRows(t, ctx, db, "task_intervention_event", task.ID), "the refused second requeue must write no additional task_intervention_event row")
}
