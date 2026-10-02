package main

// Handler-level coverage for the read-only task list (task_page.go),
// driven through the real handleTaskList against in-memory fakes and
// asserted via data-krill hooks.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

type fakeTaskLister struct {
	store.TaskStore
	tasks map[uuid.UUID][]store.TaskSummary
	err   error
	calls []uuid.UUID
}

// CountEscalatedTasks is the chrome's Needs-attention badge read, which
// every page this fake's routes render carries. Zero renders no badge,
// which keeps these tests about the task views.
func (f *fakeTaskLister) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}

// SummarizeProductTaskProgress is the read the Overview's in-flight panel
// makes. It answers with no containers so the panel renders its empty
// state: these fixtures' subject is the task list, not the panel's rows.
func (fakeTaskLister) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	return store.ProductTaskProgress{ProductID: params.ProductID, Containers: []store.ContainerTaskProgress{}}, nil
}

func (f *fakeTaskLister) ListTasksByMilestone(_ context.Context, id uuid.UUID) ([]store.TaskSummary, error) {
	f.calls = append(f.calls, id)
	if f.err != nil {
		return nil, f.err
	}
	return f.tasks[id], nil
}

type taskFixture struct {
	pid, mid, mpID, cutID uuid.UUID
	tasks                 *fakeTaskLister
	mux                   *http.ServeMux
	app                   *App
}

func newTaskFixture(t *testing.T) *taskFixture {
	t.Helper()
	f := &taskFixture{pid: uuid.New(), mid: uuid.New(), mpID: uuid.New(), cutID: uuid.New()}
	f.tasks = &fakeTaskLister{tasks: map[uuid.UUID][]store.TaskSummary{}}
	listing := slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{
		{ID: f.mid, Name: "Plain"},
		{ID: f.cutID, Name: "Cut", Milepebbles: []slice.MilepebbleListingEntry{{ID: f.mpID, Name: "Pebble"}}},
	}}
	// scopes and tasks are what the chrome reads for its Needs-attention
	// badge on every page it renders.
	app := &App{spec: &fakeSpecReader{listing: listing}, tasks: f.tasks, scopes: chromeScopes{}}
	f.app = app
	f.mux = http.NewServeMux()
	f.mux.HandleFunc("GET /spec/products/{id}/milestones/{mid}/tasks", app.handleTaskList)
	return f
}

func (f *taskFixture) get(pid, mid string, hx bool) (int, string) {
	req := httptest.NewRequest(http.MethodGet, "/spec/products/"+pid+"/milestones/"+mid+"/tasks", nil)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestTaskListOrderAttemptsAndLease(t *testing.T) {
	f := newTaskFixture(t)
	claim := uuid.New()
	lease := time.Now().Add(10 * time.Minute).UTC()
	f.tasks.tasks[f.mid] = []store.TaskSummary{
		{ID: uuid.New(), Title: "first-task", CurrentLane: store.Lane("Scaffold"), AttemptCount: 2, CurrentClaimID: &claim, LeaseExpiresAt: &lease, HasLiveClaim: true},
		{ID: uuid.New(), Title: "second-task", CurrentLane: store.Lane("Testing")},
	}
	code, html := f.get(f.pid.String(), f.mid.String(), true)
	require.Equal(t, 200, code)
	assert.Less(t, strings.Index(html, "first-task"), strings.Index(html, "second-task"))
	assert.Contains(t, html, "2 of 3")
	assert.Contains(t, html, "0 of 3")
	assert.Contains(t, html, lease.Format(time.RFC3339))
	assert.Contains(t, html, `data-krill-claim-id="`+claim.String()+`"`)
	assert.Contains(t, html, `data-krill="task-badge-live"`)
	assert.Contains(t, html, "/milestones/"+f.mid.String()+"/tasks/")
	assert.Contains(t, html, `data-krill="loaded-at"`)
	assert.Contains(t, html, `data-krill="board-link"`)
	assert.Contains(t, html, "/spec/products/"+f.pid.String()+"/delivery")
}

func TestTaskStateBadges(t *testing.T) {
	now := time.Now()
	id := uuid.New()
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	keys := func(s store.TaskSummary) []string {
		var k []string
		for _, b := range taskStateBadges(s, now) {
			k = append(k, b.Key)
		}
		return k
	}
	assert.Equal(t, []string{"live"}, keys(store.TaskSummary{CurrentClaimID: &id, LeaseExpiresAt: &future}))
	assert.Equal(t, []string{"lease-expired"}, keys(store.TaskSummary{CurrentClaimID: &id, LeaseExpiresAt: &past}))
	assert.Equal(t, []string{"capped"}, keys(store.TaskSummary{AttemptCount: store.DefaultAttemptCap}))
	assert.Equal(t, []string{"escalated"}, keys(store.TaskSummary{CurrentEscalationID: &id}))
	assert.Equal(t, []string{"cancelled"}, keys(store.TaskSummary{CancelledAt: &past}))
	assert.Empty(t, keys(store.TaskSummary{AttemptCount: 1}))
}

func TestTaskListExpiredLeaseNotShownLive(t *testing.T) {
	f := newTaskFixture(t)
	claim := uuid.New()
	past := time.Now().Add(-time.Hour).UTC()
	f.tasks.tasks[f.mid] = []store.TaskSummary{{ID: uuid.New(), Title: "stale", CurrentClaimID: &claim, LeaseExpiresAt: &past}}
	_, html := f.get(f.pid.String(), f.mid.String(), true)
	assert.Contains(t, html, `data-krill="task-badge-lease-expired"`)
	assert.NotContains(t, html, `data-krill="task-badge-live"`)
}

func TestTaskListEmptyVersusError(t *testing.T) {
	f := newTaskFixture(t)
	_, empty := f.get(f.pid.String(), f.mid.String(), true)
	assert.Contains(t, empty, "No tasks")
	assert.NotContains(t, empty, `data-krill="task-list-error"`)

	f.tasks.err = errors.New("boom")
	code, failed := f.get(f.pid.String(), f.mid.String(), true)
	assert.Equal(t, 200, code)
	assert.Contains(t, failed, `data-krill="task-list-error"`)
	assert.NotContains(t, failed, "No tasks")
	assert.NotContains(t, failed, "boom")
}

func TestTaskListCutMilestoneLinksMilepebbles(t *testing.T) {
	f := newTaskFixture(t)
	f.tasks.tasks[f.cutID] = []store.TaskSummary{{ID: uuid.New(), Title: "must-not-appear"}}
	_, html := f.get(f.pid.String(), f.cutID.String(), true)
	assert.Contains(t, html, milestoneTasksPath(f.pid, f.mpID))
	assert.Contains(t, html, milestoneBoardPath(f.pid, f.mpID))
	assert.NotContains(t, html, "must-not-appear")
	assert.NotContains(t, html, `data-krill="task-row"`)
	assert.Empty(t, f.tasks.calls, "a cut milestone must not read tasks")
}

func TestTaskListCrossProductAndMalformed(t *testing.T) {
	f := newTaskFixture(t)
	code, _ := f.get(f.pid.String(), uuid.NewString(), false)
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = f.get(f.pid.String(), "not-a-uuid", false)
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = f.get("not-a-uuid", f.mid.String(), false)
	assert.GreaterOrEqual(t, code, 400)
	assert.Empty(t, f.tasks.calls)
}

func TestTaskListFragmentShapeAndReadOnly(t *testing.T) {
	f := newTaskFixture(t)
	f.tasks.tasks[f.mid] = []store.TaskSummary{{ID: uuid.New(), Title: "t"}}
	_, frag := f.get(f.pid.String(), f.mid.String(), true)
	_, full := f.get(f.pid.String(), f.mid.String(), false)

	assert.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<section id="krill-task-list"`))
	assert.True(t, strings.HasSuffix(strings.TrimSpace(frag), "</section>"))
	assert.Equal(t, 1, strings.Count(frag, `id="krill-task-list"`))
	assert.Contains(t, frag, `hx-target="#krill-task-list"`)
	assert.Contains(t, frag, `hx-get="`+milestoneTasksPath(f.pid, f.mid)+`"`)
	assert.NotContains(t, frag, "<html")
	assert.Contains(t, full, "<html")
	assert.Contains(t, full, `id="krill-task-list"`)

	for _, body := range []string{frag, full} {
		assert.NotContains(t, body, "<form")
		assert.NotContains(t, body, "hx-post")
		assert.NotContains(t, body, "hx-put")
		assert.NotContains(t, body, "hx-delete")
		assert.NotContains(t, body, "hx-trigger=\"every")
	}
}

func TestTaskListNoTruncation(t *testing.T) {
	f := newTaskFixture(t)
	var ts []store.TaskSummary
	for i := 0; i < 250; i++ {
		ts = append(ts, store.TaskSummary{ID: uuid.New(), Title: "bulk"})
	}
	f.tasks.tasks[f.mid] = ts
	_, html := f.get(f.pid.String(), f.mid.String(), true)
	assert.Equal(t, 250, len(regexp.MustCompile(`data-krill="task-row"`).FindAllString(html, -1)))
}

func TestDeliveryPageLinksTaskViews(t *testing.T) {
	m, mp, mID, mpID := milestoneEntry(t, "M", "o", nil, store.MilestoneStatusInProgress, "P", "o", store.MilestoneStatusPlanned)
	_ = mp
	pid := uuid.New()
	html := mustRenderComponent(pages.Delivery(deliveryPageOf(store.Product{ID: pid, Name: "x"}, slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}}, nil, pid)))
	for _, id := range []uuid.UUID{mID, mpID} {
		assert.Contains(t, html, `href="`+milestoneTasksPath(pid, id)+`"`)
		assert.Contains(t, html, `href="`+milestoneBoardPath(pid, id)+`"`)
	}
}
