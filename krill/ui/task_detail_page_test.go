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
	"github.com/whale-net/everything/krill/ui/pages"
)

type fakeDetailStore struct {
	store.TaskStore
	tasks         map[uuid.UUID]store.Task
	getErr        error
	deps          []store.TaskDependency
	depsErr       error
	notes         []store.Note
	notesErr      error
	claim         store.Claim
	claimErr      error
	lastClaim     store.Claim
	lastClaimErr  error
	escalation    store.EscalationEvent
	escalationErr error

	// asked records every escalation id the page resolved, so a test can
	// assert the read was made for the task's OWN escalation rather than
	// only that the resulting page looks right.
	asked []uuid.UUID
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

// ListEscalatedTasks is the read the Overview's Needs-attention panel makes.
// An empty page is the honest answer for a fixture whose subject is the task
// detail: the panel renders its empty state and these assertions stay about
// the detail.
func (fakeDetailStore) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, nil
}

// CountConsoleOverview is the read the Overview's stat tiles make. Zero
// figures, no error: an idle deployment is the honest answer for a fixture
// whose subject is the task detail.
func (fakeDetailStore) CountConsoleOverview(context.Context, store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	return store.ConsoleOverviewCounts{}, nil
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
	return f.claim, f.claimErr
}

// LatestClaimForTask is the rail's read behind "None. Last held by X": it
// names the session that held the task last, which GetClaimByID cannot
// once the claim is released and current_claim_id goes NULL.
func (f *fakeDetailStore) LatestClaimForTask(_ context.Context, _, taskID uuid.UUID) (store.Claim, bool, error) {
	if f.lastClaimErr != nil {
		return store.Claim{}, false, f.lastClaimErr
	}
	if f.lastClaim.ID == uuid.Nil || f.lastClaim.TaskID != taskID {
		return store.Claim{}, false, nil
	}
	return f.lastClaim, true, nil
}

// GetEscalationEventByID is the detail's read behind a task's own
// current_escalation_id: the one read behind why the task is escalated,
// which the rail and the Overview callout both render.
func (f *fakeDetailStore) GetEscalationEventByID(_ context.Context, id uuid.UUID) (store.EscalationEvent, error) {
	f.asked = append(f.asked, id)
	if f.escalationErr != nil {
		return store.EscalationEvent{}, f.escalationErr
	}
	return f.escalation, nil
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
	pid, mid, mp uuid.UUID
	store        *fakeDetailStore
	spec         *fakeSliceSpec
	mux          *http.ServeMux
}

// newDetailFixture is one product holding two containers: an uncut
// milestone, and a cut milestone whose own task lives on its milepebble --
// which is what a task under a cut milestone always is, since the store
// refuses to create one against the milestone itself. Both containers are
// here because the breadcrumb reads differently over them, and a fixture
// with only one of them could not tell the two apart.
//
// The product row is on the fake as well as in the listing, because the
// two routes that reach this page find the product differently: the
// product-scoped one has it resolved onto the request, and the
// pre-redesign one has to read it.
func newDetailFixture(t *testing.T) *detailFixture {
	t.Helper()
	f := &detailFixture{pid: uuid.New(), mid: uuid.New(), mp: uuid.New()}
	f.store = &fakeDetailStore{tasks: map[uuid.UUID]store.Task{}}
	f.spec = &fakeSliceSpec{fakeSpecReader: &fakeSpecReader{
		products: []store.Product{{ID: f.pid, Name: "Detail product"}},
		product:  store.Product{ID: f.pid, Name: "Detail product"},
		listing: slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{
			{ID: f.mid, Name: "Plain"},
			{
				ID:   uuid.New(),
				Name: "Cut milestone",
				Milepebbles: []slice.MilepebbleListingEntry{
					{ID: f.mp, Name: "Cut milepebble"},
				},
			},
		}},
	}}
	// scopes is what the chrome reads for its Needs-attention badge on
	// every page it renders.
	app := &App{spec: f.spec, tasks: f.store, scopes: chromeScopes{}}
	f.mux = http.NewServeMux()
	f.mux.HandleFunc("GET /spec/products/{id}/milestones/{mid}/tasks/{tid}", app.handleTaskDetail)
	f.mux.HandleFunc("GET /products/{pid}/tasks/{tid}", app.handleProductTaskDetail)
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

// addOnMilepebble installs a task on the fixture's cut milepebble, which
// is the container a task under a cut milestone belongs to.
func (f *detailFixture) addOnMilepebble(t store.Task) store.Task {
	t.MilestoneID = f.mp
	return f.add(t)
}

// htmlEscapedURL is an href as it reads in the rendered markup: templ
// escapes it on the way out, so a query's "&" is "&amp;" in the page. An
// assertion that compares against the raw URL would fail against a link
// that is exactly right.
func htmlEscapedURL(raw string) string {
	return strings.ReplaceAll(raw, "&", "&amp;")
}

func (f *detailFixture) get(tid string, hx bool) (int, string) {
	return f.getAt("/spec/products/"+f.pid.String()+"/milestones/"+f.mid.String()+"/tasks/"+tid, hx)
}

// getProductScoped drives the FR's own URL, /products/{pid}/tasks/{tid},
// where the product is the path's and the task's container is not.
func (f *detailFixture) getProductScoped(tid string, hx bool) (int, string) {
	return f.getAt("/products/"+f.pid.String()+"/tasks/"+tid, hx)
}

func (f *detailFixture) getAt(path string, hx bool) (int, string) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// regionBetween is the markup from the first marker to the next one, so an
// assertion about one region is about that region and not about whatever
// else on the page happens to carry the same words.
func regionBetween(t *testing.T, html, start, end string) string {
	t.Helper()
	from := strings.Index(html, start)
	require.GreaterOrEqual(t, from, 0, "the page rendered no %s region:\n%s", start, html)
	rest := html[from:]
	to := strings.Index(rest, end)
	require.GreaterOrEqual(t, to, 0, "the %s region is never closed:\n%s", start, html)
	return rest[:to]
}

// breadcrumbOf is the breadcrumb's own markup.
func breadcrumbOf(t *testing.T, html string) string {
	t.Helper()
	return regionBetween(t, html, `data-krill="task-breadcrumb"`, "</nav>")
}

// laneStepsOf is the lane step strip's own markup.
func laneStepsOf(t *testing.T, html string) string {
	t.Helper()
	return regionBetween(t, html, `data-krill="task-lane-steps"`, "</ul>")
}

// TestTaskDetailBreadcrumbWalksProductMilestoneMilepebble is FR 0c03eac1's
// breadcrumb on the shape it actually has to describe: a task whose
// milestone is cut, which means the task sits on a milepebble and the path
// has three ancestors before the title.
//
// Each ancestor is named and linked, and the title is the one crumb with
// no href -- it is the page the operator is already on.
func TestTaskDetailBreadcrumbWalksProductMilestoneMilepebble(t *testing.T) {
	f := newDetailFixture(t)
	task := f.addOnMilepebble(store.Task{Title: "breadcrumb-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	crumbs := breadcrumbOf(t, html)
	for _, want := range []string{
		// The product names itself and links at its own page.
		">Detail product</a>",
		`href="` + htmlEscapedURL(productHeaderOf(store.Product{ID: f.pid}).Href) + `"`,
		// The milestone the milepebble was cut from -- which is not the
		// task's own container and so has to come from the parent the
		// container carries.
		">Cut milestone</a>",
		`href="` + htmlEscapedURL(milestoneDetailHref(f.pid, cutMilestoneIDOf(f.spec))) + `"`,
		// The milepebble the task itself sits on, linking at its own list.
		">Cut milepebble</a>",
		`href="` + htmlEscapedURL(productTaskContainerHref(f.pid, tasksSuffix,
			taskContainer{ID: f.mp, Kind: string(store.MilestoneKindMilepebble)})) + `"`,
	} {
		assert.Contains(t, crumbs, want)
	}
	// The title is the current page, so it is text and not a link.
	assert.Contains(t, crumbs, ">breadcrumb-task<")
	assert.NotContains(t, regionBetween(t, html, `data-krill="task-breadcrumb"`, `data-krill="task-detail-header"`),
		`href="`+htmlEscapedURL(productTaskDetailPath(f.pid, task.ID))+`"`,
		"the crumb for the page already open must not link to itself")
}

// cutMilestoneIDOf is the id of the fixture's cut milestone, read off the
// listing rather than stored beside it -- so a fixture that renames the
// milestone does not leave this asserting about an id nothing carries.
func cutMilestoneIDOf(spec *fakeSliceSpec) uuid.UUID {
	for _, m := range spec.listing.Milestones {
		if len(m.Milepebbles) > 0 {
			return m.ID
		}
	}
	return uuid.Nil
}

// TestTaskDetailBreadcrumbOnAnUncutMilestone has no milepebble level,
// because there is none: a task on a milestone that was never cut has no
// milepebble ancestor, and a crumb naming one would link into a container
// that does not exist.
func TestTaskDetailBreadcrumbOnAnUncutMilestone(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "uncut-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	crumbs := breadcrumbOf(t, html)
	assert.Contains(t, crumbs, ">Detail product</a>")
	assert.Contains(t, crumbs, ">Plain</a>")
	// The milestone links at its own detail: a crumb that named a level
	// without linking it would be a dead end in the path back.
	assert.Contains(t, crumbs, `href="`+htmlEscapedURL(milestoneDetailHref(f.pid, f.mid))+`"`)
	assert.Contains(t, crumbs, ">uncut-task<")
	assert.NotContains(t, crumbs, "Cut milepebble")
	assert.Equal(t, 3, strings.Count(crumbs, "<li"), "product, milestone, task -- and nothing between")
}

// TestTaskDetailDropsTheProductCrumbWhenTheProductReadFails is the
// degraded half of the breadcrumb's first level. The pre-redesign URL
// resolves its product by reading it, and a read that fails costs the
// page its first crumb and nothing else: the task itself was read
// separately, so the page is still answerable -- it just starts one level
// down. What it must never do is render a crumb with an empty label, or
// take the whole detail down over its banner.
func TestTaskDetailDropsTheProductCrumbWhenTheProductReadFails(t *testing.T) {
	f := newDetailFixture(t)
	f.spec.productErr = errors.New("product-boom")
	task := f.addOnMilepebble(store.Task{Title: "no-product-crumb", CurrentLane: store.LaneTesting})

	code, html := f.getAt(taskDetailPath(f.pid, f.mp, task.ID), true)
	require.Equal(t, 200, code, "body: %s", html)

	crumbs := breadcrumbOf(t, html)
	assert.NotContains(t, crumbs, "Detail product", "a product that could not be read is not named")
	assert.NotContains(t, crumbs, "product-boom", "the read's error is not the operator's to read")
	// The levels below it are unaffected: they came off the delivery
	// listing, which did read.
	assert.Contains(t, crumbs, ">Cut milestone</a>")
	assert.Contains(t, crumbs, ">Cut milepebble</a>")
	assert.Contains(t, crumbs, ">no-product-crumb<")
	assert.Equal(t, 3, strings.Count(crumbs, "<li"), "milestone, milepebble, task -- the page starts one level down")
	for _, li := range strings.Split(crumbs, "<li")[1:] {
		assert.NotContains(t, li, "<span></span>", "a crumb with no label is a hole in the path, not a level")
	}
}

// TestTaskDetailHeaderTitleAndBadges is the h1 half of FR 0c03eac1: the
// task's title as the page's one h1, with the state badges beside it and
// rendered through the same component the Tasks table and the Board use --
// so the three views cannot drift on either the label or the colour.
func TestTaskDetailHeaderTitleAndBadges(t *testing.T) {
	f := newDetailFixture(t)
	claim, esc := uuid.New(), uuid.New()
	lease := time.Now().Add(time.Hour).UTC()
	task := f.add(store.Task{
		Title: "header-task", CurrentLane: store.LaneTesting,
		AttemptCount:   store.DefaultAttemptCap,
		CurrentClaimID: &claim, LeaseExpiresAt: &lease, CurrentEscalationID: &esc,
	})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	header := regionBetween(t, html, `data-krill="task-detail-header"`, `data-krill="loaded-at"`)
	assert.Contains(t, header, `<h1 class="text-2xl font-semibold" data-krill="page-title">header-task</h1>`,
		"the task title is the page's one h1")
	assert.Equal(t, 1, strings.Count(html, "<h1"), "a page with two h1s has no title")
	for _, key := range []string{"claimed", "capped", "escalated"} {
		assert.Contains(t, header, `data-krill="task-badge-`+key+`"`)
	}
	// And through the shared component, not a copy of its markup.
	assert.Contains(t, header, `class="badge badge-info badge-sm" data-krill="task-badge-claimed"`)
}

// TestTaskDetailLaneStepsMarkCurrentAndPassed is the step strip's whole
// contract: the task's own lanes, in order, with everything before the
// current lane passed and the current lane itself marked.
func TestTaskDetailLaneStepsMarkCurrentAndPassed(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{
		Title: "stepped-task", CurrentLane: store.LaneTesting,
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneImplementation, store.LaneTesting, store.LaneDone},
	})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	steps := laneStepsOf(t, html)
	assert.Equal(t, []string{
		`data-krill="task-step-passed"`, `data-krill="task-step-passed"`,
		`data-krill="task-step-current"`, `data-krill="task-step-upcoming"`,
	}, hooksInOrder(steps, `data-krill="task-step-`),
		"the two lanes before the current one are passed, the current one is marked, the one after is not")
	assert.Contains(t, steps, ">Scaffold</li>")
	assert.Contains(t, steps, ">Testing</li>")
	assert.Contains(t, steps, ">Done</li>")
	assert.Contains(t, steps, `aria-current="step"`, "the current lane is marked for assistive technology too")
	assert.Equal(t, 3, strings.Count(steps, "step step-primary"),
		"both passed lanes and the current one are steps the task has reached")
}

// TestTaskDetailLaneStepsFollowTheTasksOwnSequence is store/task.go NFR5
// made visible: the strip reads the task's own lane_sequence, so a task
// created as Scaffold -> Validation -> Done shows exactly those three. A
// strip built from the store's canonical lane order would put an
// Implementation lane in the middle that this task was never on.
func TestTaskDetailLaneStepsFollowTheTasksOwnSequence(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{
		Title: "skipping-task", CurrentLane: store.LaneValidation,
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneValidation, store.LaneDone},
	})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	steps := laneStepsOf(t, html)
	assert.NotContains(t, steps, "Implementation")
	assert.Equal(t, []string{
		`data-krill="task-step-passed"`, `data-krill="task-step-current"`, `data-krill="task-step-upcoming"`,
	}, hooksInOrder(steps, `data-krill="task-step-`))
}

// hooksInOrder is every attribute value in html that opens with prefix, in
// the order they appear and including the opening quote, so a list of hooks
// compares exactly rather than by a substring match that would also accept
// a partially-named hook.
func hooksInOrder(html, prefix string) []string {
	var out []string
	for rest := html; ; {
		i := strings.Index(rest, prefix)
		if i < 0 {
			return out
		}
		rest = rest[i+len(prefix):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			return out
		}
		out = append(out, prefix+rest[:end+1])
		rest = rest[end+1:]
	}
}

// TestTaskDetailFrameHasAPropertiesRail pins the frame the rail and the
// tab panels slot into: a main column beside a rail region, side by side at
// lg and stacked below it otherwise.
func TestTaskDetailFrameHasAPropertiesRail(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "framed-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	assert.Contains(t, html, `data-krill="task-detail-frame"`)
	assert.Contains(t, html, "lg:grid-cols-[minmax(0,1fr)_18rem]")
	assert.Contains(t, html, `data-krill="task-properties-rail"`)
	assert.Contains(t, html, `data-krill="task-detail-main"`)
}

// TestTaskDetailReadsTheEscalationEvent is the read behind the reason: the
// page resolves task.CurrentEscalationID once, so the rail and the
// Overview callout cannot each answer with a different "why".
//
// The reason is asserted on the view model rather than on the markup,
// because this page does not spell the store's reason vocabulary out as
// bare words -- the panel that explains it presents it as a badge. What the
// markup owes the operator here is that the escalation exists and when.
func TestTaskDetailReadsTheEscalationEvent(t *testing.T) {
	f := newDetailFixture(t)
	esc := uuid.New()
	created := time.Date(2026, 9, 30, 13, 50, 0, 0, time.UTC)
	f.store.escalation = store.EscalationEvent{
		ID: esc, Reason: store.EscalationReasonThrashCap, CreatedAt: created,
	}
	task := f.add(store.Task{Title: "escalated-task", CurrentLane: store.LaneTesting, CurrentEscalationID: &esc})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	assert.Equal(t, []uuid.UUID{esc}, f.store.asked,
		"the page resolves the task's own escalation id, once")
	assert.Contains(t, html, `data-krill="task-escalated-at" data-krill-updated-at="`+created.Format(time.RFC3339)+`"`)
	assert.Contains(t, html, `datetime="`+created.Format(time.RFC3339)+`"`)
	assert.NotContains(t, html, "thrash-cap",
		"the reason is the callout's to badge, not this row's to spell out")

	page := detailPageOfFixture(t, taskDetailInputs{
		Task:       task,
		Escalation: &store.EscalationEvent{Reason: store.EscalationReasonThrashCap, CreatedAt: created},
	})
	assert.Equal(t, "thrash-cap", page.EscalationReason,
		"the reason travels on the view model for the rail and the callout")
	assert.Equal(t, created.Format(time.RFC3339), page.EscalatedAt)
}

// TestTaskDetailEscalationReadFailsWithoutTakingThePageDown is the
// degraded half: the reason is one read among several, so a failure on it
// costs the reason and keeps the page -- including the Escalated badge,
// which comes from the task row and is still true.
func TestTaskDetailEscalationReadFailsWithoutTakingThePageDown(t *testing.T) {
	f := newDetailFixture(t)
	esc := uuid.New()
	f.store.escalationErr = errors.New("escalation-boom")
	task := f.add(store.Task{Title: "half-readable", CurrentLane: store.LaneTesting, CurrentEscalationID: &esc})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	assert.Contains(t, html, `data-krill="task-badge-escalated"`, "the state comes from the task row, not from this read")
	assert.Contains(t, html, `data-krill="task-properties-escalated"`)
	assert.NotContains(t, html, "escalation-boom")

	page := detailPageOfFixture(t, taskDetailInputs{Task: task})
	assert.Equal(t, "escalation "+esc.String(), page.Escalation,
		"the task row still names the escalation it holds")
	assert.Empty(t, page.EscalationReason, "a reason that could not be read is not invented")
	assert.Empty(t, page.EscalatedAt)
}

// TestTaskDetailEscalationReadFindingNothingKeepsThePage covers the one
// error the handler separates out: an escalation that was resolved between
// the task read and the event read. That is a race the store settles, not
// a failure -- the task row no longer claims one and the next render says
// so -- so it must not cost the operator the page, and must not be logged
// as a read failure the on-call has to chase.
func TestTaskDetailEscalationReadFindingNothingKeepsThePage(t *testing.T) {
	f := newDetailFixture(t)
	esc := uuid.New()
	f.store.escalationErr = store.ErrNotFound
	task := f.add(store.Task{Title: "resolved-underneath", CurrentLane: store.LaneTesting, CurrentEscalationID: &esc})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	assert.Equal(t, []uuid.UUID{esc}, f.store.asked, "the read was still made, once")
	assert.Contains(t, html, `data-krill="task-badge-escalated"`)
	assert.Contains(t, html, `data-krill="task-properties-escalated"`,
		"the task row names the escalation, so the row is still there")
	assert.NotContains(t, html, `data-krill="task-escalated-at"`,
		"there is no event, so there is no time to show beside it")
	assert.NotContains(t, html, "not found", "the store's error is not the operator's to read")
}

// TestTaskDetailOfAnUnescalatedTaskCarriesNoEscalation: the three fields
// are empty together, because a reason with no event behind it is a value
// this page would have invented.
func TestTaskDetailOfAnUnescalatedTaskCarriesNoEscalation(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "quiet-task", CurrentLane: store.LaneTesting})

	page := detailPageOfFixture(t, taskDetailInputs{Task: task})

	assert.Empty(t, page.Escalation)
	assert.Empty(t, page.EscalationReason)
	assert.Empty(t, page.EscalatedAt)
}

// detailPageOfFixture builds the detail view model from the builder alone,
// for the values the markup deliberately does not render. It reaches for
// the builder rather than parsing HTML because what it checks is a value
// travelling, not a value on the page.
func detailPageOfFixture(t *testing.T, in taskDetailInputs) pages.TaskDetailPage {
	t.Helper()
	return taskDetailPageOf(uuid.New(), pages.ProductHeader{Name: "Detail product"},
		taskContainer{ID: uuid.New(), Name: "Plain", Kind: string(store.MilestoneKindMilestone)},
		in, time.Now())
}

// TestProductScopedTaskDetailRefusesATaskFromAnotherProduct drives FR
// 0c03eac1's second clause through the product-scoped route: the task
// exists and reads fine by id, and only the product's own listing can
// refuse it -- which is what keeps another product's task out of this
// product's breadcrumb and chrome.
func TestProductScopedTaskDetailRefusesATaskFromAnotherProduct(t *testing.T) {
	f := newDetailFixture(t)
	elsewhere := f.add(store.Task{
		Title: "another-products-task", MilestoneID: uuid.New(), CurrentLane: store.LaneTesting,
	})

	code, html := f.getProductScoped(elsewhere.ID.String(), false)

	assert.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, html, "<html", "the refusal renders inside the shell, not as a bare error")
	assert.NotContains(t, html, "another-products-task")
}

// TestProductScopedTaskDetailOfAnUnknownIdIsAnInShell404 is the other half
// of the same clause, and the one a cross-product fixture cannot reach: an
// id no task carries at all. The read fails with ErrNotFound rather than
// refusing a container, so this is the branch that would render a 500, a
// bare http.Error, or -- worst -- an empty detail frame under a URL that
// names nothing. It also has to be the product-scoped 404 rather than the
// per-container one: only the product's own task list is a page a detail
// whose container the URL never named can go back to.
func TestProductScopedTaskDetailOfAnUnknownIdIsAnInShell404(t *testing.T) {
	f := newDetailFixture(t)

	code, html := f.getProductScoped(uuid.NewString(), false)

	assert.Equal(t, http.StatusNotFound, code)
	assert.Contains(t, html, "<html", "the refusal renders inside the shell, not as a bare error")
	assert.Contains(t, html, "No task with that id belongs to this product.")
	assert.Contains(t, html, `href="`+htmlEscapedURL(productHref(f.pid, tasksSuffix))+`"`,
		"the way back is the product's own task list")
	assert.NotContains(t, html, `data-krill="task-detail"`,
		"a 404 that renders the detail frame anyway leaves the operator reading an empty task")

	// And it stays a 404 for an htmx Refresh too, rather than a 200 with an
	// empty region spliced over the page the operator is on.
	code, frag := f.getProductScoped(uuid.NewString(), true)
	assert.Equal(t, http.StatusNotFound, code)
	assert.NotContains(t, frag, `data-krill="task-detail"`)
}

// TestTaskDetailBreadcrumbIsNotAlsoAPropertiesRow pins the two rows the
// redesign removed. "Lane sequence" spelled the same sequence out as prose
// beside the step strip that now shows it, and "State" repeated the badges
// the header already carries -- two renderings of one value, either of
// which could drift from the strip and the badges.
func TestTaskDetailBreadcrumbIsNotAlsoAPropertiesRow(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{
		Title: "no-prose-row", CurrentLane: store.LaneTesting,
		LaneSequence: []store.Lane{store.LaneScaffold, store.LaneTesting, store.LaneDone},
	})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	main := regionBetween(t, html, `data-krill="task-detail-main"`, "</section>")
	assert.NotContains(t, main, ">Lane sequence</dt>",
		"the step strip is the one rendering of the sequence, not a prose row beside it")
	assert.NotContains(t, main, ">State</dt>",
		"the header badges are the one rendering of the state, not a properties row too")
	// What replaced them is still there.
	assert.Contains(t, main, `data-krill="task-lane-steps"`)
	assert.Contains(t, regionBetween(t, html, `data-krill="task-detail-header"`, `data-krill="loaded-at"`),
		`data-krill="task-state"`)
}

// TestTaskDetailLaneStepsMarkNoCurrentWhenTheLaneIsOffItsSequence is the
// degenerate case the strip's lookup has to survive: a task whose
// current_lane is somehow absent from its own lane_sequence. Nothing may
// be marked current, because the alternative is marking whichever step
// happened to sit at the same index in a sequence the task is not on.
func TestTaskDetailLaneStepsMarkNoCurrentWhenTheLaneIsOffItsSequence(t *testing.T) {
	steps := taskLaneSteps(
		[]store.Lane{store.LaneScaffold, store.LaneValidation, store.LaneDone},
		store.Lane("NotALane"))

	for i, s := range steps {
		assert.False(t, s.Current, "step %d is marked current for a lane the task is not in", i)
		assert.False(t, s.Passed, "step %d is marked passed when nothing before it was reached", i)
	}
	assert.Len(t, steps, 3, "the strip still shows the task's own lanes")
}

// TestTaskLaneStepsOverAnEmptySequence: a task with no lane_sequence at
// all gets no strip rather than one over the store's canonical order --
// which would be the very re-derivation store/task.go NFR5 forbids, in
// the shape where nothing on the page says the sequence was missing.
func TestTaskLaneStepsOverAnEmptySequence(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "no-sequence", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	assert.NotContains(t, html, `data-krill="task-lane-steps"`,
		"no sequence, no strip -- not a strip invented from the canonical lane order")
	assert.NotContains(t, html, "CanonicalLane", "the store's canonical order is not this page's to reach for")
}

// TestResolveTaskContainerCarriesTheMilepebblesParent is the one read the
// breadcrumb's milepebble level depends on. A milepebble is its own
// milestone_ref row, so nothing else on the container says what it was cut
// from -- a resolver that dropped the parent would leave the breadcrumb
// with a milepebble crumb and no milestone above it, and the only way to
// notice is to assert the parent is there.
func TestResolveTaskContainerCarriesTheMilepebblesParent(t *testing.T) {
	cutID, mpID := uuid.New(), uuid.New()
	plainID := uuid.New()
	listing := slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{
		{ID: plainID, Name: "Plain"},
		{ID: cutID, Name: "Cut", Milepebbles: []slice.MilepebbleListingEntry{{ID: mpID, Name: "Pebble"}}},
	}}

	pebble, found := resolveTaskContainer(listing, mpID)
	require.True(t, found)
	assert.Equal(t, cutID, pebble.ParentID)
	assert.Equal(t, "Cut", pebble.ParentName)
	assert.Equal(t, string(store.MilestoneKindMilepebble), pebble.Kind)

	plain, found := resolveTaskContainer(listing, plainID)
	require.True(t, found)
	assert.Equal(t, uuid.Nil, plain.ParentID, "a milestone was cut from nothing")
	assert.Empty(t, plain.ParentName)

	_, found = resolveTaskContainer(listing, uuid.New())
	assert.False(t, found, "a container the listing does not name is not resolved")
}

// TestTaskDetailBreadcrumbOnThePreRedesignURL is the same breadcrumb over
// the per-container URL, which finds the product by reading it rather than
// off the request. Both routes share serveTaskDetail, so this is the case
// that would catch a header wired from the request context alone.
func TestTaskDetailBreadcrumbOnThePreRedesignURL(t *testing.T) {
	f := newDetailFixture(t)
	task := f.addOnMilepebble(store.Task{Title: "legacy-url-task", CurrentLane: store.LaneTesting})

	code, html := f.getAt(taskDetailPath(f.pid, f.mp, task.ID), true)
	require.Equal(t, 200, code, "body: %s", html)

	crumbs := breadcrumbOf(t, html)
	assert.Contains(t, crumbs, ">Detail product</a>")
	assert.Contains(t, crumbs, ">Cut milestone</a>")
	assert.Contains(t, crumbs, ">Cut milepebble</a>")
}

// TestTaskDetailRefreshReRequestsTheURLThatServedIt: the Refresh button
// moved into the header row, and the header row is inside the region the
// button targets. Two things therefore have to hold at once -- the button
// re-requests the route that actually served the page (both routes reach
// this render, and only the request knows which one the operator is on),
// and it targets the region whose id the served fragment's root carries.
//
// A Refresh that asked for the other route would still answer 200, so the
// markup is the only place this is visible.
func TestTaskDetailRefreshReRequestsTheURLThatServedIt(t *testing.T) {
	f := newDetailFixture(t)
	task := f.addOnMilepebble(store.Task{Title: "refreshed-task", CurrentLane: store.LaneTesting})

	code, frag := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", frag)

	button := regionBetween(t, frag, `data-krill="refresh"`, "</button>")
	assert.Contains(t, button, `hx-get="/products/`+f.pid.String()+`/tasks/`+task.ID.String()+`"`,
		"the product-scoped page refreshes the product-scoped URL")
	assert.Contains(t, button, `hx-target="#`+pages.TaskDetailAnchor+`"`)
	assert.Contains(t, button, `hx-swap="outerHTML"`)
	assert.Contains(t, button, ">Refresh")

	code, frag = f.getAt(taskDetailPath(f.pid, f.mp, task.ID), true)
	require.Equal(t, 200, code, "body: %s", frag)
	button = regionBetween(t, frag, `data-krill="refresh"`, "</button>")
	assert.Contains(t, button, `hx-get="`+taskDetailPath(f.pid, f.mp, task.ID)+`"`,
		"the pre-redesign page refreshes the pre-redesign URL, not the product-scoped one")
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
		`data-krill="task-badge-claimed"`,
		// The way back is the product-wide list and board scoped to this
		// task's own container, not the retired per-container URLs. The
		// href is HTML-escaped in the markup, so the query's "&" reads
		// as "&amp;" -- asserted through the escape rather than around it.
		`href="` + htmlEscapedURL(productTaskContainerHref(f.pid, tasksSuffix,
			taskContainer{ID: f.mid, Kind: string(store.MilestoneKindMilestone)})) + `"`,
		`href="` + htmlEscapedURL(productTaskContainerHref(f.pid, boardSuffix,
			taskContainer{ID: f.mid, Kind: string(store.MilestoneKindMilestone)})) + `"`,
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
	assert.NotContains(t, html, `data-krill="task-badge-claimed"`)
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
	assert.NotContains(t, html, "No notes")
	assert.NotContains(t, html, "boom")
}

// TestTaskDetailEmptySections: a task with no dependencies renders no
// Depends-on card at all. The rail says what the task has, so a card
// holding nothing beside the content would read as something missing where
// the answer is "nothing depends on this".
func TestTaskDetailEmptySections(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "t"})
	_, html := f.get(task.ID.String(), true)
	assert.NotContains(t, html, `data-krill="task-depends-on"`)
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

	// The read-only assertions are about THIS page's surface, which is the
	// whole fragment for an htmx request and the detail region inside
	// <main> for a page. The chrome around it is not the page's: the
	// sidebar's Product switcher is a form of its own, and asserting
	// against the whole document would make this page's read-only contract
	// depend on whether the switcher happens to have anything to pick.
	region := regionBetween(t, full, `data-krill="task-detail"`, "</main>")
	for _, body := range []string{frag, region} {
		assert.NotContains(t, body, "<form")
		assert.NotContains(t, body, "hx-post")
		assert.NotContains(t, body, "hx-put")
		assert.NotContains(t, body, "hx-delete")
		assert.NotContains(t, body, `hx-trigger="every`)
	}
}
