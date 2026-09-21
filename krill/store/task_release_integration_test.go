//go:build integration

// Real-Postgres coverage for TaskStore.ReleaseLease (task_release.go,
// migration 016, issue #2872's Testing section, FR8): releasing a live
// lease (claimable again immediately, attempt_count+1, one task_attempt
// row, one task_intervention_event with both subjects), the original
// claimant's subsequent heartbeat/complete/abandon each rejected exactly
// the way a reclaim already rejects one, release on an unclaimed task
// refused, cap-crossing recording the attempt-cap escalation in the same
// call (and the whole release rolling back when that escalation insert
// itself fails), and release on a cancelled task refused. Shares
// task_integration_test.go's test-store/test-scope/test-world/subject
// helpers, task_dependency_integration_test.go's createTestTask helper,
// and task_claim_integration_test.go's claimTestSession/countRows
// helpers, mirroring task_reclaim_integration_test.go's own choice to
// share rather than duplicate fixture helpers. See
// store_integration_test.go's package doc for why this file only builds
// under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_release_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// TestTaskStore_ReleaseLease_LiveLease_ReleasesWithAccounting is issue
// #2872's Testing section "release" item 1: releasing a live lease
// force-closes the claim, leaves the task claimable again immediately,
// increments attempt_count by exactly one, appends exactly one `released`
// task_attempt row, and appends exactly one task_intervention_event row
// (action='release') carrying both LB4 subject pairs. current_lane is
// left untouched -- a release is not a verdict.
func TestTaskStore_ReleaseLease_LiveLease_ReleasesWithAccounting(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	acting := taskTestSubject("operator-1")
	onBehalfOf := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, acting)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "released", acting)
	sessionID := claimTestSession(t, ctx, db, scopeID, acting)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: acting, OnBehalfOf: acting,
	})
	require.NoError(t, err)

	result, err := s.Tasks().ReleaseLease(ctx, store.ReleaseParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: acting, OnBehalfOf: onBehalfOf,
	})
	require.NoError(t, err)
	assert.Equal(t, task.ID, result.TaskID)
	assert.Equal(t, claim.ID, result.ClaimID)
	assert.False(t, result.CapExhausted, "one release against a fresh task must not exhaust the attempt cap")
	assert.Nil(t, result.EscalationID)
	assert.Nil(t, result.EscalationReason)

	closed, err := s.Tasks().GetClaimByID(ctx, claim.ID)
	require.NoError(t, err)
	require.NotNil(t, closed.ReleasedAt, "the released claim must be marked released")
	require.NotNil(t, closed.ReleaseReason)
	assert.Equal(t, "release", *closed.ReleaseReason)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Nil(t, got.CurrentClaimID, "the task must be claimable again immediately")
	assert.Nil(t, got.LeaseExpiresAt)
	assert.Equal(t, 1, got.AttemptCount, "attempt_count must be incremented by exactly one")
	assert.Equal(t, store.LaneScaffold, got.CurrentLane, "current_lane must be untouched -- release is not a verdict")
	assert.Nil(t, got.CurrentEscalationID)

	assert.Equal(t, 2, countRows(t, ctx, db, "task_attempt", task.ID), "the original claimed attempt plus one new released attempt")
	var releasedCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_attempt WHERE task_id = $1 AND outcome = 'released'`, task.ID).Scan(&releasedCount))
	assert.Equal(t, 1, releasedCount, "exactly one released task_attempt row must be appended")

	var interventionCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_intervention_event WHERE task_id = $1`, task.ID).Scan(&interventionCount))
	assert.Equal(t, 1, interventionCount, "exactly one task_intervention_event row must be appended")
	assert.Equal(t, store.InterventionActionRelease, result.InterventionEvent.Action)
	assert.Equal(t, acting, result.InterventionEvent.CreatedByActing)
	assert.Equal(t, onBehalfOf, result.InterventionEvent.CreatedByOnBehalfOf)

	// The task must be immediately claimable by a different session (M4
	// FR3 + FR8's round trip).
	secondSelf := taskTestSubject("agent-2")
	secondSession := claimTestSession(t, ctx, db, scopeID, secondSelf)
	secondClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: secondSession, Acting: secondSelf, OnBehalfOf: secondSelf,
	})
	require.NoError(t, err, "a released task must be immediately claimable by a different session")
	assert.NotEqual(t, claim.ID, secondClaim.ID)
}

// TestTaskStore_ReleaseLease_OriginalClaimant_SubsequentCallsRejected is
// issue #2872's Testing section "release" item 2: the original claimant's
// subsequent heartbeat, complete, and abandon against the released claim
// are each rejected -- the same rejection M4 FR6 gives after a reclaim.
func TestTaskStore_ReleaseLease_OriginalClaimant_SubsequentCallsRejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	makeReleasedClaim := func(t *testing.T, title string) store.Claim {
		t.Helper()
		task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, title, self)
		sessionID := claimTestSession(t, ctx, db, scopeID, self)
		claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
			ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
		_, err = s.Tasks().ReleaseLease(ctx, store.ReleaseParams{
			ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
		return claim
	}

	t.Run("heartbeat", func(t *testing.T) {
		claim := makeReleasedClaim(t, "zombie-heartbeat")
		_, err := s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
			ScopeID: scopeID, TaskID: claim.TaskID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
		})
		require.Error(t, err, "a heartbeat against a released claim must be rejected")
		assert.ErrorIs(t, err, store.ErrClaimNotCurrent)
	})

	t.Run("complete", func(t *testing.T) {
		claim := makeReleasedClaim(t, "zombie-complete")
		_, err := s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
			ScopeID: scopeID, TaskID: claim.TaskID, ClaimID: claim.ID, Verdict: store.VerdictPass, Acting: self, OnBehalfOf: self,
		})
		require.Error(t, err, "a complete against a released claim must be rejected")
		assert.ErrorIs(t, err, store.ErrClaimNotCurrent)
	})

	t.Run("abandon", func(t *testing.T) {
		claim := makeReleasedClaim(t, "zombie-abandon")
		_, err := s.Tasks().AbandonClaim(ctx, store.AbandonParams{
			ScopeID: scopeID, TaskID: claim.TaskID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
		})
		require.Error(t, err, "an abandon against a released claim must be rejected")
		assert.ErrorIs(t, err, store.ErrClaimNotCurrent)
	})
}

// TestTaskStore_ReleaseLease_UnclaimedTask_Refused is issue #2872's
// Testing section "release" item 3: release on an unclaimed task is
// refused with ErrTaskNotClaimed, and writes nothing.
func TestTaskStore_ReleaseLease_UnclaimedTask_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "unclaimed", self)

	_, err := s.Tasks().ReleaseLease(ctx, store.ReleaseParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTaskNotClaimed)

	assert.Equal(t, 0, countRows(t, ctx, db, "task_attempt", task.ID), "a refused release must write no task_attempt row")
	assert.Equal(t, 0, countRows(t, ctx, db, "task_intervention_event", task.ID), "a refused release must write no task_intervention_event row")
}

// TestTaskStore_ReleaseLease_CapCrossing_RecordsAttemptCapEscalation is
// issue #2872's Testing section "release" item 4: a release that takes
// attempt_count to DefaultAttemptCap records the attempt-cap escalation
// in the same call, and the task is then unclaimable (ErrTaskEscalated).
func TestTaskStore_ReleaseLease_CapCrossing_RecordsAttemptCapEscalation(t *testing.T) {
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

	_, err = db.Pool.Exec(ctx, `UPDATE task SET attempt_count = $1 WHERE id = $2`, store.DefaultAttemptCap-1, task.ID)
	require.NoError(t, err)

	result, err := s.Tasks().ReleaseLease(ctx, store.ReleaseParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.True(t, result.CapExhausted, "a release that brings attempt_count to DefaultAttemptCap must report CapExhausted")
	require.NotNil(t, result.EscalationID)
	require.NotNil(t, result.EscalationReason)
	assert.Equal(t, store.EscalationReasonAttemptCap, *result.EscalationReason)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DefaultAttemptCap, got.AttemptCount)
	require.NotNil(t, got.CurrentEscalationID)
	assert.Equal(t, *result.EscalationID, *got.CurrentEscalationID)

	var escalationEvents int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_escalation_event WHERE task_id = $1`, task.ID).Scan(&escalationEvents))
	assert.Equal(t, 1, escalationEvents, "exactly one task_escalation_event row must exist for the cap-exhausted task")

	otherSession := claimTestSession(t, ctx, db, scopeID, self)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: otherSession, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "any subsequent claim of an escalated task must be refused")
	assert.ErrorIs(t, err, store.ErrTaskEscalated)
}

// TestTaskStore_ReleaseLease_CapCrossing_EscalationInsertFails_RollsBack
// is issue #2872's Testing section "release" item 4's second half: when
// the escalation insert itself fails (forced here by planting a
// pre-existing active escalation, exactly like
// TestTaskStore_ReclaimExpired_AttemptCapEscalation_SameTransactionRollback),
// the entire release rolls back with it -- the claim is never marked
// released, no task_attempt row is appended, and attempt_count is never
// incremented.
func TestTaskStore_ReleaseLease_CapCrossing_EscalationInsertFails_RollsBack(t *testing.T) {
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

	_, err = db.Pool.Exec(ctx, `UPDATE task SET attempt_count = $1 WHERE id = $2`, store.DefaultAttemptCap-1, task.ID)
	require.NoError(t, err)

	rowsBefore := countRows(t, ctx, db, "task_attempt", task.ID)

	// Plant a pre-existing active escalation directly (bypassing
	// recordEscalationTx) so this release's own recordEscalationTx call
	// hits its one-active-escalation rule and fails.
	var existingEscalationID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task_escalation_event (
			scope_id, task_id, reason, counter_value, cap_value, lane_at_escalation,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, 'manual', NULL, NULL, 'scaffold', 'test', 'planted', 'agent', 'test', 'planted', 'agent')
		RETURNING id
	`, scopeID, task.ID).Scan(&existingEscalationID))
	_, err = db.Pool.Exec(ctx, `UPDATE task SET current_escalation_id = $1 WHERE id = $2`, existingEscalationID, task.ID)
	require.NoError(t, err)

	_, err = s.Tasks().ReleaseLease(ctx, store.ReleaseParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "a release whose own escalation insert fails must fail as a whole")
	assert.ErrorIs(t, err, store.ErrTaskEscalated)

	stillOpen, err := s.Tasks().GetClaimByID(ctx, claim.ID)
	require.NoError(t, err)
	assert.Nil(t, stillOpen.ReleasedAt, "a rolled-back release must leave the claim exactly as it was, not released")

	assert.Equal(t, rowsBefore, countRows(t, ctx, db, "task_attempt", task.ID), "a rolled-back release must write no task_attempt row")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DefaultAttemptCap-1, got.AttemptCount, "a rolled-back release must never increment attempt_count")
	require.NotNil(t, got.CurrentClaimID, "a rolled-back release must leave current_claim_id untouched")
	assert.Equal(t, claim.ID, *got.CurrentClaimID)

	var escalationEvents int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_escalation_event WHERE task_id = $1`, task.ID).Scan(&escalationEvents))
	assert.Equal(t, 1, escalationEvents, "only the one planted escalation event must exist -- the failed release wrote no second one")
}

// TestTaskStore_ReleaseLease_CancelledTask_Refused is issue #2872's
// Testing section "Both verbs refuse a cancelled task": release against a
// cancelled task is refused with ErrTaskCancelled, and writes nothing.
func TestTaskStore_ReleaseLease_CancelledTask_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "cancelled", self)
	_, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = s.Tasks().ReleaseLease(ctx, store.ReleaseParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTaskCancelled)

	assert.Equal(t, 0, countRows(t, ctx, db, "task_attempt", task.ID), "a refused release must write no task_attempt row")
}
