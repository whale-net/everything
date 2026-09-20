//go:build integration

// Real-Postgres coverage for TaskStore.Heartbeat (task_lease.go, migration
// 015, issue #2723's Testing section, FR6): the successful-heartbeat path
// (extends task.lease_expires_at, appends exactly one task_lease_event row,
// leaves attempt_count untouched), repeated heartbeats each appending their
// own row without mutating an earlier one (NFR2), a stale ClaimID rejected
// with ErrClaimNotCurrent and nothing written, a claim already released
// (complete/abandon's own shape) rejected the same way, the zombie-run
// scenario (task reclaimed by a second claimant; the first claimant's
// heartbeat is rejected and the second claimant's lease is untouched), the
// expired-but-not-yet-reclaimed lease's documented semantic (still extends),
// and NFR3's two-subject attribution on task_lease_event. Shares
// task_integration_test.go's test-store/test-scope/test-world/subject
// helpers and task_claim_integration_test.go's claimTestSession/countRows
// helpers, mirroring task_claim_integration_test.go's own choice to share
// rather than duplicate fixture helpers. See store_integration_test.go's
// package doc for why this file only builds under the "integration" build
// tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_lease_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// TestTaskStore_Heartbeat_CurrentClaimant_ExtendsLease is issue #2723's
// Testing section item 1: a heartbeat from the current claimant extends
// task.lease_expires_at and appends exactly one task_lease_event row,
// while attempt_count stays unchanged.
func TestTaskStore_Heartbeat_CurrentClaimant_ExtendsLease(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "heartbeat-me", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	before := time.Now()
	lease, err := s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	assert.Equal(t, task.ID, lease.TaskID)
	assert.Equal(t, claim.ID, lease.ClaimID)
	assert.WithinDuration(t, before.Add(store.DefaultLeaseDuration), lease.ExtendedTo, 5*time.Second, "the lease must extend by DefaultLeaseDuration from now")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LeaseExpiresAt)
	assert.Equal(t, lease.ExtendedTo, *got.LeaseExpiresAt, "task.lease_expires_at must be updated in place to the new value")
	assert.Equal(t, 1, got.AttemptCount, "a heartbeat records no attempt -- attempt_count must stay exactly what ClaimTask left it at")

	assert.Equal(t, 1, countRows(t, ctx, db, "task_lease_event", task.ID), "exactly one task_lease_event row must exist after one heartbeat")
}

// TestTaskStore_Heartbeat_Repeated_AppendsWithoutMutatingPriorRows is issue
// #2723's Testing section item 2 (NFR2): repeated heartbeats append one row
// each; no prior row's extended_to value is ever mutated.
func TestTaskStore_Heartbeat_Repeated_AppendsWithoutMutatingPriorRows(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "repeated", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	first, err := s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	second, err := s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	assert.Equal(t, 2, countRows(t, ctx, db, "task_lease_event", task.ID), "two heartbeats must append two rows")
	assert.True(t, second.ExtendedTo.After(first.ExtendedTo) || second.ExtendedTo.Equal(first.ExtendedTo), "each successive heartbeat must extend to a time at or after the prior one")

	var firstStillExtendedTo time.Time
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT extended_to FROM task_lease_event WHERE task_id = $1 ORDER BY created_at ASC LIMIT 1
	`, task.ID).Scan(&firstStillExtendedTo))
	assert.WithinDuration(t, first.ExtendedTo, firstStillExtendedTo, time.Second, "the first heartbeat's own row must never be mutated by a later heartbeat")
}

// TestTaskStore_Heartbeat_StaleClaimID_Rejected is issue #2723's Testing
// section item 3 (FR6): a ClaimID that is not the task's current_claim_id
// is rejected with ErrClaimNotCurrent, and nothing is written.
func TestTaskStore_Heartbeat_StaleClaimID_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "stale-claim", self)
	staleClaimID := uuid.New()

	_, err := s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: staleClaimID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent)
	assert.Equal(t, 0, countRows(t, ctx, db, "task_lease_event", task.ID), "a rejected heartbeat must write no task_lease_event row")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Nil(t, got.LeaseExpiresAt, "an unclaimed task's lease_expires_at must stay untouched by a rejected heartbeat")
}

// TestTaskStore_Heartbeat_ReleasedClaim_Rejected is issue #2723's Testing
// section item 4: a heartbeat against a claim already closed (the same
// released_at shape complete/abandon produce) is rejected the same way as
// a stale ClaimID, with nothing written. complete/abandon are later M4
// tasks, so this forces released_at directly at the DB layer, mirroring
// task_claim_integration_test.go's own "force it directly" precedent for
// an expired lease.
func TestTaskStore_Heartbeat_ReleasedClaim_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "released", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `
		UPDATE task_claim SET released_at = NOW(), release_reason = 'complete' WHERE id = $1
	`, claim.ID)
	require.NoError(t, err)

	_, err = s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent, "a heartbeat against an already-released claim must be rejected the same way as a stale ClaimID")
	assert.Equal(t, 0, countRows(t, ctx, db, "task_lease_event", task.ID), "a rejected heartbeat must write no task_lease_event row")
}

// TestTaskStore_Heartbeat_AfterReclaim_FirstClaimantRejectedSecondUntouched
// is issue #2723's Testing section item 5, the zombie-run scenario FR6
// names: once a lease has lapsed and a second claimant has reclaimed the
// task, the first (now-stale) claimant's heartbeat is rejected, and the
// second claimant's own lease is left completely untouched by that
// rejected call.
func TestTaskStore_Heartbeat_AfterReclaim_FirstClaimantRejectedSecondUntouched(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "zombie", self)
	firstSession := claimTestSession(t, ctx, db, scopeID, self)
	firstClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: firstSession,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, task.ID)
	require.NoError(t, err)

	secondSession := claimTestSession(t, ctx, db, scopeID, self)
	secondClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: secondSession,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	before, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)

	_, err = s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: firstClaim.ID, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err, "the first (zombie) claimant's heartbeat must be rejected -- it no longer owns this task")
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent)

	after, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, *before.LeaseExpiresAt, *after.LeaseExpiresAt, "the second (current) claimant's lease must be completely untouched by the rejected heartbeat")
	assert.Equal(t, secondClaim.ID, *after.CurrentClaimID)
	assert.Equal(t, 0, countRows(t, ctx, db, "task_lease_event", task.ID), "the rejected heartbeat must write no task_lease_event row")
}

// TestTaskStore_Heartbeat_ExpiredButNotYetReclaimed_StillExtends is issue
// #2723's Testing section item 6: the documented semantic choice -- a
// heartbeat from the still-current claimant succeeds even though its own
// lease has already lapsed, so long as no other caller has reclaimed the
// task yet. "Current claim" means task.current_claim_id still names this
// claim, never "the claim is unexpired" (see task_lease.go's own package
// doc comment, which this test and #2724's later reclaim sweep must both
// honor).
func TestTaskStore_Heartbeat_ExpiredButNotYetReclaimed_StillExtends(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "lapsed-not-reclaimed", self)
	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, task.ID)
	require.NoError(t, err)

	before := time.Now()
	lease, err := s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err, "a heartbeat from the still-current claimant must succeed even after its own lease has lapsed, as long as no one else has reclaimed the task")
	assert.True(t, lease.ExtendedTo.After(before), "the extended lease must be back in the future")

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LeaseExpiresAt)
	assert.True(t, got.LeaseExpiresAt.After(before), "task.lease_expires_at must be pulled back into the future")
}

// TestTaskStore_Heartbeat_RecordsBothSubjectPairs is issue #2723's Testing
// section item 7 (NFR3): both subject pairs are persisted NOT NULL on
// task_lease_event, and a distinct acting/on-behalf-of pair is recorded
// distinctly, never collapsed into one.
func TestTaskStore_Heartbeat_RecordsBothSubjectPairs(t *testing.T) {
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

	_, err = s.Tasks().Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: scopeID, TaskID: task.ID, ClaimID: claim.ID,
		Acting: acting, OnBehalfOf: onBehalfOf,
	})
	require.NoError(t, err)

	var actingSub, onBehalfOfSub string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT created_by_acting_sub, created_by_on_behalf_of_sub FROM task_lease_event WHERE task_id = $1
	`, task.ID).Scan(&actingSub, &onBehalfOfSub))
	assert.Equal(t, "agent-1", actingSub)
	assert.Equal(t, "human-1", onBehalfOfSub)
	assert.NotEqual(t, actingSub, onBehalfOfSub, "a distinct acting/on-behalf-of pair must be recorded distinctly, never collapsed into one")
}
