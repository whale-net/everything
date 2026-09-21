//go:build integration

// This file is issue #2877's end-to-end conformance proof for krill M5's
// whole escalation/intervention/console axis: one connected walk (claimed-
// console view -> thrash-cap escalation -> attempt-cap escalation ->
// manual escalation (including FR9's single-event-at-cap exception) ->
// escalated-console view -> release -> requeue -> cancel -> cancelled-
// console view -> note lifecycle -> open-notes console view -> paging with
// cross-scope token rejection), proving every FR #2867-#2876 shipped
// independently composes against a real Postgres -- mirrors
// delivery_axis_integration_test.go's (M3) and work_axis_integration_test.go's
// (M4) own shape. Shares this package's target and testEnv/newTestEnv
// helper (roundtrip_integration_test.go) -- see that file's own doc
// comment for why this package only builds under the "integration" tag.
//
// Like delivery_axis_integration_test.go/work_axis_integration_test.go,
// this file never goes through //krill/importer or HTTP/MCP -- the
// escalation axis has no markdown-import path, so its fixture is built
// directly against //krill/store, the same way M3's and M4's own
// conformance tests are. NFR2's grep assertion reads the escalation axis's
// own committed store source files through Bazel runfiles -- see the
// escalationAxisSourceFiles var below and //krill/store's own
// work_axis_srcs filegroup (BUILD.bazel), whose glob ("task*.go") already
// covers every M5 store file without needing to be widened.
//
// FR-to-assertion map (root plan #2851; every FR/NFR below has at least
// one assert/require carrying its own tag in its failure message, so a
// validator can grep this file for "FR<n>:"/"NFR<n>:" and find each one):
//
//   - FR1 (lane-thrash counter), FR2 (thrash-cap escalation): "Step 3".
//   - FR3 (attempt-cap escalation via lease-expiry/abandon): "Step 4".
//   - FR4 (claimed-console view): "Step 2" -- documents a real production
//     defect this walk uncovered (ListClaimedTasks is a permanent stub);
//     see that step's own comment and follow-up issue #2916.
//   - FR5 (escalated-console view, summary history only): "Step 6".
//   - FR6 (requeue): "Step 8".
//   - FR7 (cancel): "Step 9".
//   - FR8 (release): "Step 7".
//   - FR9 (manual escalate, incl. single-event-at-cap exception): "Step 5".
//   - FR10 (cancelled-console view): "Step 9".
//   - FR11 (note lifecycle status): "Step 10".
//   - FR12 (open-notes console view): "Step 10".
//   - NFR1 (scope_id NOT NULL + scope filtering): t.Run "NFR1_ScopeQualification".
//   - NFR2 (no UPDATE/DELETE against the three event tables): t.Run "NFR2_NoMutationOfEventTables".
//   - NFR3 (both subject pairs NOT NULL, every mutating call records them): t.Run "NFR3_TwoSubjectAttribution".
//   - NFR4 (counter independence): inline assertions in "Step 3"/"Step 4",
//     plus t.Run "NFR4_CounterIndependence" re-confirming both final values.
//   - NFR5 (requeue/cancel leave prior rows unchanged): t.Run "NFR5_RequeueAndCancelLeavePriorRowsUnchanged".
//   - NFR6 (bounded/ordered/resumable paging, cross-scope token rejection): "Step 11".
//   - Claimability (task_claimable_idx's three predicates): t.Run "Claimability_TaskClaimableIdxThreePredicates".
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/conformance:roundtrip_integration_test --test_output=all --test_filter=TestEscalationConsole
package conformance

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// escalationOperator/escalationHuman mint the fixed LB4 subject pair this
// file uses for every operator-driven verb (release, escalate, requeue,
// cancel, note-lifecycle transition) -- an Agent acting on a Swarm
// Operator's behalf, mirroring workAxisAgent/workAxisHuman's identical
// precedent (work_axis_integration_test.go).
func escalationOperator() store.Subject {
	return store.Subject{Iss: "https://whagent.example.test", Sub: "agent-escalation-console", Kind: store.SubjectKindService}
}

func escalationHuman() store.Subject {
	return store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "swarm-operator-escalation-console", Kind: store.SubjectKindHuman}
}

// escalationAgentSession mints a distinguishable Agent/Human subject pair
// keyed by name, for each distinct claiming session this walk needs --
// mirrors workAxisAgent/workAxisHuman's own per-name pattern.
func escalationAgentSession(name string) (store.Subject, store.Subject) {
	agent := store.Subject{Iss: "https://whagent.example.test", Sub: "agent-console-" + name, Kind: store.SubjectKindService}
	human := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "operator-console-" + name, Kind: store.SubjectKindHuman}
	return agent, human
}

// escalationAxisM5Tables is every table migration 016 created -- named
// once so NFR1/NFR2/NFR3's schema assertions share one authoritative list.
var escalationAxisM5Tables = []string{"task_escalation_event", "task_intervention_event", "task_note_lifecycle_event"}

// escalationAxisSourceFiles is NFR2's grep target: every non-test
// krill/store source file that can write to task/task_claim/task_attempt/
// task_note in a way relevant to the escalation axis -- the exact set
// //krill/store:work_axis_srcs' glob ("task*.go") already exposes as
// runfiles data (mirrors work_axis_integration_test.go's own
// workAxisSourceFiles list, scoped here to krill/store only per this
// task's own issue body: "grep committed sources... via
// //krill/store:work_axis_srcs").
var escalationAxisSourceFiles = []string{
	"_main/krill/store/task.go",
	"_main/krill/store/task_abandon.go",
	"_main/krill/store/task_cancel.go",
	"_main/krill/store/task_claim.go",
	"_main/krill/store/task_complete.go",
	"_main/krill/store/task_console.go",
	"_main/krill/store/task_dependency.go",
	"_main/krill/store/task_escalate.go",
	"_main/krill/store/task_escalation.go",
	"_main/krill/store/task_lease.go",
	"_main/krill/store/task_note.go",
	"_main/krill/store/task_note_console.go",
	"_main/krill/store/task_note_lifecycle.go",
	"_main/krill/store/task_reclaim.go",
	"_main/krill/store/task_release.go",
	"_main/krill/store/task_requeue.go",
}

// singleLaneTask creates a one-lane task under milepebbleID -- the shared
// shape every step below that only cares about claim/lease/escalation
// mechanics (never a lane transition) uses, so each step's fixture reads
// as "one throwaway task", not a five-lane sequence nothing in that step
// exercises.
func singleLaneTask(t *testing.T, ctx context.Context, tasks store.TaskStore, scopeID, milepebbleID uuid.UUID, title string, operator, operatorHuman store.Subject) store.Task {
	t.Helper()
	task, err := tasks.CreateTask(ctx, store.CreateTaskParams{
		ScopeID: scopeID, MilestoneID: milepebbleID, Title: title,
		LaneSequence: []store.Lane{store.LaneScaffold}, StartingLane: store.LaneScaffold,
		Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)
	return task
}

// TestEscalationConsole_EndToEndWalk_AndNFRAudit is issue #2877's own walk
// -- see this file's own package doc comment for the full FR/NFR-to-step
// map.
func TestEscalationConsole_EndToEndWalk_AndNFRAudit(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	tasks := env.store.Tasks()
	querier := slice.NewQuerier(env.store)
	assembler := work.NewAssembler(tasks, querier)

	operator, operatorHuman := escalationOperator(), escalationHuman()

	// -- Fixture: one Product -> FeatureSet -> Feature, one Milestone
	// delivering it, one Milepebble cut from it delivering it too (M4
	// FR1) -- every task below is scoped to this one milepebble. --------
	product, err := env.store.Products().Create(ctx, env.scopeID, "Escalation Console Conformance", "prove krill M5's escalation/intervention/console axis composes end to end")
	require.NoError(t, err)
	featureSet, err := env.store.FeatureSets().Create(ctx, env.scopeID, product.ID, "Escalation Console Surface", nil)
	require.NoError(t, err)
	feature, err := env.store.Features().Create(ctx, env.scopeID, featureSet.ID, "Escalation console feature", nil)
	require.NoError(t, err)
	milestone, err := env.store.MilestoneAuthoring().CreateMilestone(ctx, env.scopeID, product.ID, "M-Escalation", "ship the escalation console axis", nil, operator, operatorHuman)
	require.NoError(t, err)
	require.NoError(t, env.store.MilestoneAuthoring().AddDelivers(ctx, env.scopeID, milestone.ID, feature.ID, operator, operatorHuman))
	milepebble, err := env.store.MilestoneAuthoring().CreateMilepebble(ctx, env.scopeID, milestone.ID, "MP-Escalation", "a slice of M-Escalation", operator, operatorHuman)
	require.NoError(t, err)
	require.NoError(t, env.store.MilestoneAuthoring().AddMilepebbleDelivers(ctx, env.scopeID, milepebble.ID, feature.ID, operator, operatorHuman))

	// ==================================================================
	// Step 2: claim several tasks from distinct sessions; FR4's claimed-
	// console view.
	// ==================================================================

	var claimedTasks []store.Task
	var claimedClaims []store.Claim
	for i, name := range []string{"claimed-a", "claimed-b", "claimed-c"} {
		task := singleLaneTask(t, ctx, tasks, env.scopeID, milepebble.ID, "claimed task "+name, operator, operatorHuman)
		agent, human := escalationAgentSession(name)
		sessID, err := env.sessions.InitSession(ctx, env.scopeID, agent, human, nil)
		require.NoError(t, err)
		claim, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
			ScopeID: env.scopeID, TaskID: task.ID, SessionID: sessID, Acting: agent, OnBehalfOf: human,
		})
		require.NoError(t, err)
		claimedTasks = append(claimedTasks, task)
		claimedClaims = append(claimedClaims, claim)
		_ = i
	}

	// FR4: GET /console/claimed (store.TaskStore.ListClaimedTasks) is
	// wired end-to-end through the HTTP handler (console.go) and the
	// list_claimed_tasks MCP tool (mcp/tools/console.go), but the store
	// method itself is a permanent stub -- a genuine production defect
	// this walk uncovered, not a gap in this test (see #2869's own
	// "Implementation" section, never actually landed, versus its three
	// siblings #2873/#2874/#2875, which are). Filed as a follow-up:
	// issue #2916. This assertion documents the CURRENT (broken)
	// behaviour explicitly, so a real fix flips it red rather than this
	// walk silently staying green forever with FR4 unproven.
	_, err = tasks.ListClaimedTasks(ctx, store.ListClaimedTasksParams{ScopeID: env.scopeID})
	assert.ErrorIs(t, err, store.ErrNotImplemented,
		"FR4 (known production defect, see follow-up issue #2916): ListClaimedTasks has never been implemented despite issue #2869 closing -- GET /console/claimed and list_claimed_tasks fail identically in production for every scope")

	// "What is claimed" is still answerable at the store layer directly
	// (claimant session, lane, lease expiry, attempt count, title,
	// delivery reference) -- proving the underlying claim mechanics work
	// even though the one console query over them does not.
	for i, task := range claimedTasks {
		reread, err := tasks.GetTaskByID(ctx, task.ID)
		require.NoError(t, err)
		require.NotNil(t, reread.CurrentClaimID)
		assert.Equal(t, claimedClaims[i].ID, *reread.CurrentClaimID, "FR4 (store-layer fallback): the claimed task's current claim id must be resolvable directly")
		assert.Equal(t, store.LaneScaffold, reread.CurrentLane)
		require.NotNil(t, reread.LeaseExpiresAt)
		assert.True(t, reread.LeaseExpiresAt.After(time.Now()), "FR4 (store-layer fallback): a freshly-claimed task's lease must not already be expired")
		assert.Equal(t, 0, reread.AttemptCount)
	}

	// ==================================================================
	// Step 3: drive one task through alternating pass/fail until the
	// thrash cap trips (FR1, FR2, NFR4).
	// ==================================================================

	taskThrash, err := tasks.CreateTask(ctx, store.CreateTaskParams{
		ScopeID: env.scopeID, MilestoneID: milepebble.ID, Title: "thrash-cap task",
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation, store.LaneTesting},
		StartingLane: store.LaneTesting, Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)
	agentThrash, humanThrash := escalationAgentSession("thrash")
	sessionThrashID, err := env.sessions.InitSession(ctx, env.scopeID, agentThrash, humanThrash, nil)
	require.NoError(t, err)

	// fail, pass, fail, pass, fail -- store.DefaultThrashCap is 3, and FR1
	// counts every failing verdict total, never consecutive-only, so this
	// alternating sequence must still trip on the third fail.
	verdicts := []store.Verdict{store.VerdictFail, store.VerdictPass, store.VerdictFail, store.VerdictPass, store.VerdictFail}
	var lastResult store.TaskLaneResult
	for i, verdict := range verdicts {
		claim, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
			ScopeID: env.scopeID, TaskID: taskThrash.ID, SessionID: sessionThrashID, Acting: agentThrash, OnBehalfOf: humanThrash,
		})
		require.NoError(t, err, "verdict round %d: task must still be claimable below the thrash cap", i+1)
		result, err := tasks.CompleteTask(ctx, store.CompleteTaskParams{
			ScopeID: env.scopeID, TaskID: taskThrash.ID, ClaimID: claim.ID, Verdict: verdict, Acting: agentThrash, OnBehalfOf: humanThrash,
		})
		require.NoError(t, err, "verdict round %d: complete must still succeed even on the cap-tripping round", i+1)
		lastResult = result
	}

	// FR2: the final complete still succeeds (already proven by the
	// require.NoError above, round 5), reports state escalated / reason
	// thrash-cap / the HELD lane (NextLane's own revert is never
	// applied), and the task becomes unclaimable.
	assert.Equal(t, store.TaskStateEscalated, lastResult.State, "FR2: the cap-tripping complete must report state escalated")
	require.NotNil(t, lastResult.EscalationReason)
	assert.Equal(t, store.EscalationReasonThrashCap, *lastResult.EscalationReason, "FR2: the cap-tripping complete must name reason thrash-cap")
	assert.Equal(t, lastResult.FromLane, lastResult.ToLane, "FR2: a thrash-cap trip holds the task at its current lane -- NextLane's revert must never be applied")

	taskThrashAfter, err := tasks.GetTaskByID(ctx, taskThrash.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, taskThrashAfter.ThrashCount, "FR1: three total failing verdicts must trip store.DefaultThrashCap exactly")
	assert.Equal(t, 0, taskThrashAfter.AttemptCount, "NFR4: a failing verdict must never move attempt_count -- only thrash_count")
	require.NotNil(t, taskThrashAfter.CurrentEscalationID)
	escalationThrash, err := tasks.GetEscalationEventByID(ctx, *taskThrashAfter.CurrentEscalationID)
	require.NoError(t, err)
	assert.Equal(t, store.EscalationReasonThrashCap, escalationThrash.Reason)
	require.NotNil(t, escalationThrash.CounterValue)
	require.NotNil(t, escalationThrash.CapValue)
	assert.Equal(t, 3, *escalationThrash.CounterValue)
	assert.Equal(t, store.DefaultThrashCap, *escalationThrash.CapValue)

	_, err = tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskThrash.ID, SessionID: sessionThrashID, Acting: agentThrash, OnBehalfOf: humanThrash,
	})
	assert.ErrorIs(t, err, store.ErrTaskEscalated, "FR2: an escalated task must be unclaimable regardless of claim state")

	// An investigation note, recorded while the task sits escalated --
	// Step 8 (requeue) proves this survives, still visible, in the next
	// claim payload after the task returns to claimable.
	noteThrash, err := tasks.RecordNote(ctx, store.RecordNoteParams{
		ScopeID: env.scopeID, TaskID: &taskThrash.ID, Kind: store.NoteKindComment,
		Body: "investigation note recorded while thrash-cap-escalated", Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)

	// ==================================================================
	// Step 4: drive a second task to the attempt cap via lease expiry and
	// abandon (FR3, NFR4) -- the escalation exists the moment
	// CapExhausted is returned, with no intervening claim.
	// ==================================================================

	taskAttemptCap := singleLaneTask(t, ctx, tasks, env.scopeID, milepebble.ID, "attempt-cap task", operator, operatorHuman)
	agentAttempt, humanAttempt := escalationAgentSession("attempt-cap")
	sessionAttemptID, err := env.sessions.InitSession(ctx, env.scopeID, agentAttempt, humanAttempt, nil)
	require.NoError(t, err)

	// Round 1: lease expiry + sweep (attempt_count -> 1).
	claim1, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskAttemptCap.ID, SessionID: sessionAttemptID, Acting: agentAttempt, OnBehalfOf: humanAttempt,
	})
	require.NoError(t, err)
	_, err = env.pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, taskAttemptCap.ID)
	require.NoError(t, err)
	reclaim1, err := tasks.ReclaimExpired(ctx, store.ReclaimParams{ScopeID: env.scopeID, TaskID: &taskAttemptCap.ID, Acting: operator, OnBehalfOf: operatorHuman})
	require.NoError(t, err)
	require.Len(t, reclaim1.Reclaimed, 1)
	assert.False(t, reclaim1.Reclaimed[0].CapExhausted)
	_ = claim1

	// Round 2: abandon (attempt_count -> 2).
	claim2, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskAttemptCap.ID, SessionID: sessionAttemptID, Acting: agentAttempt, OnBehalfOf: humanAttempt,
	})
	require.NoError(t, err)
	abandon2, err := tasks.AbandonClaim(ctx, store.AbandonParams{
		ScopeID: env.scopeID, TaskID: taskAttemptCap.ID, ClaimID: claim2.ID, Acting: agentAttempt, OnBehalfOf: humanAttempt,
	})
	require.NoError(t, err)
	assert.False(t, abandon2.CapExhausted)

	// Round 3: lease expiry + sweep, reaching store.DefaultAttemptCap
	// (attempt_count -> 3) -- FR3's escalation must exist the instant
	// this call returns, with no ClaimTask in between.
	claim3, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskAttemptCap.ID, SessionID: sessionAttemptID, Acting: agentAttempt, OnBehalfOf: humanAttempt,
	})
	require.NoError(t, err)
	_, err = env.pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, taskAttemptCap.ID)
	require.NoError(t, err)
	reclaim3, err := tasks.ReclaimExpired(ctx, store.ReclaimParams{ScopeID: env.scopeID, TaskID: &taskAttemptCap.ID, Acting: operator, OnBehalfOf: operatorHuman})
	require.NoError(t, err)
	require.Len(t, reclaim3.Reclaimed, 1)
	assert.True(t, reclaim3.Reclaimed[0].CapExhausted, "FR3: the third lapse must reach store.DefaultAttemptCap")
	require.NotNil(t, reclaim3.Reclaimed[0].EscalationID, "FR3: the escalation must exist in this same call's own return value -- no intervening claim required to discover it")
	require.NotNil(t, reclaim3.Reclaimed[0].EscalationReason)
	assert.Equal(t, store.EscalationReasonAttemptCap, *reclaim3.Reclaimed[0].EscalationReason)
	_ = claim3

	taskAttemptCapAfter, err := tasks.GetTaskByID(ctx, taskAttemptCap.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, taskAttemptCapAfter.AttemptCount)
	assert.Equal(t, 0, taskAttemptCapAfter.ThrashCount, "NFR4: a lapse must never move thrash_count -- only attempt_count")

	_, err = tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskAttemptCap.ID, SessionID: sessionAttemptID, Acting: agentAttempt, OnBehalfOf: humanAttempt,
	})
	assert.ErrorIs(t, err, store.ErrTaskEscalated, "FR2/FR3: once escalated (regardless of reason), ClaimTask must refuse with ErrTaskEscalated, never ErrAttemptCapExhausted")

	noteAttemptCap, err := tasks.RecordNote(ctx, store.RecordNoteParams{
		ScopeID: env.scopeID, TaskID: &taskAttemptCap.ID, Kind: store.NoteKindComment,
		Body: "investigation note recorded while attempt-cap-escalated", Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)

	// ==================================================================
	// Step 5: manually escalate a third, live-claimed task (FR9),
	// including the FR9 exception where the force-close itself crosses
	// the attempt cap -- exactly one escalation event, never two.
	// ==================================================================

	taskManual := singleLaneTask(t, ctx, tasks, env.scopeID, milepebble.ID, "manually escalated task", operator, operatorHuman)
	agentManual, humanManual := escalationAgentSession("manual")
	sessionManualID, err := env.sessions.InitSession(ctx, env.scopeID, agentManual, humanManual, nil)
	require.NoError(t, err)
	claimManual, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskManual.ID, SessionID: sessionManualID, Acting: agentManual, OnBehalfOf: humanManual,
	})
	require.NoError(t, err)

	escalateManual, err := tasks.EscalateTask(ctx, store.EscalateParams{
		ScopeID: env.scopeID, TaskID: taskManual.ID, Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)
	assert.True(t, escalateManual.ClaimForceClosed, "FR9: escalating a live-claimed task must force-close its claim")
	assert.False(t, escalateManual.CapExhausted)
	assert.Equal(t, store.EscalationReasonManual, escalateManual.EscalationEvent.Reason)
	assert.Nil(t, escalateManual.EscalationEvent.CounterValue, "FR9: a manual escalation has no triggering counter")
	assert.Nil(t, escalateManual.EscalationEvent.CapValue)

	// FR9: the original claimant's subsequent heartbeat/complete/abandon
	// must all be rejected -- the force-close is not merely cosmetic.
	_, err = tasks.Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: env.scopeID, TaskID: taskManual.ID, ClaimID: claimManual.ID, Acting: agentManual, OnBehalfOf: humanManual,
	})
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent, "FR9: the force-closed claimant's heartbeat must be rejected")
	_, err = tasks.CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: env.scopeID, TaskID: taskManual.ID, ClaimID: claimManual.ID, Verdict: store.VerdictPass, Acting: agentManual, OnBehalfOf: humanManual,
	})
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent, "FR9: the force-closed claimant's complete must be rejected")
	_, err = tasks.AbandonClaim(ctx, store.AbandonParams{
		ScopeID: env.scopeID, TaskID: taskManual.ID, ClaimID: claimManual.ID, Acting: agentManual, OnBehalfOf: humanManual,
	})
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent, "FR9: the force-closed claimant's abandon must be rejected")

	taskManualAfter, err := tasks.GetTaskByID(ctx, taskManual.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, taskManualAfter.AttemptCount, "FR9: the force-close itself must count as one attempt")

	// Escalating an already-escalated task is refused, not a silent
	// no-op (task_escalate.go's own design note, this task's own
	// "close the open ambiguity" resolution).
	_, err = tasks.EscalateTask(ctx, store.EscalateParams{ScopeID: env.scopeID, TaskID: taskManual.ID, Acting: operator, OnBehalfOf: operatorHuman})
	assert.ErrorIs(t, err, store.ErrTaskEscalated, "FR9: escalating an already-escalated task must be refused, never a silent no-op")

	// FR9's own single-event-at-cap exception: a task whose force-close
	// itself crosses store.DefaultAttemptCap must still carry exactly one
	// escalation event.
	taskManualAtCap := singleLaneTask(t, ctx, tasks, env.scopeID, milepebble.ID, "manually escalated task at cap", operator, operatorHuman)
	agentAtCap, humanAtCap := escalationAgentSession("manual-at-cap")
	sessionAtCapID, err := env.sessions.InitSession(ctx, env.scopeID, agentAtCap, humanAtCap, nil)
	require.NoError(t, err)

	// Two lapses bring attempt_count to DefaultAttemptCap-1 (2).
	for i := 0; i < 2; i++ {
		claim, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
			ScopeID: env.scopeID, TaskID: taskManualAtCap.ID, SessionID: sessionAtCapID, Acting: agentAtCap, OnBehalfOf: humanAtCap,
		})
		require.NoError(t, err)
		_, err = env.pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, taskManualAtCap.ID)
		require.NoError(t, err)
		reclaim, err := tasks.ReclaimExpired(ctx, store.ReclaimParams{ScopeID: env.scopeID, TaskID: &taskManualAtCap.ID, Acting: operator, OnBehalfOf: operatorHuman})
		require.NoError(t, err)
		require.Len(t, reclaim.Reclaimed, 1)
		assert.False(t, reclaim.Reclaimed[0].CapExhausted)
		_ = claim
	}
	claimAtCap, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskManualAtCap.ID, SessionID: sessionAtCapID, Acting: agentAtCap, OnBehalfOf: humanAtCap,
	})
	require.NoError(t, err)
	_ = claimAtCap

	escalateAtCap, err := tasks.EscalateTask(ctx, store.EscalateParams{ScopeID: env.scopeID, TaskID: taskManualAtCap.ID, Acting: operator, OnBehalfOf: operatorHuman})
	require.NoError(t, err)
	assert.True(t, escalateAtCap.ClaimForceClosed)
	assert.True(t, escalateAtCap.CapExhausted, "FR9 exception: this force-close must itself cross store.DefaultAttemptCap")

	var escalationEventCountAtCap int
	require.NoError(t, env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM task_escalation_event WHERE task_id = $1`, taskManualAtCap.ID).Scan(&escalationEventCountAtCap))
	assert.Equal(t, 1, escalationEventCountAtCap, "FR9 exception: even where the force-close crosses the attempt cap, exactly one escalation event (the manual one) must exist -- never a second attempt-cap event")

	taskManualAtCapAfter, err := tasks.GetTaskByID(ctx, taskManualAtCap.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, taskManualAtCapAfter.AttemptCount)
	require.NotNil(t, taskManualAtCapAfter.CurrentEscalationID)
	escalationAtCap, err := tasks.GetEscalationEventByID(ctx, *taskManualAtCapAfter.CurrentEscalationID)
	require.NoError(t, err)
	assert.Equal(t, store.EscalationReasonManual, escalationAtCap.Reason, "FR9 exception: the one active escalation must still be the manual one, not a substituted attempt-cap event")

	// ==================================================================
	// Step 6: GET /console/escalated (FR5) -- reason, triggering
	// counter/cap, summary history only, no inline history.
	// ==================================================================

	escalatedPage, err := tasks.ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: env.scopeID})
	require.NoError(t, err)
	escalatedByTaskID := make(map[uuid.UUID]store.EscalatedTaskRow, len(escalatedPage.Items))
	for _, row := range escalatedPage.Items {
		escalatedByTaskID[row.TaskID] = row
	}

	rowThrash, ok := escalatedByTaskID[taskThrash.ID]
	require.True(t, ok, "FR5: the thrash-cap-escalated task must appear in the escalated console view")
	assert.Equal(t, store.EscalationReasonThrashCap, rowThrash.Reason)
	require.NotNil(t, rowThrash.CounterValue)
	require.NotNil(t, rowThrash.CapValue)
	assert.Equal(t, 3, *rowThrash.CounterValue)
	assert.Equal(t, store.DefaultThrashCap, *rowThrash.CapValue)
	require.NotNil(t, rowThrash.MostRecentVerdict, "FR5: a thrash-cap escalation's most recent verdict is deterministically Fail")
	assert.Equal(t, store.VerdictFail, *rowThrash.MostRecentVerdict)
	assert.Equal(t, 1, rowThrash.NoteCount, "FR5: NoteCount is a summary count, never the note list itself")
	assert.Equal(t, milepebble.ID, rowThrash.DeliveryRef.ID)
	assert.Equal(t, store.MilestoneKindMilepebble, rowThrash.DeliveryRef.Kind)
	assert.Equal(t, "claimed task claimed-a", claimedTasks[0].Title, "sanity: fixture titles are distinguishable")
	assert.Equal(t, "thrash-cap task", rowThrash.Title)

	rowAttemptCap, ok := escalatedByTaskID[taskAttemptCap.ID]
	require.True(t, ok, "FR5: the attempt-cap-escalated task must appear")
	assert.Equal(t, store.EscalationReasonAttemptCap, rowAttemptCap.Reason)
	require.NotNil(t, rowAttemptCap.CounterValue)
	assert.Equal(t, 3, *rowAttemptCap.CounterValue)
	assert.Nil(t, rowAttemptCap.MostRecentVerdict, "FR5: an attempt-cap escalation's triggering event is never itself a verdict")
	assert.Equal(t, 1, rowAttemptCap.NoteCount)

	rowManual, ok := escalatedByTaskID[taskManual.ID]
	require.True(t, ok, "FR5: the manually-escalated task must appear")
	assert.Equal(t, store.EscalationReasonManual, rowManual.Reason)
	assert.Nil(t, rowManual.CounterValue, "FR5: a manual escalation carries no counter/cap")
	assert.Nil(t, rowManual.CapValue)
	assert.Nil(t, rowManual.MostRecentVerdict)

	rowManualAtCap, ok := escalatedByTaskID[taskManualAtCap.ID]
	require.True(t, ok, "FR5: the manual-at-cap-escalated task must appear")
	assert.Equal(t, store.EscalationReasonManual, rowManualAtCap.Reason)
	assert.Nil(t, rowManualAtCap.CounterValue)

	// FR5's "no inline history" is a type-level guarantee, not just a
	// runtime one: EscalatedTaskRow (task_console.go) carries only
	// AttemptCount/FailingVerdictCount/NoteCount (ints) and
	// MostRecentVerdict (a single value) -- there is no []Note/
	// []Attempt field for this line to even reach into, so the note
	// count above (a summary) is verifiably NOT the one note this row's
	// own task carries via the full-history path.
	fullNotesThrash, err := tasks.ListNotesForTask(ctx, env.scopeID, taskThrash.ID)
	require.NoError(t, err)
	require.Len(t, fullNotesThrash, 1)
	assert.Equal(t, noteThrash.ID, fullNotesThrash[0].ID, "FR5: the full note history (M4 FR10's per-task fetch) is a different call entirely from this row's own NoteCount summary")
	_ = noteAttemptCap

	// ==================================================================
	// Step 7: release a lease on a fourth task (FR8).
	// ==================================================================

	taskRelease := singleLaneTask(t, ctx, tasks, env.scopeID, milepebble.ID, "released task", operator, operatorHuman)
	agentRelease, humanRelease := escalationAgentSession("release")
	sessionReleaseID, err := env.sessions.InitSession(ctx, env.scopeID, agentRelease, humanRelease, nil)
	require.NoError(t, err)
	claimRelease, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskRelease.ID, SessionID: sessionReleaseID, Acting: agentRelease, OnBehalfOf: humanRelease,
	})
	require.NoError(t, err)

	releaseResult, err := tasks.ReleaseLease(ctx, store.ReleaseParams{ScopeID: env.scopeID, TaskID: taskRelease.ID, Acting: operator, OnBehalfOf: operatorHuman})
	require.NoError(t, err)
	assert.False(t, releaseResult.CapExhausted)
	assert.Nil(t, releaseResult.EscalationID)

	taskReleaseAfter, err := tasks.GetTaskByID(ctx, taskRelease.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, taskReleaseAfter.AttemptCount, "FR8 (Assumption 2): a manual release counts as an attempt")
	assert.Nil(t, taskReleaseAfter.CurrentClaimID, "FR8: the task must be claimable again immediately")

	_, err = tasks.Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: env.scopeID, TaskID: taskRelease.ID, ClaimID: claimRelease.ID, Acting: agentRelease, OnBehalfOf: humanRelease,
	})
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent, "FR8: the original claimant's heartbeat must be rejected after a release")

	claimReleaseAgain, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskRelease.ID, SessionID: sessionReleaseID, Acting: agentRelease, OnBehalfOf: humanRelease,
	})
	require.NoError(t, err, "FR8: a released task must be claimable again immediately")
	assert.NotEqual(t, claimRelease.ID, claimReleaseAgain.ID)

	// ==================================================================
	// Step 8: requeue each escalated task (FR6) -- correct counter reset
	// per reason, held lane preserved, claimable again, notes carried
	// through, manual-at-cap not stranded.
	// ==================================================================

	attemptRowsBeforeRequeue := snapshotTaskAttempts(t, ctx, env.pool, taskThrash.ID)
	notesBeforeRequeue := snapshotTaskNotes(t, ctx, env.pool, taskThrash.ID)

	requeueThrash, err := tasks.RequeueTask(ctx, store.RequeueParams{ScopeID: env.scopeID, TaskID: taskThrash.ID, Acting: operator, OnBehalfOf: operatorHuman})
	require.NoError(t, err)
	assert.Equal(t, store.ResetCounterThrash, requeueThrash.CounterReset, "FR6: a thrash-cap escalation's requeue must reset thrash_count")
	assert.Equal(t, lastResult.ToLane, requeueThrash.ResultingLane, "FR6: requeue must preserve the held lane, never advance or revert it")

	// Snapshotted immediately after RequeueTask returns, before the
	// claim below (itself a fresh task_attempt row) -- NFR5's own
	// before/after comparison is scoped tightly around the requeue call
	// alone, never contaminated by subsequent unrelated writes.
	attemptRowsAfterRequeue := snapshotTaskAttempts(t, ctx, env.pool, taskThrash.ID)
	notesAfterRequeue := snapshotTaskNotes(t, ctx, env.pool, taskThrash.ID)

	taskThrashRequeued, err := tasks.GetTaskByID(ctx, taskThrash.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, taskThrashRequeued.ThrashCount)
	assert.Nil(t, taskThrashRequeued.CurrentEscalationID)

	claimThrashRequeued, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskThrash.ID, SessionID: sessionThrashID, Acting: agentThrash, OnBehalfOf: humanThrash,
	})
	require.NoError(t, err, "FR6: a requeued task must be claimable again immediately")
	payloadThrashRequeued, err := assembler.Assemble(ctx, env.scopeID, taskThrash.ID)
	require.NoError(t, err)
	assert.Equal(t, requeueThrash.ResultingLane, store.Lane(payloadThrashRequeued.Task.CurrentLane))
	var foundInvestigationNote bool
	for _, n := range payloadThrashRequeued.Task.Notes {
		if n.ID == noteThrash.ID {
			foundInvestigationNote = true
			assert.Equal(t, "investigation note recorded while thrash-cap-escalated", n.Body, "FR6: the investigation note's body must survive requeue unchanged")
		}
	}
	assert.True(t, foundInvestigationNote, "FR6: the note recorded during the investigation must be present in the next claim payload")
	_ = claimThrashRequeued

	requeueAttemptCap, err := tasks.RequeueTask(ctx, store.RequeueParams{ScopeID: env.scopeID, TaskID: taskAttemptCap.ID, Acting: operator, OnBehalfOf: operatorHuman})
	require.NoError(t, err)
	assert.Equal(t, store.ResetCounterAttempt, requeueAttemptCap.CounterReset, "FR6: an attempt-cap escalation's requeue must reset attempt_count")
	taskAttemptCapRequeued, err := tasks.GetTaskByID(ctx, taskAttemptCap.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, taskAttemptCapRequeued.AttemptCount)
	_, err = tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskAttemptCap.ID, SessionID: sessionAttemptID, Acting: agentAttempt, OnBehalfOf: humanAttempt,
	})
	require.NoError(t, err, "FR6: a requeued attempt-cap task must be claimable again")

	requeueManual, err := tasks.RequeueTask(ctx, store.RequeueParams{ScopeID: env.scopeID, TaskID: taskManual.ID, Acting: operator, OnBehalfOf: operatorHuman})
	require.NoError(t, err)
	assert.Equal(t, store.ResetCounterNone, requeueManual.CounterReset, "FR6: a manual escalation below DefaultAttemptCap must reset neither counter")
	taskManualRequeued, err := tasks.GetTaskByID(ctx, taskManual.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, taskManualRequeued.AttemptCount, "FR6: requeue is not itself an attempt, and below-cap manual requeue must not touch attempt_count")
	_, err = tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskManual.ID, SessionID: sessionManualID, Acting: agentManual, OnBehalfOf: humanManual,
	})
	require.NoError(t, err, "FR6: a requeued manual-escalation task must be claimable again")

	requeueManualAtCap, err := tasks.RequeueTask(ctx, store.RequeueParams{ScopeID: env.scopeID, TaskID: taskManualAtCap.ID, Acting: operator, OnBehalfOf: operatorHuman})
	require.NoError(t, err)
	assert.Equal(t, store.ResetCounterAttempt, requeueManualAtCap.CounterReset, "FR6 (FR9 exception): a manual escalation whose own force-close reached the cap must still reset attempt_count on requeue")
	taskManualAtCapRequeued, err := tasks.GetTaskByID(ctx, taskManualAtCap.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, taskManualAtCapRequeued.AttemptCount)
	_, err = tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskManualAtCap.ID, SessionID: sessionAtCapID, Acting: agentAtCap, OnBehalfOf: humanAtCap,
	})
	require.NoError(t, err, "FR6: the manual-at-cap case must not be stranded -- without the FR9 exception, ClaimTask would refuse with ErrAttemptCapExhausted here")

	// ==================================================================
	// Step 9: cancel a task (FR7) -- dead-lettered, distinct from Done,
	// claim force-closed, requeue refuses it, history intact; FR10's
	// cancelled-console view.
	// ==================================================================

	taskCancel := singleLaneTask(t, ctx, tasks, env.scopeID, milepebble.ID, "cancelled task", operator, operatorHuman)
	agentCancel, humanCancel := escalationAgentSession("cancel")
	sessionCancelID, err := env.sessions.InitSession(ctx, env.scopeID, agentCancel, humanCancel, nil)
	require.NoError(t, err)
	claimCancel, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskCancel.ID, SessionID: sessionCancelID, Acting: agentCancel, OnBehalfOf: humanCancel,
	})
	require.NoError(t, err)
	noteCancel, err := tasks.RecordNote(ctx, store.RecordNoteParams{
		ScopeID: env.scopeID, TaskID: &taskCancel.ID, Kind: store.NoteKindComment, Body: "note recorded before cancellation",
		Acting: agentCancel, OnBehalfOf: humanCancel,
	})
	require.NoError(t, err)
	attemptRowsBeforeCancel := snapshotTaskAttempts(t, ctx, env.pool, taskCancel.ID)
	notesBeforeCancel := snapshotTaskNotes(t, ctx, env.pool, taskCancel.ID)

	cancelResult, err := tasks.CancelTask(ctx, store.CancelTaskParams{ScopeID: env.scopeID, TaskID: taskCancel.ID, Acting: operator, OnBehalfOf: operatorHuman})
	require.NoError(t, err)
	assert.True(t, cancelResult.ClaimForceClosed)

	taskCancelAfter, err := tasks.GetTaskByID(ctx, taskCancel.ID)
	require.NoError(t, err)
	require.NotNil(t, taskCancelAfter.CancelledAt)
	assert.Equal(t, store.LaneScaffold, taskCancelAfter.CurrentLane, "FR7: cancelling must never touch current_lane")
	assert.NotEqual(t, store.LaneDone, taskCancelAfter.CurrentLane, "FR7: a cancelled task's dead-lettered state is distinct from lane Done")

	_, err = tasks.Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: env.scopeID, TaskID: taskCancel.ID, ClaimID: claimCancel.ID, Acting: agentCancel, OnBehalfOf: humanCancel,
	})
	assert.ErrorIs(t, err, store.ErrClaimNotCurrent, "FR7: the original claimant's heartbeat must be rejected after cancel")

	_, err = tasks.RequeueTask(ctx, store.RequeueParams{ScopeID: env.scopeID, TaskID: taskCancel.ID, Acting: operator, OnBehalfOf: operatorHuman})
	assert.ErrorIs(t, err, store.ErrTaskCancelled, "FR7: requeue must refuse to reopen a cancelled task")

	_, err = tasks.CancelTask(ctx, store.CancelTaskParams{ScopeID: env.scopeID, TaskID: taskCancel.ID, Acting: operator, OnBehalfOf: operatorHuman})
	assert.ErrorIs(t, err, store.ErrTaskAlreadyCancelled, "FR7: cancelling an already-cancelled task must be refused, never a silent no-op")

	assert.Equal(t, attemptRowsBeforeCancel, snapshotTaskAttempts(t, ctx, env.pool, taskCancel.ID), "FR7/NFR5: cancel must leave task_attempt history intact")
	assert.Equal(t, notesBeforeCancel, snapshotTaskNotes(t, ctx, env.pool, taskCancel.ID), "FR7/NFR5: cancel must leave task_note history intact")

	cancelledPage, err := tasks.ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: env.scopeID})
	require.NoError(t, err)
	var cancelledRow *store.CancelledTaskRow
	for i := range cancelledPage.Items {
		if cancelledPage.Items[i].TaskID == taskCancel.ID {
			cancelledRow = &cancelledPage.Items[i]
		}
	}
	require.NotNil(t, cancelledRow, "FR10: the cancelled task must appear in the cancelled console view")
	assert.Equal(t, "cancelled task", cancelledRow.Title)
	assert.Equal(t, milepebble.ID, cancelledRow.DeliveryRef.ID)
	assert.Equal(t, store.MilestoneKindMilepebble, cancelledRow.DeliveryRef.Kind)
	assert.Equal(t, operator.Sub, cancelledRow.CancelledByActing.Sub, "FR10/NFR3: both cancellation subject pairs must be present")
	assert.Equal(t, operatorHuman.Sub, cancelledRow.CancelledByOnBehalfOf.Sub)
	_ = noteCancel

	// ==================================================================
	// Step 10: note lifecycle + open-notes console view (FR11, FR12).
	// ==================================================================

	taskNotes := singleLaneTask(t, ctx, tasks, env.scopeID, milepebble.ID, "note lifecycle task", operator, operatorHuman)

	noteOpenTask, err := tasks.RecordNote(ctx, store.RecordNoteParams{
		ScopeID: env.scopeID, TaskID: &taskNotes.ID, Kind: store.NoteKindComment, Body: "stays open, task target",
		Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)
	assert.Equal(t, store.NoteLifecycleStatusNoted, noteOpenTask.CurrentStatus, "FR11: a note starts at status noted")

	entityKindFeature := store.NoteEntityKindFeature
	noteOpenEntity, err := tasks.RecordNote(ctx, store.RecordNoteParams{
		ScopeID: env.scopeID, EntityKind: &entityKindFeature, EntityID: &feature.ID, Kind: store.NoteKindScopeNote,
		Body: "stays open, entity target", Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)

	noteClosedTask, err := tasks.RecordNote(ctx, store.RecordNoteParams{
		ScopeID: env.scopeID, TaskID: &taskNotes.ID, Kind: store.NoteKindComment, Body: "will be transitioned away",
		Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)

	// FR11: any persona may transition a note's status -- append-only
	// history, current_status mirrored onto task_note. noted -> carried-
	// over -> closed, proving more than one transition is recorded (never
	// a silent no-op on re-affirmation of the current status either).
	_, err = tasks.TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
		ScopeID: env.scopeID, NoteID: noteClosedTask.ID, Status: store.NoteLifecycleStatusCarriedOver, Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)
	finalTransition, err := tasks.TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
		ScopeID: env.scopeID, NoteID: noteClosedTask.ID, Status: store.NoteLifecycleStatusClosed, Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)
	assert.Equal(t, store.NoteLifecycleStatusClosed, finalTransition.Status)

	var lifecycleEventCount int
	require.NoError(t, env.pool.QueryRow(ctx, `SELECT COUNT(*) FROM task_note_lifecycle_event WHERE note_id = $1`, noteClosedTask.ID).Scan(&lifecycleEventCount))
	assert.Equal(t, 2, lifecycleEventCount, "FR11: every transition, including intermediate ones, is an appended row -- never overwritten")

	_, err = tasks.TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
		ScopeID: env.scopeID, NoteID: noteOpenEntity.ID, Status: store.NoteLifecycleStatusDeferred, Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)
	// Re-open it -- FR12's open-notes view must reflect the CURRENT
	// status, not merely "was it ever noted".
	_, err = tasks.TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
		ScopeID: env.scopeID, NoteID: noteOpenEntity.ID, Status: store.NoteLifecycleStatusNoted, Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)

	openNotesPage, err := tasks.ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: env.scopeID})
	require.NoError(t, err)
	openNoteIDs := make(map[uuid.UUID]store.OpenNoteRow, len(openNotesPage.Items))
	for _, row := range openNotesPage.Items {
		openNoteIDs[row.NoteID] = row
	}
	_, closedStillOpen := openNoteIDs[noteClosedTask.ID]
	assert.False(t, closedStillOpen, "FR12: a note transitioned away from noted must not appear in the open-notes view")

	rowOpenTask, ok := openNoteIDs[noteOpenTask.ID]
	require.True(t, ok, "FR12: a note still at status noted, targeting a task, must appear")
	require.NotNil(t, rowOpenTask.TaskContext)
	assert.Nil(t, rowOpenTask.EntityContext)
	assert.Equal(t, taskNotes.ID, rowOpenTask.TaskContext.TaskID)
	assert.Equal(t, "note lifecycle task", rowOpenTask.TaskContext.Title)
	assert.Equal(t, milepebble.ID, rowOpenTask.TaskContext.DeliveryRef.ID)

	rowOpenEntity, ok := openNoteIDs[noteOpenEntity.ID]
	require.True(t, ok, "FR12: a note re-affirmed at status noted, targeting a spec entity, must appear")
	require.NotNil(t, rowOpenEntity.EntityContext)
	assert.Nil(t, rowOpenEntity.TaskContext)
	assert.Equal(t, store.NoteEntityKindFeature, rowOpenEntity.EntityContext.EntityKind)
	assert.Equal(t, feature.ID, rowOpenEntity.EntityContext.EntityID)
	assert.Equal(t, "Escalation console feature", rowOpenEntity.EntityContext.Title)

	// FR11/FR12: status appears in both the claim payload and the
	// per-task fetch -- claim taskNotes and read its payload.
	agentNotes, humanNotes := escalationAgentSession("notes")
	sessionNotesID, err := env.sessions.InitSession(ctx, env.scopeID, agentNotes, humanNotes, nil)
	require.NoError(t, err)
	_, err = tasks.ClaimTask(ctx, store.ClaimTaskParams{ScopeID: env.scopeID, TaskID: taskNotes.ID, SessionID: sessionNotesID, Acting: agentNotes, OnBehalfOf: humanNotes})
	require.NoError(t, err)
	payloadNotes, err := assembler.Assemble(ctx, env.scopeID, taskNotes.ID)
	require.NoError(t, err)
	statusByID := make(map[uuid.UUID]string, len(payloadNotes.Task.Notes))
	for _, n := range payloadNotes.Task.Notes {
		statusByID[n.ID] = n.Status
	}
	assert.Equal(t, string(store.NoteLifecycleStatusNoted), statusByID[noteOpenTask.ID], "FR11: the claim payload must carry the note's current lifecycle status")
	assert.Equal(t, string(store.NoteLifecycleStatusClosed), statusByID[noteClosedTask.ID], "FR11: the claim payload must reflect the transitioned status, not the note's original one")

	notesFromFetch, err := tasks.ListNotesForTask(ctx, env.scopeID, taskNotes.ID)
	require.NoError(t, err)
	var perTaskFetchOK bool
	for _, n := range notesFromFetch {
		if n.ID == noteClosedTask.ID {
			perTaskFetchOK = n.CurrentStatus == store.NoteLifecycleStatusClosed
		}
	}
	assert.True(t, perTaskFetchOK, "FR11: the per-task fetch (ListNotesForTask, M4 FR10's own read) must also carry the current lifecycle status")

	// ==================================================================
	// Step 11: paging across the console queries (NFR6), with
	// cross-scope token rejection. FR4's ListClaimedTasks is excluded --
	// see Step 2's own comment; it cannot be paged while it remains an
	// unconditional ErrNotImplemented stub.
	// ==================================================================

	var otherScopeID uuid.UUID
	require.NoError(t, env.pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "whale-net/escalation-console-other-scope-"+uuid.NewString()).Scan(&otherScopeID))

	// Seed five more cancelled tasks purely so a page size of 2 forces
	// three pages (2, 2, 1).
	for i := 0; i < 5; i++ {
		task := singleLaneTask(t, ctx, tasks, env.scopeID, milepebble.ID, "paging cancelled task", operator, operatorHuman)
		_, err := tasks.CancelTask(ctx, store.CancelTaskParams{ScopeID: env.scopeID, TaskID: task.ID, Acting: operator, OnBehalfOf: operatorHuman})
		require.NoError(t, err)
	}
	cancelledIDs, firstCancelledToken := pageAllCancelled(t, ctx, tasks, env.scopeID, 2)
	assert.GreaterOrEqual(t, len(cancelledIDs), 6, "NFR6: paging must surface every cancelled task with no gaps")
	assert.Len(t, cancelledIDs, len(uniqueUUIDs(cancelledIDs)), "NFR6: paging must never repeat a row across pages")
	require.NotEmpty(t, firstCancelledToken, "NFR6: with more rows than one page, the first page's token must be non-empty")
	_, err = tasks.ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: otherScopeID, Page: store.PageParams{ContinuationToken: firstCancelledToken}})
	assert.ErrorIs(t, err, store.ErrTokenScopeMismatch, "NFR6: a token issued under one scope must be rejected when resumed against another")

	// Five more escalated tasks (manual, for simplicity) for the same
	// three-page walk.
	for i := 0; i < 5; i++ {
		task := singleLaneTask(t, ctx, tasks, env.scopeID, milepebble.ID, "paging escalated task", operator, operatorHuman)
		_, err := tasks.EscalateTask(ctx, store.EscalateParams{ScopeID: env.scopeID, TaskID: task.ID, Acting: operator, OnBehalfOf: operatorHuman})
		require.NoError(t, err)
	}
	escalatedIDs, firstEscalatedToken := pageAllEscalated(t, ctx, tasks, env.scopeID, 2)
	// The main walk's own four escalated tasks (taskThrash/taskAttemptCap/
	// taskManual/taskManualAtCap) were all already requeued back to
	// claimable in Step 8 -- by this point only the five seeded here are
	// still escalated, exercising the exact same paging/token machinery.
	assert.GreaterOrEqual(t, len(escalatedIDs), 5, "NFR6: paging must surface every one of the 5 seeded escalated tasks with no gaps")
	assert.Len(t, escalatedIDs, len(uniqueUUIDs(escalatedIDs)))
	require.NotEmpty(t, firstEscalatedToken)
	_, err = tasks.ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: otherScopeID, Page: store.PageParams{ContinuationToken: firstEscalatedToken}})
	assert.ErrorIs(t, err, store.ErrTokenScopeMismatch)

	// Five more open notes for the same three-page walk.
	for i := 0; i < 5; i++ {
		task := singleLaneTask(t, ctx, tasks, env.scopeID, milepebble.ID, "paging note task", operator, operatorHuman)
		_, err := tasks.RecordNote(ctx, store.RecordNoteParams{ScopeID: env.scopeID, TaskID: &task.ID, Kind: store.NoteKindComment, Body: "paging note", Acting: operator, OnBehalfOf: operatorHuman})
		require.NoError(t, err)
	}
	openNoteResultIDs, firstOpenNoteToken := pageAllOpenNotes(t, ctx, tasks, env.scopeID, 2)
	assert.GreaterOrEqual(t, len(openNoteResultIDs), 7, "NFR6: paging must surface every open note (2 from Step 10 + 5 seeded) with no gaps")
	assert.Len(t, openNoteResultIDs, len(uniqueUUIDs(openNoteResultIDs)))
	require.NotEmpty(t, firstOpenNoteToken)
	_, err = tasks.ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: otherScopeID, Page: store.PageParams{ContinuationToken: firstOpenNoteToken}})
	assert.ErrorIs(t, err, store.ErrTokenScopeMismatch)

	// A malformed token is rejected too, distinctly from a cross-scope one.
	_, err = tasks.ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: env.scopeID, Page: store.PageParams{ContinuationToken: "not-a-real-token"}})
	assert.ErrorIs(t, err, store.ErrInvalidContinuationToken, "NFR6: a malformed token must be rejected distinctly from a cross-scope one")

	// ==================================================================
	// Part 3: NFR audit -- each assertion below is executable test code.
	// ==================================================================

	t.Run("NFR1_ScopeQualification", func(t *testing.T) {
		var notNullScopeIDCount int
		require.NoError(t, env.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name = ANY($1) AND column_name = 'scope_id' AND is_nullable = 'NO'
		`, escalationAxisM5Tables).Scan(&notNullScopeIDCount))
		assert.Equal(t, len(escalationAxisM5Tables), notNullScopeIDCount, "NFR1: every M5 event table must carry a NOT NULL scope_id")

		cancelledOther, err := tasks.ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: otherScopeID})
		require.NoError(t, err)
		assert.Empty(t, cancelledOther.Items, "NFR1: ListCancelledTasks must filter by scope -- another scope's cancelled tasks must never appear")

		escalatedOther, err := tasks.ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: otherScopeID})
		require.NoError(t, err)
		assert.Empty(t, escalatedOther.Items, "NFR1: ListEscalatedTasks must filter by scope")

		openNotesOther, err := tasks.ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: otherScopeID})
		require.NoError(t, err)
		assert.Empty(t, openNotesOther.Items, "NFR1: ListOpenNotes must filter by scope")
	})

	t.Run("NFR2_NoMutationOfEventTables", func(t *testing.T) {
		forbidden := []string{}
		for _, table := range escalationAxisM5Tables {
			forbidden = append(forbidden, "update "+table, "delete from "+table, "delete\n\t\t\tfrom "+table)
		}

		for _, rlocation := range escalationAxisSourceFiles {
			path, err := runfiles.Rlocation(rlocation)
			require.NoError(t, err, "is the work_axis_srcs filegroup still a data dep of this test target? (%s)", rlocation)
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			lower := strings.ToLower(string(content))
			for _, pat := range forbidden {
				assert.NotContains(t, lower, pat, "NFR2: %s must never UPDATE or DELETE against %s -- append-only, resolution recorded only by a fresh task_intervention_event row", rlocation, pat)
			}
		}

		// No M5 event table is SCD2 -- no valid_from/valid_to column
		// anywhere (this migration's own doc comment, AGENTS.md's SCD2
		// section).
		var scd2ColumnCount int
		require.NoError(t, env.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name = ANY($1) AND column_name IN ('valid_from', 'valid_to')
		`, escalationAxisM5Tables).Scan(&scd2ColumnCount))
		assert.Zero(t, scd2ColumnCount, "NFR2: no M5 event table may carry a valid_from/valid_to column")
	})

	t.Run("NFR3_TwoSubjectAttribution", func(t *testing.T) {
		for _, table := range escalationAxisM5Tables {
			var missing int
			require.NoError(t, env.pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM `+table+`
				WHERE scope_id = $1 AND (
					created_by_acting_iss = '' OR created_by_acting_sub = '' OR created_by_acting_kind = '' OR
					created_by_on_behalf_of_iss = '' OR created_by_on_behalf_of_sub = '' OR created_by_on_behalf_of_kind = ''
				)
			`, env.scopeID).Scan(&missing))
			assert.Zero(t, missing, "NFR3: every row on %s must carry all six populated subject columns", table)

			var notNullCount int
			require.NoError(t, env.pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM information_schema.columns
				WHERE table_name = $1 AND column_name IN (
					'created_by_acting_iss', 'created_by_acting_sub', 'created_by_acting_kind',
					'created_by_on_behalf_of_iss', 'created_by_on_behalf_of_sub', 'created_by_on_behalf_of_kind'
				) AND is_nullable = 'NO'
			`, table).Scan(&notNullCount))
			assert.Equal(t, 6, notNullCount, "NFR3: %s must declare all six subject columns NOT NULL at the schema level", table)
		}
	})

	t.Run("NFR4_CounterIndependence", func(t *testing.T) {
		// Re-confirm (post-requeue) both final independence facts this
		// walk already established inline in Step 3/Step 4: a failing
		// verdict never moved attempt_count on taskThrash, and a lapse/
		// abandon never moved thrash_count on taskAttemptCap -- true
		// throughout the whole walk, not just at the moment each cap
		// tripped.
		finalThrash, err := tasks.GetTaskByID(ctx, taskThrash.ID)
		require.NoError(t, err)
		assert.Equal(t, 0, finalThrash.AttemptCount, "NFR4: taskThrash's attempt_count must still be untouched by its three failing verdicts")

		finalAttemptCap, err := tasks.GetTaskByID(ctx, taskAttemptCap.ID)
		require.NoError(t, err)
		assert.Equal(t, 0, finalAttemptCap.ThrashCount, "NFR4: taskAttemptCap's thrash_count must still be untouched by its lapse/abandon/lapse sequence")
	})

	t.Run("NFR5_RequeueAndCancelLeavePriorRowsUnchanged", func(t *testing.T) {
		assert.Equal(t, attemptRowsBeforeRequeue, attemptRowsAfterRequeue, "NFR5: requeue must never rewrite a prior task_attempt row")
		assert.Equal(t, notesBeforeRequeue, notesAfterRequeue, "NFR5: requeue must never rewrite a prior task_note row")
		// Cancel's own before/after comparison already ran inline in
		// Step 9 (attemptRowsBeforeCancel/notesBeforeCancel) -- repeated
		// here as a named NFR5 subtest entry point for a validator
		// scanning for "NFR5:".
		assert.Equal(t, attemptRowsBeforeCancel, snapshotTaskAttempts(t, ctx, env.pool, taskCancel.ID), "NFR5: cancel must never rewrite a prior task_attempt row")
		assert.Equal(t, notesBeforeCancel, snapshotTaskNotes(t, ctx, env.pool, taskCancel.ID), "NFR5: cancel must never rewrite a prior task_note row")

		// The escalation event itself is never rewritten either -- still
		// readable, byte-for-byte, after the requeue that resolved it.
		rereadEscalation, err := tasks.GetEscalationEventByID(ctx, escalationThrash.ID)
		require.NoError(t, err)
		assert.Equal(t, escalationThrash, rereadEscalation, "NFR5/NFR2: a resolved escalation's own event row must never be rewritten")
	})

	t.Run("Claimability_TaskClaimableIdxThreePredicates", func(t *testing.T) {
		var indexDef string
		require.NoError(t, env.pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = 'task_claimable_idx'`).Scan(&indexDef))
		lowered := strings.ToLower(indexDef)
		assert.Contains(t, lowered, "current_claim_id is null", "task_claimable_idx must carry the claim predicate")
		assert.Contains(t, lowered, "current_escalation_id is null", "task_claimable_idx must carry the escalation predicate (FR2: escalated regardless of claim state)")
		assert.Contains(t, lowered, "cancelled_at is null", "task_claimable_idx must carry the cancellation predicate (FR7: dead-lettered is permanently unclaimable)")
	})
}

// attemptSnapshot/noteSnapshot are one row's comparable content --
// snapshotTaskAttempts/snapshotTaskNotes capture every task_attempt/
// task_note row for taskID, ordered by id, as a slice of these; NFR5's
// "byte-for-byte unchanged" assertions compare two snapshots taken around
// one isolated requeue/cancel call with assert.Equal.
type attemptSnapshot struct {
	ID        uuid.UUID
	Outcome   string
	CreatedAt time.Time
}

func snapshotTaskAttempts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) []attemptSnapshot {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT id, outcome, created_at FROM task_attempt WHERE task_id = $1 ORDER BY id
	`, taskID)
	require.NoError(t, err)
	defer rows.Close()

	var snaps []attemptSnapshot
	for rows.Next() {
		var s attemptSnapshot
		require.NoError(t, rows.Scan(&s.ID, &s.Outcome, &s.CreatedAt))
		snaps = append(snaps, s)
	}
	require.NoError(t, rows.Err())
	return snaps
}

type noteSnapshot struct {
	ID        uuid.UUID
	Body      string
	CreatedAt time.Time
}

func snapshotTaskNotes(t *testing.T, ctx context.Context, pool *pgxpool.Pool, taskID uuid.UUID) []noteSnapshot {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT id, body, created_at FROM task_note WHERE task_id = $1 ORDER BY id
	`, taskID)
	require.NoError(t, err)
	defer rows.Close()

	var snaps []noteSnapshot
	for rows.Next() {
		var s noteSnapshot
		require.NoError(t, rows.Scan(&s.ID, &s.Body, &s.CreatedAt))
		snaps = append(snaps, s)
	}
	require.NoError(t, rows.Err())
	return snaps
}

// pageAllCancelled/pageAllEscalated/pageAllOpenNotes each walk their own
// console query to exhaustion with pageSize-sized pages and a
// continuation token, returning every row's own id (order-insensitive --
// NFR6's own deterministic-order proof already lives in
// task_console_integration_test.go) plus the very first page's token, for
// the cross-scope-rejection check that follows each call below.
func pageAllCancelled(t *testing.T, ctx context.Context, tasks store.TaskStore, scopeID uuid.UUID, pageSize int) ([]uuid.UUID, string) {
	t.Helper()
	var ids []uuid.UUID
	var firstToken string
	token := ""
	for page := 0; ; page++ {
		result, err := tasks.ListCancelledTasks(ctx, store.ListCancelledTasksParams{ScopeID: scopeID, Page: store.PageParams{PageSize: pageSize, ContinuationToken: token}})
		require.NoError(t, err)
		for _, row := range result.Items {
			ids = append(ids, row.TaskID)
		}
		if page == 0 {
			firstToken = result.NextToken
		}
		if result.NextToken == "" {
			break
		}
		token = result.NextToken
	}
	return ids, firstToken
}

func pageAllEscalated(t *testing.T, ctx context.Context, tasks store.TaskStore, scopeID uuid.UUID, pageSize int) ([]uuid.UUID, string) {
	t.Helper()
	var ids []uuid.UUID
	var firstToken string
	token := ""
	for page := 0; ; page++ {
		result, err := tasks.ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{ScopeID: scopeID, Page: store.PageParams{PageSize: pageSize, ContinuationToken: token}})
		require.NoError(t, err)
		for _, row := range result.Items {
			ids = append(ids, row.TaskID)
		}
		if page == 0 {
			firstToken = result.NextToken
		}
		if result.NextToken == "" {
			break
		}
		token = result.NextToken
	}
	return ids, firstToken
}

func pageAllOpenNotes(t *testing.T, ctx context.Context, tasks store.TaskStore, scopeID uuid.UUID, pageSize int) ([]uuid.UUID, string) {
	t.Helper()
	var ids []uuid.UUID
	var firstToken string
	token := ""
	for page := 0; ; page++ {
		result, err := tasks.ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID, Page: store.PageParams{PageSize: pageSize, ContinuationToken: token}})
		require.NoError(t, err)
		for _, row := range result.Items {
			ids = append(ids, row.NoteID)
		}
		if page == 0 {
			firstToken = result.NextToken
		}
		if result.NextToken == "" {
			break
		}
		token = result.NextToken
	}
	return ids, firstToken
}

func uniqueUUIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	var out []uuid.UUID
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
