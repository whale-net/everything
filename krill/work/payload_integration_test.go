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
	"testing"

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

	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "MP1", "a slice of M1", self, self)
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
