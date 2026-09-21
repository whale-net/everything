//go:build integration

// Real-Postgres coverage for TaskStore.ReclaimExpired (task_reclaim.go,
// migration 015, issue #2724's Testing section, FR7; attempt-cap escalation
// issue #2871, root plan #2851's M5, FR3): the successful-reclaim
// accounting (prior claim released with release_reason='reclaim', one
// lapsed task_attempt row, attempt_count+1 from the lapse alone,
// current_claim_id/lease_expires_at cleared, current_lane untouched),
// immediate re-claimability by a different
// session (the FR3+FR7 round trip), the zombie-heartbeat rejection after
// reclaim (the FR6/FR7 interaction), a live (unexpired) lease left
// untouched, a single-TaskID sweep touching only the named task, the
// attempt-cap terminal state (a lapse that brings attempt_count to
// DefaultAttemptCap records exactly one task_escalation_event, reason
// 'attempt-cap', in the same transaction, sets task.current_escalation_id,
// and a subsequent ClaimTask refuses with ErrTaskEscalated, not
// ErrAttemptCapExhausted), a same-transaction rollback proof when the
// escalation insert itself fails, a multi-task sweep where only the
// capped task gets an event, repeated-sweep idempotency (NFR2, including
// no second escalation event on a repeat sweep of an already-escalated
// task), NFR1's scope qualification, and NFR3's two-subject attribution
// on the lapsed task_attempt row. Shares
// task_integration_test.go's test-store/test-scope/test-world/subject
// helpers, task_dependency_integration_test.go's createTestTask helper, and
// task_claim_integration_test.go's claimTestSession/countRows helpers,
// mirroring task_lease_integration_test.go's own choice to share rather
// than duplicate fixture helpers. See store_integration_test.go's package
// doc for why this file only builds under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_reclaim_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// TestTaskStore_ReclaimExpired_LeaseExpired_ReclaimsWithAccounting is issue
// #2724's Testing section item 1: a task whose lease expired is reclaimed --
// the prior claim is marked released_at/release_reason='reclaim', exactly
// one lapsed task_attempt row is appended, attempt_count is incremented by
// exactly one (the lapse alone -- the earlier claim never moved it),
// current_claim_id/lease_expires_at are cleared, and current_lane is left
// completely untouched (a lapse is not a verdict).
func TestTaskStore_ReclaimExpired_LeaseExpired_ReclaimsWithAccounting(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "lapsed", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, task.ID)
	require.NoError(t, err)

	result, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{
		ScopeID: scopeID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	require.Len(t, result.Reclaimed, 1)
	assert.Equal(t, task.ID, result.Reclaimed[0].TaskID)
	assert.False(t, result.Reclaimed[0].CapExhausted, "one lapse against a fresh task must not exhaust the attempt cap")

	closed, err := s.Tasks().GetClaimByID(ctx, claim.ID)
	require.NoError(t, err)
	require.NotNil(t, closed.ReleasedAt, "the stale claim must be marked released")
	require.NotNil(t, closed.ReleaseReason)
	assert.Equal(t, "reclaim", *closed.ReleaseReason)

	assert.Equal(t, 2, countRows(t, ctx, db, "task_attempt", task.ID), "the original claimed attempt plus one new lapsed attempt")
	var lapsedCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_attempt WHERE task_id = $1 AND outcome = 'lapsed'`, task.ID).Scan(&lapsedCount))
	assert.Equal(t, 1, lapsedCount, "exactly one lapsed task_attempt row must be appended")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.AttemptCount, "attempt_count must be incremented by exactly one, from the lapse alone -- the claim itself never moves it")
	assert.Nil(t, got.CurrentClaimID, "current_claim_id must be cleared")
	assert.Nil(t, got.LeaseExpiresAt, "lease_expires_at must be cleared")
	assert.Equal(t, store.LaneScaffold, got.CurrentLane, "current_lane must be untouched by a reclaim -- a lapse is not a verdict")
	assert.Nil(t, got.CurrentEscalationID, "a below-cap lapse must not escalate the task")
	assert.Nil(t, result.Reclaimed[0].EscalationID, "a below-cap lapse's result must carry no escalation reference")

	var escalationEvents int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_escalation_event WHERE task_id = $1`, task.ID).Scan(&escalationEvents))
	assert.Equal(t, 0, escalationEvents, "a below-cap lapse must write no task_escalation_event row (issue #2871's FR3 regression)")
}

// TestTaskStore_ReclaimExpired_ReclaimedTask_ImmediatelyClaimableByAnotherSession
// is issue #2724's Testing section item 2: the reclaimed task is
// immediately claimable by a different session, and that claim succeeds
// (the FR3 + FR7 round trip).
func TestTaskStore_ReclaimExpired_ReclaimedTask_ImmediatelyClaimableByAnotherSession(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "round-trip", self)
	firstSession := claimTestSession(t, ctx, db, scopeID, self)
	firstClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: firstSession,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, task.ID)
	require.NoError(t, err)

	_, err = s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeID, Acting: self, OnBehalfOf: self})
	require.NoError(t, err)

	secondSelf := taskTestSubject("agent-2")
	secondSession := claimTestSession(t, ctx, db, scopeID, secondSelf)
	secondClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: secondSession,
		Acting: secondSelf, OnBehalfOf: secondSelf,
	})
	require.NoError(t, err, "a reclaimed task must be immediately claimable by a different session")
	assert.NotEqual(t, firstClaim.ID, secondClaim.ID)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CurrentClaimID)
	assert.Equal(t, secondClaim.ID, *got.CurrentClaimID)
}

// TestTaskStore_ReclaimExpired_ZombieHeartbeat_Rejected is issue #2724's
// Testing section item 3, the FR6/FR7 interaction: a heartbeat from the
// original (zombie) run after reclaim is rejected with ErrClaimNotCurrent,
// and writes nothing.
func TestTaskStore_ReclaimExpired_ZombieHeartbeat_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "zombie", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, task.ID)
	require.NoError(t, err)

	_, err = s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeID, Acting: self, OnBehalfOf: self})
	require.NoError(t, err)

	_, err = s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "a heartbeat from the reclaimed-out zombie run must be rejected")
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent)
	assert.Equal(t, 0, countRows(t, ctx, db, "task_lease_event", task.ID), "a rejected heartbeat must write no task_lease_event row")
}

// TestTaskStore_ReclaimExpired_LiveLease_NotReclaimed is issue #2724's
// Testing section item 4: a task with a live (unexpired) lease is not
// reclaimed by the sweep.
func TestTaskStore_ReclaimExpired_LiveLease_NotReclaimed(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "live", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	result, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeID, Acting: self, OnBehalfOf: self})
	require.NoError(t, err)
	assert.Empty(t, result.Reclaimed, "a live lease must not be reclaimed")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CurrentClaimID)
	assert.Equal(t, claim.ID, *got.CurrentClaimID, "the live claim must be completely untouched")
	assert.Equal(t, 1, countRows(t, ctx, db, "task_attempt", task.ID), "no lapsed attempt row must be written for a live lease")
}

// TestTaskStore_ReclaimExpired_SingleTaskID_SweepsOnlyThatTask is issue
// #2724's Testing section coverage of ReclaimParams.TaskID: sweeping one
// named task id reclaims only that task, leaving another lease-expired task
// in the same scope completely untouched.
func TestTaskStore_ReclaimExpired_SingleTaskID_SweepsOnlyThatTask(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	taskA := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "A", self)
	taskB := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "B", self)

	sessionA := claimTestSession(t, ctx, db, scopeID, self)
	_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: taskA.ID, SessionID: sessionA, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	sessionB := claimTestSession(t, ctx, db, scopeID, self)
	claimB, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: taskB.ID, SessionID: sessionB, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id IN ($1, $2)`, taskA.ID, taskB.ID)
	require.NoError(t, err)

	result, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{
		ScopeID: scopeID, TaskID: &taskA.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	require.Len(t, result.Reclaimed, 1)
	assert.Equal(t, taskA.ID, result.Reclaimed[0].TaskID)

	gotA, err := s.Tasks().GetTaskByID(ctx, taskA.ID)
	require.NoError(t, err)
	assert.Nil(t, gotA.CurrentClaimID, "the named task must be reclaimed")

	gotB, err := s.Tasks().GetTaskByID(ctx, taskB.ID)
	require.NoError(t, err)
	require.NotNil(t, gotB.CurrentClaimID, "an un-named, equally lease-expired task must be left untouched by a single-TaskID sweep")
	assert.Equal(t, claimB.ID, *gotB.CurrentClaimID)
	assert.Equal(t, 1, countRows(t, ctx, db, "task_attempt", taskB.ID), "no lapsed attempt row must be written for the un-named task")
}

// TestTaskStore_ReclaimExpired_AttemptCapReached_RefusesToReserve is issue
// #2724's Testing section item 5 (FR7's terminal state), updated by issue
// #2871's Testing section (FR3): a task at the attempt cap is reclaimed
// (the lapse still counts, and CapExhausted is reported), the same
// transaction records exactly one task_escalation_event (reason
// 'attempt-cap', counter_value/cap_value = DefaultAttemptCap,
// lane_at_escalation = the task's current_lane) and sets
// task.current_escalation_id to it, and a subsequent ClaimTask by any
// session is refused with ErrTaskEscalated -- not ErrAttemptCapExhausted,
// since the task is now excluded "regardless of claim state" (#2851 FR2)
// rather than merely over the counter.
func TestTaskStore_ReclaimExpired_AttemptCapReached_RefusesToReserve(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "capped", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	// Force attempt_count to one below the cap so this reclaim's own
	// increment lands exactly on DefaultAttemptCap -- the boundary FR7
	// names as the terminal state.
	_, err = db.Pool.Exec(ctx, `UPDATE task SET attempt_count = $1, lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $2`, store.DefaultAttemptCap-1, task.ID)
	require.NoError(t, err)

	result, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeID, Acting: self, OnBehalfOf: self})
	require.NoError(t, err)
	require.Len(t, result.Reclaimed, 1)
	assert.True(t, result.Reclaimed[0].CapExhausted, "a lapse that brings attempt_count to DefaultAttemptCap must report CapExhausted")
	require.NotNil(t, result.Reclaimed[0].EscalationID, "a cap-exhausted reclaim must report the escalation event it wrote, in the same call")
	require.NotNil(t, result.Reclaimed[0].EscalationReason)
	assert.Equal(t, store.EscalationReasonAttemptCap, *result.Reclaimed[0].EscalationReason)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DefaultAttemptCap, got.AttemptCount)
	assert.Nil(t, got.CurrentClaimID, "a cap-exhausted task must be left claimed by no one")
	require.NotNil(t, got.CurrentEscalationID)
	assert.Equal(t, *result.Reclaimed[0].EscalationID, *got.CurrentEscalationID)

	var reason, laneAtEscalation string
	var counterValue, capValue int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT reason, counter_value, cap_value, lane_at_escalation FROM task_escalation_event WHERE task_id = $1
	`, task.ID).Scan(&reason, &counterValue, &capValue, &laneAtEscalation))
	assert.Equal(t, "attempt-cap", reason)
	assert.Equal(t, store.DefaultAttemptCap, counterValue)
	assert.Equal(t, store.DefaultAttemptCap, capValue)
	assert.Equal(t, string(store.LaneScaffold), laneAtEscalation, "lane_at_escalation must snapshot the task's current_lane at escalation time")

	var escalationEvents int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM task_escalation_event WHERE task_id = $1
	`, task.ID).Scan(&escalationEvents))
	assert.Equal(t, 1, escalationEvents, "exactly one task_escalation_event row must exist for the cap-exhausted task")

	otherSession := claimTestSession(t, ctx, db, scopeID, self)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: otherSession, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "any subsequent claim of an escalated task must be refused")
	assert.ErrorIs(t, err, store.ErrTaskEscalated, "an escalated task is refused with ErrTaskEscalated, not ErrAttemptCapExhausted, once the escalation event exists")

	// A repeat sweep leaves the task alone: it has no live claim (cleared
	// by the first reclaim), so it is no longer a reclaim candidate at
	// all, and no second escalation event appears.
	repeat, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeID, Acting: self, OnBehalfOf: self})
	require.NoError(t, err)
	assert.Empty(t, repeat.Reclaimed, "an already cap-exhausted, unclaimed task is not a reclaim candidate on a repeat sweep")
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM task_escalation_event WHERE task_id = $1
	`, task.ID).Scan(&escalationEvents))
	assert.Equal(t, 1, escalationEvents, "a repeat sweep must write no second escalation event")
}

// TestTaskStore_ReclaimExpired_RepeatedSweep_Idempotent is issue #2724's
// Testing section item 6 (NFR2): repeated sweeps over an already-reclaimed
// task write no extra attempt rows and never double-increment
// attempt_count.
func TestTaskStore_ReclaimExpired_RepeatedSweep_Idempotent(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "repeat-sweep", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, task.ID)
	require.NoError(t, err)

	first, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeID, Acting: self, OnBehalfOf: self})
	require.NoError(t, err)
	require.Len(t, first.Reclaimed, 1)

	afterFirst, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	attemptCountAfterFirst := afterFirst.AttemptCount
	rowsAfterFirst := countRows(t, ctx, db, "task_attempt", task.ID)

	second, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeID, Acting: self, OnBehalfOf: self})
	require.NoError(t, err)
	assert.Empty(t, second.Reclaimed, "a repeated sweep over an already-reclaimed task must be a no-op")

	afterSecond, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, attemptCountAfterFirst, afterSecond.AttemptCount, "a repeated sweep must never double-increment attempt_count")
	assert.Equal(t, rowsAfterFirst, countRows(t, ctx, db, "task_attempt", task.ID), "a repeated sweep must write no extra task_attempt rows")
}

// TestTaskStore_ReclaimExpired_ScopeQualified_OtherScopeUntouched is issue
// #2724's Testing section item 7 (NFR1): reclaim is scope-qualified -- an
// expired task in another scope is left completely untouched by a sweep of
// the first scope.
func TestTaskStore_ReclaimExpired_ScopeQualified_OtherScopeUntouched(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeA := newTaskTestScope(t, ctx, db)
	scopeB := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	worldA := newTaskTestWorld(t, ctx, s, scopeA, self)
	worldB := newTaskTestWorld(t, ctx, s, scopeB, self)

	taskA := createTestTask(t, ctx, s, scopeA, worldA.milepebbleID, "scope-a", self)
	taskB := createTestTask(t, ctx, s, scopeB, worldB.milepebbleID, "scope-b", self)

	sessionA := claimTestSession(t, ctx, db, scopeA, self)
	_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeA, TaskID: taskA.ID, SessionID: sessionA, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	sessionB := claimTestSession(t, ctx, db, scopeB, self)
	claimB, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeB, TaskID: taskB.ID, SessionID: sessionB, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id IN ($1, $2)`, taskA.ID, taskB.ID)
	require.NoError(t, err)

	result, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeA, Acting: self, OnBehalfOf: self})
	require.NoError(t, err)
	require.Len(t, result.Reclaimed, 1)
	assert.Equal(t, taskA.ID, result.Reclaimed[0].TaskID)

	gotB, err := s.Tasks().GetTaskByID(ctx, taskB.ID)
	require.NoError(t, err)
	require.NotNil(t, gotB.CurrentClaimID, "an expired task in a different scope must be left untouched")
	assert.Equal(t, claimB.ID, *gotB.CurrentClaimID)
	assert.Equal(t, 1, countRows(t, ctx, db, "task_attempt", taskB.ID), "no lapsed attempt row may be written for a task outside the swept scope")
}

// TestTaskStore_ReclaimExpired_ScopeQualified_UnknownTaskIDInOtherScope_NotFound
// further pins NFR1's scope qualification for the single-TaskID sweep path:
// naming a real task id that belongs to a different scope than
// params.ScopeID must be rejected as not found, never silently swept.
func TestTaskStore_ReclaimExpired_ScopeQualified_UnknownTaskIDInOtherScope_NotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeA := newTaskTestScope(t, ctx, db)
	scopeB := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	worldB := newTaskTestWorld(t, ctx, s, scopeB, self)

	taskB := createTestTask(t, ctx, s, scopeB, worldB.milepebbleID, "cross-scope", self)

	_, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{
		ScopeID: scopeA, TaskID: &taskB.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// TestTaskStore_ReclaimExpired_RecordsBothSubjectPairsOnLapsedAttempt is
// issue #2724's Testing section item 8 (NFR3): both subject pairs are
// persisted NOT NULL on the lapsed task_attempt row, and a distinct
// acting/on-behalf-of pair is recorded distinctly, never collapsed into one.
func TestTaskStore_ReclaimExpired_RecordsBothSubjectPairsOnLapsedAttempt(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	acting := taskTestSubject("agent-1")
	onBehalfOf := taskTestSubject("human-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, acting)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "attributed-lapse", acting)
	sessionID := claimTestSession(t, ctx, db, scopeID, acting)
	_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: acting, OnBehalfOf: acting,
	})
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, task.ID)
	require.NoError(t, err)

	_, err = s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{
		ScopeID: scopeID, Acting: acting, OnBehalfOf: onBehalfOf,
	})
	require.NoError(t, err)

	var actingSub, onBehalfOfSub string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT created_by_acting_sub, created_by_on_behalf_of_sub FROM task_attempt WHERE task_id = $1 AND outcome = 'lapsed'
	`, task.ID).Scan(&actingSub, &onBehalfOfSub))
	assert.Equal(t, "agent-1", actingSub)
	assert.Equal(t, "human-1", onBehalfOfSub)
	assert.NotEqual(t, actingSub, onBehalfOfSub, "a distinct acting/on-behalf-of pair must be recorded distinctly, never collapsed into one")
}

// TestTaskStore_ReclaimExpired_AttemptCapEscalation_SameTransactionRollback
// is issue #2871's Testing section "same-transaction proof": when the
// escalation insert itself fails (here, because the task already has an
// active escalation, forcing recordEscalationTx's own one-active-escalation
// rule to reject it), the entire reclaim rolls back with it -- the stale
// claim is never marked released, no lapsed task_attempt row is appended,
// and attempt_count is never incremented. A task is never left
// cap-exhausted with no escalation event explaining it, which is the
// precise failure FR3 exists to prevent.
func TestTaskStore_ReclaimExpired_AttemptCapEscalation_SameTransactionRollback(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "rollback-proof", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	// Force attempt_count to one below the cap, exactly like the ordinary
	// cap-reached test, so this reclaim's own increment would reach
	// DefaultAttemptCap and attempt to escalate -- the failure this test
	// forces below.
	_, err = db.Pool.Exec(ctx, `UPDATE task SET attempt_count = $1 WHERE id = $2`, store.DefaultAttemptCap-1, task.ID)
	require.NoError(t, err)

	rowsBefore := countRows(t, ctx, db, "task_attempt", task.ID)

	// Plant a pre-existing active escalation directly (bypassing
	// recordEscalationTx, which is exactly what this test must not go
	// through) so this reclaim's own recordEscalationTx call hits its
	// one-active-escalation rule and fails.
	var existingEscalationID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task_escalation_event (
			scope_id, task_id, reason, counter_value, cap_value, lane_at_escalation,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, 'manual', NULL, NULL, 'scaffold', 'test', 'planted', 'agent', 'test', 'planted', 'agent')
		RETURNING id
	`, scopeID, task.ID).Scan(&existingEscalationID))
	_, err = db.Pool.Exec(ctx, `UPDATE task SET current_escalation_id = $1, lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $2`,
		existingEscalationID, task.ID)
	require.NoError(t, err)

	_, err = s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeID, Acting: self, OnBehalfOf: self})
	require.Error(t, err, "a reclaim whose own escalation insert fails must fail as a whole")
	assert.ErrorIs(t, err, store.ErrTaskEscalated)

	stillOpen, err := s.Tasks().GetClaimByID(ctx, claim.ID)
	require.NoError(t, err)
	assert.Nil(t, stillOpen.ReleasedAt, "a rolled-back reclaim must leave the stale claim exactly as it was, not released")

	assert.Equal(t, rowsBefore, countRows(t, ctx, db, "task_attempt", task.ID), "a rolled-back reclaim must write no lapsed task_attempt row")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DefaultAttemptCap-1, got.AttemptCount, "a rolled-back reclaim must never increment attempt_count")
	require.NotNil(t, got.CurrentClaimID, "a rolled-back reclaim must leave current_claim_id untouched")
	assert.Equal(t, claim.ID, *got.CurrentClaimID)

	var escalationEvents int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_escalation_event WHERE task_id = $1`, task.ID).Scan(&escalationEvents))
	assert.Equal(t, 1, escalationEvents, "only the one planted escalation event must exist -- the failed reclaim wrote no second one")
}

// TestTaskStore_ReclaimExpired_MultiTaskSweep_OnlyCappedTaskEscalates is
// issue #2871's Testing section: a multi-task ReclaimExpired sweep where
// one task hits the attempt cap and another does not records exactly one
// escalation event, for the capped task alone -- one task's escalation
// never touches, or is affected by, another task's own reclaim in the same
// sweep.
func TestTaskStore_ReclaimExpired_MultiTaskSweep_OnlyCappedTaskEscalates(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	cappedTask := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "sweep-capped", self)
	plainTask := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "sweep-plain", self)

	cappedSession := claimTestSession(t, ctx, db, scopeID, self)
	_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: cappedTask.ID, SessionID: cappedSession, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `UPDATE task SET attempt_count = $1, lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $2`,
		store.DefaultAttemptCap-1, cappedTask.ID)
	require.NoError(t, err)

	plainSession := claimTestSession(t, ctx, db, scopeID, self)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: plainTask.ID, SessionID: plainSession, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, plainTask.ID)
	require.NoError(t, err)

	result, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeID, Acting: self, OnBehalfOf: self})
	require.NoError(t, err)
	require.Len(t, result.Reclaimed, 2)

	byID := map[uuid.UUID]store.ReclaimedTask{}
	for _, r := range result.Reclaimed {
		byID[r.TaskID] = r
	}
	require.Contains(t, byID, cappedTask.ID)
	require.Contains(t, byID, plainTask.ID)
	assert.True(t, byID[cappedTask.ID].CapExhausted)
	assert.NotNil(t, byID[cappedTask.ID].EscalationID)
	assert.False(t, byID[plainTask.ID].CapExhausted)
	assert.Nil(t, byID[plainTask.ID].EscalationID)

	var totalEscalations int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM task_escalation_event WHERE task_id IN ($1, $2)
	`, cappedTask.ID, plainTask.ID).Scan(&totalEscalations))
	assert.Equal(t, 1, totalEscalations, "exactly one escalation event must exist across the sweep, for the capped task alone")
}
