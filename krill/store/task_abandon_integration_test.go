//go:build integration

// Real-Postgres coverage for TaskStore.AbandonClaim (task_abandon.go, issue
// #2726's Testing section, FR9; attempt-cap escalation issue #2871, root
// plan #2851's M5, FR3): abandon-from-the-current-claimant
// accounting (claim released with release_reason='abandon', one
// `abandoned` task_attempt row, attempt_count+1 from the abandon alone,
// current_lane untouched, immediate re-claimability by a different
// session), a non-current claim id
// rejected with ErrClaimNotCurrent writing nothing, an already-released
// claim (via reclaim or complete) rejected the same way, the shared-cap
// terminal state (abandons alone, and a mix of abandons and lapses, both
// summing to DefaultAttemptCap record exactly one task_escalation_event in
// the same transaction and refuse re-service with ErrTaskEscalated -- the
// exact same cap #2724's ReclaimExpired enforces, proving this file adds
// no second cap check or a second escalation path), a same-transaction
// rollback proof when the escalation insert itself fails, a heartbeat from
// the abandoning run afterwards rejected (#2723 interaction), and NFR3's
// two-subject attribution on the abandoned task_attempt row. Shares
// task_integration_test.go's test-store/test-scope/test-world/subject
// helpers, task_dependency_integration_test.go's createTestTask helper, and
// task_claim_integration_test.go's claimTestSession/countRows helpers,
// mirroring task_reclaim_integration_test.go's own choice to share rather
// than duplicate fixture helpers. See store_integration_test.go's package
// doc for why this file only builds under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_abandon_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// TestTaskStore_AbandonClaim_CurrentClaimant_ReleasesWithAccounting is issue
// #2726's Testing section item 1: abandoning the task's current, unreleased
// claim releases it immediately (release_reason='abandon'), records exactly
// one `abandoned` task_attempt row, increments attempt_count by exactly
// one (the abandon alone -- the earlier claim never moved it), leaves
// current_lane completely untouched (abandoning is not a verdict), and the
// task is immediately claimable by a different session.
func TestTaskStore_AbandonClaim_CurrentClaimant_ReleasesWithAccounting(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "abandon-me", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	result, err := s.Tasks().AbandonClaim(ctx, store.AbandonParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.Equal(t, task.ID, result.TaskID)
	assert.Equal(t, claim.ID, result.ClaimID)
	assert.False(t, result.CapExhausted, "one abandon against a fresh task must not exhaust the attempt cap")

	closed, err := s.Tasks().GetClaimByID(ctx, claim.ID)
	require.NoError(t, err)
	require.NotNil(t, closed.ReleasedAt, "the abandoned claim must be marked released")
	require.NotNil(t, closed.ReleaseReason)
	assert.Equal(t, "abandon", *closed.ReleaseReason)

	assert.Equal(t, 2, countRows(t, ctx, db, "task_attempt", task.ID), "the original claimed attempt plus one new abandoned attempt")
	var abandonedCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_attempt WHERE task_id = $1 AND outcome = 'abandoned'`, task.ID).Scan(&abandonedCount))
	assert.Equal(t, 1, abandonedCount, "exactly one abandoned task_attempt row must be appended")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.AttemptCount, "attempt_count must be incremented by exactly one, from the abandon alone -- the claim itself never moves it")
	assert.Nil(t, got.CurrentClaimID, "current_claim_id must be cleared")
	assert.Nil(t, got.LeaseExpiresAt, "lease_expires_at must be cleared")
	assert.Equal(t, store.LaneScaffold, got.CurrentLane, "current_lane must be untouched by an abandon -- it is not a verdict")
	assert.Nil(t, got.CurrentEscalationID, "a below-cap abandon must not escalate the task")
	assert.Nil(t, result.EscalationID, "a below-cap abandon's result must carry no escalation reference")

	var escalationEvents int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_escalation_event WHERE task_id = $1`, task.ID).Scan(&escalationEvents))
	assert.Equal(t, 0, escalationEvents, "a below-cap abandon must write no task_escalation_event row (issue #2871's FR3 regression)")

	secondSelf := taskTestSubject("agent-2")
	secondSession := claimTestSession(t, ctx, db, scopeID, secondSelf)
	secondClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: secondSession,
		Acting: secondSelf, OnBehalfOf: secondSelf,
	})
	require.NoError(t, err, "an abandoned task must be immediately claimable by a different session")
	assert.NotEqual(t, claim.ID, secondClaim.ID)
}

// TestTaskStore_AbandonClaim_NonCurrentClaimID_Rejected is issue #2726's
// Testing section item 2: abandoning with a claim id that is not the task's
// current claim is rejected with ErrClaimNotCurrent, and nothing is
// written.
func TestTaskStore_AbandonClaim_NonCurrentClaimID_Rejected(t *testing.T) {
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

	_, err = s.Tasks().AbandonClaim(ctx, store.AbandonParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: uuid.New(),
		Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CurrentClaimID)
	assert.Equal(t, claim.ID, *got.CurrentClaimID, "a rejected abandon must leave the real claim untouched")
	assert.Equal(t, 1, countRows(t, ctx, db, "task_attempt", task.ID), "a rejected abandon must write no extra task_attempt row")
}

// TestTaskStore_AbandonClaim_AlreadyReleasedByReclaim_Rejected is issue
// #2726's Testing section item 3 (the reclaim half): abandoning a claim
// that has already been released -- here, by a lease-expiry reclaim -- is
// rejected with ErrClaimNotCurrent, and nothing is written.
func TestTaskStore_AbandonClaim_AlreadyReleasedByReclaim_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "zombie-abandon", self)
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

	rowsBefore := countRows(t, ctx, db, "task_attempt", task.ID)

	_, err = s.Tasks().AbandonClaim(ctx, store.AbandonParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID,
		Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "abandoning a claim already released by reclaim must be rejected")
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent)
	assert.Equal(t, rowsBefore, countRows(t, ctx, db, "task_attempt", task.ID), "a rejected abandon must write no extra task_attempt row")
}

// TestTaskStore_AbandonClaim_AlreadyReleasedByComplete_Rejected is issue
// #2726's Testing section item 3 (the complete half): abandoning a claim
// that has already been released by CompleteTask is rejected with
// ErrClaimNotCurrent, and nothing is written.
func TestTaskStore_AbandonClaim_AlreadyReleasedByComplete_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "completed-abandon", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = s.Tasks().CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Verdict: store.VerdictPass,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	rowsBefore := countRows(t, ctx, db, "task_attempt", task.ID)

	_, err = s.Tasks().AbandonClaim(ctx, store.AbandonParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID,
		Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "abandoning a claim already released by complete must be rejected")
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent)
	assert.Equal(t, rowsBefore, countRows(t, ctx, db, "task_attempt", task.ID), "a rejected abandon must write no extra task_attempt row")
}

// TestTaskStore_AbandonClaim_AttemptCapReached_RefusesToReserve is issue
// #2726's Testing section item 4 (the shared-cap terminal state, abandons
// alone), updated by issue #2871's Testing section (FR3): a task abandoned
// enough times to reach DefaultAttemptCap reports CapExhausted, records
// exactly one task_escalation_event (reason 'attempt-cap') in the same
// transaction, sets task.current_escalation_id, and refuses re-service -- a
// subsequent ClaimTask by any session returns ErrTaskEscalated, not
// ErrAttemptCapExhausted, since AbandonClaim adds no second cap or
// escalation path of its own.
func TestTaskStore_AbandonClaim_AttemptCapReached_RefusesToReserve(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "capped-by-abandon", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	// Force attempt_count to one below the cap so this abandon's own
	// increment lands exactly on DefaultAttemptCap -- the boundary FR9
	// shares with FR7's terminal state.
	_, err = db.Pool.Exec(ctx, `UPDATE task SET attempt_count = $1 WHERE id = $2`, store.DefaultAttemptCap-1, task.ID)
	require.NoError(t, err)

	result, err := s.Tasks().AbandonClaim(ctx, store.AbandonParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.True(t, result.CapExhausted, "an abandon that brings attempt_count to DefaultAttemptCap must report CapExhausted")
	require.NotNil(t, result.EscalationID, "a cap-exhausted abandon must report the escalation event it wrote, in the same call")
	require.NotNil(t, result.EscalationReason)
	assert.Equal(t, store.EscalationReasonAttemptCap, *result.EscalationReason)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DefaultAttemptCap, got.AttemptCount)
	assert.Nil(t, got.CurrentClaimID, "a cap-exhausted task must be left claimed by no one")
	require.NotNil(t, got.CurrentEscalationID)
	assert.Equal(t, *result.EscalationID, *got.CurrentEscalationID)

	var reason string
	var counterValue, capValue int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT reason, counter_value, cap_value FROM task_escalation_event WHERE task_id = $1
	`, task.ID).Scan(&reason, &counterValue, &capValue))
	assert.Equal(t, "attempt-cap", reason)
	assert.Equal(t, store.DefaultAttemptCap, counterValue)
	assert.Equal(t, store.DefaultAttemptCap, capValue)

	otherSession := claimTestSession(t, ctx, db, scopeID, self)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: otherSession, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "any subsequent claim of an escalated task must be refused")
	assert.ErrorIs(t, err, store.ErrTaskEscalated, "an escalated task is refused with ErrTaskEscalated, not ErrAttemptCapExhausted, once the escalation event exists")
}

// TestTaskStore_AbandonClaim_MixOfAbandonsAndLapses_SharesOneCap is issue
// #2726's Testing section item 4 (the shared-cap terminal state, the mix):
// one lapse (via ReclaimExpired) plus abandons summing to DefaultAttemptCap
// refuse re-service too -- proving abandon and reclaim share exactly one
// cap and one counter, never two parallel ones.
func TestTaskStore_AbandonClaim_MixOfAbandonsAndLapses_SharesOneCap(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)
	require.GreaterOrEqual(t, store.DefaultAttemptCap, 2, "this scenario needs at least one lapse and one abandon to reach the cap")

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "mixed-cap", self)

	// Attempt 1: a lapse via ReclaimExpired.
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, task.ID)
	require.NoError(t, err)
	reclaimResult, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeID, Acting: self, OnBehalfOf: self})
	require.NoError(t, err)
	require.Len(t, reclaimResult.Reclaimed, 1)

	// Attempts 2..cap: abandons, until the shared counter reaches the cap.
	var lastResult store.AbandonClaimResult
	for got, err := s.Tasks().GetTaskByID(ctx, task.ID); got.AttemptCount < store.DefaultAttemptCap; got, err = s.Tasks().GetTaskByID(ctx, task.ID) {
		require.NoError(t, err)
		nextSession := claimTestSession(t, ctx, db, scopeID, self)
		claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
			ScopeID: scopeID, TaskID: task.ID, SessionID: nextSession, Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
		lastResult, err = s.Tasks().AbandonClaim(ctx, store.AbandonParams{
			ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
	}
	assert.True(t, lastResult.CapExhausted, "the mix of one lapse plus abandons reaching DefaultAttemptCap must exhaust the shared cap")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, got.AttemptCount, store.DefaultAttemptCap, "attempt_count must have reached at least the shared cap")

	var lapsedCount, abandonedCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_attempt WHERE task_id = $1 AND outcome = 'lapsed'`, task.ID).Scan(&lapsedCount))
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_attempt WHERE task_id = $1 AND outcome = 'abandoned'`, task.ID).Scan(&abandonedCount))
	assert.Equal(t, 1, lapsedCount, "the mix must include exactly one lapsed attempt")
	assert.GreaterOrEqual(t, abandonedCount, 1, "the mix must include at least one abandoned attempt")

	require.NotNil(t, lastResult.EscalationID, "whichever call (lapse or abandon) brought the shared counter to the cap must record the escalation")
	assert.True(t, lastResult.CapExhausted)

	var escalationEvents int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_escalation_event WHERE task_id = $1`, task.ID).Scan(&escalationEvents))
	assert.Equal(t, 1, escalationEvents, "the mix must record exactly one escalation event, regardless of how many calls contributed to the shared counter")

	otherSession := claimTestSession(t, ctx, db, scopeID, self)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: otherSession, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "a task at the shared cap via a mix of lapses and abandons must refuse any further claim")
	assert.ErrorIs(t, err, store.ErrTaskEscalated)
}

// TestTaskStore_AbandonClaim_ZombieHeartbeat_Rejected is issue #2726's
// Testing section item 6, the #2723 interaction: a heartbeat from the
// abandoning run after the abandon is rejected with ErrClaimNotCurrent, and
// writes nothing.
func TestTaskStore_AbandonClaim_ZombieHeartbeat_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "zombie-heartbeat", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = s.Tasks().AbandonClaim(ctx, store.AbandonParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "a heartbeat from the abandoning run must be rejected")
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent)
	assert.Equal(t, 0, countRows(t, ctx, db, "task_lease_event", task.ID), "a rejected heartbeat must write no task_lease_event row")
}

// TestTaskStore_AbandonClaim_RecordsBothSubjectPairsOnAbandonedAttempt is
// issue #2726's Testing section item 7 (NFR3): both subject pairs are
// persisted NOT NULL on the abandoned task_attempt row, and a distinct
// acting/on-behalf-of pair is recorded distinctly, never collapsed into one.
func TestTaskStore_AbandonClaim_RecordsBothSubjectPairsOnAbandonedAttempt(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	acting := taskTestSubject("agent-1")
	onBehalfOf := taskTestSubject("human-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, acting)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "attributed-abandon", acting)
	sessionID := claimTestSession(t, ctx, db, scopeID, acting)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: acting, OnBehalfOf: acting,
	})
	require.NoError(t, err)

	_, err = s.Tasks().AbandonClaim(ctx, store.AbandonParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID,
		Acting: acting, OnBehalfOf: onBehalfOf,
	})
	require.NoError(t, err)

	var actingSub, onBehalfOfSub string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT created_by_acting_sub, created_by_on_behalf_of_sub FROM task_attempt WHERE task_id = $1 AND outcome = 'abandoned'
	`, task.ID).Scan(&actingSub, &onBehalfOfSub))
	assert.Equal(t, "agent-1", actingSub)
	assert.Equal(t, "human-1", onBehalfOfSub)
	assert.NotEqual(t, actingSub, onBehalfOfSub, "a distinct acting/on-behalf-of pair must be recorded distinctly, never collapsed into one")
}

// TestTaskStore_AbandonClaim_AttemptCapEscalation_SameTransactionRollback is
// issue #2871's Testing section "same-transaction proof": when the
// escalation insert itself fails (here, because the task already has an
// active escalation, forcing recordEscalationTx's own one-active-escalation
// rule to reject it), the entire abandon rolls back with it -- the claim is
// never marked released, no abandoned task_attempt row is appended, and
// attempt_count is never incremented.
func TestTaskStore_AbandonClaim_AttemptCapEscalation_SameTransactionRollback(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "abandon-rollback-proof", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	// Force attempt_count to one below the cap, exactly like the ordinary
	// cap-reached test, so this abandon's own increment would reach
	// DefaultAttemptCap and attempt to escalate -- the failure this test
	// forces below.
	_, err = db.Pool.Exec(ctx, `UPDATE task SET attempt_count = $1 WHERE id = $2`, store.DefaultAttemptCap-1, task.ID)
	require.NoError(t, err)

	rowsBefore := countRows(t, ctx, db, "task_attempt", task.ID)

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

	_, err = s.Tasks().AbandonClaim(ctx, store.AbandonParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "an abandon whose own escalation insert fails must fail as a whole")
	assert.ErrorIs(t, err, store.ErrTaskEscalated)

	stillOpen, err := s.Tasks().GetClaimByID(ctx, claim.ID)
	require.NoError(t, err)
	assert.Nil(t, stillOpen.ReleasedAt, "a rolled-back abandon must leave the claim exactly as it was, not released")

	assert.Equal(t, rowsBefore, countRows(t, ctx, db, "task_attempt", task.ID), "a rolled-back abandon must write no abandoned task_attempt row")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, store.DefaultAttemptCap-1, got.AttemptCount, "a rolled-back abandon must never increment attempt_count")
	require.NotNil(t, got.CurrentClaimID, "a rolled-back abandon must leave current_claim_id untouched")
	assert.Equal(t, claim.ID, *got.CurrentClaimID)

	var escalationEvents int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM task_escalation_event WHERE task_id = $1`, task.ID).Scan(&escalationEvents))
	assert.Equal(t, 1, escalationEvents, "only the one planted escalation event must exist -- the failed abandon wrote no second one")
}
