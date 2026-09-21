//go:build integration

// Real-Postgres coverage for TaskStore.ListOpenNotes (task_note_console.go,
// migration 016, issue #2874's Testing section, FR12): only notes at
// current_status = 'noted' appear, a note transitioned away from 'noted'
// (to each of the other three statuses) disappears from the page, each row
// carries body/kind plus the identifying context of whichever one target
// it names -- both a task-scoped note and a spec-entity-scoped note,
// covering more than one entity_kind -- paging default/max/clamp,
// deterministic order, a continuation walk with no gaps or duplicates, a
// cross-scope token rejected, and another scope's notes absent. Shares
// task_note_integration_test.go's test-store/test-scope/test-world/subject
// helpers rather than duplicating them, mirroring
// task_console_integration_test.go's own paging-test shape for the sibling
// FR10 console query. See store_integration_test.go's package doc for why
// this file only builds under the "integration" build tag.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:task_note_console_integration_test --test_output=all
package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// TestTaskNoteConsoleStore_ListOpenNotes_OnlyNotedAppear is issue #2874's
// Testing section item 6's first half (FR12): a note left at 'noted'
// appears, and a note moved to each of the other three statuses
// disappears from the page.
func TestTaskNoteConsoleStore_ListOpenNotes_OnlyNotedAppear(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	stillOpen, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKindScopeNote, Body: "still open", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	for _, status := range []store.NoteLifecycleStatus{
		store.NoteLifecycleStatusCarriedOver,
		store.NoteLifecycleStatusDeferred,
		store.NoteLifecycleStatusClosed,
	} {
		moved, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
			ScopeID: scopeID, TaskID: &taskID,
			Kind: store.NoteKindScopeNote, Body: "moved to " + string(status), Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
		_, err = s.Tasks().TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
			ScopeID: scopeID, NoteID: moved.ID, Status: status, Acting: self, OnBehalfOf: self,
		})
		require.NoError(t, err)
	}

	page, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 1, "only the note still at 'noted' may appear -- the three moved-away notes must all be absent")
	assert.Equal(t, stillOpen.ID, page.Items[0].NoteID)

	// Explicitly confirm the note transitioned to 'noted' via a no-op
	// re-affirmation still counts as open -- the filter is on
	// current_status's value, never on "has never been transitioned".
	_, err = s.Tasks().TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
		ScopeID: scopeID, NoteID: stillOpen.ID, Status: store.NoteLifecycleStatusNoted, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	page, err = s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 1, "a no-op re-affirmation to 'noted' must not remove the note from the open view")
}

// TestTaskNoteConsoleStore_ListOpenNotes_RowContent_BothTargetShapes is
// issue #2874's Testing section item 6's second half (FR12): rows carry
// body + kind + identifying context for both a task-scoped note and a
// spec-entity-scoped note, covering more than one entity_kind (Requirement
// and Product here).
func TestTaskNoteConsoleStore_ListOpenNotes_RowContent_BothTargetShapes(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, requirementID := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	product, err := s.Products().Create(ctx, scopeID, "A Second Product", "another product entity to note against")
	require.NoError(t, err)

	taskNote, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKindComment, Body: "task-scoped body", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	requirementKind := store.NoteEntityKindRequirement
	requirementNote, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, EntityKind: &requirementKind, EntityID: &requirementID,
		Kind: store.NoteKindScopeNote, Body: "requirement-scoped body", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	productKind := store.NoteEntityKindProduct
	productNote, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, EntityKind: &productKind, EntityID: &product.ID,
		Kind: store.NoteKindScopeNote, Body: "product-scoped body", Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)

	page, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, 3)

	byID := map[uuid.UUID]store.OpenNoteRow{}
	for _, row := range page.Items {
		byID[row.NoteID] = row
	}

	taskRow := byID[taskNote.ID]
	assert.Equal(t, store.NoteKindComment, taskRow.Kind)
	assert.Equal(t, "task-scoped body", taskRow.Body)
	require.NotNil(t, taskRow.TaskContext, "a task-targeted note must carry TaskContext")
	assert.Nil(t, taskRow.EntityContext, "a task-targeted note must never also carry EntityContext")
	assert.Equal(t, taskID, taskRow.TaskContext.TaskID)
	assert.Equal(t, "do the thing", taskRow.TaskContext.Title)
	assert.NotEmpty(t, taskRow.TaskContext.DeliveryRef.Title)

	requirementRow := byID[requirementNote.ID]
	assert.Equal(t, store.NoteKindScopeNote, requirementRow.Kind)
	assert.Equal(t, "requirement-scoped body", requirementRow.Body)
	require.NotNil(t, requirementRow.EntityContext, "an entity-targeted note must carry EntityContext")
	assert.Nil(t, requirementRow.TaskContext, "an entity-targeted note must never also carry TaskContext")
	assert.Equal(t, store.NoteEntityKindRequirement, requirementRow.EntityContext.EntityKind)
	assert.Equal(t, requirementID, requirementRow.EntityContext.EntityID)
	assert.Equal(t, "FR1", requirementRow.EntityContext.Title)

	productRow := byID[productNote.ID]
	assert.Equal(t, store.NoteKindScopeNote, productRow.Kind)
	assert.Equal(t, "product-scoped body", productRow.Body)
	require.NotNil(t, productRow.EntityContext)
	assert.Equal(t, store.NoteEntityKindProduct, productRow.EntityContext.EntityKind)
	assert.Equal(t, product.ID, productRow.EntityContext.EntityID)
	assert.Equal(t, "A Second Product", productRow.EntityContext.Title)
}

// listOpenNotesTestTask creates a fresh task in scopeID and records one
// open note against it -- the minimum fixture the paging tests below need,
// repeated many times.
func recordOpenTaskNote(t *testing.T, ctx context.Context, s *store.Store, scopeID, taskID uuid.UUID, body string, self store.Subject) store.Note {
	t.Helper()
	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, TaskID: &taskID,
		Kind: store.NoteKindComment, Body: body, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	return note
}

// TestTaskNoteConsoleStore_ListOpenNotes_PageSizeDefaultMaxClamp mirrors
// TestTaskStore_ListCancelledTasks_PageSizeDefaultMaxClamp for
// ListOpenNotes: an absent page size applies DefaultConsolePageSize, and a
// page size above MaxConsolePageSize is clamped down rather than rejected.
func TestTaskNoteConsoleStore_ListOpenNotes_PageSizeDefaultMaxClamp(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	const total = store.DefaultConsolePageSize + 5
	for i := 0; i < total; i++ {
		recordOpenTaskNote(t, ctx, s, scopeID, taskID, fmt.Sprintf("note %d", i), self)
	}

	page, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID})
	require.NoError(t, err)
	assert.Len(t, page.Items, store.DefaultConsolePageSize, "an absent page size must apply DefaultConsolePageSize")
	assert.NotEmpty(t, page.NextToken, "more rows remain beyond the default page")

	clamped, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID: scopeID,
		Page:    store.PageParams{PageSize: store.MaxConsolePageSize + 1000},
	})
	require.NoError(t, err)
	assert.Len(t, clamped.Items, total, "a page size above MaxConsolePageSize must clamp down, not reject, and every row here fits within the clamp")
}

// TestTaskNoteConsoleStore_ListOpenNotes_ContinuationNoGapsOrDuplicates
// mirrors TestTaskStore_ListCancelledTasks_ContinuationNoGapsOrDuplicates
// for ListOpenNotes: walking every page via NextToken visits every open
// note exactly once, in the query's own deterministic order
// (oldest-flagged first, id as tiebreak).
func TestTaskNoteConsoleStore_ListOpenNotes_ContinuationNoGapsOrDuplicates(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskID, _ := seedTaskNoteWorld(t, ctx, s, scopeID, self)

	const total = 23
	const pageSize = 5
	want := make(map[uuid.UUID]bool, total)
	for i := 0; i < total; i++ {
		note := recordOpenTaskNote(t, ctx, s, scopeID, taskID, fmt.Sprintf("note %d", i), self)
		want[note.ID] = true
	}

	seen := map[uuid.UUID]bool{}
	var order []uuid.UUID
	token := ""
	pages := 0
	for {
		page, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
			ScopeID: scopeID,
			Page:    store.PageParams{PageSize: pageSize, ContinuationToken: token},
		})
		require.NoError(t, err)
		pages++
		require.Less(t, pages, 20, "must terminate well within a sane number of pages")

		for _, row := range page.Items {
			require.False(t, seen[row.NoteID], "note %s must not be seen twice across the continuation walk", row.NoteID)
			seen[row.NoteID] = true
			order = append(order, row.NoteID)
		}

		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}

	assert.Len(t, seen, total, "every open note must be visited exactly once with no gaps")
	for id := range want {
		assert.True(t, seen[id], "note %s must appear somewhere in the continuation walk", id)
	}

	var replay []uuid.UUID
	token = ""
	for {
		page, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
			ScopeID: scopeID,
			Page:    store.PageParams{PageSize: pageSize, ContinuationToken: token},
		})
		require.NoError(t, err)
		for _, row := range page.Items {
			replay = append(replay, row.NoteID)
		}
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}
	assert.Equal(t, order, replay, "the same walk repeated must produce the exact same order")
}

// TestTaskNoteConsoleStore_ListOpenNotes_CrossScopeToken_Rejected mirrors
// TestTaskStore_ListCancelledTasks_CrossScopeToken_Rejected for
// ListOpenNotes: a continuation token issued for one scope is rejected
// when presented against a different scope's query.
func TestTaskNoteConsoleStore_ListOpenNotes_CrossScopeToken_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeA := newTaskNoteTestScope(t, ctx, db)
	scopeB := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskA, _ := seedTaskNoteWorld(t, ctx, s, scopeA, self)
	taskB, _ := seedTaskNoteWorld(t, ctx, s, scopeB, self)

	for i := 0; i < 3; i++ {
		recordOpenTaskNote(t, ctx, s, scopeA, taskA, fmt.Sprintf("a-note %d", i), self)
	}
	recordOpenTaskNote(t, ctx, s, scopeB, taskB, "b-note", self)

	pageA, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID: scopeA,
		Page:    store.PageParams{PageSize: 1},
	})
	require.NoError(t, err)
	require.NotEmpty(t, pageA.NextToken)

	_, err = s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID: scopeB,
		Page:    store.PageParams{PageSize: 1, ContinuationToken: pageA.NextToken},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTokenScopeMismatch)
}

// TestTaskNoteConsoleStore_ListOpenNotes_OtherScopeRowsAbsent mirrors
// TestTaskStore_ListCancelledTasks_OtherScopeRowsAbsent for ListOpenNotes
// (NFR1): a second scope's open note never appears in the first scope's
// page.
func TestTaskNoteConsoleStore_ListOpenNotes_OtherScopeRowsAbsent(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeA := newTaskNoteTestScope(t, ctx, db)
	scopeB := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")
	taskA, _ := seedTaskNoteWorld(t, ctx, s, scopeA, self)
	taskB, _ := seedTaskNoteWorld(t, ctx, s, scopeB, self)

	noteA := recordOpenTaskNote(t, ctx, s, scopeA, taskA, "scope a's own note", self)
	recordOpenTaskNote(t, ctx, s, scopeB, taskB, "scope b's own note", self)

	page, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeA})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, noteA.ID, page.Items[0].NoteID)
}
