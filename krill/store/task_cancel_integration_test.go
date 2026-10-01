//go:build integration

// Real-Postgres coverage for TaskStore.CancelTask (task_cancel.go,
// migration 016, issue #2873's Testing section, FR7, NFR5): cancelling an
// unclaimed task (cancelled_at set, one intervention event with both
// subjects, current_lane unchanged and never Done), cancelling a claimed
// task (claim force-closed, a subsequent heartbeat/complete rejected the
// same way a reclaim already rejects one), cancelling an escalated task
// (succeeds), ClaimTask afterwards refusing with ErrTaskCancelled
// (including under concurrency), cancelling an already-cancelled task
// (ErrTaskAlreadyCancelled, nothing further written), and NFR5's
// byte-for-byte task_attempt/task_note proof. Shares
// task_integration_test.go's test-store/test-scope/test-world/subject
// helpers, task_dependency_integration_test.go's createTestTask helper,
// and task_claim_integration_test.go's claimTestSession/countRows helpers
// rather than duplicating them. See store_integration_test.go's package
// doc for why this file only builds under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_cancel_integration_test --test_output=all
package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// setTaskEscalated forces taskID's current_escalation_id directly (no
// escalate verb exists yet -- FR9 is a later M5 task) so
// TestTaskStore_CancelTask_EscalatedTask_Succeeds can exercise FR7's
// "works on an escalated task too" rule without depending on that later
// task landing first. Mirrors task_dependency_integration_test.go's
// setTaskLane precedent for the identical "no store method exists yet"
// situation.
func setTaskEscalated(t *testing.T, ctx context.Context, db *dbtest.Postgres, taskID uuid.UUID) uuid.UUID {
	t.Helper()
	var escalationID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO task_escalation_event (
			scope_id, task_id, reason, counter_value, cap_value, lane_at_escalation,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		)
		SELECT scope_id, id, 'manual', NULL, NULL, current_lane,
			'https://issuer.example.com', 'operator-1', 'human',
			'https://issuer.example.com', 'operator-1', 'human'
		FROM task WHERE id = $1
		RETURNING id
	`, taskID).Scan(&escalationID))
	_, err := db.Pool.Exec(ctx, `UPDATE task SET current_escalation_id = $1 WHERE id = $2`, escalationID, taskID)
	require.NoError(t, err)
	return escalationID
}

// TestTaskStore_CancelTask_UnclaimedTask_SetsTerminalState is issue
// #2873's Testing section item 1: cancelling an unclaimed task sets
// cancelled_at, appends exactly one task_intervention_event row carrying
// both LB4 subject pairs, and leaves current_lane exactly where it was --
// never routed to Done.
func TestTaskStore_CancelTask_UnclaimedTask_SetsTerminalState(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	acting := taskTestSubject("operator-1")
	onBehalfOf := taskTestSubject("swarm-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, acting)
	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "cancel me", acting)

	reason := "no longer needed"
	result, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID:    scopeID,
		TaskID:     task.ID,
		Reason:     &reason,
		Acting:     acting,
		OnBehalfOf: onBehalfOf,
	})
	require.NoError(t, err)
	assert.Equal(t, task.ID, result.TaskID)
	assert.False(t, result.ClaimForceClosed, "an unclaimed task has no claim to force-close")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CancelledAt, "cancelled_at must be set")
	assert.Equal(t, store.LaneScaffold, got.CurrentLane, "current_lane must be left exactly where it was")
	assert.NotEqual(t, store.LaneDone, got.CurrentLane, "cancel is distinct from the Done terminal state")

	assert.Equal(t, 1, countRows(t, ctx, db, "task_intervention_event", task.ID), "exactly one intervention event must be appended")
	assert.Equal(t, store.InterventionActionCancel, result.InterventionEvent.Action)
	assert.Nil(t, result.InterventionEvent.EscalationEventID, "cancel never names an escalation -- only requeue does")
	require.NotNil(t, result.InterventionEvent.Reason)
	assert.Equal(t, reason, *result.InterventionEvent.Reason)
	assert.Equal(t, acting, result.InterventionEvent.CreatedByActing)
	assert.Equal(t, onBehalfOf, result.InterventionEvent.CreatedByOnBehalfOf)
}

// TestTaskStore_CancelTask_ClaimedTask_ForceClosesClaim is issue #2873's
// Testing section item 2: cancelling a claimed task force-closes the
// claim, and the claimant's subsequent heartbeat and complete are both
// rejected exactly the way they already are after a reclaim
// (ErrClaimNotCurrent) -- a still-heartbeating claimant must not be able
// to keep writing against a task the console now shows as dead-lettered.
func TestTaskStore_CancelTask_ClaimedTask_ForceClosesClaim(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)
	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "claimed then cancelled", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)

	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	result, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.True(t, result.ClaimForceClosed)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Nil(t, got.CurrentClaimID, "the force-closed claim must be cleared from the task row")
	assert.Nil(t, got.LeaseExpiresAt)

	var releasedAt *string
	var releaseReason *string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT released_at::text, release_reason FROM task_claim WHERE id = $1
	`, claim.ID).Scan(&releasedAt, &releaseReason))
	require.NotNil(t, releasedAt, "the claim must be marked released")
	require.NotNil(t, releaseReason)
	assert.Equal(t, "cancel", *releaseReason)

	// The claimant's heartbeat, arriving after the force-close, is
	// rejected exactly like a heartbeat arriving after a reclaim.
	_, err = s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent)

	// The claimant's complete, arriving after the force-close, is
	// rejected the same way.
	_, err = s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Verdict: store.VerdictPass, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent)
}

// TestTaskStore_CancelTask_EscalatedTask_Succeeds is issue #2873's Testing
// section item 3: cancelling an escalated task succeeds -- cancel is the
// "terminate" half of the recover-or-terminate pair FR6's requeue is the
// other half of.
func TestTaskStore_CancelTask_EscalatedTask_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)
	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "escalated then cancelled", self)
	setTaskEscalated(t, ctx, db, task.ID)

	_, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err, "cancelling an escalated task must succeed (FR7)")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CancelledAt)
}

// TestTaskStore_CancelTask_ThenClaimTask_Rejected is issue #2873's Testing
// section item 4: ClaimTask against a cancelled task returns
// ErrTaskCancelled, and N concurrent claims all fail the same way
// (task_claimable_idx's own predicate excludes it, not only ClaimTask's
// Go-level check).
func TestTaskStore_CancelTask_ThenClaimTask_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)
	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "unclaimable forever", self)

	_, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	const concurrency = 5
	var wg sync.WaitGroup
	errs := make([]error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sessionID := claimTestSession(t, ctx, db, scopeID, self)
			_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
				ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		require.Error(t, err)
		assert.ErrorIs(t, err, store.ErrTaskCancelled)
	}
	assert.Equal(t, 0, countRows(t, ctx, db, "task_claim", task.ID), "no concurrent claim against a cancelled task may ever write a task_claim row")
}

// TestTaskStore_CancelTask_AlreadyCancelled_Rejected is issue #2873's
// Testing section item 5: cancelling an already-cancelled task is a clear
// error (ErrTaskAlreadyCancelled), and writes nothing further -- exactly
// one intervention event ever exists for the task.
func TestTaskStore_CancelTask_AlreadyCancelled_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)
	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "cancel twice", self)

	_, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTaskAlreadyCancelled)

	assert.Equal(t, 1, countRows(t, ctx, db, "task_intervention_event", task.ID), "a rejected re-cancel must write no second intervention event")
}

// TestTaskStore_CancelTask_NFR5_HistoryUntouched is issue #2873's Testing
// section's NFR5 item: task_attempt and task_note rows are byte-for-byte
// identical before and after a cancel -- cancellation is an appended
// event plus the terminal marker on the task row, nothing already
// written is ever rewritten.
func TestTaskStore_CancelTask_NFR5_HistoryUntouched(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)
	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "history preserved", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)

	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &task.ID, Kind: store.NoteKindComment, Body: "worth remembering", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	beforeAttempts := fetchTaskAttemptRows(t, ctx, db, task.ID)
	beforeNotes := fetchTaskNoteRows(t, ctx, db, task.ID)
	require.NotEmpty(t, beforeAttempts)
	require.NotEmpty(t, beforeNotes)

	_, err = s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	afterAttempts := fetchTaskAttemptRows(t, ctx, db, task.ID)
	afterNotes := fetchTaskNoteRows(t, ctx, db, task.ID)
	assert.Equal(t, beforeAttempts, afterAttempts, "task_attempt rows must be byte-for-byte identical before and after cancel")
	assert.Equal(t, beforeNotes, afterNotes, "task_note rows must be byte-for-byte identical before and after cancel")

	_ = claim
	_ = note
}

// fetchTaskAttemptRows returns every task_attempt row for taskID, in a
// stable order, as a comparable snapshot for NFR5's before/after proof.
func fetchTaskAttemptRows(t *testing.T, ctx context.Context, db *dbtest.Postgres, taskID uuid.UUID) []map[string]any {
	t.Helper()
	rows, err := db.Pool.Query(ctx, `
		SELECT id, claim_id, outcome, created_at FROM task_attempt WHERE task_id = $1 ORDER BY created_at, id
	`, taskID)
	require.NoError(t, err)
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var id, claimID uuid.UUID
		var outcome string
		var createdAt any
		require.NoError(t, rows.Scan(&id, &claimID, &outcome, &createdAt))
		out = append(out, map[string]any{"id": id, "claim_id": claimID, "outcome": outcome, "created_at": createdAt})
	}
	require.NoError(t, rows.Err())
	return out
}

// fetchTaskNoteRows returns every task_note row for taskID, in a stable
// order, as a comparable snapshot for NFR5's before/after proof.
func fetchTaskNoteRows(t *testing.T, ctx context.Context, db *dbtest.Postgres, taskID uuid.UUID) []map[string]any {
	t.Helper()
	rows, err := db.Pool.Query(ctx, `
		SELECT id, kind, body, current_status, created_at FROM task_note WHERE task_id = $1 ORDER BY created_at, id
	`, taskID)
	require.NoError(t, err)
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var id uuid.UUID
		var kind, body, currentStatus string
		var createdAt any
		require.NoError(t, rows.Scan(&id, &kind, &body, &currentStatus, &createdAt))
		out = append(out, map[string]any{"id": id, "kind": kind, "body": body, "current_status": currentStatus, "created_at": createdAt})
	}
	require.NoError(t, rows.Err())
	return out
}

// TestTaskStore_CancelTask_ExpectedClaimID_Matches_Succeeds is the
// observed-state guard's happy path for a Claimed-row cancel: supplying the
// claim the row named still cancels, force-closing that claim.
func TestTaskStore_CancelTask_ExpectedClaimID_Matches_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "guarded claimed-row cancel", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	result, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
		ExpectedClaimID: &claim.ID,
	})
	require.NoError(t, err, "a cancel guarding the claim that is still current must succeed")
	assert.True(t, result.ClaimForceClosed)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CancelledAt)
}

// TestTaskStore_CancelTask_ExpectedEscalationID_Matches_Succeeds is the
// Escalated-row half: supplying the escalation the row named still cancels
// an escalated task (FR7's rule), leaving the escalation itself untouched.
func TestTaskStore_CancelTask_ExpectedEscalationID_Matches_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "guarded escalated-row cancel", self)
	escalationID := setTaskEscalated(t, ctx, db, task.ID)

	result, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
		ExpectedEscalationID: &escalationID,
	})
	require.NoError(t, err, "a cancel guarding the escalation that is still current must succeed")
	assert.False(t, result.ClaimForceClosed)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CancelledAt)
	require.NotNil(t, got.CurrentEscalationID, "cancel never clears current_escalation_id -- the escalation's history is left as the record it was")
	assert.Equal(t, escalationID, *got.CurrentEscalationID, "a guarded escalated-row cancel must still leave exactly that escalation current")
}

// TestTaskStore_CancelTask_ExpectedClaimID_Stale_Refused is the guard on a
// Claimed-row cancel: a cancel posted from a stale page is refused with
// ErrObservedStateMismatch, writes nothing, and never force-closes the
// claim that became current in the meantime.
func TestTaskStore_CancelTask_ExpectedClaimID_Stale_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "stale claimed-row cancel", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	staleClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	expireTaskLease(t, ctx, db, task.ID)
	currentClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: claimTestSession(t, ctx, db, scopeID, self), Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	require.NotEqual(t, staleClaim.ID, currentClaim.ID)

	_, err = s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
		ExpectedClaimID: &staleClaim.ID,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrObservedStateMismatch)
	assert.NotErrorIs(t, err, store.ErrTaskAlreadyCancelled)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Nil(t, got.CancelledAt, "a refused guarded cancel must not have cancelled the task")
	assert.Equal(t, 0, countRows(t, ctx, db, "task_intervention_event", task.ID), "a refused guarded cancel must write no task_intervention_event row")

	stillOpen, err := s.Tasks().GetClaimByID(ctx, currentClaim.ID)
	require.NoError(t, err)
	assert.Nil(t, stillOpen.ReleasedAt, "a cancel guarded by a stale claim id must never force-close the claim that is current now")
}

// TestTaskStore_CancelTask_ExpectedEscalationID_Stale_Refused is the
// Escalated-row guard: a cancel guarding a superseded escalation is
// refused and writes nothing.
func TestTaskStore_CancelTask_ExpectedEscalationID_Stale_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "stale escalated-row cancel", self)
	staleEscalationID := setTaskEscalated(t, ctx, db, task.ID)
	_, err := s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	currentEscalationID := setTaskEscalated(t, ctx, db, task.ID)
	require.NotEqual(t, staleEscalationID, currentEscalationID)

	_, err = s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
		ExpectedEscalationID: &staleEscalationID,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrObservedStateMismatch)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Nil(t, got.CancelledAt, "a refused guarded cancel must not have cancelled the task")
	require.NotNil(t, got.CurrentEscalationID)
	assert.Equal(t, currentEscalationID, *got.CurrentEscalationID, "the escalation that is current now must survive a cancel guarded by a stale id")
}

// TestTaskStore_CancelTask_ExpectedClaimID_NoClaim_Mismatch is cancel's own
// mismatch rule: an expected claim id supplied while the task holds no
// claim is the mismatch refusal -- cancel has no not-claimed legality
// refusal to confuse it with, and the supplied id is what makes the
// difference.
func TestTaskStore_CancelTask_ExpectedClaimID_NoClaim_Mismatch(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "cancel unclaimed", self)
	staleClaim := uuid.New()

	_, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
		ExpectedClaimID: &staleClaim,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrObservedStateMismatch)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Nil(t, got.CancelledAt, "a refused guarded cancel must not have cancelled the task")
}

// TestTaskStore_CancelTask_ExpectedEscalationID_NoEscalation_Mismatch is
// the same rule for the escalation half of an Escalated-row cancel.
func TestTaskStore_CancelTask_ExpectedEscalationID_NoEscalation_Mismatch(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "cancel not escalated", self)
	staleEscalation := uuid.New()

	_, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
		ExpectedEscalationID: &staleEscalation,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrObservedStateMismatch)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Nil(t, got.CancelledAt, "a refused guarded cancel must not have cancelled the task")
}

// TestTaskStore_CancelTask_AlreadyCancelled_BeatsObservedMismatch is the
// check order every guarded call shares, seen from cancel: a stale
// double-submitted Cancel gets already-cancelled, not the mismatch -- even
// though cancel has no legality refusal for "holds no claim", so the
// mismatch is what an unguarded double submit would otherwise hit.
func TestTaskStore_CancelTask_AlreadyCancelled_BeatsObservedMismatch(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "cancel twice guarded", self)
	_, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	staleClaim := uuid.New()
	_, err = s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
		ExpectedClaimID: &staleClaim,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTaskAlreadyCancelled, "the already-cancelled refusal runs ahead of the observed-state guard")
	assert.NotErrorIs(t, err, store.ErrObservedStateMismatch)
	assert.Equal(t, 1, countRows(t, ctx, db, "task_intervention_event", task.ID), "exactly one intervention event may ever exist for the task")
}

// TestTaskStore_CancelTask_NoExpectedIDs_Unguarded is the back-compat
// half: with both ids omitted the cancel behaves exactly as before, on
// both a claimed and an escalated task.
func TestTaskStore_CancelTask_NoExpectedIDs_Unguarded(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	claimedTask := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "unguarded claimed cancel", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	staleClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: claimedTask.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	expireTaskLease(t, ctx, db, claimedTask.ID)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: claimedTask.ID, SessionID: claimTestSession(t, ctx, db, scopeID, self), Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	result, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: claimedTask.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err, "omitting both ids must leave today's behaviour untouched")
	assert.True(t, result.ClaimForceClosed)
	_ = staleClaim

	escalatedTask := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "unguarded escalated cancel", self)
	setTaskEscalated(t, ctx, db, escalatedTask.ID)
	_, err = s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: escalatedTask.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err, "omitting both ids must leave an escalated task cancellable exactly as before")
}

// TestTaskStore_CancelTask_BothExpectedIDs_Succeeds is the guard's both-ids
// happy path: a task holding a claim and an escalation at once (a claim
// taken while an escalation was current) cancelled with both ids supplied
// still cancels -- the two ids are each checked against their own column,
// not against each other.
func TestTaskStore_CancelTask_BothExpectedIDs_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "guarded both-ids cancel", self)
	// Claim first, then escalate: an escalated task is excluded from
	// task_claimable_idx, so the claim has to predate the escalation.
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: claimTestSession(t, ctx, db, scopeID, self), Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	escalationID := setTaskEscalated(t, ctx, db, task.ID)

	result, err := s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: self, OnBehalfOf: self,
		ExpectedClaimID: &claim.ID, ExpectedEscalationID: &escalationID,
	})
	require.NoError(t, err, "both ids current must let the cancel through")
	assert.True(t, result.ClaimForceClosed)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CancelledAt)
}

// TestTaskStore_CancelTask_BothExpectedIDs_EitherMismatchRefused is "each
// id that is supplied must match", with both supplied: a cancel whose claim
// id is current but whose escalation id is stale (and the mirror) is
// refused. This is what stops a cancel from being guarded on the id the
// caller happened to look at while the other moved on unseen.
func TestTaskStore_CancelTask_BothExpectedIDs_EitherMismatchRefused(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("operator-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	// A claim matching while the escalation is stale.
	staleEscalationTask := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "stale escalation half", self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: staleEscalationTask.ID, SessionID: claimTestSession(t, ctx, db, scopeID, self), Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	staleEscalationID := setTaskEscalated(t, ctx, db, staleEscalationTask.ID)
	// Resolve that escalation and record a second one, so the caller's id is
	// stale while a different escalation is current.
	_, err = s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: staleEscalationTask.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	currentEscalationID := setTaskEscalated(t, ctx, db, staleEscalationTask.ID)
	require.NotEqual(t, staleEscalationID, currentEscalationID)

	interventionRowsBefore := countRows(t, ctx, db, "task_intervention_event", staleEscalationTask.ID)

	_, err = s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: staleEscalationTask.ID, Acting: self, OnBehalfOf: self,
		ExpectedClaimID: &claim.ID, ExpectedEscalationID: &staleEscalationID,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrObservedStateMismatch, "a matching claim id must not excuse a stale escalation id")

	got, err := s.Tasks().GetTaskByID(ctx, staleEscalationTask.ID)
	require.NoError(t, err)
	assert.Nil(t, got.CancelledAt, "a refused guarded cancel must not have cancelled the task")
	assert.Equal(t, interventionRowsBefore, countRows(t, ctx, db, "task_intervention_event", staleEscalationTask.ID),
		"a refused guarded cancel must write no task_intervention_event row of its own")
	stillOpen, err := s.Tasks().GetClaimByID(ctx, claim.ID)
	require.NoError(t, err)
	assert.Nil(t, stillOpen.ReleasedAt, "a refused cancel must never force-close the claim")

	// The mirror: an escalation matching while the claim id is stale.
	staleClaimTask := createTestTask(t, ctx, s, scopeID, world.uncutMilestoneID, "stale claim half", self)
	matchingEscalationID := setTaskEscalated(t, ctx, db, staleClaimTask.ID)
	staleClaimID := uuid.New()

	_, err = s.Tasks().CancelTask(ctx, store.CancelTaskParams{
		ScopeID: scopeID, TaskID: staleClaimTask.ID, Acting: self, OnBehalfOf: self,
		ExpectedClaimID: &staleClaimID, ExpectedEscalationID: &matchingEscalationID,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrObservedStateMismatch, "a matching escalation id must not excuse a stale claim id")

	got, err = s.Tasks().GetTaskByID(ctx, staleClaimTask.ID)
	require.NoError(t, err)
	assert.Nil(t, got.CancelledAt, "a refused guarded cancel must not have cancelled the task")
	require.NotNil(t, got.CurrentEscalationID)
	assert.Equal(t, matchingEscalationID, *got.CurrentEscalationID, "the escalation that is current now must survive")
}
