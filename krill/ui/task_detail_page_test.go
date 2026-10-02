package main

// Handler-level coverage for the read-only task detail page
// (task_detail_page.go), driven through the real handleTaskDetail against
// in-memory fakes and asserted via data-krill hooks.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

type fakeDetailStore struct {
	store.TaskStore
	tasks    map[uuid.UUID]store.Task
	getErr   error
	deps     []store.TaskDependency
	depsErr  error
	notes    []store.Note
	notesErr error
	claim    store.Claim
}

// CountEscalatedTasks is the chrome's Needs-attention badge read, which
// every page this store's routes render carries -- including the 404, which
// renders inside the same chrome.
func (f *fakeDetailStore) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}

// SummarizeProductTaskProgress is the read the Overview's in-flight panel
// makes. It answers with no containers so the panel renders its empty
// state: these fixtures' subject is the task detail, not the panel's rows.
func (fakeDetailStore) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	return store.ProductTaskProgress{ProductID: params.ProductID, Containers: []store.ContainerTaskProgress{}}, nil
}

func (f *fakeDetailStore) GetTaskByID(_ context.Context, id uuid.UUID) (store.Task, error) {
	if f.getErr != nil {
		return store.Task{}, f.getErr
	}
	t, ok := f.tasks[id]
	if !ok {
		return store.Task{}, store.ErrNotFound
	}
	return t, nil
}

func (f *fakeDetailStore) ListDependencies(context.Context, uuid.UUID, uuid.UUID) ([]store.TaskDependency, error) {
	return f.deps, f.depsErr
}

func (f *fakeDetailStore) ListNotesForTask(context.Context, uuid.UUID, uuid.UUID) ([]store.Note, error) {
	return f.notes, f.notesErr
}

func (f *fakeDetailStore) GetClaimByID(context.Context, uuid.UUID) (store.Claim, error) {
	return f.claim, nil
}

type fakeSliceSpec struct {
	*fakeSpecReader
	doc slice.Document
	err error
}

func (f *fakeSliceSpec) MilestoneDeliversSlice(context.Context, uuid.UUID) (slice.Document, error) {
	return f.doc, f.err
}

type detailFixture struct {
	pid, mid uuid.UUID
	store    *fakeDetailStore
	spec     *fakeSliceSpec
	mux      *http.ServeMux
}

func newDetailFixture(t *testing.T) *detailFixture {
	t.Helper()
	f := &detailFixture{pid: uuid.New(), mid: uuid.New()}
	f.store = &fakeDetailStore{tasks: map[uuid.UUID]store.Task{}}
	f.spec = &fakeSliceSpec{fakeSpecReader: &fakeSpecReader{listing: slice.DeliveryListing{
		Milestones: []slice.MilestoneListingEntry{{ID: f.mid, Name: "Plain"}},
	}}}
	// scopes is what the chrome reads for its Needs-attention badge on
	// every page it renders.
	app := &App{spec: f.spec, tasks: f.store, scopes: chromeScopes{}}
	f.mux = http.NewServeMux()
	f.mux.HandleFunc("GET /spec/products/{id}/milestones/{mid}/tasks/{tid}", app.handleTaskDetail)
	return f
}

func (f *detailFixture) add(t store.Task) store.Task {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	if t.MilestoneID == uuid.Nil {
		t.MilestoneID = f.mid
	}
	f.store.tasks[t.ID] = t
	return t
}

func (f *detailFixture) get(tid string, hx bool) (int, string) {
	req := httptest.NewRequest(http.MethodGet, "/spec/products/"+f.pid.String()+"/milestones/"+f.mid.String()+"/tasks/"+tid, nil)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestTaskDetailFieldsReachPage(t *testing.T) {
	f := newDetailFixture(t)
	claim := uuid.New()
	lease := time.Now().Add(10 * time.Minute).UTC()
	body := "the-task-body"
	dep := f.add(store.Task{Title: "dep-task", CurrentLane: store.Lane("Done")})
	task := f.add(store.Task{
		Title: "main-task", Body: &body, CurrentLane: store.Lane("Testing"),
		LaneSequence: []store.Lane{"Scaffold", "Testing", "Done"}, AttemptCount: 2,
		CurrentClaimID: &claim, LeaseExpiresAt: &lease,
	})
	f.store.deps = []store.TaskDependency{{DependsOnTaskID: dep.ID}}
	f.store.notes = []store.Note{{Kind: store.NoteKindScopeNote, Body: "note-body-x", CurrentStatus: "noted"}}
	sessID := store.SessionID(uuid.New())
	f.store.claim = store.Claim{ID: claim, SessionID: sessID, ClaimedAt: time.Now().UTC()}

	code, html := f.get(task.ID.String(), true)
	require.Equal(t, 200, code)
	for _, want := range []string{
		"main-task", "the-task-body", "Testing", "Scaffold", "2 of 3", "current attempt: 2",
		claim.String(), sessID.String(), lease.Format(time.RFC3339),
		"dep-task", "/tasks/" + dep.ID.String(),
		"scope-note", "noted", "note-body-x",
		`data-krill-claim-id="` + claim.String() + `"`,
		`data-krill-lease-expires-at="` + lease.Format(time.RFC3339) + `"`,
		`data-krill="loaded-at"`,
		`data-krill="task-badge-live"`,
		milestoneTasksPath(f.pid, f.mid), milestoneBoardPath(f.pid, f.mid),
	} {
		assert.Contains(t, html, want)
	}
	assert.NotContains(t, html, `data-krill="task-badge-lease-expired"`)
}

func TestTaskDetailStuckStates(t *testing.T) {
	f := newDetailFixture(t)
	claim := uuid.New()
	past := time.Now().Add(-time.Hour).UTC()
	task := f.add(store.Task{Title: "stale", CurrentClaimID: &claim, LeaseExpiresAt: &past})
	_, html := f.get(task.ID.String(), true)
	assert.Contains(t, html, `data-krill="task-badge-lease-expired"`)
	assert.NotContains(t, html, `data-krill="task-badge-live"`)
	assert.Contains(t, html, "lease expired, not live")

	esc := uuid.New()
	capped := f.add(store.Task{Title: "c", AttemptCount: store.DefaultAttemptCap, CurrentEscalationID: &esc, CancelledAt: &past})
	_, html = f.get(capped.ID.String(), true)
	for _, k := range []string{"capped", "escalated", "cancelled"} {
		assert.Contains(t, html, `data-krill="task-badge-`+k+`"`)
	}
}

func TestTaskDetailNotFound(t *testing.T) {
	f := newDetailFixture(t)
	code, html := f.get(uuid.NewString(), false)
	assert.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, html, "<html")

	code, _ = f.get("not-a-uuid", false)
	assert.Equal(t, http.StatusNotFound, code)

	other := f.add(store.Task{Title: "elsewhere", MilestoneID: uuid.New()})
	code, html = f.get(other.ID.String(), false)
	assert.Equal(t, http.StatusNotFound, code)
	assert.NotContains(t, html, "elsewhere")

	f.store.getErr = errors.New("boom")
	code, html = f.get(uuid.NewString(), false)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.NotContains(t, html, "boom")
}

func TestTaskDetailWrongProductMilestone(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "t"})
	req := httptest.NewRequest(http.MethodGet, "/spec/products/"+f.pid.String()+"/milestones/"+uuid.NewString()+"/tasks/"+task.ID.String(), nil)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTaskDetailPartialReadFailuresAlert(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "t"})
	f.store.depsErr = errors.New("deps-boom")
	f.store.notesErr = errors.New("notes-boom")
	f.spec.err = errors.New("slice-boom")
	code, html := f.get(task.ID.String(), true)
	require.Equal(t, 200, code)
	assert.Contains(t, html, "The dependencies could not be read")
	assert.Contains(t, html, "The notes could not be read")
	assert.Contains(t, html, "The spec slice could not be read")
	assert.NotContains(t, html, "No dependencies")
	assert.NotContains(t, html, "No notes")
	assert.NotContains(t, html, "boom")
}

func TestTaskDetailEmptySections(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "t"})
	_, html := f.get(task.ID.String(), true)
	assert.Contains(t, html, "No dependencies")
	assert.Contains(t, html, "No notes")
	assert.NotContains(t, html, "could not be read")
}

func TestTaskDetailFragmentShapeAndReadOnly(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "t"})
	_, frag := f.get(task.ID.String(), true)
	_, full := f.get(task.ID.String(), false)

	assert.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<section id="`))
	assert.NotContains(t, frag, "<html")
	assert.Contains(t, full, "<html")
	assert.Contains(t, frag, `data-krill="refresh"`)
	assert.Contains(t, frag, `hx-get="`+taskDetailPath(f.pid, f.mid, task.ID)+`"`)
	for _, body := range []string{frag, full} {
		assert.NotContains(t, body, "<form")
		assert.NotContains(t, body, "hx-post")
		assert.NotContains(t, body, "hx-put")
		assert.NotContains(t, body, "hx-delete")
		assert.NotContains(t, body, `hx-trigger="every`)
	}
}
