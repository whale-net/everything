//go:build integration

// Real-Postgres coverage for TaskStore.TransitionNoteLifecycle
// (task_note_lifecycle.go, migration 016, issue #2874's Testing section,
// FR11): a note created by RecordNote defaults to 'noted' with no
// lifecycle event required, each of the three transitions writes exactly
// one event and mirrors it onto task_note.current_status while leaving
// body/kind/task_id/entity_kind/entity_id byte-for-byte unchanged (NFR2's
// row-level immutability proof), multiple transitions accumulate as an
// append-only, queryable/ordered history, an unknown status is rejected at
// both the Go and DB layers, a non-claimant/non-operator persona can still
// transition (FR11's "any persona" regression guard), a no-op transition
// to the current status is accepted and recorded (task_note_lifecycle.go's
// own doc comment), and an unknown/cross-scope note id is rejected (NFR1).
// Shares task_note_integration_test.go's test-store/test-scope/test-world/
// subject helpers rather than duplicating them, mirroring
// task_cancel_integration_test.go's own reuse of task_integration_test.go's
// helpers for the sibling M5 shape. See store_integration_test.go's
// package doc for why this file only builds under the "integration" build
// tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_note_lifecycle_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// countNoteLifecycleEvents returns the number of task_note_lifecycle_event
// rows for noteID -- used to prove a rejected transition writes nothing and
// an accepted one writes exactly one.
func countNoteLifecycleEvents(t *testing.T, ctx context.Context, db *dbtest.Postgres, noteID uuid.UUID) int {
	t.Helper()
	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM task_note_lifecycle_event WHERE note_id = $1
	`, noteID).Scan(&count))
	return count
}

// noteRow is a comparable snapshot of one task_note row's immutable
// columns -- current_status deliberately excluded, since that is the one
// column TransitionNoteLifecycle is allowed (indeed required) to change.
type noteRow struct {
	Body       string
	Kind       string
	TaskID     *uuid.UUID
	EntityKind *string
	EntityID   *uuid.UUID
}

// fetchNoteRow snapshots noteID's own immutable columns for the
// before/after byte-for-byte comparison
// TestTaskNoteLifecycleStore_TransitionNoteLifecycle_NeverTouchesBodyOrKind
// performs.
func fetchNoteRow(t *testing.T, ctx context.Context, db *dbtest.Postgres, noteID uuid.UUID) noteRow {
	t.Helper()
	var row noteRow
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT body, kind, task_id, entity_kind, entity_id FROM task_note WHERE id = $1
	`, noteID).Scan(&row.Body, &row.Kind, &row.TaskID, &row.EntityKind, &row.EntityID))
	return row
}

// currentNoteStatus reads noteID's task_note.current_status directly.
func currentNoteStatus(t *testing.T, ctx context.Context, db *dbtest.Postgres, noteID uuid.UUID) string {
	t.Helper()
	var status string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT current_status FROM task_note WHERE id = $1
	`, noteID).Scan(&status))
	return status
}

// TestTaskNoteLifecycleStore_RecordNote_DefaultsToNoted_NoEventRequired is
// issue #2874's Testing section item 1: a note created by RecordNote has
// current_status = 'noted' with no lifecycle event required (the DB
// DEFAULT, not a TransitionNoteLifecycle call).
func TestTaskNoteLifecycleStore_RecordNote_DefaultsToNoted_NoEventRequired(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKindComment, Body: "fresh note", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	assert.Equal(t, store.NoteLifecycleStatusNoted, note.CurrentStatus, "a freshly recorded note must default to 'noted'")

	assert.Equal(t, 0, countNoteLifecycleEvents(t, ctx, db, note.ID), "no lifecycle event row is required for the default 'noted' status")
	assert.Equal(t, string(store.NoteLifecycleStatusNoted), currentNoteStatus(t, ctx, db, note.ID))
}

// TestTaskNoteLifecycleStore_TransitionNoteLifecycle_AppendsEventAndMirrorsStatus
// is issue #2874's Testing section item 2: each of the three transitions
// writes exactly one event and updates current_status.
func TestTaskNoteLifecycleStore_TransitionNoteLifecycle_AppendsEventAndMirrorsStatus(t *testing.T) {
	for _, status := range []store.NoteLifecycleStatus{
		store.NoteLifecycleStatusCarriedOver,
		store.NoteLifecycleStatusDeferred,
		store.NoteLifecycleStatusClosed,
	} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			s, db := newTaskNoteTestStore(t)
			scopeID := newTaskNoteTestScope(t, ctx, db)
			acting := taskNoteTestSubject("agent-1")
			onBehalfOf := taskNoteTestSubject("human-1")
			taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, acting)

			note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
				ScopeID: scopeID, TaskID: &taskID,
				Kind: store.NoteKindComment, Body: "transition me", Acting: acting, OnBehalfOf: acting,
			})
			require.NoError(t, err)

			event, err := s.Tasks().TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
				ScopeID: scopeID, NoteID: note.ID, Status: status, Acting: acting, OnBehalfOf: onBehalfOf,
			})
			require.NoError(t, err)
			assert.Equal(t, note.ID, event.NoteID)
			assert.Equal(t, status, event.Status)
			assert.Equal(t, acting, event.CreatedByActing, "NFR3: the acting subject must be recorded")
			assert.Equal(t, onBehalfOf, event.CreatedByOnBehalfOf, "NFR3: the on-behalf-of subject must be recorded")

			assert.Equal(t, 1, countNoteLifecycleEvents(t, ctx, db, note.ID), "exactly one lifecycle event must be appended")
			assert.Equal(t, string(status), currentNoteStatus(t, ctx, db, note.ID), "current_status must mirror the transitioned-to status")
		})
	}
}

// TestTaskNoteLifecycleStore_TransitionNoteLifecycle_NeverTouchesBodyOrKind
// is issue #2874's Testing section item 2's immutability half (FR11): the
// note's body/kind/task_id/entity_kind/entity_id are byte-for-byte
// unchanged after every transition. Cited by name from
// task_note_integration_test.go's TestTaskNoteStore_NoUpdateOrDeletePath
// doc comment as "the row-level proof" -- keep this exact function name.
func TestTaskNoteLifecycleStore_TransitionNoteLifecycle_NeverTouchesBodyOrKind(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	_, requirementID := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	entityKind := store.NoteEntityKindRequirement
	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, EntityKind: &entityKind, EntityID: &requirementID,
		Kind: store.NoteKindScopeNote, Body: "immutable body text", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	before := fetchNoteRow(t, ctx, db, note.ID)
	assert.Equal(t, "immutable body text", before.Body, "fixture sanity")

	for _, status := range []store.NoteLifecycleStatus{
		store.NoteLifecycleStatusCarriedOver,
		store.NoteLifecycleStatusDeferred,
		store.NoteLifecycleStatusClosed,
	} {
		_, err := s.Tasks().TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
			ScopeID: scopeID, NoteID: note.ID, Status: status, Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)

		after := fetchNoteRow(t, ctx, db, note.ID)
		assert.Equal(t, before, after, "body/kind/task_id/entity_kind/entity_id must be byte-for-byte unchanged after transitioning to %q", status)
	}
}

// TestTaskNoteLifecycleStore_TransitionNoteLifecycle_History_AppendOnly_Ordered
// is issue #2874's Testing section item 3 (NFR2): multiple transitions
// accumulate as an append-only history, queryable and ordered, nothing
// overwritten or deleted.
func TestTaskNoteLifecycleStore_TransitionNoteLifecycle_History_AppendOnly_Ordered(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKindComment, Body: "history", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	sequence := []store.NoteLifecycleStatus{
		store.NoteLifecycleStatusCarriedOver,
		store.NoteLifecycleStatusDeferred,
		store.NoteLifecycleStatusCarriedOver,
		store.NoteLifecycleStatusClosed,
	}
	var eventIDs []uuid.UUID
	for _, status := range sequence {
		event, err := s.Tasks().TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
			ScopeID: scopeID, NoteID: note.ID, Status: status, Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
		eventIDs = append(eventIDs, event.ID)
	}

	assert.Equal(t, len(sequence), countNoteLifecycleEvents(t, ctx, db, note.ID), "every transition must append its own row -- nothing collapsed or overwritten")

	rows, err := db.Pool.Query(ctx, `
		SELECT id, status FROM task_note_lifecycle_event WHERE note_id = $1 ORDER BY created_at ASC, id ASC
	`, note.ID)
	require.NoError(t, err)
	defer rows.Close()

	var gotIDs []uuid.UUID
	var gotStatuses []string
	for rows.Next() {
		var id uuid.UUID
		var status string
		require.NoError(t, rows.Scan(&id, &status))
		gotIDs = append(gotIDs, id)
		gotStatuses = append(gotStatuses, status)
	}
	require.NoError(t, rows.Err())

	assert.Equal(t, eventIDs, gotIDs, "the history must be queryable in the exact order the transitions were made")
	assert.Equal(t, []string{"carried-over", "deferred", "carried-over", "closed"}, gotStatuses,
		"a status repeated later in the sequence must appear again as its own row, never collapsed with the earlier one")

	assert.Equal(t, string(store.NoteLifecycleStatusClosed), currentNoteStatus(t, ctx, db, note.ID), "current_status must mirror only the most recent transition")
}

// TestTaskNoteLifecycleStore_TransitionNoteLifecycle_UnknownStatus_RejectedAtGoLayer
// is issue #2874's Testing section item 4's Go-layer half: a Status
// outside NoteLifecycleStatus's fixed enumeration is rejected with
// ErrUnknownNoteLifecycleStatus, inserting no row and leaving
// current_status untouched.
func TestTaskNoteLifecycleStore_TransitionNoteLifecycle_UnknownStatus_RejectedAtGoLayer(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKindComment, Body: "x", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = s.Tasks().TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
		ScopeID: scopeID, NoteID: note.ID, Status: store.NoteLifecycleStatus("bogus-status"), Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrUnknownNoteLifecycleStatus)

	assert.Equal(t, 0, countNoteLifecycleEvents(t, ctx, db, note.ID), "a rejected transition must insert no event row")
	assert.Equal(t, string(store.NoteLifecycleStatusNoted), currentNoteStatus(t, ctx, db, note.ID), "current_status must stay untouched after a rejected transition")
}

// TestTaskNoteLifecycleStore_DirectInsert_UnknownStatus_RejectedByCheck is
// issue #2874's Testing section item 4's DB-layer half: a direct INSERT of
// an unknown status, bypassing TransitionNoteLifecycle's own Go-layer
// validation entirely, is still rejected by migration 016's CHECK
// constraint.
func TestTaskNoteLifecycleStore_DirectInsert_UnknownStatus_RejectedByCheck(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKindComment, Body: "x", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO task_note_lifecycle_event (
			scope_id, note_id, status,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, 'bogus-status',
			'https://issuer.example.com', 'agent-1', 'service',
			'https://issuer.example.com', 'agent-1', 'service')
	`, scopeID, note.ID)
	require.Error(t, err, "a direct DB insert of an unknown lifecycle status must be rejected by the CHECK constraint")
}

// TestTaskNoteLifecycleStore_TransitionNoteLifecycle_AnyPersona_Succeeds is
// issue #2874's Testing section item 5 (FR11's "any persona" rule): a
// subject that neither recorded the note nor holds the parent task's
// current claim can still transition it -- the regression guard against
// accidentally claim- or ownership-gating this verb.
func TestTaskNoteLifecycleStore_TransitionNoteLifecycle_AnyPersona_Succeeds(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	claimant := taskNoteTestSubject("agent-claimant")
	noteAuthor := taskNoteTestSubject("agent-author")
	transitioner := taskNoteTestSubject("agent-bystander")
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
		Kind: store.NoteKindComment, Body: "noted by someone else", Acting: noteAuthor, OnBehalfOf: noteAuthor,
	})
	require.NoError(t, err)

	event, err := s.Tasks().TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
		ScopeID: scopeID, NoteID: note.ID, Status: store.NoteLifecycleStatusDeferred,
		Acting: transitioner, OnBehalfOf: transitioner,
	})
	require.NoError(t, err, "FR11: any persona, not only the note's author or the task's claimant, may transition a note")
	assert.Equal(t, transitioner, event.CreatedByActing)
	assert.Equal(t, string(store.NoteLifecycleStatusDeferred), currentNoteStatus(t, ctx, db, note.ID))
}

// TestTaskNoteLifecycleStore_TransitionNoteLifecycle_NoOp_Accepted proves
// task_note_lifecycle.go's own documented decision: a transition to the
// note's own current status is accepted, not rejected, and recorded as an
// ordinary event.
func TestTaskNoteLifecycleStore_TransitionNoteLifecycle_NoOp_Accepted(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKindComment, Body: "x", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = s.Tasks().TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
		ScopeID: scopeID, NoteID: note.ID, Status: store.NoteLifecycleStatusNoted, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err, "a transition to the current status must be accepted, not rejected")
	assert.Equal(t, 1, countNoteLifecycleEvents(t, ctx, db, note.ID), "a no-op transition must still append its own event row -- never silently dropped")
	assert.Equal(t, string(store.NoteLifecycleStatusNoted), currentNoteStatus(t, ctx, db, note.ID))
}

// TestTaskNoteLifecycleStore_TransitionNoteLifecycle_UnknownNoteID_Rejected
// proves an id naming no task_note row is rejected with store.ErrNotFound,
// writing nothing.
func TestTaskNoteLifecycleStore_TransitionNoteLifecycle_UnknownNoteID_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	unknownNoteID := uuid.New()

	_, err := s.Tasks().TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
		ScopeID: scopeID, NoteID: unknownNoteID, Status: store.NoteLifecycleStatusClosed, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNotFound)
	assert.Equal(t, 0, countNoteLifecycleEvents(t, ctx, db, unknownNoteID))
}

// TestTaskNoteLifecycleStore_TransitionNoteLifecycle_CrossScopeNoteID_Rejected
// proves NFR1's cross-scope guard also applies here: a note id belonging
// to another scope is rejected exactly like an unknown one.
func TestTaskNoteLifecycleStore_TransitionNoteLifecycle_CrossScopeNoteID_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeA := newTaskNoteTestScope(t, ctx, db)
	scopeB := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskInScopeB, _ := seedTaskNoteWorld(t, ctx, s, scopeB, self)

	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeB, TaskID: &taskInScopeB,
		Kind: store.NoteKindComment, Body: "x", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	_, err = s.Tasks().TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
		ScopeID: scopeA, NoteID: note.ID, Status: store.NoteLifecycleStatusClosed, Acting: self, OnBehalfOf: self,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNotFound, "a note id belonging to another scope must be rejected as not found in the caller's scope")
}
