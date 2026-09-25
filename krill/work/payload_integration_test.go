//go:build integration

// Real-Postgres coverage for Assembler.Assemble (issue #2721's Testing
// section). Follows task_integration_test.go's harness (newTaskTestStore-
// equivalent below) and query_integration_test.go's seeded-world pattern --
// see those files' own doc comments for the dbtest/migration/seeding
// pattern this reuses rather than duplicates.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/work:payload_integration_test --test_output=all
package work_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/migrate/schema"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newPayloadTestStore provisions an isolated, migrated Postgres database
// (krill's own real embedded schema) and returns a ready *store.Store plus
// the *pgxpool.Pool it is built over -- mirrors
// krill/slice/query_integration_test.go's newTestStore.
func newPayloadTestStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	pool, err := pgxpool.New(ctx, db.ConnString)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return store.New(pool), pool
}

func payloadTestScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var scopeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "whale-net/work-payload-test-"+uuid.NewString()).Scan(&scopeID))
	return scopeID
}

func payloadTestSubject(sub string) store.Subject {
	return store.Subject{Iss: "https://issuer.example.com", Sub: sub, Kind: store.SubjectKindService}
}

func payloadTestStrPtr(s string) *string { return &s }

// payloadWorld is a seeded product + delivery-axis graph: a Product with
// one Feature and one LoadBearingDecision, a milestone that delivers both,
// and a milepebble cut from that milestone that also delivers both (FR3's
// subset invariant) -- the shape a task payload's embedded spec slice
// needs real content to assert against.
type payloadWorld struct {
	ScopeID uuid.UUID

	MilestoneID  uuid.UUID
	MilepebbleID uuid.UUID

	Feature  store.Feature
	Decision store.LoadBearingDecision
}

func seedPayloadWorld(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID, self store.Subject) payloadWorld {
	t.Helper()

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	decision, err := s.Decisions().Create(ctx, scopeID, featureSet.ID, "LB-1", payloadTestStrPtr("a load-bearing decision"))
	require.NoError(t, err)

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship it", nil, self, self)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, feature.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, decision.ID, self, self))

	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "MP1", "a slice of M1", nil, self, self)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, milepebble.ID, feature.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, milepebble.ID, decision.ID, self, self))

	return payloadWorld{
		ScopeID:      scopeID,
		MilestoneID:  milestone.ID,
		MilepebbleID: milepebble.ID,
		Feature:      feature,
		Decision:     decision,
	}
}

func createPayloadTestTask(t *testing.T, ctx context.Context, s *store.Store, scopeID, milestoneID uuid.UUID, self store.Subject) store.Task {
	t.Helper()
	task, err := s.Tasks().CreateTask(ctx, store.CreateTaskParams{
		ScopeID:      scopeID,
		MilestoneID:  milestoneID,
		Title:        "do the thing",
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation, store.LaneTesting, store.LaneValidation, store.LaneDone},
		StartingLane: store.LaneScaffold,
		Acting:       self,
		OnBehalfOf:   self,
	})
	require.NoError(t, err)
	return task
}

// TestAssemble_TaskOnMilepebble_CarriesSpecSliceLaneDependenciesAttempt is
// issue #2721's Testing section item 1: a task scoped to a milepebble
// carries the milepebble's own delivered spec content (Feature and
// LoadBearingDecision), its current lane, its lane sequence, its declared
// dependency list, and its attempt number.
func TestAssemble_TaskOnMilepebble_CarriesSpecSliceLaneDependenciesAttempt(t *testing.T) {
	ctx := context.Background()
	s, pool := newPayloadTestStore(t)
	scopeID := payloadTestScope(t, ctx, pool)
	self := payloadTestSubject("agent-1")
	w := seedPayloadWorld(t, ctx, s, scopeID, self)

	dependsOn := createPayloadTestTask(t, ctx, s, scopeID, w.MilepebbleID, self)
	task := createPayloadTestTask(t, ctx, s, scopeID, w.MilepebbleID, self)
	require.NoError(t, s.Tasks().DeclareDependency(ctx, store.DeclareDependencyParams{
		ScopeID:          scopeID,
		TaskID:           task.ID,
		DependsOnTaskIDs: []uuid.UUID{dependsOn.ID},
		Acting:           self,
		OnBehalfOf:       self,
	}))

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	payload, err := assembler.Assemble(ctx, scopeID, task.ID)
	require.NoError(t, err)

	// The embedded slice carries the milepebble's own delivered content --
	// never leaves it empty, never substitutes the whole product's content.
	require.Len(t, payload.Slice.Features, 1)
	assert.Equal(t, w.Feature.ID, payload.Slice.Features[0].ID)
	require.Len(t, payload.Slice.Decisions, 1)
	assert.Equal(t, w.Decision.ID, payload.Slice.Decisions[0].ID)

	assert.Equal(t, task.ID, payload.Task.ID)
	assert.Equal(t, w.MilepebbleID, payload.Task.MilestoneID)
	assert.Equal(t, string(store.LaneScaffold), payload.Task.CurrentLane)
	assert.Equal(t, []string{"Scaffold", "Implementation", "Testing", "Validation", "Done"}, payload.Task.LaneSequence)
	assert.Equal(t, 0, payload.Task.AttemptNumber)

	require.Len(t, payload.Task.Dependencies, 1)
	assert.Equal(t, dependsOn.ID, payload.Task.Dependencies[0].DependsOnTaskID)
}

// TestAssemble_SliceMatchesQuerierDirectly is NFR4's regression test
// (issue #2721's Testing section item 2): the payload's embedded slice --
// schema_version and every entity's revision_id/id -- must be byte-equal
// to what slice.Querier.GetMilestoneDeliversSlice returns directly for the
// same milestone. This is what would go red the moment krill/work started
// re-deriving the slice itself instead of enriching M1's real output
// (verified during development by temporarily hand-building a Document
// inside Assemble instead of calling the querier: this assertion failed on
// the mismatched RevisionIDs, then passed again once Assemble called
// GetMilestoneDeliversSlice for real).
func TestAssemble_SliceMatchesQuerierDirectly(t *testing.T) {
	ctx := context.Background()
	s, pool := newPayloadTestStore(t)
	scopeID := payloadTestScope(t, ctx, pool)
	self := payloadTestSubject("agent-1")
	w := seedPayloadWorld(t, ctx, s, scopeID, self)
	task := createPayloadTestTask(t, ctx, s, scopeID, w.MilepebbleID, self)

	querier := slice.NewQuerier(s)
	assembler := work.NewAssembler(s.Tasks(), querier)

	payload, err := assembler.Assemble(ctx, scopeID, task.ID)
	require.NoError(t, err)

	directDoc, err := querier.GetMilestoneDeliversSlice(ctx, w.MilepebbleID)
	require.NoError(t, err)

	require.NotEmpty(t, directDoc.SchemaVersion, "the fixture itself must produce a non-empty schema_version, or this test would pass vacuously")
	assert.Equal(t, directDoc.SchemaVersion, payload.Slice.SchemaVersion)

	// Structural equality of the same slice.Document type is byte-equal
	// for this field: encode both independently and compare the bytes
	// directly, rather than relying on require.Equal's own reflection.
	directJSON, err := json.Marshal(directDoc)
	require.NoError(t, err)
	payloadSliceJSON, err := json.Marshal(payload.Slice)
	require.NoError(t, err)
	assert.Equal(t, string(directJSON), string(payloadSliceJSON), "the payload's embedded slice must be byte-equal to slice.Querier's own direct output")

	require.Len(t, payload.Slice.Features, 1)
	require.Len(t, directDoc.Features, 1)
	assert.Equal(t, directDoc.Features[0].ID, payload.Slice.Features[0].ID)
	assert.Equal(t, directDoc.Features[0].RevisionID, payload.Slice.Features[0].RevisionID)

	require.Len(t, payload.Slice.Decisions, 1)
	require.Len(t, directDoc.Decisions, 1)
	assert.Equal(t, directDoc.Decisions[0].ID, payload.Slice.Decisions[0].ID)
	assert.Equal(t, directDoc.Decisions[0].RevisionID, payload.Slice.Decisions[0].RevisionID)
}

// TestAssemble_NoDependencies_EmptyNotNullList is issue #2721's Testing
// section item 3: a task with no declared dependencies must marshal its
// dependency list as `[]`, never `null` -- a caller must never have to
// special-case a nil slice vs. an empty one.
func TestAssemble_NoDependencies_EmptyNotNullList(t *testing.T) {
	ctx := context.Background()
	s, pool := newPayloadTestStore(t)
	scopeID := payloadTestScope(t, ctx, pool)
	self := payloadTestSubject("agent-1")
	w := seedPayloadWorld(t, ctx, s, scopeID, self)
	task := createPayloadTestTask(t, ctx, s, scopeID, w.MilepebbleID, self)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	payload, err := assembler.Assemble(ctx, scopeID, task.ID)
	require.NoError(t, err)

	require.NotNil(t, payload.Task.Dependencies, "a task with no declared dependencies must still carry a non-nil (empty) Dependencies slice")
	assert.Empty(t, payload.Task.Dependencies)

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"dependencies":[]`, "an empty dependency list must marshal as [], never null")
}

// TestAssemble_AttemptCapEscalatedTask_SurfacesCurrentEscalationID is issue
// #2871's own Validation criterion: after a reclaim brings a task's
// attempt_count to the cap (FR3), GET /tasks/{id} -- this package's one
// payload document -- shows the task escalated, without any second call.
// An unescalated task's payload carries no current_escalation_id at all
// (omitempty), proving this field is purely additive.
func TestAssemble_AttemptCapEscalatedTask_SurfacesCurrentEscalationID(t *testing.T) {
	ctx := context.Background()
	s, pool := newPayloadTestStore(t)
	scopeID := payloadTestScope(t, ctx, pool)
	self := payloadTestSubject("agent-1")
	w := seedPayloadWorld(t, ctx, s, scopeID, self)
	task := createPayloadTestTask(t, ctx, s, scopeID, w.MilepebbleID, self)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))

	unescalated, err := assembler.Assemble(ctx, scopeID, task.ID)
	require.NoError(t, err)
	assert.Nil(t, unescalated.Task.CurrentEscalationID, "an ordinary task's payload must carry no current_escalation_id")
	unescalatedJSON, err := json.Marshal(unescalated)
	require.NoError(t, err)
	assert.NotContains(t, string(unescalatedJSON), "current_escalation_id", "omitempty must drop the field entirely when the task is not escalated")

	sessions := store.NewSessionStore(pool)
	sessionID, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: sessionID, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE task SET attempt_count = $1, lease_expires_at = NOW() - INTERVAL '1 minute' WHERE id = $2`,
		store.DefaultAttemptCap-1, task.ID)
	require.NoError(t, err)
	reclaimResult, err := s.Tasks().ReclaimExpired(ctx, store.ReclaimParams{ScopeID: scopeID, Acting: self, OnBehalfOf: self})
	require.NoError(t, err)
	require.Len(t, reclaimResult.Reclaimed, 1)
	require.True(t, reclaimResult.Reclaimed[0].CapExhausted)

	escalated, err := assembler.Assemble(ctx, scopeID, task.ID)
	require.NoError(t, err)
	require.NotNil(t, escalated.Task.CurrentEscalationID, "GET /tasks/{id} must show the task escalated, with no second call needed")
	assert.Equal(t, *reclaimResult.Reclaimed[0].EscalationID, *escalated.Task.CurrentEscalationID)
}

// TestAssemble_NoteStatus_AppearsInPayload is issue #2874's Testing section
// item: a note's current lifecycle status (M5's C26, FR11) actually
// appears in the assembled GET /tasks/{id} payload -- 'noted' immediately
// after RecordNote, and the transitioned-to status after
// TransitionNoteLifecycle, with the body left untouched. Exercises the
// real store.TaskStore.ListNotesForTask/TransitionNoteLifecycle path, not
// just the fake store's stub methods payload_test.go's fakeTaskStore needs
// for interface compilation.
func TestAssemble_NoteStatus_AppearsInPayload(t *testing.T) {
	ctx := context.Background()
	s, pool := newPayloadTestStore(t)
	scopeID := payloadTestScope(t, ctx, pool)
	self := payloadTestSubject("agent-1")
	w := seedPayloadWorld(t, ctx, s, scopeID, self)
	task := createPayloadTestTask(t, ctx, s, scopeID, w.MilepebbleID, self)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))

	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &task.ID,
		Kind: store.NoteKindComment, Body: "worth flagging", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	fresh, err := assembler.Assemble(ctx, scopeID, task.ID)
	require.NoError(t, err)
	require.Len(t, fresh.Task.Notes, 1)
	assert.Equal(t, "noted", fresh.Task.Notes[0].Status, "a freshly recorded note's status must default to 'noted' in the payload")
	assert.Equal(t, "worth flagging", fresh.Task.Notes[0].Body)

	_, err = s.Tasks().TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
		ScopeID: scopeID, NoteID: note.ID, Status: store.NoteLifecycleStatusDeferred, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	transitioned, err := assembler.Assemble(ctx, scopeID, task.ID)
	require.NoError(t, err)
	require.Len(t, transitioned.Task.Notes, 1)
	assert.Equal(t, "deferred", transitioned.Task.Notes[0].Status, "GET /tasks/{id} must reflect the transitioned status, with no second call needed")
	assert.Equal(t, "worth flagging", transitioned.Task.Notes[0].Body, "the note's body must stay unchanged by the status transition")
}

// TestAssemble_RequeueRoundTrip_ClaimPayloadCarriesEveryNoteAtCurrentStatus
// is issue #2876's Testing section round-trip item: escalate -> requeue ->
// claim -> the claim payload (this package's own Assemble, M4 FR4) must
// carry every note recorded against the task, including one an operator
// recorded during the escalation investigation, each at whatever
// lifecycle status FR11 (#2874) has it at -- requeue neither strips nor
// filters that history; an operator's investigation reaches the next run
// through this payload, not a separate hand-off mechanic.
func TestAssemble_RequeueRoundTrip_ClaimPayloadCarriesEveryNoteAtCurrentStatus(t *testing.T) {
	ctx := context.Background()
	s, pool := newPayloadTestStore(t)
	scopeID := payloadTestScope(t, ctx, pool)
	agent := payloadTestSubject("agent-1")
	operator := payloadTestSubject("operator-1")
	w := seedPayloadWorld(t, ctx, s, scopeID, agent)
	task := createPayloadTestTask(t, ctx, s, scopeID, w.MilepebbleID, agent)

	// A note recorded before the escalation, by the Agent doing the work.
	earlyNote, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &task.ID,
		Kind: store.NoteKindComment, Body: "started on this", Acting: agent, OnBehalfOf: agent,
	})
	require.NoError(t, err)

	// The operator escalates the task manually, then records a note during
	// the investigation, then requeues it -- the investigation note must
	// still reach the next claimant's payload after requeue.
	_, err = s.Tasks().EscalateTask(ctx, store.EscalateParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: operator, OnBehalfOf: operator,
	})
	require.NoError(t, err)

	investigationNote, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &task.ID,
		Kind: store.NoteKindScopeNote, Body: "root cause: flaky upstream dependency", Acting: operator, OnBehalfOf: operator,
	})
	require.NoError(t, err)
	_, err = s.Tasks().TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
		ScopeID: scopeID, NoteID: investigationNote.ID, Status: store.NoteLifecycleStatusCarriedOver, Acting: operator, OnBehalfOf: operator,
	})
	require.NoError(t, err)

	requeueResult, err := s.Tasks().RequeueTask(ctx, store.RequeueParams{
		ScopeID: scopeID, TaskID: task.ID, Acting: operator, OnBehalfOf: operator,
	})
	require.NoError(t, err)
	assert.Equal(t, store.ResetCounterNone, requeueResult.CounterReset, "an unclaimed, uncapped manual escalation resets no counter")

	sessions := store.NewSessionStore(pool)
	nextSessionID, err := sessions.InitSession(ctx, scopeID, agent, agent, nil)
	require.NoError(t, err)
	claim, err := s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: task.ID, SessionID: nextSessionID, Acting: agent, OnBehalfOf: agent,
	})
	require.NoError(t, err, "the requeued task must be immediately claimable again")

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	payload, err := assembler.Assemble(ctx, scopeID, task.ID)
	require.NoError(t, err)

	require.NotNil(t, payload.Task.CurrentClaim, "the claim payload must show the fresh claim")
	assert.Equal(t, claim.ID, payload.Task.CurrentClaim.ClaimID)
	assert.Nil(t, payload.Task.CurrentEscalationID, "a requeued, re-claimed task's payload must show no active escalation")
	assert.Equal(t, string(requeueResult.ResultingLane), payload.Task.CurrentLane, "the claim must be at the lane the task was requeued at")

	require.Len(t, payload.Task.Notes, 2, "the claim payload must carry every note recorded against the task, including the operator's investigation note")
	byID := map[uuid.UUID]work.NoteView{}
	for _, n := range payload.Task.Notes {
		byID[n.ID] = n
	}
	require.Contains(t, byID, earlyNote.ID)
	assert.Equal(t, "started on this", byID[earlyNote.ID].Body)
	assert.Equal(t, "noted", byID[earlyNote.ID].Status, "the early note's status must be unaffected by the escalation/requeue round trip")

	require.Contains(t, byID, investigationNote.ID)
	assert.Equal(t, "root cause: flaky upstream dependency", byID[investigationNote.ID].Body)
	assert.Equal(t, "carried-over", byID[investigationNote.ID].Status, "the operator's investigation note must reach the next claimant at its current lifecycle status, neither stripped nor filtered by requeue")
}

// TestAssemble_PayloadValidatesAgainstMCPOutputSchema is a regression test
// for the get_task/claim_task/complete_task/abandon_task/cancel_task/
// release_task/requeue_task/escalate_task output-schema bug
// (krill/mcp/tools/task_payload.go's workPayloadOutputSchema): left to
// mcp.AddTool's default reflection, jsonschema-go infers JSON schema type
// "array" for every embedded uuid.UUID leaf (its underlying Go kind is
// [16]byte), while encoding/json actually marshals a uuid.UUID as a string
// via MarshalText -- so every one of those MCP tools failed its own
// advertised output schema on any populated result (surfaced first via
// get_task, since it needs no write persona to reach). This mirrors
// task_payload.go's own schema construction (TypeSchemas overriding
// uuid.UUID to {Type: "string"}) against a REAL assembled Payload --
// Feature/Decision/Task ids included -- rather than a synthetic fixture,
// so it fails the same way the real tool call failed before the fix. The
// second half is a negative control: without the override, the same
// Payload must still fail validation, proving this test would have caught
// the original bug rather than exercising a schema that happens to always
// pass.
func TestAssemble_PayloadValidatesAgainstMCPOutputSchema(t *testing.T) {
	ctx := context.Background()
	s, pool := newPayloadTestStore(t)
	scopeID := payloadTestScope(t, ctx, pool)
	self := payloadTestSubject("agent-1")
	w := seedPayloadWorld(t, ctx, s, scopeID, self)
	task := createPayloadTestTask(t, ctx, s, scopeID, w.MilepebbleID, self)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	payload, err := assembler.Assemble(ctx, scopeID, task.ID)
	require.NoError(t, err)

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	var unmarshaled any
	require.NoError(t, json.Unmarshal(encoded, &unmarshaled))

	fixedSchema, err := jsonschema.For[work.Payload](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[uuid.UUID](): {Type: "string"},
		},
	})
	require.NoError(t, err)
	fixedResolved, err := fixedSchema.Resolve(nil)
	require.NoError(t, err)
	assert.NoError(t, fixedResolved.Validate(&unmarshaled), "a real assembled Payload must validate against the same output schema task_payload.go advertises")

	defaultSchema, err := jsonschema.For[work.Payload](nil)
	require.NoError(t, err)
	defaultResolved, err := defaultSchema.Resolve(nil)
	require.NoError(t, err)
	assert.Error(t, defaultResolved.Validate(&unmarshaled), "jsonschema-go's default reflection must still mis-infer uuid.UUID as \"array\" -- if this stops failing, the TypeSchemas override may no longer be necessary and this test's own premise should be revisited")
}
