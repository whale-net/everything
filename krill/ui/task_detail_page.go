// The read-only task detail page: task, dependencies, notes and spec slice, plus the
// intervention controls bound to the shared legality predicate.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
	"github.com/whale-net/everything/libs/go/htmxui"
)

// taskSliceReader is the optional spec read for a task's embedded spec slice (as get_task
// embeds); without it the page shows an inline slice error.
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

	// Escalation is the event behind Task.CurrentEscalationID, read once so the rail and the
	// Overview callout cannot disagree on the reason.
	Escalation *store.EscalationEvent

	// LastClaim is the most recent claim, read only when the task holds none: GetClaimByID
	// cannot answer "last held by X" once current_claim_id is NULL.
	LastClaim *store.Claim
}

// taskDetailTabKeys is the ordered set of facet tabs; a slice because order matters.
var taskDetailTabKeys = []string{
	pages.TaskTabOverview,
	pages.TaskTabNotes,
	pages.TaskTabDependencies,
	pages.TaskTabSlice,
}

// taskDetailTabLabels is each tab's display name ("slice" is only the wire value).
var taskDetailTabLabels = map[string]string{
	pages.TaskTabOverview:     "Overview",
	pages.TaskTabNotes:        "Notes",
	pages.TaskTabDependencies: "Dependencies",
	pages.TaskTabSlice:        "Spec slice",
}

// taskDetailTabOf resolves ?tab= to a known tab. Absent or unknown values fall back to
// overview, so stale or hand-edited links still render.
func taskDetailTabOf(r *http.Request) string {
	tab := r.URL.Query().Get("tab")
	for _, known := range taskDetailTabKeys {
		if tab == known {
			return known
		}
	}
	return pages.TaskTabOverview
}

// taskDetailTabHref is one tab's URL on the request's own path; Overview is the bare path,
// so the shared address is the shortest true one.
func taskDetailTabHref(path, tab string) string {
	if tab == pages.TaskTabOverview {
		return path
	}
	return path + "?tab=" + tab
}

// taskDetailTabsOf builds the tab strip. Counts are the lengths of the lists the panels
// render; a failed read shows no count rather than a false zero.
func taskDetailTabsOf(path, active string, notes []pages.TaskNoteRow, notesErr string, deps []pages.TaskDepLink, depsErr string) []pages.TaskTab {
	tabs := make([]pages.TaskTab, 0, len(taskDetailTabKeys))
	for _, key := range taskDetailTabKeys {
		tab := pages.TaskTab{Key: key, Label: taskDetailTabLabels[key], Href: taskDetailTabHref(path, key), Active: key == active}
		switch key {
		case pages.TaskTabNotes:
			if notesErr == "" {
				tab.Count, tab.HasCount = len(notes), true
			}
		case pages.TaskTabDependencies:
			if depsErr == "" {
				tab.Count, tab.HasCount = len(deps), true
			}
		}
		tabs = append(tabs, tab)
	}
	return tabs
}

// taskDetailPageOf assembles the detail view model; now judges lease expiry against read time.
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
		// Back to the product-wide list scoped to this task's container.
		TasksPath:     productTaskContainerHref(pid, tasksSuffix, c),
		BoardPath:     productTaskContainerHref(pid, boardSuffix, c),
		ContainerName: c.Name,
	}
	// The rail's Milepebble row exists only for a task on a milepebble.
	if c.Kind == string(store.MilestoneKindMilepebble) {
		page.MilepebbleName = c.Name
		page.MilepebblePath = page.TasksPath
	}
	if t.Body != nil {
		page.Body = *t.Body
	}
	// A cancelled task keeps its escalation on record but is terminal, so it shows as cancelled only.
	if t.CurrentEscalationID != nil && t.CancelledAt == nil {
		page.Escalation = "escalation " + t.CurrentEscalationID.String()
		if in.Escalation != nil {
			page.EscalationReason = string(in.Escalation.Reason)
			page.EscalatedAt = in.Escalation.CreatedAt.UTC().Format(time.RFC3339)
			// Automatic reasons carry counter and cap, manual carries neither; the store's CHECK
			// guarantees both-or-neither.
			if in.Escalation.CounterValue != nil && in.Escalation.CapValue != nil {
				page.EscalationCounter = *in.Escalation.CounterValue
				page.EscalationCap = *in.Escalation.CapValue
				page.HasEscalationCounter = true
			}
		}
	}
	page.AttemptCount = t.AttemptCount
	page.AttemptCap = store.DefaultAttemptCap
	if t.AttemptCount > 0 {
		page.Attempts += fmt.Sprintf(" (current attempt: %d)", t.AttemptCount)
	}
	if t.CurrentClaimID != nil {
		page.ClaimID = t.CurrentClaimID.String()
		if in.Claim != nil {
			page.ClaimHolder = in.Claim.SessionID.String()
		}
		if t.LeaseExpiresAt != nil && !t.LeaseExpiresAt.After(now) {
			page.ClaimExpired = true
		}
	} else if in.LastClaim != nil {
		// Unclaimed: show who held it last.
		page.LastClaimHolder = in.LastClaim.SessionID.String()
	}
	if t.LeaseExpiresAt != nil {
		page.LeaseExpiresAt = t.LeaseExpiresAt.UTC().Format(time.RFC3339)
	}
	if in.DepsErr != nil {
		page.DepsError = "The dependencies could not be read. See the logs."
	}
	for _, d := range in.Deps {
		// A dependency may sit on any container under the product, so link the product-scoped detail.
		link := pages.TaskDepLink{
			Title:      d.DependsOnTaskID.String(),
			DetailPath: productTaskDetailPath(pid, d.DependsOnTaskID),
		}
		if dt, ok := in.DepTasks[d.DependsOnTaskID]; ok {
			link.Title = dt.Title
			link.Lane = string(dt.CurrentLane)
		}
		page.Deps = append(page.Deps, link)
	}
	if in.NotesErr != nil {
		page.NotesError = "The notes could not be read. See the logs."
	}
	for _, n := range in.Notes {
		page.Notes = append(page.Notes, pages.TaskNoteRow{Kind: string(n.Kind), Status: string(n.CurrentStatus), Body: n.Body})
	}
	// The Overview shows the newest notes (the list is oldest-first) and the total, so a
	// truncated list is not read as complete.
	page.NotesTotal = len(page.Notes)
	if len(page.Notes) > pages.LatestNotesLimit {
		page.LatestNotes = page.Notes[len(page.Notes)-pages.LatestNotesLimit:]
	} else {
		page.LatestNotes = page.Notes
	}
	page.Callouts = taskDetailCallouts(t, page, now)
	if in.SliceErr != nil {
		page.SliceError = "The spec slice could not be read. See the logs."
	} else if b, err := json.MarshalIndent(in.Slice, "", "  "); err != nil {
		page.SliceError = "The spec slice could not be rendered. See the logs."
	} else {
		page.SliceJSON = string(b)
	}
	return page
}

// taskDetailCallouts builds Overview banners for escalated, capped and lapsed-lease tasks,
// each on its own line since they can co-occur. A healthy task gets none, so banners stay
// meaningful. Severity follows components.TaskStateStyle.
func taskDetailCallouts(t store.Task, page pages.TaskDetailPage, now time.Time) []pages.TaskCallout {
	var out []pages.TaskCallout
	if c, ok := escalationCallout(page); ok {
		out = append(out, c)
	}
	if page.AttemptCap > 0 && page.AttemptCount >= page.AttemptCap {
		out = append(out, pages.TaskCallout{
			Key:     pages.TaskCalloutCapped,
			Variant: htmxui.AlertWarning,
			Message: fmt.Sprintf(
				"At the attempt cap (%d of %d). Every claim, reclaim, release and abandon counts against it; a requeue resets the counter.",
				page.AttemptCount, page.AttemptCap),
		})
	}
	if t.LeaseExpiresAt != nil && !t.LeaseExpiresAt.After(now) {
		out = append(out, pages.TaskCallout{
			Key:     pages.TaskCalloutLeaseExpired,
			Variant: htmxui.AlertWarning,
			Message: "The lease on this task expired at " +
				t.LeaseExpiresAt.UTC().Format(time.RFC3339) +
				". No worker holds it any more; a reclaim sweep closes the claim and counts the lapse as an attempt.",
		})
	}
	return out
}

// escalationCallout is an escalated task's banner, naming the reason and, for automatic
// reasons, the counter and cap. An unreadable event still gets a banner saying so.
func escalationCallout(page pages.TaskDetailPage) (pages.TaskCallout, bool) {
	if page.Escalation == "" {
		return pages.TaskCallout{}, false
	}
	if page.EscalationReason == "" {
		return pages.TaskCallout{
			Key:     pages.TaskCalloutEscalated,
			Variant: htmxui.AlertError,
			Message: "This task is escalated. The reason behind it could not be read; see the logs.",
		}, true
	}
	label := components.EscalationReasonLabel(page.EscalationReason)
	if !page.HasEscalationCounter {
		// Manual escalation has no counter, so the reason is the whole explanation.
		return pages.TaskCallout{
			Key:     pages.TaskCalloutEscalated,
			Variant: htmxui.AlertError,
			Message: "Escalated (" + label + "), so no worker can claim it. Requeue returns it to the claimable queue.",
		}, true
	}
	// Name the specific counter so a thrash-cap banner is not read as attempts.
	return pages.TaskCallout{
		Key:     pages.TaskCalloutEscalated,
		Variant: htmxui.AlertError,
		Message: fmt.Sprintf("%s Requeue resets the %s and keeps the notes.",
			escalationHeadline(page), escalationCounterName(page.EscalationReason)),
	}, true
}

// escalationHeadline is the automatic-reason banner's first sentence: which
// reason fired, and the counter and cap it fired against.
func escalationHeadline(page pages.TaskDetailPage) string {
	label := components.EscalationReasonLabel(page.EscalationReason)
	switch page.EscalationReason {
	case string(store.EscalationReasonThrashCap):
		return fmt.Sprintf("Escalated after %d %s (%s of %d).",
			page.EscalationCounter, calloutPluralN(page.EscalationCounter, "failing verdict", "failing verdicts"),
			label, page.EscalationCap)
	default:
		return fmt.Sprintf("Escalated at the %s (%d of %d).", label, page.EscalationCounter, page.EscalationCap)
	}
}

// escalationCounterName is the counter a requeue resets for this reason, matching RequeueTask.
func escalationCounterName(reason string) string {
	if reason == string(store.EscalationReasonThrashCap) {
		return "thrash counter"
	}
	return "attempt counter"
}

// calloutPluralN picks the noun form for n.
func calloutPluralN(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// taskDetailCrumbsOf is the breadcrumb product -> milestone -> milepebble -> task title.
// The milepebble crumb appears only when the container is a milepebble; the title has no href.
func taskDetailCrumbsOf(product pages.ProductHeader, pid uuid.UUID, c taskContainer, title string) []pages.TaskCrumb {
	crumbs := make([]pages.TaskCrumb, 0, 4)
	if product.Name != "" {
		crumbs = append(crumbs, pages.TaskCrumb{Label: product.Name, Href: product.Href})
	}
	if c.Kind == string(store.MilestoneKindMilepebble) {
		// The parent comes from the same delivery listing that proved product membership.
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

// taskLaneSteps is the step strip over the task's own lane_sequence, not the canonical order,
// so it never shows lanes the task lacks. A current lane missing from the sequence marks none.
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

// taskDetailProductHeader is the breadcrumb's product crumb, from the request when resolved;
// a failed read costs only that crumb.
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

// handleTaskDetail renders a task at the per-container URL. No route mounts it (legacyURLs
// redirects); it remains where the container-membership rule is exercised.
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
	app.serveTaskDetail(w, r, pid, tid, func(task store.Task) (taskContainer, bool) {
		if task.MilestoneID != c.ID {
			return taskContainer{}, false
		}
		return c, true
	})
}

// handleProductTaskDetail serves /products/{pid}/tasks/{tid}. The container is the task's
// own, resolved against the product listing so another product's task is a 404.
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

// taskInterventionStateOf is the task's intervention state, as the Needs attention rows read it.
// Precedence: cancelled, escalated, claimed, else ready.
func taskInterventionStateOf(t store.Task) taskInterventionState {
	switch {
	case t.CancelledAt != nil:
		return taskInterventionCancelled
	case t.CurrentEscalationID != nil:
		return taskInterventionEscalated
	case t.CurrentClaimID != nil:
		return taskInterventionClaimed
	default:
		return taskInterventionReady
	}
}

// taskDetailControls is the one builder of the detail's intervention controls, from the shared
// legality predicate. Controls carry the claim or escalation id this page observed, so a changed
// state is refused. Triggers and popovers both derive from it, so no trigger points at a missing popover.
func taskDetailControls(t store.Task, returnTo string) []pages.TaskActionControl {
	state := taskInterventionStateOf(t)
	verbs := legalInterventions(state, t.CurrentLane)
	if len(verbs) == 0 {
		return nil
	}
	opts := taskActionOptions{target: pages.TaskDetailAnchor}
	if state == taskInterventionEscalated {
		opts.primary = actionRequeue
	}
	controls := taskActionControls(opts, t.ID.String(), t.Title, returnTo, verbs...)
	switch state {
	case taskInterventionClaimed:
		// An all-zero claim id matches no write, so leave the control unguarded.
		if id := *t.CurrentClaimID; id != uuid.Nil {
			observed := id.String()
			for i := range controls {
				// Both htmx and no-JS halves carry it as the hidden expected_claim_id input.
				controls[i].ObservedClaimID = observed
			}
		}
	case taskInterventionEscalated:
		observed := t.CurrentEscalationID.String()
		for i := range controls {
			controls[i].ObservedField = escalatedGuardField
			controls[i].ObservedID = observed
		}
	}
	return controls
}

// taskDetailActions renders the trigger half of taskDetailControls; nil when no verb applies.
func taskDetailActions(t store.Task, returnTo string) templ.Component {
	controls := taskDetailControls(t, returnTo)
	if controls == nil {
		return nil
	}
	return pages.TaskActionsMenu(controls)
}

// taskDetailActionPopovers renders one reason popover per control, naming the same form as the
// trigger; nil exactly when the actions are.
func taskDetailActionPopovers(t store.Task, returnTo string) templ.Component {
	controls := taskDetailControls(t, returnTo)
	if controls == nil {
		return nil
	}
	return pages.TaskActionPopovers(controls)
}

// taskDetailContainerOf resolves a task's container against the product's delivery listing;
// a failed read returns false.
func (app *App) taskDetailContainerOf(ctx context.Context, pid uuid.UUID, task store.Task) (taskContainer, bool) {
	listing, err := app.spec.Delivery(ctx, pid, nil)
	if err != nil {
		logger.Error("task detail: delivery listing read failed", "product", pid.String(), "error", err)
		return taskContainer{}, false
	}
	return resolveTaskContainer(listing, task.MilestoneID)
}

// taskDetailViewFor composes the detail view. self is the page's address, passed in because an
// intervention POST only knows it from return_to; everything address-bound reads self.
func (app *App) taskDetailViewFor(ctx context.Context, r *http.Request, pid uuid.UUID, task store.Task, c taskContainer, self *url.URL) pages.TaskDetailPage {
	tid := task.ID
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
	} else if cl, ok, err := app.tasks.LatestClaimForTask(ctx, task.ScopeID, tid); err != nil {
		// A failed last-claim read costs only the "last held by" clause.
		logger.Warn("task last-claim read failed", "task", tid.String(), "error", err)
	} else if ok {
		in.LastClaim = &cl
	}
	if task.CurrentEscalationID != nil {
		// A task whose escalation resolved between reads is not actionable; the next render shows it.
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
	// Refresh re-requests the serving path without the tab: the button sits outside the panel and
	// takes the current tab via hx-include at press time.
	page.Path = self.Path
	// The tab comes from the address, so it survives reload, shared links and Back.
	page.Tab = taskDetailTabOf(&http.Request{URL: self})
	page.Tabs = taskDetailTabsOf(self.Path, page.Tab, page.Notes, page.NotesError, page.Deps, page.DepsError)
	// return_to is path and query, so the tab survives; interventionReturnTo validates it on return.
	page.Actions = taskDetailActions(task, self.RequestURI())
	// Popovers must accompany the actions, or every trigger is a dead button.
	page.Popovers = taskDetailActionPopovers(task, self.RequestURI())
	return page
}

// serveTaskDetail is the read-and-render both detail routes share; only the container resolver differs.
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

	page := app.taskDetailViewFor(ctx, r, pid, task, c, r.URL)
	// HX-Target distinguishes a tab click (panel) from Refresh (whole section), so Refresh on
	// ?tab=notes re-renders the section rather than a bare panel.
	if r.Header.Get("HX-Request") != "" && tabSwapRequested(r) {
		renderFragment(w, r, pages.TaskDetailTabs(page))
		return
	}
	body := pages.TaskDetail(page)
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, body)
		return
	}
	app.renderShell(w, r, "Task", r.URL.Path, body)
}

// tabSwapRequested reports whether htmx targeted the tab panel region; checking ?tab= instead
// would make a Refresh on a non-default tab return a bare panel.
func tabSwapRequested(r *http.Request) bool {
	return hxTargetID(r) == pages.TaskPanelAnchor
}

// renderProductTaskDetailNotFound is the in-shell 404 for a task outside this product,
// linking back to the product-wide Tasks page.
func (app *App) renderProductTaskDetailNotFound(w http.ResponseWriter, r *http.Request, pid uuid.UUID) {
	app.renderSpecStatus(w, r, http.StatusNotFound, pages.StatusPage{
		Title:    "Not found",
		Detail:   "No task with that id belongs to this product.",
		BackHref: productHref(pid, tasksSuffix),
		BackText: "Back to tasks",
	})
}
