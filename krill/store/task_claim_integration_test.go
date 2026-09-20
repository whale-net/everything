//go:build integration

// Real-Postgres coverage for TaskStore.ClaimTask/GetClaimByID
// (task_claim.go, migration 015, issue #2722's Testing section, FR3/FR5):
// the successful-claim path (a new task_claim row, a claimed task_attempt
// row, and task.current_claim_id/lease_expires_at updated in place, with
// attempt_count left untouched), the real-Postgres concurrency race (two
// goroutines racing the same task's row lock -- exactly one wins), the
// unsatisfied-dependency rejection naming the blocking task, the
// expired-lease reclaim path, attempt-cap exhaustion, attempt_count never
// incrementing from a claim alone (a fresh claim or a reclaim of a lapsed
// lease), and NFR3's two-subject attribution on both task_claim and
// task_attempt. Shares task_integration_test.go's
// test-store/test-scope/test-world/subject helpers and
// task_dependency_integration_test.go's createTestTask/setTaskLane
// helpers, mirroring milepebble_integration_test.go's own choice to share
// rather than duplicate fixture helpers. See store_integration_test.go's
// package doc for why this file only builds under the "integration" build
// tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_claim_integration_test --test_output=all
package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// claimTestSession mints a real krill_session row -- task_claim.session_id
// is a real DB-enforced REFERENCES onto krill_session(id) (015_work_axis.
// up.sql), so ClaimTaskParams.SessionID must always name a genuine session,
// never an arbitrary uuid.
func claimTestSession(t *testing.T, ctx context.Context, db *dbtest.Postgres, scopeID uuid.UUID, self store.Subject) store.SessionID {
	t.Helper()
	sessions := store.NewSessionStore(db.Pool)
	id, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)
	return id
}

// countRows returns the number of rows in table matching a task_id column
// equal to taskID -- the shared row-count assertion every scenario below
// uses to prove "nothing written" or "exactly one row" without depending on
// each other's fixtures.
func countRows(t *testing.T, ctx context.Context, db *dbtest.Postgres, table string, taskID uuid.UUID) int {
	t.Helper()
	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE task_id = $1`, taskID).Scan(&count))
	return count
}

// TestTaskStore_ClaimTask_UnclaimedTaskAllDepsDone_Succeeds is issue
// #2722's Testing section item 1: claiming an unclaimed task with all
// dependencies Done succeeds, mints a lease, records exactly one
// task_claim row and one claimed task_attempt row, and updates the task's
// own claim/lease/attempt-count state in place.
func TestTaskStore_ClaimTask_UnclaimedTaskAllDepsDone_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	taskA := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "A", self)
	dep := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "dep", self)
	require.NoError(t, s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID: scopeID, TaskID: taskA.ID, DependsOnTaskIDs: []uuid.UUID{dep.ID},
		Acting: self, OnBehalfOf: self,
	}))
	setTaskLane(t, ctx, db, dep.ID, store.LaneDone)

	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	before := time.Now()
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: taskA.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	assert.Equal(t, scopeID, claim.ScopeID)
	assert.Equal(t, taskA.ID, claim.TaskID)
	assert.Equal(t, sessionID, claim.SessionID)
	assert.Nil(t, claim.ReleasedAt)
	assert.Nil(t, claim.ReleaseReason)
	assert.True(t, claim.InitialLeaseExpiresAt.After(before), "the minted lease must expire in the future")
	assert.WithinDuration(t, before.Add(store.DefaultLeaseDuration), claim.InitialLeaseExpiresAt, 5*time.Second, "the lease must expire DefaultLeaseDuration from now")

	task, err := s.Tasks().GetTaskByID(ctx, taskA.ID)
	require.NoError(t, err)
	require.NotNil(t, task.CurrentClaimID)
	assert.Equal(t, claim.ID, *task.CurrentClaimID)
	require.NotNil(t, task.LeaseExpiresAt)
	assert.Equal(t, claim.InitialLeaseExpiresAt, *task.LeaseExpiresAt)
	assert.Equal(t, 0, task.AttemptCount, "a successful claim of an unclaimed task must never move attempt_count -- it only counts lapsed/abandoned attempts")

	assert.Equal(t, 1, countRows(t, ctx, db, "task_claim", taskA.ID), "exactly one task_claim row must exist")
	assert.Equal(t, 1, countRows(t, ctx, db, "task_attempt", taskA.ID), "exactly one task_attempt row must exist")

	var outcome string
	var attemptClaimID uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT outcome, claim_id FROM task_attempt WHERE task_id = $1`, taskA.ID).Scan(&outcome, &attemptClaimID))
	assert.Equal(t, "claimed", outcome)
	assert.Equal(t, claim.ID, attemptClaimID)
}

// TestTaskStore_ClaimTask_Concurrent_ExactlyOneSucceeds is issue #2722's
// Testing section item 2 (FR3): two goroutines racing to claim the same
// task against a real Postgres -- exactly one succeeds, the other gets
// ErrTaskAlreadyClaimed, and exactly one task_claim row and one claimed
// task_attempt row exist afterwards. This is the scenario a fake or an
// application-level mutex cannot prove -- only a real row lock
// (SELECT ... FOR UPDATE) can.
func TestTaskStore_ClaimTask_Concurrent_ExactlyOneSucceeds(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "racy", self)
	session1 := claimTestSession(t, ctx, db, scopeID, taskTestSubject("agent-1"))
	session2 := claimTestSession(t, ctx, db, scopeID, taskTestSubject("agent-2"))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	sessions := []store.SessionID{session1, session2}
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
				ScopeID: scopeID, TaskID: task.ID, SessionID: sessions[i],
				Acting: self, OnBehalfOf: self,
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	successes, failures := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, store.ErrTaskAlreadyClaimed):
			failures++
		default:
			t.Fatalf("unexpected error from concurrent ClaimTask: %v", err)
		}
	}
	assert.Equal(t, 1, successes, "exactly one concurrent claimant must succeed")
	assert.Equal(t, 1, failures, "exactly one concurrent claimant must lose the race with ErrTaskAlreadyClaimed")

	assert.Equal(t, 1, countRows(t, ctx, db, "task_claim", task.ID), "exactly one task_claim row must exist after the race")
	assert.Equal(t, 1, countRows(t, ctx, db, "task_attempt", task.ID), "exactly one claimed task_attempt row must exist after the race")
}

// TestTaskStore_ClaimTask_UnsatisfiedDependency_Rejected is issue #2722's
// Testing section item 3: claiming a task with an unsatisfied dependency
// is rejected with ErrDependenciesUnsatisfied naming the blocking task,
// and writes no claim row and no attempt row.
func TestTaskStore_ClaimTask_UnsatisfiedDependency_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	taskA := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "A", self)
	blocker := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "blocker", self)
	require.NoError(t, s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID: scopeID, TaskID: taskA.ID, DependsOnTaskIDs: []uuid.UUID{blocker.ID},
		Acting: self, OnBehalfOf: self,
	}))

	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: taskA.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrDependenciesUnsatisfied)
	assert.Contains(t, err.Error(), blocker.ID.String(), "the rejection must name the blocking task")

	assert.Equal(t, 0, countRows(t, ctx, db, "task_claim", taskA.ID), "a rejected claim must write no task_claim row")
	assert.Equal(t, 0, countRows(t, ctx, db, "task_attempt", taskA.ID), "a rejected claim must write no task_attempt row")
}

// TestTaskStore_ClaimTask_ExpiredLease_Reclaims is issue #2722's Testing
// section item 4: claiming a task whose lease has expired succeeds, mints
// a new claim, and marks the prior claim row released with
// release_reason='reclaim' -- the same accounting #2724's later sweep
// produces for the identical situation.
func TestTaskStore_ClaimTask_ExpiredLease_Reclaims(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "lapsed", self)
	firstSession := claimTestSession(t, ctx, db, scopeID, self)
	firstClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: firstSession,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	// Force the lease into the past directly at the DB layer -- the store
	// exposes no "expire a lease" method yet (that is #2723/#2724's own
	// job), mirroring setTaskLane's own "force it directly" precedent.
	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, task.ID)
	require.NoError(t, err)

	secondSession := claimTestSession(t, ctx, db, scopeID, self)
	secondClaim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: secondSession,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err, "claiming a task whose lease has lapsed must succeed")
	assert.NotEqual(t, firstClaim.ID, secondClaim.ID, "a reclaim must mint a genuinely new claim row")

	first, err := s.Tasks().GetClaimByID(ctx, firstClaim.ID)
	require.NoError(t, err)
	require.NotNil(t, first.ReleasedAt, "the prior lapsed claim must be marked released")
	require.NotNil(t, first.ReleaseReason)
	assert.Equal(t, "reclaim", *first.ReleaseReason)

	second, err := s.Tasks().GetClaimByID(ctx, secondClaim.ID)
	require.NoError(t, err)
	assert.Nil(t, second.ReleasedAt, "the new claim must not itself be released")

	task, err = s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, task.CurrentClaimID)
	assert.Equal(t, secondClaim.ID, *task.CurrentClaimID, "the task must now point at the new claim, not the reclaimed one")
}

// TestTaskStore_ClaimTask_AttemptCapExhausted_Rejected is issue #2722's
// Testing section item 5 (FR7): claiming a task already at the attempt cap
// is rejected with ErrAttemptCapExhausted, and the task stays claimed by
// no one -- FR7's terminal state for this milestone, since escalation is
// M5's C26, out of scope here.
func TestTaskStore_ClaimTask_AttemptCapExhausted_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "exhausted", self)
	_, err := db.Pool.Exec(ctx, `UPDATE task SET attempt_count = $1 WHERE id = $2`, store.DefaultAttemptCap, task.ID)
	require.NoError(t, err)

	sessionID := claimTestSession(t, ctx, db, scopeID, self)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID,
		Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrAttemptCapExhausted)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Nil(t, got.CurrentClaimID, "a task rejected for attempt-cap exhaustion must stay claimed by no one")
	assert.Equal(t, 0, countRows(t, ctx, db, "task_claim", task.ID), "a rejected claim must write no task_claim row")
	assert.Equal(t, 0, countRows(t, ctx, db, "task_attempt", task.ID), "a rejected claim must write no task_attempt row")
}

// TestTaskStore_ClaimTask_NeverIncrementsAttemptCount asserts a claim --
// whether of a never-claimed task or of one whose prior lease had just
// lapsed -- never moves attempt_count on its own. Only ReclaimExpired's
// sweep and AbandonClaim count a strike against the cap
// (task_complete.go's CompleteTask doc comment); ClaimTask closing out a
// lapsed lease inline is not a second place that lapse gets counted.
func TestTaskStore_ClaimTask_NeverIncrementsAttemptCount(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskTestStore(t)
	scopeID := newTaskTestScope(t, ctx, db)
	self := taskTestSubject("agent-1")
	world := newTaskTestWorld(t, ctx, s, scopeID, self)

	task := createTestTask(t, ctx, s, scopeID, world.milepebbleID, "counted", self)

	firstSession := claimTestSession(t, ctx, db, scopeID, self)
	_, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: firstSession,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	got, err := s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, got.AttemptCount, "the first successful claim must leave attempt_count at 0")

	_, err = db.Pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, task.ID)
	require.NoError(t, err)

	secondSession := claimTestSession(t, ctx, db, scopeID, self)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: secondSession,
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	got, err = s.Tasks().GetTaskByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, got.AttemptCount, "reclaiming a lapsed lease via ClaimTask itself must still leave attempt_count at 0 -- only ReclaimExpired's sweep counts a lapse")
}

// TestTaskStore_ClaimTask_RecordsBothSubjectPairs is issue #2722's Testing
// section item 7 (NFR3): both subject pairs are persisted NOT NULL on both
// task_claim and task_attempt, and a distinct acting/on-behalf-of pair is
// recorded distinctly, never collapsed into one.
func TestTaskStore_ClaimTask_RecordsBothSubjectPairs(t *testing.T) {
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
		Acting: acting, OnBehalfOf: onBehalfOf,
	})
	require.NoError(t, err)

	assert.Equal(t, acting, claim.CreatedByActing)
	assert.Equal(t, onBehalfOf, claim.CreatedByOnBehalfOf)
	assert.NotEqual(t, claim.CreatedByActing, claim.CreatedByOnBehalfOf, "a distinct acting/on-behalf-of pair must be recorded distinctly, never collapsed into one")

	var claimActingSub, claimOnBehalfOfSub string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT created_by_acting_sub, created_by_on_behalf_of_sub FROM task_claim WHERE id = $1
	`, claim.ID).Scan(&claimActingSub, &claimOnBehalfOfSub))
	assert.Equal(t, "agent-1", claimActingSub)
	assert.Equal(t, "human-1", claimOnBehalfOfSub)

	var attemptActingSub, attemptOnBehalfOfSub string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT created_by_acting_sub, created_by_on_behalf_of_sub FROM task_attempt WHERE claim_id = $1
	`, claim.ID).Scan(&attemptActingSub, &attemptOnBehalfOfSub))
	assert.Equal(t, "agent-1", attemptActingSub)
	assert.Equal(t, "human-1", attemptOnBehalfOfSub)
}
