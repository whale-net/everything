//go:build integration

// Real-Postgres coverage for TaskStore.CompleteTask (task_complete.go,
// migration 015, issue #2725's Testing section, FR8): claim-then-complete
// with `pass` advancing one lane, `fail` reverting one lane, a
// non-current claim id rejected with ErrClaimNotCurrent writing nothing,
// the completed task becoming unclaimed and claimable again, NFR3's
// two-subject attribution on the completed task_attempt row, that a clean
// claim/complete cycle never increments task.attempt_count (this file's
// own task_complete.go doc comment) no matter how many cycles a task's
// full lane_sequence takes to walk end to end, and that a dependent task
// becomes claimable once its dependency reaches Done even past that many
// cycles. NextLane's own routing branches are
// covered exhaustively, DB-free, by lane_test.go -- this file only proves
// CompleteTask's transactional wiring around it. Shares
// task_integration_test.go's test-store/test-scope/test-world/subject
// helpers, task_dependency_integration_test.go's createTestTask/
// setTaskLane helpers, and task_claim_integration_test.go's
// claimTestSession/countRows helpers rather than duplicating them. See
// store_integration_test.go's package doc for why this file only builds
// under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_complete_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// TestTaskStore_CompleteTask_Pass_AdvancesOneLane is issue #2725's Testing
// section: claiming an unclaimed task and completing it with `pass`
// advances current_lane to the next member of its own lane_sequence,
// releases the claim (release_reason='complete'), records one `completed`
// task_attempt row, and clears current_claim_id/lease_expires_at.
func TestTaskStore_CompleteTask_Pass_AdvancesOneLane(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "pass-me", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	result, err := s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Verdict: store.VerdictPass,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.Equal(t, store.LaneScaffold, result.FromLane)
	assert.Equal(t, store.LaneImplementation, result.ToLane, "pass from Scaffold in the full sequence must land on Implementation")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.LaneImplementation, got.CurrentLane)
	assert.Nil(t, got.CurrentClaimID, "a completed task must be left unclaimed")
	assert.Nil(t, got.LeaseExpiresAt)

	released, err := s.Tasks().GetClaimByID(ctx, claim.ID)
	require.NoError(t, err)
	require.NotNil(t, released.ReleasedAt)
	require.NotNil(t, released.ReleaseReason)
	assert.Equal(t, "complete", *released.ReleaseReason)

	assert.Equal(t, 1, countRows(t, ctx, db, "task_claim", task.ID))
	assert.Equal(t, 2, countRows(t, ctx, db, "task_attempt", task.ID), "the claim's own `claimed` attempt plus the complete's own `completed` attempt")

	var outcome string
	var attemptClaimID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT outcome, claim_id FROM task_attempt WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1
	`, task.ID).Scan(&outcome, &attemptClaimID))
	assert.Equal(t, "completed", outcome)
	assert.Equal(t, claim.ID, attemptClaimID)
}

// TestTaskStore_CompleteTask_PassFromLastLane_LandsOnDone proves FR8's "or
// to Done if its current lane is the last one" against a real task whose
// lane_sequence's last element is Validation.
func TestTaskStore_CompleteTask_PassFromLastLane_LandsOnDone(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "almost-done", self)
	setTaskLane(t, ctx, db, task.ID, store.LaneValidation)

	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	result, err := s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Verdict: store.VerdictPass,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.Equal(t, store.LaneDone, result.ToLane)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.LaneDone, got.CurrentLane)
}

// TestTaskStore_CompleteTask_Fail_RevertsOneLane proves the fail half of
// FR8: completing with `fail` from a middle lane goes back exactly one
// lane in the task's own sequence.
func TestTaskStore_CompleteTask_Fail_RevertsOneLane(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "fail-me", self)
	setTaskLane(t, ctx, db, task.ID, store.LaneTesting)

	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	result, err := s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Verdict: store.VerdictFail,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.Equal(t, store.LaneImplementation, result.ToLane, "fail from Testing in the full sequence must land back on Implementation")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.LaneImplementation, got.CurrentLane)
}

// TestTaskStore_CompleteTask_NonCurrentClaim_Rejected is issue #2725's
// Testing section: completing with a claim id that is not the task's
// current claim is rejected with ErrClaimNotCurrent, writes no attempt
// row, and leaves the task's lane/claim state untouched.
func TestTaskStore_CompleteTask_NonCurrentClaim_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "stale-claim", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	foreignClaimID := uuid.New()
	_, err = s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: foreignClaimID, Verdict: store.VerdictPass,
		Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.LaneScaffold, got.CurrentLane, "a rejected complete must never change current_lane")
	require.NotNil(t, got.CurrentClaimID)
	assert.Equal(t, claim.ID, *got.CurrentClaimID, "a rejected complete must leave the real current claim untouched")

	assert.Equal(t, 1, countRows(t, ctx, db, "task_attempt", task.ID), "a rejected complete must write no additional attempt row (only the original claimed one)")

	still, err := s.Tasks().GetClaimByID(ctx, claim.ID)
	require.NoError(t, err)
	assert.Nil(t, still.ReleasedAt, "a rejected complete must not release the real current claim")
}

// TestTaskStore_CompleteTask_UnclaimedTask_Rejected proves completing a
// task with no live claim at all is rejected the same way a stale claim
// id is -- ErrClaimNotCurrent, not a different error.
func TestTaskStore_CompleteTask_UnclaimedTask_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "never-claimed", self)

	_, err := s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: uuid.New(), Verdict: store.VerdictPass,
		Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent)
}

// TestTaskStore_CompleteTask_UnclaimedAndClaimableAgain is issue #2725's
// Testing section: after a successful complete, the task is unclaimed and
// claimable again (it has not reached Done).
func TestTaskStore_CompleteTask_UnclaimedAndClaimableAgain(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "reclaimable", self)
	firstSession := claimTestSession(t, ctx, db, scopeID, self)
	firstClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: firstSession,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	_, err = s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: firstClaim.ID, Verdict: store.VerdictFail,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	secondSession := claimTestSession(t, ctx, db, scopeID, self)
	secondClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: secondSession,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err, "a completed (not Done) task must be claimable again")
	assert.NotEqual(t, firstClaim.ID, secondClaim.ID)
}

// TestTaskStore_CompleteTask_CleanClaimCompleteCycleNeverIncrementsAttemptCount
// proves this file's own task_complete.go doc comment: neither the claim
// nor the completion of a clean claim/complete cycle ever moves
// task.attempt_count -- only a lapsed or abandoned attempt does
// (ReclaimExpired/AbandonClaim).
func TestTaskStore_CompleteTask_CleanClaimCompleteCycleNeverIncrementsAttemptCount(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "counted", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	before, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, 0, before.AttemptCount, "the claim itself must leave attempt_count at 0")

	_, err = s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Verdict: store.VerdictPass,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	after, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, after.AttemptCount, "completing must never increment attempt_count")
}

// TestTaskStore_CompleteTask_FullLaneSequence_NeverExhaustsAttemptCap is a
// regression test for a task walking its own lane_sequence end to end via
// nothing but passing verdicts: it must never be refused a claim, no
// matter how many claim/complete cycles that walk takes -- even well past
// DefaultAttemptCap's count of cycles, since a clean complete never counts
// against the cap.
func TestTaskStore_CompleteTask_FullLaneSequence_NeverExhaustsAttemptCap(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "full-sequence", self)
	cycles := len(task.LaneSequence) - 1 // one claim/complete cycle per lane transition, Done needs none of its own
	require.Greater(t, cycles, store.DefaultAttemptCap,
		"the default lane sequence must need more claim/complete cycles than DefaultAttemptCap to reach Done, to actually exercise this bug")

	for i := 0; i < cycles; i++ {
		sessionID := claimTestSession(t, ctx, db, scopeID, self)
		claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
			ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
			Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err, "claim %d of %d must never be refused by the attempt cap on a clean pass-only walk", i+1, cycles)

		_, err = s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
			ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Verdict: store.VerdictPass,
			Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
	}

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.LaneDone, got.CurrentLane, "a full walk of clean pass cycles must reach Done")
	assert.Equal(t, 0, got.AttemptCount, "a clean pass-only walk must never move attempt_count, no matter how many cycles it took")
}

// TestTaskStore_CompleteTask_DependentTask_ClaimableOnceDependencyDone_EvenPastAttemptCap
// is FR2's knock-on regression test: a dependent task must become
// claimable once its dependency reaches Done, even though that dependency
// needed more than DefaultAttemptCap claim/complete cycles to get there.
func TestTaskStore_CompleteTask_DependentTask_ClaimableOnceDependencyDone_EvenPastAttemptCap(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	depTask := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "dependency", self)
	cycles := len(depTask.LaneSequence) - 1 // one claim/complete cycle per lane transition, Done needs none of its own
	require.Greater(t, cycles, store.DefaultAttemptCap,
		"the dependency must need more claim/complete cycles than DefaultAttemptCap to reach Done, to actually exercise this bug")
	depTaskB := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "dependent", self)
	require.NoError(t, s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID: scopeID, TaskID: depTaskB.ID, DependsOnTaskIDs: []uuid.UUID{depTask.ID},
		Acting: self, OnBehalfOf: self,
	}))

	for i := 0; i < cycles; i++ {
		sessionID := claimTestSession(t, ctx, db, scopeID, self)
		claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
			ScopeID: scopeID, TaskID: depTask.ID, SessionID: sessionID,
			Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err, "dependency claim %d of %d must never be refused by the attempt cap on a clean pass-only walk", i+1, cycles)

		_, err = s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
			ScopeID: scopeID, TaskID: depTask.ID, ClaimID: claim.ID, Verdict: store.VerdictPass,
			Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
	}

	gotDep, err := s.Tasks().GetTaskByID(ctx, depTask.ID)
	require.NoError(t, err)
	require.Equal(t, store.LaneDone, gotDep.CurrentLane, "the dependency must have reached Done")

	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: depTaskB.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err, "the dependent task must be claimable once its dependency reaches Done, even though that took more than DefaultAttemptCap cycles")
}

// TestTaskStore_CompleteTask_RecordsBothSubjectPairs is issue #2725's
// Testing section (NFR3): both subject pairs are persisted NOT NULL on
// the `completed` task_attempt row, and a distinct acting/on-behalf-of
// pair is recorded distinctly, never collapsed into one.
func TestTaskStore_CompleteTask_RecordsBothSubjectPairs(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	acting := taskTestSubject("agent-1")
	onBehalfOf := taskTestSubject("human-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, acting)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "attributed", acting)
	sessionID := claimTestSession(t, ctx, db, scopeID, acting)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: acting, OnBehalfOf: acting,
	})
	require.NoError(t, err)

	_, err = s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Verdict: store.VerdictPass,
		Acting: acting, OnBehalfOf: onBehalfOf,
	})
	require.NoError(t, err)

	var attemptActingSub, attemptOnBehalfOfSub string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT created_by_acting_sub, created_by_on_behalf_of_sub
		FROM task_attempt WHERE claim_id = $1 AND outcome = 'completed'
	`, claim.ID).Scan(&attemptActingSub, &attemptOnBehalfOfSub))
	assert.Equal(t, "agent-1", attemptActingSub)
	assert.Equal(t, "human-1", attemptOnBehalfOfSub)
	assert.NotEqual(t, attemptActingSub, attemptOnBehalfOfSub, "a distinct acting/on-behalf-of pair must be recorded distinctly, never collapsed into one")
}
