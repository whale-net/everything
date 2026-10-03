// The read-only task detail page, reached from the task list. Composes
// store.GetTaskByID + ListDependencies + ListNotesForTask and the task's
// spec slice; issues no write.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// taskSliceReader is the optional spec read that returns a task's embedded
// spec slice (the same document get_task embeds). A reader without it
// shows an inline slice error rather than an empty section.
type taskSliceReader interface {
	MilestoneDeliversSlice(ctx context.Context, milestoneID uuid.UUID) (slice.Document, error)
}

// MilestoneDeliversSlice is the slice get_task embeds for a task's milestone.
func (r *specReader) MilestoneDeliversSlice(ctx context.Context, milestoneID uuid.UUID) (slice.Document, error) {
	doc, err := r.querier.GetMilestoneDeliversSlice(ctx, milestoneID)
	if err != nil {
		return slice.Document{}, fmt.Errorf("milestone delivers slice: %w", err)
	}
	return doc, nil
}

func (app *App) taskDetailNotFound(w http.ResponseWriter, r *http.Request, c taskContainer, pid uuid.UUID) {
	app.renderSpecStatus(w, r, http.StatusNotFound, pages.StatusPage{
		Title:    "Not found",
		Detail:   "No task with that id belongs to this milestone or milepebble.",
		BackHref: productTaskContainerHref(pid, tasksSuffix, c),
		BackText: "Back to " + c.Name + " tasks",
	})
}

// taskDetailInputs are the reads composed into one detail view; each
// section carries its own error so a partial failure stays visible.
type taskDetailInputs struct {
	Task     store.Task
	Claim    *store.Claim
	Deps     []store.TaskDependency
	DepTasks map[uuid.UUID]store.Task
	DepsErr  error
	Notes    []store.Note
	NotesErr error
	Slice    slice.Document
	SliceErr error

	// Escalation is the event behind Task.CurrentEscalationID, read once
	// so the properties rail and the Overview callout can both show why a
	// task was escalated without either re-reading it (and without one of
	// them rendering a reason the other contradicts).
	Escalation *store.EscalationEvent
}

// taskDetailPageOf assembles the detail view model. now is injected so a
// lease's expiry is judged against the read time.
//
// product is the breadcrumb's product crumb. It is a header rather than an
// id because the same banner every product-scoped page carries is already
// this value; a second spelling of "the product this task belongs to"
// would be one more thing to keep in step.
func taskDetailPageOf(pid uuid.UUID, product pages.ProductHeader, c taskContainer, in taskDetailInputs, now time.Time) pages.TaskDetailPage {
	t := in.Task
	summary := store.TaskSummary{
		ID: t.ID, Title: t.Title, CurrentLane: t.CurrentLane, AttemptCount: t.AttemptCount,
		CurrentClaimID: t.CurrentClaimID, LeaseExpiresAt: t.LeaseExpiresAt,
		CurrentEscalationID: t.CurrentEscalationID, CancelledAt: t.CancelledAt,
	}
	page := pages.TaskDetailPage{
		Path:     taskDetailPath(pid, c.ID, t.ID),
		ID:       t.ID.String(),
		Title:    t.Title,
		Crumbs:   taskDetailCrumbsOf(product, pid, c, t.Title),
		Steps:    taskLaneSteps(t.LaneSequence, t.CurrentLane),
		Lane:     string(t.CurrentLane),
		Attempts: taskAttemptsLabel(t.AttemptCount),
		LoadedAt: now.UTC().Format(time.RFC3339),
		Badges:   taskStateBadges(summary, now),
		// The way back is the product-wide list scoped to this task's own
		// container, which is where the list an operator reaches a detail
		// from now lives. The per-container list URL still redirects there,
		// so the old link and the new one open the same page.
		TasksPath:     productTaskContainerHref(pid, tasksSuffix, c),
		BoardPath:     productTaskContainerHref(pid, boardSuffix, c),
		ContainerName: c.Name,
	}
	if t.Body != nil {
		page.Body = *t.Body
	}
	if t.CurrentEscalationID != nil {
		page.Escalation = "escalation " + t.CurrentEscalationID.String()
		if in.Escalation != nil {
			page.EscalationReason = string(in.Escalation.Reason)
			page.EscalatedAt = in.Escalation.CreatedAt.UTC().Format(time.RFC3339)
		}
	}
	if t.AttemptCount > 0 {
		page.Attempts += fmt.Sprintf(" (current attempt: %d)", t.AttemptCount)
	}
	if t.CurrentClaimID != nil {
		page.ClaimID = t.CurrentClaimID.String()
		page.ClaimSummary = "claim " + page.ClaimID
		if in.Claim != nil {
			page.ClaimSummary += fmt.Sprintf(", session %s, claimed at %s", in.Claim.SessionID, in.Claim.ClaimedAt.UTC().Format(time.RFC3339))
		}
		if t.LeaseExpiresAt != nil && !t.LeaseExpiresAt.After(now) {
			page.ClaimSummary += " (lease expired, not live)"
		}
	}
	if t.LeaseExpiresAt != nil {
		page.LeaseExpiresAt = t.LeaseExpiresAt.UTC().Format(time.RFC3339)
	}
	if in.DepsErr != nil {
		page.DepsError = "The dependencies could not be read. See the logs."
	}
	for _, d := range in.Deps {
		link := pages.TaskDepLink{Title: d.DependsOnTaskID.String()}
		if dt, ok := in.DepTasks[d.DependsOnTaskID]; ok {
			link.Title = dt.Title
			link.Lane = string(dt.CurrentLane)
			link.DetailPath = taskDetailPath(pid, dt.MilestoneID, dt.ID)
		} else {
			link.DetailPath = taskDetailPath(pid, c.ID, d.DependsOnTaskID)
		}
		page.Deps = append(page.Deps, link)
	}
	if in.NotesErr != nil {
		page.NotesError = "The notes could not be read. See the logs."
	}
	for _, n := range in.Notes {
		page.Notes = append(page.Notes, pages.TaskNoteRow{Kind: string(n.Kind), Status: string(n.CurrentStatus), Body: n.Body})
	}
	if in.SliceErr != nil {
		page.SliceError = "The spec slice could not be read. See the logs."
	} else if b, err := json.MarshalIndent(in.Slice, "", "  "); err != nil {
		page.SliceError = "The spec slice could not be rendered. See the logs."
	} else {
		page.SliceJSON = string(b)
	}
	return page
}

// taskDetailCrumbsOf is the breadcrumb from the product down to this
// task: product -> milestone -> milepebble -> the task's own title.
//
// The milepebble crumb appears only when the task sits on a cut
// milepebble. A task on an uncut milestone has no such level, and a
// crumb naming one would offer a link into a container that does not
// exist -- while a task's container is always a milepebble once its
// milestone is cut, so "when the container is a milepebble" and "when
// the task sits on one" are the same condition, decided by the
// container's own Kind rather than by a second lookup.
//
// Each ancestor links to the page that names it: the milestone to its own
// detail, the milepebble to its own task list. The task's title is the
// page the operator is already on, so it is the one crumb with no href.
func taskDetailCrumbsOf(product pages.ProductHeader, pid uuid.UUID, c taskContainer, title string) []pages.TaskCrumb {
	crumbs := make([]pages.TaskCrumb, 0, 4)
	if product.Name != "" {
		crumbs = append(crumbs, pages.TaskCrumb{Label: product.Name, Href: product.Href})
	}
	if c.Kind == string(store.MilestoneKindMilepebble) {
		// The parent is carried on the container rather than re-read: the
		// delivery listing that named the milepebble also named its
		// parent, and that listing is the check that decided this task
		// belongs to this product at all.
		if c.ParentName != "" {
			crumbs = append(crumbs, pages.TaskCrumb{
				Label: c.ParentName,
				Href:  milestoneDetailHref(pid, c.ParentID),
			})
		}
		crumbs = append(crumbs, pages.TaskCrumb{
			Label: c.Name,
			Href:  productTaskContainerHref(pid, tasksSuffix, c),
		})
	} else {
		crumbs = append(crumbs, pages.TaskCrumb{
			Label: c.Name,
			Href:  milestoneDetailHref(pid, c.ID),
		})
	}
	return append(crumbs, pages.TaskCrumb{Label: title})
}

// taskLaneSteps is the detail's step strip: the task's OWN lane_sequence,
// with every lane before the one it is in marked passed and that lane
// marked current.
//
// The sequence is the task's rather than the store's canonical lane
// order (store/task.go NFR5): a task created as Scaffold -> Validation ->
// Done has said which lanes it is on, and a strip built from the
// canonical order would show it passing through an Implementation lane it
// never had. The current lane is looked up in the sequence rather than
// compared against a position, so a task whose lane is somehow absent
// marks no step current instead of marking one that is not.
func taskLaneSteps(sequence []store.Lane, current store.Lane) []pages.TaskLaneStep {
	currentAt := -1
	for i, lane := range sequence {
		if lane == current {
			currentAt = i
			break
		}
	}
	steps := make([]pages.TaskLaneStep, 0, len(sequence))
	for i, lane := range sequence {
		steps = append(steps, pages.TaskLaneStep{
			Label:   string(lane),
			Passed:  currentAt >= 0 && i < currentAt,
			Current: i == currentAt,
		})
	}
	return steps
}

// taskDetailProductHeader is the breadcrumb's product crumb.
//
// The product-scoped route has already resolved the product and put it on
// the request, so it is read from there. The pre-redesign per-container URL
// resolves its own; a read that fails costs the page its first crumb and
// nothing else, because the task itself was read separately and the page
// is still answerable without it -- the same rule product_scope.go's
// rememberUnprefixedProduct follows, for the same reason.
func (app *App) taskDetailProductHeader(ctx context.Context, r *http.Request, pid uuid.UUID) pages.ProductHeader {
	if p, ok := currentProduct(r.Context()); ok && p.ID == pid {
		return productHeaderOf(p)
	}
	p, err := app.spec.Product(ctx, pid)
	if err != nil {
		logger.Warn("task detail: product read failed for the breadcrumb", "product", pid.String(), "error", err)
		return pages.ProductHeader{Href: productPath(pid)}
	}
	return productHeaderOf(p)
}

// handleTaskDetail renders one task's detail at the pre-redesign
// per-container URL, resolving the container from the path.
//
// No route mounts it: legacyURLs retires that URL into the product-scoped
// detail (FR 0c03eac1). It stays because it is the one caller that resolves
// the container by hand rather than from the task, so it is where the
// per-container membership rule — a task belonging to another container
// under the same product is not this page's answer — is exercised.
func (app *App) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	pid, c, ok := app.resolveTaskRoute(w, r)
	if !ok {
		return
	}
	tid, err := uuid.Parse(r.PathValue("tid"))
	if err != nil {
		app.taskDetailNotFound(w, r, c, pid)
		return
	}
	// The per-container URL names the container, so a task belonging to
	// another one under the same product is not this page's answer.
	app.serveTaskDetail(w, r, pid, tid, func(task store.Task) (taskContainer, bool) {
		if task.MilestoneID != c.ID {
			return taskContainer{}, false
		}
		return c, true
	})
}

// handleProductTaskDetail serves /products/{pid}/tasks/{tid} -- the
// product-scoped detail the product-wide Tasks table's rows link to
// (FR f41a352d).
//
// The container is the task's own rather than the URL's, because this URL
// names only the product: the table is not scoped to a container, so a row
// in it may belong to any milestone under the product. It is still resolved
// against the product's own listing, so a task from another product is a
// 404 rather than that product's task rendered under this one's chrome.
func (app *App) handleProductTaskDetail(w http.ResponseWriter, r *http.Request) {
	r, product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	tid, err := uuid.Parse(r.PathValue("tid"))
	if err != nil {
		app.renderProductTaskDetailNotFound(w, r, product.ID)
		return
	}
	listing, err := app.spec.Delivery(r.Context(), product.ID, nil)
	if err != nil {
		logger.Error("product task detail: delivery listing read failed",
			"product", product.ID.String(), "error", err)
		app.renderProductTaskDetailNotFound(w, r, product.ID)
		return
	}
	app.serveTaskDetail(w, r, product.ID, tid, func(task store.Task) (taskContainer, bool) {
		return resolveTaskContainer(listing, task.MilestoneID)
	})
}

// serveTaskDetail is the one read-and-render both detail routes share:
// resolve the task, refuse it unless container says this URL's answer, then
// compose and serve. That resolver is the only thing the two routes disagree
// on, so the reads that build the page cannot drift between them.
func (app *App) serveTaskDetail(w http.ResponseWriter, r *http.Request, pid, tid uuid.UUID, container func(store.Task) (taskContainer, bool)) {
	ctx := r.Context()
	task, err := app.tasks.GetTaskByID(ctx, tid)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			logger.Error("task read failed", "task", tid.String(), "error", err)
			app.renderSpecStatus(w, r, http.StatusInternalServerError, pages.StatusPage{
				Title:    "Could not load the task",
				Detail:   "The task could not be read. See the logs.",
				BackHref: productHref(pid, tasksSuffix),
				BackText: "Back to tasks",
			})
			return
		}
		app.renderProductTaskDetailNotFound(w, r, pid)
		return
	}
	c, found := container(task)
	if !found {
		app.renderProductTaskDetailNotFound(w, r, pid)
		return
	}

	in := taskDetailInputs{Task: task, DepTasks: map[uuid.UUID]store.Task{}}
	in.Deps, in.DepsErr = app.tasks.ListDependencies(ctx, task.ScopeID, tid)
	if in.DepsErr != nil {
		logger.Error("task dependencies read failed", "task", tid.String(), "error", in.DepsErr)
	}
	for _, d := range in.Deps {
		if dt, err := app.tasks.GetTaskByID(ctx, d.DependsOnTaskID); err == nil {
			in.DepTasks[d.DependsOnTaskID] = dt
		}
	}
	in.Notes, in.NotesErr = app.tasks.ListNotesForTask(ctx, task.ScopeID, tid)
	if in.NotesErr != nil {
		logger.Error("task notes read failed", "task", tid.String(), "error", in.NotesErr)
	}
	if task.CurrentClaimID != nil {
		if cl, err := app.tasks.GetClaimByID(ctx, *task.CurrentClaimID); err == nil {
			in.Claim = &cl
		}
	}
	if task.CurrentEscalationID != nil {
		// The escalation event is read once and carried, so the rail and
		// the Overview callout cannot disagree about why this task was
		// escalated. A task whose escalation was resolved between the two
		// reads is not an error the operator can act on -- the task row no
		// longer claims one, and the next render says so.
		ev, err := app.tasks.GetEscalationEventByID(ctx, *task.CurrentEscalationID)
		switch {
		case err == nil:
			in.Escalation = &ev
		case errors.Is(err, store.ErrNotFound):
			logger.Warn("task escalation event read found nothing", "task", tid.String(), "escalation", task.CurrentEscalationID.String())
		default:
			logger.Error("task escalation event read failed", "task", tid.String(), "escalation", task.CurrentEscalationID.String(), "error", err)
		}
	}
	if sr, ok := app.spec.(taskSliceReader); ok {
		in.Slice, in.SliceErr = sr.MilestoneDeliversSlice(ctx, task.MilestoneID)
	} else {
		in.SliceErr = fmt.Errorf("spec reader has no task slice read")
	}
	if in.SliceErr != nil {
		logger.Error("task slice read failed", "task", tid.String(), "error", in.SliceErr)
	}

	page := taskDetailPageOf(pid, app.taskDetailProductHeader(ctx, r, pid), c, in, time.Now())
	// Refresh re-requests whatever URL served this page, not the
	// per-container detail: both routes reach here, and only the request
	// knows which one the operator is on.
	page.Path = r.URL.Path
	body := pages.TaskDetail(page)
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, body)
		return
	}
	app.renderShell(w, r, "Task", r.URL.Path, body)
}

// renderProductTaskDetailNotFound is the in-shell 404 for a task detail
// whose id belongs to no milestone under this product. It points back at the
// product-wide Tasks page, which is the page a detail reached from a table
// row should return to -- and the only one that exists for a task whose
// container the URL never named.
func (app *App) renderProductTaskDetailNotFound(w http.ResponseWriter, r *http.Request, pid uuid.UUID) {
	app.renderSpecStatus(w, r, http.StatusNotFound, pages.StatusPage{
		Title:    "Not found",
		Detail:   "No task with that id belongs to this product.",
		BackHref: productHref(pid, tasksSuffix),
		BackText: "Back to tasks",
	})
}
