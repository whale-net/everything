//go:build integration

// Real-Postgres coverage for TaskStore.ReclaimExpired (task_reclaim.go,
// migration 015, issue #2724's Testing section, FR7): the successful-reclaim
// accounting (prior claim released with release_reason='reclaim', one
// lapsed task_attempt row, attempt_count+1 from the lapse alone,
// current_claim_id/lease_expires_at cleared, current_lane untouched),
// immediate re-claimability by a different
// session (the FR3+FR7 round trip), the zombie-heartbeat rejection after
// reclaim (the FR6/FR7 interaction), a live (unexpired) lease left
// untouched, a single-TaskID sweep touching only the named task, the
// attempt-cap terminal state (reclaim refuses to re-serve, and no
// escalation table exists anywhere in the schema), repeated-sweep
// idempotency (NFR2), NFR1's scope qualification, and NFR3's two-subject
// attribution on the lapsed task_attempt row. Shares
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
// #2724's Testing section item 5 (FR7's terminal state): a task at the
// attempt cap is reclaimed (the lapse still counts, and CapExhausted is
// reported), but reclaim refuses to re-serve it -- a subsequent ClaimTask
// by any session returns ErrAttemptCapExhausted, the task stays claimed by
// no one, and no escalation row/queue/table exists anywhere in the schema
// (M5's C26 is explicitly out of scope for this milestone).
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

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DefaultAttemptCap, got.AttemptCount)
	assert.Nil(t, got.CurrentClaimID, "a cap-exhausted task must be left claimed by no one")

	otherSession := claimTestSession(t, ctx, db, scopeID, self)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: otherSession, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "any subsequent claim of a cap-exhausted task must be refused")
	assert.ErrorIs(t, err, store.ErrAttemptCapExhausted)

	var escalationTables int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public'
		AND (table_name ILIKE '%escalation%' OR table_name ILIKE '%dead_letter%' OR table_name ILIKE '%human_attention%')
	`).Scan(&escalationTables))
	assert.Equal(t, 0, escalationTables, "no escalation destination, human-attention queue, or dead-letter table may exist anywhere in the schema (M5's C26 is out of scope)")
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
