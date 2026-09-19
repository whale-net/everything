//go:build integration

// Real-Postgres coverage for TaskStore.RecordNote/ListNotesForTask/
// ListNotesForEntity (task_note.go, migration 015, issue #2727's Testing
// section, FR11/FR12): a task-targeted note and an entity-targeted note
// each persist against exactly the target named, both/neither-target and
// unknown-kind rejection at the Go layer (backstopped by task_note's own
// CHECK constraints at the DB layer), the scope-note kind round trip
// (FR11), a non-claimant and an unclaimed task both recording a note
// successfully (FR11's "any Agent, claimant or not"), FR12's "no
// update/delete path" structural guarantee, NFR1's cross-scope isolation,
// and the note round trip actually appearing in a real assembled GET
// /tasks/{id} payload (work.Assembler, FR4). Self-contained (its own
// store/scope/subject/world helpers) rather than sharing
// task_integration_test.go's, mirroring pointer_integration_test.go's own
// choice to duplicate a small fixture instead of widening this go_test
// target's srcs list. See store_integration_test.go's package doc for why
// this file only builds under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_note_integration_test --test_output=all
package store_test

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
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

func newTaskNoteTestStore(t *testing.T) (*store.Store, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return store.New(db.Pool), db
}

func newTaskNoteTestScope(t *testing.T, ctx context.Context, db *dbtest.Postgres) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO scope (repo_full_name, default_branch) VALUES ($1, 'main') RETURNING id
	`, "scope-task-note-"+uuid.NewString()).Scan(&id))
	return id
}

func taskNoteTestSubject(sub string) store.Subject {
	return store.Subject{Iss: "https://issuer.example.com", Sub: sub, Kind: store.SubjectKindService}
}

// seedTaskNoteWorld seeds a Product -> FeatureSet -> Feature -> Requirement
// chain, an uncut milestone, and one task scoped to that milestone
// directly (FR1's "milestone with no milepebble cut" allowed shape) --
// the minimum fixture every test below needs: a task to note against, and
// a spec-axis entity (the Requirement) to note against instead.
func seedTaskNoteWorld(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID, self store.Subject) (taskID, requirementID uuid.UUID) {
	t.Helper()

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	requirement, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "FR1", nil)
	require.NoError(t, err)

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship it", nil, self, self)
	require.NoError(t, err)

	task, err := s.Tasks().CreateTask(ctx, store.CreateTaskParams{
		ScopeID:      scopeID,
		MilestoneID:  milestone.ID,
		Title:        "do the thing",
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation, store.LaneTesting, store.LaneValidation, store.LaneDone},
		StartingLane: store.LaneScaffold,
		Acting:       self,
		OnBehalfOf:   self,
	})
	require.NoError(t, err)

	return task.ID, requirement.ID
}

// assertNoTaskNoteRows fails the test unless task_note carries zero rows
// for scopeID -- used after a rejected RecordNote call to prove the
// rejection happened before any write, not just that RecordNote returned
// an error.
func assertNoTaskNoteRows(t *testing.T, ctx context.Context, db *dbtest.Postgres, scopeID uuid.UUID) {
	t.Helper()
	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM task_note WHERE scope_id = $1`, scopeID).Scan(&count))
	assert.Equal(t, 0, count, "a rejected RecordNote call must insert no row")
}

// rawInsertTaskNote bypasses store.TaskStore.RecordNote entirely, issuing
// a direct INSERT against task_note -- used to prove migration 015's own
// CHECK constraints reject an invalid row even when nothing routes
// through the Go-layer validation RecordNote performs ahead of its own
// INSERT.
func rawInsertTaskNote(t *testing.T, ctx context.Context, db *dbtest.Postgres, scopeID uuid.UUID, taskID, entityID *uuid.UUID, entityKind *string, kind string) error {
	t.Helper()
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO task_note (
			scope_id, task_id, entity_kind, entity_id, kind, body,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, 'raw body',
			'https://issuer.example.com', 'agent-1', 'service',
			'https://issuer.example.com', 'agent-1', 'service')
	`, scopeID, taskID, entityKind, entityID, kind)
	return err
}

// TestTaskNoteStore_RecordNote_AgainstTask_Persisted_AndAppearsInPayload
// is issue #2727's Testing section item 1: a task-targeted note persists
// with its kind, body, and both subject pairs, and appears in a real
// assembled GET /tasks/{id} payload (FR4) via work.Assembler -- the exact
// call GetTaskPayloadHandler wraps.
func TestTaskNoteStore_RecordNote_AgainstTask_Persisted_AndAppearsInPayload(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	acting := taskNoteTestSubject("agent-1")
	onBehalfOf := taskNoteTestSubject("human-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, acting)

	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKindComment, Body: "left a comment",
		Acting: acting, OnBehalfOf: onBehalfOf,
	})
	require.NoError(t, err)
	require.NotNil(t, note.TaskID)
	assert.Equal(t, taskID, *note.TaskID)
	assert.Nil(t, note.EntityKind)
	assert.Nil(t, note.EntityID)
	assert.Equal(t, store.NoteKindComment, note.Kind)
	assert.Equal(t, "left a comment", note.Body)
	assert.Equal(t, acting, note.CreatedByActing, "NFR3: the acting subject must be recorded")
	assert.Equal(t, onBehalfOf, note.CreatedByOnBehalfOf, "NFR3: the on-behalf-of subject must be recorded")

	notes, err := s.Tasks().ListNotesForTask(ctx, scopeID, taskID)
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.Equal(t, note.ID, notes[0].ID)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	payload, err := assembler.Assemble(ctx, scopeID, taskID)
	require.NoError(t, err)
	require.Len(t, payload.Task.Notes, 1, "FR4: a recorded note must appear in GET /tasks/{id}'s payload")
	assert.Equal(t, note.ID, payload.Task.Notes[0].ID)
	assert.Equal(t, "comment", payload.Task.Notes[0].Kind)
	assert.Equal(t, "left a comment", payload.Task.Notes[0].Body)
}

// TestTaskNoteStore_RecordNote_AgainstEntity_Persisted_NotAgainstTask is
// issue #2727's Testing section item 2: a note targeting a spec-axis
// entity (a Requirement) persists against that entity, is returned by
// ListNotesForEntity, and never appears against the task via
// ListNotesForTask.
func TestTaskNoteStore_RecordNote_AgainstEntity_Persisted_NotAgainstTask(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, requirementID := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	entityKind := store.NoteEntityKindRequirement
	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, EntityKind: &entityKind, EntityID: &requirementID,
		Kind: store.NoteKindComment, Body: "requirement drift", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.Nil(t, note.TaskID)
	require.NotNil(t, note.EntityKind)
	assert.Equal(t, store.NoteEntityKindRequirement, *note.EntityKind)
	require.NotNil(t, note.EntityID)
	assert.Equal(t, requirementID, *note.EntityID)

	entityNotes, err := s.Tasks().ListNotesForEntity(ctx, scopeID, store.NoteEntityKindRequirement, requirementID)
	require.NoError(t, err)
	require.Len(t, entityNotes, 1)
	assert.Equal(t, note.ID, entityNotes[0].ID)

	taskNotes, err := s.Tasks().ListNotesForTask(ctx, scopeID, taskID)
	require.NoError(t, err)
	assert.Empty(t, taskNotes, "an entity-targeted note must never appear against the task")
}

// TestTaskNoteStore_RecordNote_BothTargets_Rejected is issue #2727's
// Testing section item 3's first half (Go layer): a params naming both a
// task and an entity is rejected by ErrInvalidNoteTarget, inserting no
// row.
func TestTaskNoteStore_RecordNote_BothTargets_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, requirementID := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	entityKind := store.NoteEntityKindRequirement
	_, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID, EntityKind: &entityKind, EntityID: &requirementID,
		Kind: store.NoteKindComment, Body: "x", Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrInvalidNoteTarget)
	assertNoTaskNoteRows(t, ctx, db, scopeID)
}

// TestTaskNoteStore_RecordNote_NeitherTarget_Rejected is issue #2727's
// Testing section item 3's second half (Go layer): a params naming
// neither a task nor an entity is rejected by ErrInvalidNoteTarget,
// inserting no row.
func TestTaskNoteStore_RecordNote_NeitherTarget_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	seedTaskNoteWorld(t, ctx, s, scopeID, self)

	_, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, Kind: store.NoteKindComment, Body: "x", Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrInvalidNoteTarget)
	assertNoTaskNoteRows(t, ctx, db, scopeID)
}

// TestTaskNoteStore_RecordNote_CrossScopeTaskID_Rejected proves NFR1's
// cross-scope guard also applies to RecordNote's own target-existence
// check, mirroring task_dependency_integration_test.go's own cross-scope
// coverage of the same TaskStore-widening shape.
func TestTaskNoteStore_RecordNote_CrossScopeTaskID_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeA := newTaskNoteTestScope(t, ctx, db)
	scopeB := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskInScopeB, _ := seedTaskNoteWorld(t, ctx, s, scopeB, self)

	_, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeA, TaskID: &taskInScopeB,
		Kind: store.NoteKindComment, Body: "x", Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNotFound, "a task id belonging to another scope must be rejected as not found in the caller's scope")
}

// TestTaskNoteStore_RecordNote_UnknownKind_Rejected is issue #2727's
// Testing section item 4's first half (Go layer): a Kind outside
// NoteKind's fixed enumeration is rejected by ErrUnknownNoteKind,
// inserting no row.
func TestTaskNoteStore_RecordNote_UnknownKind_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	_, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKind("bogus-kind"), Body: "x", Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrUnknownNoteKind)
	assertNoTaskNoteRows(t, ctx, db, scopeID)
}

// TestTaskNoteStore_DirectInsert_UnknownKind_RejectedByCheck is issue
// #2727's Testing section item 4's second half (DB layer): a direct
// INSERT of an unknown kind, bypassing RecordNote's own Go-layer
// validation entirely, is still rejected by migration 015's CHECK
// constraint.
func TestTaskNoteStore_DirectInsert_UnknownKind_RejectedByCheck(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	err := rawInsertTaskNote(t, ctx, db, scopeID, &taskID, nil, nil, "bogus-kind")
	require.Error(t, err, "a direct DB insert of an unknown note kind must be rejected by the CHECK constraint")
}

// TestTaskNoteStore_DirectInsert_BothOrNeitherTarget_RejectedByCheck
// backstops TestTaskNoteStore_RecordNote_BothTargets_Rejected/
// NeitherTarget_Rejected at the DB layer: a direct INSERT naming both a
// task and an entity, or neither, is rejected by migration 015's own
// target CHECK even with no Go-layer validation in the way.
func TestTaskNoteStore_DirectInsert_BothOrNeitherTarget_RejectedByCheck(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, requirementID := seedTaskNoteWorld(t, ctx, s, scopeID, self)
	entityKind := string(store.NoteEntityKindRequirement)

	t.Run("both", func(t *testing.T) {
		err := rawInsertTaskNote(t, ctx, db, scopeID, &taskID, &requirementID, &entityKind, "comment")
		require.Error(t, err)
	})
	t.Run("neither", func(t *testing.T) {
		err := rawInsertTaskNote(t, ctx, db, scopeID, nil, nil, nil, "comment")
		require.Error(t, err)
	})
}

// TestTaskNoteStore_RecordNote_ScopeNoteKind_RoundTrips is issue #2727's
// Testing section item 5 (FR11): the named `scope-note` kind round-trips
// through RecordNote/ListNotesForTask unchanged.
func TestTaskNoteStore_RecordNote_ScopeNoteKind_RoundTrips(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKindScopeNote, Body: "noticed unrelated scope", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.Equal(t, store.NoteKindScopeNote, note.Kind)

	notes, err := s.Tasks().ListNotesForTask(ctx, scopeID, taskID)
	require.NoError(t, err)
	require.Len(t, notes, 1)
	assert.Equal(t, store.NoteKindScopeNote, notes[0].Kind)
}

// TestTaskNoteStore_RecordNote_NonClaimant_Succeeds is issue #2727's
// Testing section item 6 (FR11): an Agent that does not hold the task's
// current claim can still record a note successfully.
func TestTaskNoteStore_RecordNote_NonClaimant_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	claimant := taskNoteTestSubject("agent-claimant")
	noteRecorder := taskNoteTestSubject("agent-other")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, claimant)

	sessions := store.NewSessionStore(db.Pool)
	sessionID, err := sessions.InitSession(ctx, scopeID, claimant, claimant, nil)
	require.NoError(t, err)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: taskID, SessionID: sessionID, Acting: claimant, OnBehalfOf: claimant,
	})
	require.NoError(t, err, "fixture sanity: the claim itself must succeed")

	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKindComment, Body: "not the claimant but noting anyway",
		Acting: noteRecorder, OnBehalfOf: noteRecorder,
	})
	require.NoError(t, err, "FR11: any Agent, not only the task's current claimant, may record a note")
	assert.Equal(t, noteRecorder, note.CreatedByActing)
}

// TestTaskNoteStore_RecordNote_UnclaimedTask_Succeeds is issue #2727's
// Testing section item 7: a note recorded while the task is unclaimed
// succeeds.
func TestTaskNoteStore_RecordNote_UnclaimedTask_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	task, err := s.Tasks().GetTaskByID(ctx, taskID)
	require.NoError(t, err)
	require.Nil(t, task.CurrentClaimID, "fixture sanity: the task must start unclaimed")

	_, err = s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKindComment, Body: "noting an unclaimed task",
		Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err, "FR11: a note recorded while the task is unclaimed must succeed")
}

// TestTaskNoteStore_NoUpdateOrDeletePath is issue #2727's Testing section
// item 8 (FR12): no exported store.TaskStore method mutates a task_note
// row -- exactly RecordNote (the one append) plus the two scope-qualified
// reads exist -- and task_note itself carries no status/lifecycle column.
func TestTaskNoteStore_NoUpdateOrDeletePath(t *testing.T) {
	ifaceType := reflect.TypeOf((*store.TaskStore)(nil)).Elem()
	var noteMethods []string
	for i := 0; i < ifaceType.NumMethod(); i++ {
		name := ifaceType.Method(i).Name
		if strings.Contains(name, "Note") {
			noteMethods = append(noteMethods, name)
		}
	}
	assert.ElementsMatch(t, []string{"RecordNote", "ListNotesForTask", "ListNotesForEntity"}, noteMethods,
		"FR12: task_note must have no update/delete method -- only the one append and its two scope-qualified reads")

	ctx := context.Background()
	_, db := newTaskNoteTestStore(t)
	rows, err := db.Pool.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_name = 'task_note'`)
	require.NoError(t, err)
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var col string
		require.NoError(t, rows.Scan(&col))
		columns = append(columns, col)
	}
	require.NoError(t, rows.Err())
	assert.NotContains(t, columns, "status", "FR12: task_note must never gain a status/lifecycle column")
	assert.NotContains(t, columns, "state", "FR12: task_note must never gain a status/lifecycle column")
}

// TestTaskNoteStore_ListNotesForTask_CrossScopeIsolation is issue #2727's
// Testing section item 9 (NFR1): a note recorded in one scope never
// appears when its owning task id is read back under a different scope
// id.
func TestTaskNoteStore_ListNotesForTask_CrossScopeIsolation(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeA := newTaskNoteTestScope(t, ctx, db)
	scopeB := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskA, _ := seedTaskNoteWorld(t, ctx, s, scopeA, self)

	_, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeA, TaskID: &taskA,
		Kind: store.NoteKindComment, Body: "scope A's own note", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	notesInOwnScope, err := s.Tasks().ListNotesForTask(ctx, scopeA, taskA)
	require.NoError(t, err)
	assert.Len(t, notesInOwnScope, 1)

	notesFromWrongScope, err := s.Tasks().ListNotesForTask(ctx, scopeB, taskA)
	require.NoError(t, err)
	assert.Empty(t, notesFromWrongScope, "NFR1: a task's notes must never appear when read under another scope's id")
}
