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

// noteFilterWorld is the fixture one product's whole note-targetable graph:
// its own id, the five NoteEntityKind tables beneath it, and the delivery
// container plus one task a task-targeted note can name. Every entity note
// in these tests resolves to a product by a different path, so a filter on
// one product exercises all five at once.
type noteFilterWorld struct {
	productID     uuid.UUID
	featureSetID  uuid.UUID
	featureID     uuid.UUID
	requirementID uuid.UUID
	decisionID    uuid.UUID
	milestoneID   uuid.UUID
	taskID        uuid.UUID
}

func newNoteFilterWorld(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID, name string, self store.Subject) noteFilterWorld {
	t.Helper()

	product, err := s.Products().Create(ctx, scopeID, name, "a product to narrow against")
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS-"+name, nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F-"+name, nil)
	require.NoError(t, err)
	requirement, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "FR-"+name, nil)
	require.NoError(t, err)
	decision, err := s.Decisions().Create(ctx, scopeID, featureSet.ID, "LB-"+name, nil)
	require.NoError(t, err)
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M-"+name, "ship it", nil, self, self)
	require.NoError(t, err)
	task, err := s.Tasks().CreateTask(ctx, store.CreateTaskParams{
		ScopeID:      scopeID,
		MilestoneID:  milestone.ID,
		Title:        "the task under " + name,
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation, store.LaneTesting, store.LaneValidation, store.LaneDone},
		StartingLane: store.LaneScaffold,
		Acting:       self,
		OnBehalfOf:   self,
	})
	require.NoError(t, err)

	return noteFilterWorld{
		productID:     product.ID,
		featureSetID:  featureSet.ID,
		featureID:     feature.ID,
		requirementID: requirement.ID,
		decisionID:    decision.ID,
		milestoneID:   milestone.ID,
		taskID:        task.ID,
	}
}

// recordEntityNote records one open note against a spec entity of the
// given kind.
func recordEntityNote(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID, kind store.NoteEntityKind, entityID uuid.UUID, body string, self store.Subject) store.Note {
	t.Helper()
	note, err := s.Tasks().RecordNote(ctx, store.RecordNoteParams{
		ScopeID: scopeID, EntityKind: &kind, EntityID: &entityID,
		Kind: store.NoteKindScopeNote, Body: body, Acting: self, OnBehalfOf: self,
	})
	require.NoError(t, err)
	return note
}

// TestTaskNoteConsoleStore_ListOpenNotes_ProductFilter_ResolvesEntityProduct
// is FR a6cd917f's product-narrowing half for open notes: a product filter
// keeps a note on a spec entity exactly when that entity belongs to the
// product -- resolved across all five NoteEntityKind tables, each of which
// reaches a product by its own path -- alongside the notes on that
// product's tasks.
func TestTaskNoteConsoleStore_ListOpenNotes_ProductFilter_ResolvesEntityProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")

	a := newNoteFilterWorld(t, ctx, s, scopeID, "A", self)
	b := newNoteFilterWorld(t, ctx, s, scopeID, "B", self)

	// One note on each of the five NoteEntityKind tables plus the one task
	// target, in each of the two products.
	var wantA []store.Note
	for _, world := range []noteFilterWorld{a, b} {
		notes := []store.Note{
			recordEntityNote(t, ctx, s, scopeID, store.NoteEntityKindProduct, world.productID, "product note", self),
			recordEntityNote(t, ctx, s, scopeID, store.NoteEntityKindFeatureSet, world.featureSetID, "feature set note", self),
			recordEntityNote(t, ctx, s, scopeID, store.NoteEntityKindFeature, world.featureID, "feature note", self),
			recordEntityNote(t, ctx, s, scopeID, store.NoteEntityKindRequirement, world.requirementID, "requirement note", self),
			recordEntityNote(t, ctx, s, scopeID, store.NoteEntityKindLoadBearingDecision, world.decisionID, "decision note", self),
			recordOpenTaskNote(t, ctx, s, scopeID, world.taskID, "task note", self),
		}
		if world.productID == a.productID {
			wantA = notes
		}
	}
	require.Len(t, wantA, 6, "each product must contribute one note per NoteEntityKind plus one task note")

	page, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{ProductID: &a.productID},
	})
	require.NoError(t, err)

	seen := map[uuid.UUID]bool{}
	for _, row := range page.Items {
		seen[row.NoteID] = true
	}
	assert.Len(t, seen, len(wantA), "a product filter must keep exactly the notes whose target belongs to that product")
	for _, note := range wantA {
		assert.True(t, seen[note.ID], "note %s belongs to product A and must survive the filter", note.ID)
	}

	unfiltered, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID})
	require.NoError(t, err)
	assert.Len(t, unfiltered.Items, 12, "an un-narrowed read must still return every product's notes, entity-targeted included")
}

// TestTaskNoteConsoleStore_ListOpenNotes_MilestoneFilter_ExcludesEntityNotes
// is FR a6cd917f's container-narrowing half for open notes: a milestone
// filter keeps that container's task notes (its milepebbles' included) and
// excludes every entity-targeted note, which has no delivery container at
// all.
func TestTaskNoteConsoleStore_ListOpenNotes_MilestoneFilter_ExcludesEntityNotes(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")

	world := newNoteFilterWorld(t, ctx, s, scopeID, "A", self)
	elsewhere := newNoteFilterWorld(t, ctx, s, scopeID, "B", self)

	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, world.milestoneID, "MP1", "a slice", nil, self, self)
	require.NoError(t, err)
	milepebbleTask, err := s.Tasks().CreateTask(ctx, store.CreateTaskParams{
		ScopeID:      scopeID,
		MilestoneID:  milepebble.ID,
		Title:        "under the milepebble",
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation, store.LaneTesting, store.LaneValidation, store.LaneDone},
		StartingLane: store.LaneScaffold,
		Acting:       self,
		OnBehalfOf:   self,
	})
	require.NoError(t, err)

	onTask := recordOpenTaskNote(t, ctx, s, scopeID, world.taskID, "on the milestone's own task", self)
	onMilepebble := recordOpenTaskNote(t, ctx, s, scopeID, milepebbleTask.ID, "on the milepebble's task", self)
	recordEntityNote(t, ctx, s, scopeID, store.NoteEntityKindRequirement, world.requirementID, "on a spec entity", self)
	recordOpenTaskNote(t, ctx, s, scopeID, elsewhere.taskID, "on another container's task", self)

	page, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{MilestoneID: &world.milestoneID},
	})
	require.NoError(t, err)

	seen := map[uuid.UUID]bool{}
	for _, row := range page.Items {
		seen[row.NoteID] = true
	}
	assert.Len(t, seen, 2, "a milestone filter must keep the container's own task notes and its milepebbles', and nothing else")
	assert.True(t, seen[onTask.ID])
	assert.True(t, seen[onMilepebble.ID])

	byMilepebble, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{MilestoneID: &milepebble.ID},
	})
	require.NoError(t, err)
	require.Len(t, byMilepebble.Items, 1, "a milepebble filter must keep only that milepebble's task notes")
	assert.Equal(t, onMilepebble.ID, byMilepebble.Items[0].NoteID)
}

// TestTaskNoteConsoleStore_ListOpenNotes_FilterSurvivesContinuation is FR
// a6cd917f's narrowing meeting FR 5713b7da's paging on the open-notes read:
// a product-narrowed walk stays narrowed on every page, and the token that
// walk carries binds the filter so it cannot be resumed as a different one.
//
// The two halves are separate on purpose. The walk catches a read whose
// narrowing applied only to the first page -- the single-page
// product-filter test above would pass that, since it never asks for a
// second one. The token half catches the mirror: a read that issues a
// scope-only token while filtering, which every page of that walk accepts
// while narrowing nothing.
func TestTaskNoteConsoleStore_ListOpenNotes_FilterSurvivesContinuation(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")

	worldA := newNoteFilterWorld(t, ctx, s, scopeID, "A", self)
	worldB := newNoteFilterWorld(t, ctx, s, scopeID, "B", self)

	// Enough notes inside the filter to cross a page boundary, and enough
	// outside it that a dropped predicate serves product B's notes rather
	// than merely duplicating A's.
	want := map[uuid.UUID]bool{}
	for i := 0; i < 5; i++ {
		want[recordOpenTaskNote(t, ctx, s, scopeID, worldA.taskID, fmt.Sprintf("a %d", i), self).ID] = true
	}
	for i := 0; i < 3; i++ {
		recordOpenTaskNote(t, ctx, s, scopeID, worldB.taskID, fmt.Sprintf("b %d", i), self)
	}

	// Each product's own note is a second, differently-targeted row: the
	// walk below must exclude it along with product B's, since a product
	// filter keeps an entity note exactly when that entity is in the
	// product.
	productNoteB := recordEntityNote(t, ctx, s, scopeID, store.NoteEntityKindProduct, worldB.productID, "a note on B's product", self)

	narrow := store.ConsoleFilter{ProductID: &worldA.productID}
	const pageSize = 2

	seen, pages := map[uuid.UUID]bool{}, 0
	token := ""
	for {
		page, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
			ScopeID: scopeID, ConsoleFilter: narrow,
			Page: store.PageParams{PageSize: pageSize, ContinuationToken: token},
		})
		require.NoError(t, err)
		pages++
		require.Less(t, pages, 20, "the walk must terminate well within a sane number of pages")
		for _, row := range page.Items {
			require.False(t, seen[row.NoteID], "note %s seen twice across the filtered walk", row.NoteID)
			seen[row.NoteID] = true
		}
		if page.NextToken == "" {
			break
		}
		token = page.NextToken
	}

	assert.Greater(t, pages, 1, "the fixture must cross a page boundary, or this proves nothing about continuation under a filter")
	assert.Equal(t, want, seen, "the filtered walk must visit exactly product A's notes -- no gaps, and no product B note leaking onto a later page")
	assert.NotContains(t, seen, productNoteB.ID, "a note on another product's entity must never appear in this product's filtered walk")

	// The token that walk carried must be bound to the product it was
	// issued under: resuming it unfiltered (or under another product)
	// would silently serve notes the caller never narrowed to.
	_, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID: scopeID,
		Page:    store.PageParams{PageSize: 1, ContinuationToken: token},
	})
	assert.ErrorIs(t, err, store.ErrTokenFilterMismatch, "a product-filtered token is never a valid unfiltered resume")

	_, err = s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{ProductID: &worldB.productID},
		Page:          store.PageParams{PageSize: 1, ContinuationToken: token},
	})
	assert.ErrorIs(t, err, store.ErrTokenFilterMismatch, "a token issued under one product's filter is refused under another's")

	_, err = s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID:       scopeID,
		ConsoleFilter: narrow,
		Page:          store.PageParams{PageSize: 1, ContinuationToken: token},
	})
	assert.NoError(t, err, "the same filter set must resume normally")
}

// ============================================================================
// CountOpenNotes (FR c4ab6c68)
// ============================================================================

// walkOpenNotes drains every page of ListOpenNotes for params and returns
// how many rows the unpaged list holds -- the figure CountOpenNotes has to
// report for the same params.
func walkOpenNotes(t *testing.T, ctx context.Context, s *store.Store, params store.ListOpenNotesParams) int {
	t.Helper()
	total, token := 0, ""
	for {
		params.Page = store.PageParams{PageSize: 2, ContinuationToken: token}
		page, err := s.Tasks().ListOpenNotes(ctx, params)
		require.NoError(t, err)
		total += len(page.Items)
		if page.NextToken == "" {
			return total
		}
		token = page.NextToken
	}
}

// TestTaskNoteConsoleStore_CountOpenNotes_MatchesList_AcrossFilters is FR
// c4ab6c68's agreement criterion for the open-notes queue, across the two
// target shapes the queue holds: a note on a task and a note on a spec
// entity, filtered by product, by milestone, by both and by neither.
//
// The fixture is deliberately larger than a page, since the failure this
// guards against is a count that answers with the page length.
func TestTaskNoteConsoleStore_CountOpenNotes_MatchesList_AcrossFilters(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")

	a := newNoteFilterWorld(t, ctx, s, scopeID, "A", self)
	b := newNoteFilterWorld(t, ctx, s, scopeID, "B", self)

	// Five task notes in A and one on each of two of A's spec entities, so
	// the unfiltered read is eight rows against a two-row page; three task
	// notes and an entity note in B, so a dropped predicate serves B's
	// rows rather than merely duplicating A's.
	for i := 0; i < 5; i++ {
		recordOpenTaskNote(t, ctx, s, scopeID, a.taskID, fmt.Sprintf("a task note %d", i), self)
	}
	recordEntityNote(t, ctx, s, scopeID, store.NoteEntityKindProduct, a.productID, "a product note", self)
	recordEntityNote(t, ctx, s, scopeID, store.NoteEntityKindRequirement, a.requirementID, "a requirement note", self)
	for i := 0; i < 3; i++ {
		recordOpenTaskNote(t, ctx, s, scopeID, b.taskID, fmt.Sprintf("b task note %d", i), self)
	}
	recordEntityNote(t, ctx, s, scopeID, store.NoteEntityKindFeature, b.featureID, "a feature note", self)

	for name, filter := range map[string]store.ConsoleFilter{
		"unfiltered":     {},
		"product_a":      {ProductID: &a.productID},
		"product_b":      {ProductID: &b.productID},
		"milestone_a":    {MilestoneID: &a.milestoneID},
		"product_a_pair": {ProductID: &a.productID, MilestoneID: &a.milestoneID},
	} {
		t.Run(name, func(t *testing.T) {
			params := store.ListOpenNotesParams{ScopeID: scopeID, ConsoleFilter: filter}
			count, err := s.Tasks().CountOpenNotes(ctx, params)
			require.NoError(t, err)
			assert.Equal(t, walkOpenNotes(t, ctx, s, params), count,
				"the count and the list it describes must agree for the same filters")
		})
	}

	// And the unfiltered figure is a total, not a page: eleven rows
	// (7 in A, 4 in B) against a two-row page.
	count, err := s.Tasks().CountOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID})
	require.NoError(t, err)
	assert.Equal(t, 11, count)
	page, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID: scopeID, Page: store.PageParams{PageSize: 2},
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.NotEqual(t, len(page.Items), count)
}

// TestTaskNoteConsoleStore_CountOpenNotes_PerProductFiguresDoNotSum is the
// caveat CountOpenNotes' own doc comment states, pinned by a test: an
// open note on a spec entity is attributed to a product only while that
// entity is current. Void the entity and the note keeps its scope-wide
// row but belongs to no product, so the two per-product counts sum to less
// than the scope-wide one -- a UI must show the product's own figure and
// never derive the scope-wide one from it.
func TestTaskNoteConsoleStore_CountOpenNotes_PerProductFiguresDoNotSum(t *testing.T) {
	ctx := context.Background()
	s, db := newTaskNoteTestStore(t)
	scopeID := newTaskNoteTestScope(t, ctx, db)
	self := taskNoteTestSubject("agent-1")

	a := newNoteFilterWorld(t, ctx, s, scopeID, "A", self)
	b := newNoteFilterWorld(t, ctx, s, scopeID, "B", self)

	recordOpenTaskNote(t, ctx, s, scopeID, a.taskID, "in A", self)
	onBRequirement := recordEntityNote(t, ctx, s, scopeID, store.NoteEntityKindRequirement, b.requirementID, "on B's requirement", self)
	recordOpenTaskNote(t, ctx, s, scopeID, b.taskID, "in B", self)

	countA, err := s.Tasks().CountOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID: scopeID, ConsoleFilter: store.ConsoleFilter{ProductID: &a.productID},
	})
	require.NoError(t, err)
	countB, err := s.Tasks().CountOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID: scopeID, ConsoleFilter: store.ConsoleFilter{ProductID: &b.productID},
	})
	require.NoError(t, err)
	scopeWide, err := s.Tasks().CountOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Equal(t, scopeWide, countA+countB, "while every noted entity is current, the per-product counts do sum")

	// Void B's requirement: the note is still open and still in the scope,
	// but it now resolves to no product at all.
	require.NoError(t, s.Void().VoidRequirement(ctx, scopeID, b.requirementID, nil, self, self))

	countA, err = s.Tasks().CountOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID: scopeID, ConsoleFilter: store.ConsoleFilter{ProductID: &a.productID},
	})
	require.NoError(t, err)
	countB, err = s.Tasks().CountOpenNotes(ctx, store.ListOpenNotesParams{
		ScopeID: scopeID, ConsoleFilter: store.ConsoleFilter{ProductID: &b.productID},
	})
	require.NoError(t, err)
	scopeWide, err = s.Tasks().CountOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID})
	require.NoError(t, err)

	assert.Equal(t, 3, scopeWide, "the open note survives its entity's void in the scope-wide count")
	assert.Equal(t, 1, countA)
	assert.Equal(t, 1, countB, "a note on a since-voided entity belongs to no product")
	assert.Less(t, countA+countB, scopeWide,
		"per-product open-notes counts are not additive -- which is why the UI shows the current product's figure and never sums siblings for a scope-wide one")

	// The list agrees with the count on the same rows, so the caveat is a
	// property of the shared predicate rather than of one of the two
	// reads.
	page, err := s.Tasks().ListOpenNotes(ctx, store.ListOpenNotesParams{ScopeID: scopeID})
	require.NoError(t, err)
	require.Len(t, page.Items, scopeWide)
	var found bool
	for _, row := range page.Items {
		if row.NoteID == onBRequirement.ID {
			found = true
		}
	}
	assert.True(t, found, "the note on the voided entity is still an open note, listed scope-wide")
}
