//go:build integration

// This file is issue #2728's end-to-end conformance proof for krill M4's
// whole work axis: one connected walk (task create -> dependency gating ->
// claim -> heartbeat -> lease-expiry reclaim -> abandon -> complete with
// verdict lane advance/revert -> resume via fetch-by-id), proving every FR
// #2719-#2727 shipped independently composes against a real Postgres --
// mirrors delivery_axis_integration_test.go's own shape (M3's end-to-end
// proof) -- plus a mechanical audit of every NFR the root plan (#2717)
// states for this milestone (NFR1-NFR7). Shares this package's target and
// testEnv/newTestEnv helper (roundtrip_integration_test.go) -- see that
// file's own doc comment for why this package only builds under the
// "integration" tag.
//
// Unlike roundtrip_integration_test.go/whagent_net_import_integration_test.go,
// this file never goes through //krill/importer -- the work axis has no
// markdown-import path of its own -- so its fixture is built directly
// against //krill/store, the same way delivery_axis_integration_test.go's
// own fixture is. NFR6's write-only-gate audit is the one place this file
// reaches into //krill/api/handlers and //krill/work: it wires the exact
// same handlers.RequireSession(sessions)(handler) pattern every per-task
// handler test already proves individually (e.g.
// krill/api/handlers/task_lease_test.go), in one place, across all seven
// mutating work-axis endpoints plus the two read endpoints the gate must
// never touch.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/conformance:roundtrip_integration_test --test_output=all --test_filter=TestWorkAxis
package conformance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// workAxisAgent/workAxisHuman mint distinguishable LB4 subject pairs (an
// Agent acting on a Swarm Operator/human's behalf), keyed by a caller-
// supplied name so each actor in this file's walk (the operator, the
// lifecycle agent, session A, session B) is a genuinely distinct subject
// pair, never one subject standing in for several roles -- mirrors
// deliveryAxisAgent/deliveryAxisHuman's identical precedent.
func workAxisAgent(name string) store.Subject {
	return store.Subject{Iss: "https://whagent.example.test", Sub: "agent-work-axis-" + name, Kind: store.SubjectKindService}
}

func workAxisHuman(name string) store.Subject {
	return store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "swarm-operator-" + name, Kind: store.SubjectKindHuman}
}

// workAxisM4Tables is every table migration 015 (issue #2719) created --
// named once here so the NFR1/NFR2/NFR3 assertions below share one
// authoritative list rather than each repeating it.
var workAxisM4Tables = []string{"task", "task_dependency", "task_claim", "task_lease_event", "task_attempt", "task_note"}

// workAxisSourceFiles is NFR5's grep target: every non-test Go source file
// the work axis (#2719-#2727) added, located through Bazel runfiles via
// the work_axis_srcs filegroups store/BUILD.bazel, api/handlers/BUILD.bazel,
// and work/BUILD.bazel each declare for this purpose.
var workAxisSourceFiles = []string{
	"_main/krill/store/task.go",
	"_main/krill/store/task_abandon.go",
	"_main/krill/store/task_claim.go",
	"_main/krill/store/task_complete.go",
	"_main/krill/store/task_dependency.go",
	"_main/krill/store/task_lease.go",
	"_main/krill/store/task_note.go",
	"_main/krill/store/task_reclaim.go",
	"_main/krill/api/handlers/task.go",
	"_main/krill/api/handlers/task_abandon.go",
	"_main/krill/api/handlers/task_claim.go",
	"_main/krill/api/handlers/task_complete.go",
	"_main/krill/api/handlers/task_dependency.go",
	"_main/krill/api/handlers/task_lease.go",
	"_main/krill/api/handlers/task_note.go",
	"_main/krill/api/handlers/task_payload.go",
	"_main/krill/api/handlers/task_reclaim.go",
	"_main/krill/work/payload.go",
}

// TestWorkAxis_EndToEndLifecycle_ResumeAndNFRAudit is issue #2728's own
// walk: fixture setup, then Part 1 (the full lifecycle round trip -- FR1,
// FR2, FR3, FR5, FR6, FR8, FR9), Part 2 (FR10's cross-host resume proof),
// and Part 3 (the NFR1-NFR7 audit, one subtest per NFR).
func TestWorkAxis_EndToEndLifecycle_ResumeAndNFRAudit(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	tasks := env.store.Tasks()
	querier := slice.NewQuerier(env.store)
	assembler := work.NewAssembler(tasks, querier)

	operator := workAxisAgent("operator")
	operatorHuman := workAxisHuman("operator")

	// -- Fixture: one Product -> FeatureSet -> Feature, one Milestone
	// delivering it, one Milepebble cut from it delivering it too -- every
	// task below is scoped to this one milepebble (FR1). --------------
	product, err := env.store.Products().Create(ctx, env.scopeID, "Work Axis Conformance", "prove krill M4's work axis composes end to end")
	require.NoError(t, err)
	featureSet, err := env.store.FeatureSets().Create(ctx, env.scopeID, product.ID, "Work Axis Surface", nil)
	require.NoError(t, err)
	feature, err := env.store.Features().Create(ctx, env.scopeID, featureSet.ID, "Work axis feature", nil)
	require.NoError(t, err)
	milestone, err := env.store.MilestoneAuthoring().CreateMilestone(ctx, env.scopeID, product.ID, "M-Work", "ship the work axis", nil, operator, operatorHuman)
	require.NoError(t, err)
	require.NoError(t, env.store.MilestoneAuthoring().AddDelivers(ctx, env.scopeID, milestone.ID, feature.ID, operator, operatorHuman))
	milepebble, err := env.store.MilestoneAuthoring().CreateMilepebble(ctx, env.scopeID, milestone.ID, "MP-Work", "a slice of M-Work", nil, operator, operatorHuman)
	require.NoError(t, err)
	require.NoError(t, env.store.MilestoneAuthoring().AddMilepebbleDelivers(ctx, env.scopeID, milepebble.ID, feature.ID, operator, operatorHuman))

	// ==================================================================
	// Part 1: full lifecycle round trip (FR1, FR2, FR3, FR5, FR6, FR8, FR9)
	// ==================================================================

	taskB, err := tasks.CreateTask(ctx, store.CreateTaskParams{
		ScopeID: env.scopeID, MilestoneID: milepebble.ID, Title: "dependency task",
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation},
		StartingLane: store.LaneScaffold, Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)

	// taskA starts at Testing (the last real lane before Done) so a
	// single failing verdict reverting exactly one lane, followed by two
	// passing verdicts, lands exactly on Done -- the issue's own numbered
	// scenario (Testing --fail--> Implementation --pass--> Testing
	// --pass--> Done).
	taskA, err := tasks.CreateTask(ctx, store.CreateTaskParams{
		ScopeID: env.scopeID, MilestoneID: milepebble.ID, Title: "dependent task",
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation, store.LaneTesting},
		StartingLane: store.LaneTesting, Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)

	require.NoError(t, tasks.DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID: env.scopeID, TaskID: taskA.ID, DependsOnTaskIDs: []uuid.UUID{taskB.ID},
		Acting: operator, OnBehalfOf: operatorHuman,
	}))

	agentLifecycle, humanLifecycle := workAxisAgent("lifecycle"), workAxisHuman("lifecycle")
	sessionLifecycleID, err := env.sessions.InitSession(ctx, env.scopeID, agentLifecycle, humanLifecycle, nil)
	require.NoError(t, err)

	// FR2: A is not claimable while B, its declared dependency, has not
	// reached Done.
	_, err = tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskA.ID, SessionID: sessionLifecycleID, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	assert.ErrorIs(t, err, store.ErrDependenciesUnsatisfied, "FR2: a task with an unfinished dependency must not be claimable")

	// Drive B to Done, one claim/complete(pass) cycle per lane (FR3/FR8).
	claimB1, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskB.ID, SessionID: sessionLifecycleID, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err)
	resultB1, err := tasks.CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: env.scopeID, TaskID: taskB.ID, ClaimID: claimB1.ID, Verdict: store.VerdictPass, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err)
	assert.Equal(t, store.LaneImplementation, resultB1.ToLane, "FR8: a passing verdict advances to the next lane in the task's own sequence")

	claimB2, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskB.ID, SessionID: sessionLifecycleID, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err)
	resultB2, err := tasks.CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: env.scopeID, TaskID: taskB.ID, ClaimID: claimB2.ID, Verdict: store.VerdictPass, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err)
	assert.Equal(t, store.LaneDone, resultB2.ToLane, "FR8: a passing verdict from the last lane in sequence reaches Done")

	// FR2/FR3: now that B has reached Done, A becomes claimable.
	claimA1, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskA.ID, SessionID: sessionLifecycleID, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err, "FR2/FR3: once its dependency reaches Done, the dependent task must become claimable")

	// FR6: heartbeat extends the lease.
	_, err = tasks.Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: env.scopeID, TaskID: taskA.ID, ClaimID: claimA1.ID, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err)

	// FR8: a failing verdict reverts exactly one lane.
	resultAFail, err := tasks.CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: env.scopeID, TaskID: taskA.ID, ClaimID: claimA1.ID, Verdict: store.VerdictFail, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err)
	assert.Equal(t, store.LaneTesting, resultAFail.FromLane)
	assert.Equal(t, store.LaneImplementation, resultAFail.ToLane, "FR8: a failing verdict reverts exactly one lane, the one immediately preceding current in the task's own sequence")

	claimA2, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskA.ID, SessionID: sessionLifecycleID, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err)
	resultAPass1, err := tasks.CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: env.scopeID, TaskID: taskA.ID, ClaimID: claimA2.ID, Verdict: store.VerdictPass, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err)
	assert.Equal(t, store.LaneTesting, resultAPass1.ToLane)

	claimA3, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskA.ID, SessionID: sessionLifecycleID, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err)
	resultAPass2, err := tasks.CompleteTask(ctx, store.CompleteTaskParams{
		ScopeID: env.scopeID, TaskID: taskA.ID, ClaimID: claimA3.ID, Verdict: store.VerdictPass, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err)
	assert.Equal(t, store.LaneDone, resultAPass2.ToLane, "FR8: two passing verdicts from Testing (start) reach Done: Testing--fail-->Implementation--pass-->Testing--pass-->Done")

	finalPayloadA, err := assembler.Assemble(ctx, env.scopeID, taskA.ID)
	require.NoError(t, err)
	assert.Equal(t, string(store.LaneDone), finalPayloadA.Task.CurrentLane)
	require.Len(t, finalPayloadA.Task.Dependencies, 1)
	assert.Equal(t, taskB.ID, finalPayloadA.Task.Dependencies[0].DependsOnTaskID, "FR4: the claim payload carries the declared dependency list")

	// FR9: abandon releases a claim with no verdict, leaves the lane
	// unchanged, and makes the task claimable again immediately.
	taskD, err := tasks.CreateTask(ctx, store.CreateTaskParams{
		ScopeID: env.scopeID, MilestoneID: milepebble.ID, Title: "abandoned task",
		LaneSequence: []store.Lane{store.LaneScaffold}, StartingLane: store.LaneScaffold,
		Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)
	claimD1, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskD.ID, SessionID: sessionLifecycleID, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err)
	abandonResult, err := tasks.AbandonClaim(ctx, store.AbandonParams{
		ScopeID: env.scopeID, TaskID: taskD.ID, ClaimID: claimD1.ID, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err)
	assert.Equal(t, taskD.ID, abandonResult.TaskID)
	assert.False(t, abandonResult.CapExhausted)

	taskDAfterAbandon, err := tasks.GetTaskByID(ctx, taskD.ID)
	require.NoError(t, err)
	assert.Equal(t, store.LaneScaffold, taskDAfterAbandon.CurrentLane, "FR9: abandoning is not a verdict -- the task's lane must be unchanged")
	assert.Nil(t, taskDAfterAbandon.CurrentClaimID, "FR9: abandoning releases the claim immediately")

	claimD2, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskD.ID, SessionID: sessionLifecycleID, Acting: agentLifecycle, OnBehalfOf: humanLifecycle,
	})
	require.NoError(t, err, "FR9: an abandoned task must become claimable again immediately, with no lease expiry needed")
	assert.NotEqual(t, claimD1.ID, claimD2.ID)

	// ==================================================================
	// Part 2: FR10 cross-host resume -- the milestone's headline story
	// ==================================================================

	taskC, err := tasks.CreateTask(ctx, store.CreateTaskParams{
		ScopeID: env.scopeID, MilestoneID: milepebble.ID, Title: "resume task",
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation},
		StartingLane: store.LaneScaffold, Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)

	agentA, humanA := workAxisAgent("session-a"), workAxisHuman("session-a")
	agentB, humanB := workAxisAgent("session-b"), workAxisHuman("session-b")
	sessionAID, err := env.sessions.InitSession(ctx, env.scopeID, agentA, humanA, nil)
	require.NoError(t, err)
	sessionBID, err := env.sessions.InitSession(ctx, env.scopeID, agentB, humanB, nil)
	require.NoError(t, err)

	// Session A claims the task and does partial work: a note, one
	// heartbeat.
	claimSessionA, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskC.ID, SessionID: sessionAID, Acting: agentA, OnBehalfOf: humanA,
	})
	require.NoError(t, err)

	_, err = tasks.RecordNote(ctx, store.RecordNoteParams{
		ScopeID: env.scopeID, TaskID: &taskC.ID, Kind: store.NoteKindComment,
		Body: "partial work recorded by session A before the simulated crash",
		Acting: agentA, OnBehalfOf: humanA,
	})
	require.NoError(t, err)

	_, err = tasks.Heartbeat(ctx, store.HeartbeatParams{
		ScopeID: env.scopeID, TaskID: taskC.ID, ClaimID: claimSessionA.ID, Acting: agentA, OnBehalfOf: humanA,
	})
	require.NoError(t, err)

	payloadSessionA, err := assembler.Assemble(ctx, env.scopeID, taskC.ID)
	require.NoError(t, err)
	require.NotNil(t, payloadSessionA.Task.CurrentClaim)
	require.Len(t, payloadSessionA.Task.Notes, 1, "session A's own note must be on its own claim payload")

	// Simulate a crash: session A stops heartbeating and its lease lapses
	// -- forced directly since DefaultLeaseDuration is 15 real minutes.
	_, err = env.pool.Exec(ctx, `UPDATE task SET lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`, taskC.ID)
	require.NoError(t, err)

	reclaimResult, err := tasks.ReclaimExpired(ctx, store.ReclaimParams{
		ScopeID: env.scopeID, TaskID: &taskC.ID, Acting: operator, OnBehalfOf: operatorHuman,
	})
	require.NoError(t, err)
	require.Len(t, reclaimResult.Reclaimed, 1, "FR7: the sweep must reclaim the one lease-expired task")
	assert.Equal(t, taskC.ID, reclaimResult.Reclaimed[0].TaskID)
	assert.False(t, reclaimResult.Reclaimed[0].CapExhausted)

	// Session B -- a distinct session with distinct subjects, standing in
	// for a fresh run on another host -- fetches and claims the task.
	claimSessionB, err := tasks.ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: env.scopeID, TaskID: taskC.ID, SessionID: sessionBID, Acting: agentB, OnBehalfOf: humanB,
	})
	require.NoError(t, err, "FR7: a reclaimed task must become claimable again")

	payloadSessionB, err := assembler.Assemble(ctx, env.scopeID, taskC.ID)
	require.NoError(t, err)

	// FR10: session B's payload carries the same spec slice, lane, lane
	// sequence, dependency list, and notes session A's claim payload
	// carried -- data equality, not narrative. Only claim/lease/attempt
	// fields are allowed to differ (checked separately below).
	assert.Equal(t, payloadSessionA.Slice, payloadSessionB.Slice, "FR10: the spec slice must be identical across resume -- durable in krill, not held in the original run's memory")
	assert.Equal(t, payloadSessionA.Task.CurrentLane, payloadSessionB.Task.CurrentLane, "FR10: the lane must be identical across resume")
	assert.Equal(t, payloadSessionA.Task.LaneSequence, payloadSessionB.Task.LaneSequence, "FR10: the lane sequence must be identical across resume")
	assert.Equal(t, payloadSessionA.Task.Dependencies, payloadSessionB.Task.Dependencies, "FR10: the dependency list must be identical across resume")
	assert.Equal(t, payloadSessionA.Task.Notes, payloadSessionB.Task.Notes, "FR10: every note recorded before the crash must still be present, byte-identical, after resume")

	require.NotNil(t, payloadSessionB.Task.CurrentClaim)
	assert.NotEqual(t, claimSessionA.ID, claimSessionB.ID, "the claim/lease/attempt fields are the ones legitimately allowed to differ across resume")
	assert.NotEqual(t, uuid.UUID(sessionAID), payloadSessionB.Task.CurrentClaim.SessionID)
	assert.Equal(t, uuid.UUID(sessionBID), payloadSessionB.Task.CurrentClaim.SessionID)

	// ==================================================================
	// Part 3: NFR audit (NFR1-NFR7) -- each assertion below is executable
	// test code, never a review note.
	// ==================================================================

	t.Run("NFR4_PayloadSliceIsByteEqualToQuerierOutput", func(t *testing.T) {
		want, err := querier.GetMilestoneDeliversSlice(ctx, milepebble.ID)
		require.NoError(t, err)
		assert.Equal(t, want, finalPayloadA.Slice, "NFR4: the payload's slice portion must be exactly slice.Querier's own output for the same scope, never independently re-derived")
	})

	t.Run("NFR2_AppendOnlyRowsNeverContentMutated", func(t *testing.T) {
		// claimB1 was closed by CompleteTask long before this subtest --
		// every immutable column must read back unchanged from what was
		// returned at INSERT time; released_at/release_reason are the one
		// pair this table permits an in-place update to close.
		reread, err := tasks.GetClaimByID(ctx, claimB1.ID)
		require.NoError(t, err)
		assert.Equal(t, claimB1.TaskID, reread.TaskID)
		assert.Equal(t, claimB1.SessionID, reread.SessionID)
		assert.True(t, claimB1.ClaimedAt.Equal(reread.ClaimedAt))
		assert.True(t, claimB1.InitialLeaseExpiresAt.Equal(reread.InitialLeaseExpiresAt))
		assert.Equal(t, claimB1.CreatedByActing, reread.CreatedByActing)
		assert.Equal(t, claimB1.CreatedByOnBehalfOf, reread.CreatedByOnBehalfOf)
		assert.True(t, claimB1.CreatedAt.Equal(reread.CreatedAt))
		assert.Nil(t, claimB1.ReleasedAt, "at INSERT time the claim was not yet closed")
		require.NotNil(t, reread.ReleasedAt, "CompleteTask must have closed this claim in place -- the one permitted mutation")
		require.NotNil(t, reread.ReleaseReason)
		assert.Equal(t, "complete", *reread.ReleaseReason)

		// Every sibling append-only log must only ever grow: record one
		// more note and confirm task_note grew by exactly one while every
		// other M4 table's row count in this scope is untouched.
		before := countM4Rows(ctx, t, env.pool, env.scopeID)
		_, err = tasks.RecordNote(ctx, store.RecordNoteParams{
			ScopeID: env.scopeID, TaskID: &taskA.ID, Kind: store.NoteKindScopeNote,
			Body:   "NFR2 probe: recording one more note must only grow task_note",
			Acting: operator, OnBehalfOf: operatorHuman,
		})
		require.NoError(t, err)
		after := countM4Rows(ctx, t, env.pool, env.scopeID)
		for _, table := range workAxisM4Tables {
			if table == "task_note" {
				assert.Equal(t, before[table]+1, after[table], "NFR2: task_note must grow by exactly one")
				continue
			}
			assert.Equal(t, before[table], after[table], "NFR2: %s must be unchanged by an unrelated note write", table)
		}

		// No M4 table is SCD2 -- no valid_from/valid_to column anywhere.
		var scd2ColumnCount int
		require.NoError(t, env.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name = ANY($1) AND column_name IN ('valid_from', 'valid_to')
		`, workAxisM4Tables).Scan(&scd2ColumnCount))
		assert.Zero(t, scd2ColumnCount, "NFR2: no M4 table may carry a valid_from/valid_to column")
	})

	t.Run("NFR1_ScopeQualification", func(t *testing.T) {
		var notNullScopeIDCount int
		require.NoError(t, env.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name = ANY($1) AND column_name = 'scope_id' AND is_nullable = 'NO'
		`, workAxisM4Tables).Scan(&notNullScopeIDCount))
		assert.Equal(t, len(workAxisM4Tables), notNullScopeIDCount, "NFR1: every M4 table must carry a NOT NULL scope_id")

		var otherScopeID uuid.UUID
		require.NoError(t, env.pool.QueryRow(ctx, `
			INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
		`, "whale-net/work-axis-other-scope-"+uuid.NewString()).Scan(&otherScopeID))

		_, err := assembler.Assemble(ctx, otherScopeID, taskA.ID)
		assert.ErrorIs(t, err, store.ErrNotFound, "NFR1: a task must never resolve under a scope it does not belong to")

		notes, err := tasks.ListNotesForTask(ctx, otherScopeID, taskC.ID)
		require.NoError(t, err)
		assert.Empty(t, notes, "NFR1: a note must be invisible when read under a different scope")
	})

	t.Run("NFR3_TwoSubjectAttributionOnEveryRow", func(t *testing.T) {
		for _, table := range workAxisM4Tables {
			var missing int
			require.NoError(t, env.pool.QueryRow(ctx, `
				SELECT COUNT(*) FROM `+table+`
				WHERE scope_id = $1 AND (
					created_by_acting_iss = '' OR created_by_acting_sub = '' OR created_by_acting_kind = '' OR
					created_by_on_behalf_of_iss = '' OR created_by_on_behalf_of_sub = '' OR created_by_on_behalf_of_kind = ''
				)
			`, env.scopeID).Scan(&missing))
			assert.Zero(t, missing, "NFR3: every row on %s must carry all six populated subject columns", table)
		}
	})

	t.Run("NFR6_WriteOnlyGate", func(t *testing.T) {
		gate := handlers.RequireSession(env.sessions)
		mux := http.NewServeMux()
		mux.Handle("POST /tasks", gate(handlers.CreateTaskHandler(tasks)))
		mux.Handle("POST /tasks/{id}/dependencies", gate(handlers.DeclareTaskDependenciesHandler(tasks)))
		mux.Handle("POST /tasks/{id}/claim", gate(handlers.ClaimTaskHandler(tasks, assembler)))
		mux.Handle("POST /tasks/{id}/heartbeat", gate(handlers.HeartbeatHandler(tasks)))
		mux.Handle("POST /tasks/{id}/complete", gate(handlers.CompleteTaskHandler(tasks, assembler)))
		mux.Handle("POST /tasks/{id}/abandon", gate(handlers.AbandonTaskHandler(tasks, assembler)))
		mux.Handle("POST /notes", gate(handlers.RecordNoteHandler(tasks)))
		mux.HandleFunc("GET /tasks/{id}", handlers.GetTaskPayloadHandler(tasks, assembler))
		mux.HandleFunc("GET /tasks/{id}/notes", handlers.ListTaskNotesHandler(tasks))

		// The seven mutating calls NFR3 names: each rejected with no
		// X-Krill-Session-Id header, before the store is ever reached.
		writeRequests := []string{
			"POST /tasks",
			"POST /tasks/" + taskA.ID.String() + "/dependencies",
			"POST /tasks/" + taskA.ID.String() + "/claim",
			"POST /tasks/" + taskA.ID.String() + "/heartbeat",
			"POST /tasks/" + taskA.ID.String() + "/complete",
			"POST /tasks/" + taskA.ID.String() + "/abandon",
			"POST /notes",
		}
		for _, spec := range writeRequests {
			parts := strings.SplitN(spec, " ", 2)
			req := httptest.NewRequest(parts[0], parts[1], strings.NewReader("{}"))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "NFR6: %s with no X-Krill-Session-Id header must be rejected", spec)
		}

		// GET /tasks/{id} and GET /tasks/{id}/notes: the gate is
		// write-only -- a call with no session must succeed.
		req := httptest.NewRequest(http.MethodGet, "/tasks/"+taskA.ID.String(), nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, "NFR6: GET /tasks/{id} needs no session -- the gate is write-only")

		req = httptest.NewRequest(http.MethodGet, "/tasks/"+taskC.ID.String()+"/notes", nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code, "NFR6: GET /tasks/{id}/notes needs no session -- the gate is write-only")
	})

	t.Run("NFR7_DeliveryReferenceNeverPointsAtSpecChain", func(t *testing.T) {
		_, err := tasks.CreateTask(ctx, store.CreateTaskParams{
			ScopeID: env.scopeID, MilestoneID: feature.ID, Title: "must be rejected",
			LaneSequence: []store.Lane{store.LaneScaffold}, StartingLane: store.LaneScaffold,
			Acting: operator, OnBehalfOf: operatorHuman,
		})
		assert.ErrorIs(t, err, store.ErrNotFound, "NFR7: a task's delivery reference must never be a Feature id -- a Feature names no milestone_ref row at all")
	})

	t.Run("NFR5_NoGitRefDerivesTaskState", func(t *testing.T) {
		forbidden := []string{`exec.command("git"`, "refs/heads", "github_ref", "branchname", ".git/head"}

		for _, rlocation := range workAxisSourceFiles {
			path, err := runfiles.Rlocation(rlocation)
			require.NoError(t, err, "is the work_axis_srcs filegroup still a data dep of this test target? (%s)", rlocation)
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			lower := strings.ToLower(string(content))
			for _, pat := range forbidden {
				assert.NotContains(t, lower, pat, "NFR5: %s must never derive task state from a git branch/ref/PR (found %q)", rlocation, pat)
			}
		}

		migration, err := schema.Migrations.ReadFile("migrations/015_work_axis.up.sql")
		require.NoError(t, err)
		lowerMigration := strings.ToLower(string(migration))
		for _, pat := range forbidden {
			assert.NotContains(t, lowerMigration, pat, "NFR5: migration 015 must never derive task state from a git branch/ref/PR (found %q)", pat)
		}
	})
}

// countM4Rows returns every workAxisM4Tables member's current row count in
// scopeID, keyed by table name -- NFR2's "only ever grows" assertion
// compares two snapshots of this map around one isolated write.
func countM4Rows(ctx context.Context, t *testing.T, pool *pgxpool.Pool, scopeID uuid.UUID) map[string]int {
	t.Helper()
	counts := make(map[string]int, len(workAxisM4Tables))
	for _, table := range workAxisM4Tables {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+` WHERE scope_id = $1`, scopeID).Scan(&n))
		counts[table] = n
	}
	return counts
}
