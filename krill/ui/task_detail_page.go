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
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
	"github.com/whale-net/everything/libs/go/htmxui"
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

	// LastClaim is the task's most recent claim row, read only when the
	// task holds none. GetClaimByID resolves task.current_claim_id alone,
	// which is NULL once the claim is released, so this is the only read
	// that can answer "None. Last held by X" -- the question the rail asks
	// of every task nobody is working on.
	LastClaim *store.Claim
}

// taskDetailTabKeys is the fixed, ordered set of facet tabs. It is a
// literal, not a map's keys, because the strip's ORDER is part of the
// page: Overview, Notes, Dependencies, Spec slice.
var taskDetailTabKeys = []string{
	pages.TaskTabOverview,
	pages.TaskTabNotes,
	pages.TaskTabDependencies,
	pages.TaskTabSlice,
}

// taskDetailTabLabels is each tab's operator-facing name. "Spec slice"
// rather than the URL value "slice", which is the wire spelling and not
// what a reader is shown.
var taskDetailTabLabels = map[string]string{
	pages.TaskTabOverview:     "Overview",
	pages.TaskTabNotes:        "Notes",
	pages.TaskTabDependencies: "Dependencies",
	pages.TaskTabSlice:        "Spec slice",
}

// taskDetailTabOf resolves the request's ?tab= to one of the four tab
// keys.
//
// An ABSENT value and an UNRECOGNISED one both resolve to overview. That
// is the whole degradation rule: the tab is URL-carried, so a hand-edited
// or stale link reaches this page as readily as a copied one, and a
// value this build does not know must render the page rather than 404 or
// render an empty panel. Nothing here errors.
func taskDetailTabOf(r *http.Request) string {
	tab := r.URL.Query().Get("tab")
	for _, known := range taskDetailTabKeys {
		if tab == known {
			return known
		}
	}
	return pages.TaskTabOverview
}

// taskDetailTabHref is one tab's own URL: the page's own path with the
// tab applied.
//
// Overview's href is the bare path, with no ?tab= at all -- the default
// state is the page's own address rather than a parameter that spells out
// the absence of a choice, so the address an operator shares, bookmarks
// or copies is the shortest true one.
//
// path is the request's own path, so this works for both detail routes
// (the product-scoped one and the pre-redesign per-container one) without
// either being named here.
func taskDetailTabHref(path, tab string) string {
	if tab == pages.TaskTabOverview {
		return path
	}
	return path + "?tab=" + tab
}

// taskDetailTabsOf builds the strip over the request's own path, marking
// active the tab the URL resolved to.
//
// The Notes and Dependencies counts are the lengths of the lists the
// panels render -- the SAME reads, already taken once by the caller. A
// second read here would be a second number that could disagree with the
// list an operator is about to click into, and the whole point of the
// count is to agree with it.
//
// A failed read carries NO count rather than zero. Zero is a claim about
// the list, and we could not read the list; a tab with no badge says
// nothing, which is the honest state (README's "never render a read
// failure as an empty view", applied to a count).
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
	// The rail's Milepebble row exists only for a task that sits on one.
	// An uncut milestone's task would otherwise name its own container
	// again, in the one place on the page where a second naming of it is
	// not also a link the breadcrumb already offers.
	if c.Kind == string(store.MilestoneKindMilepebble) {
		page.MilepebbleName = c.Name
		page.MilepebblePath = page.TasksPath
	}
	if t.Body != nil {
		page.Body = *t.Body
	}
	if t.CurrentEscalationID != nil {
		page.Escalation = "escalation " + t.CurrentEscalationID.String()
		if in.Escalation != nil {
			page.EscalationReason = string(in.Escalation.Reason)
			page.EscalatedAt = in.Escalation.CreatedAt.UTC().Format(time.RFC3339)
			// The two automatic reasons carry the counter that tripped
			// them and the cap it tripped against; manual carries
			// neither. The store's CHECK refuses a mismatched pair, so
			// one flag covers both-or-neither.
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
		// Unclaimed, but not untouched: the session that held the task
		// last is the whole answer to "why is nobody on this?", and
		// GetClaimByID cannot supply it because the claim is released.
		page.LastClaimHolder = in.LastClaim.SessionID.String()
	}
	if t.LeaseExpiresAt != nil {
		page.LeaseExpiresAt = t.LeaseExpiresAt.UTC().Format(time.RFC3339)
	}
	if in.DepsErr != nil {
		page.DepsError = "The dependencies could not be read. See the logs."
	}
	for _, d := range in.Deps {
		// The product-scoped detail is the only address a dependency needs:
		// a dependency may sit on any container under the product, so the
		// retired per-container form would name the wrong one and cost a
		// redirect to reach the same page.
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
	// The Overview shows the TAIL of that list -- ListNotesForTask already
	// returned them oldest-first, so the most recent are at the end. The
	// full list stays on page.Notes for the Notes tab; the two are the
	// same notes, and the Overview names how many it kept so a truncated
	// list is never read as the whole one.
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

// taskDetailCallouts composes the Overview panel's explanation banners for
// the three states that mean a task is not simply in flight: escalated, at
// the attempt cap, and holding a lapsed lease.
//
// It returns EMPTY for a healthy task, and that is the important case. A
// banner that appeared for every task would teach an operator to read past
// it, and the escalation that genuinely needs them would be the third
// thing on the page they had already learned to skip. There is no neutral
// "nothing is wrong" state here -- the absence of a banner IS that state.
//
// The three are independent and can co-occur: a task can be escalated AND
// at the cap AND hold an expired lease. Each is named on its own line
// rather than merged, because each carries a different counter and a
// different fix, and a merged sentence would pick one of the three to be
// the headline.
//
// Severity follows components.TaskStateStyle's mapping, so a banner and the
// task's own state badge are the same colour: escalated is the state that
// needs a human and is an error, while being capped or holding a lapsed
// lease is the system catching up on its own bookkeeping and is a warning.
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

// escalationCallout is the banner for an escalated task, worded by the
// event's own reason.
//
// The reason is the whole point of the banner, so it names which of the
// three fired and -- for the two automatic ones -- the counter that tripped
// it and the cap it tripped against. A task escalated with no event behind
// it (the read failed) still gets a banner, because the task row's claim
// that it is escalated is true even when the reason could not be read; that
// banner says so rather than inventing a reason.
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
		// A manual escalation is the one reason with no triggering
		// counter at all, so there is no number to state -- the reason
		// is the whole explanation.
		return pages.TaskCallout{
			Key:     pages.TaskCalloutEscalated,
			Variant: htmxui.AlertError,
			Message: "Escalated (" + label + "), so no worker can claim it. Requeue returns it to the claimable queue.",
		}, true
	}
	// Each reason trips its OWN counter, and they are independent
	// bookkeeping: naming the counter rather than saying "after n" is what
	// keeps a thrash-cap banner from reading as though n were attempts.
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

// escalationCounterName is the counter a requeue resets for this reason --
// the same mapping RequeueTask's ResetCounter implements.
func escalationCounterName(reason string) string {
	if reason == string(store.EscalationReasonThrashCap) {
		return "thrash counter"
	}
	return "attempt counter"
}

// calloutPluralN picks the noun form for n, so "1 failing verdict" is not
// "1 failing verdicts".
func calloutPluralN(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
	} else if cl, ok, err := app.tasks.LatestClaimForTask(ctx, task.ScopeID, tid); err != nil {
		// The rail still renders its Claim row -- it just cannot say who
		// held the task last. A read failure on a convenience clause must
		// not cost the operator the page.
		logger.Warn("task last-claim read failed", "task", tid.String(), "error", err)
	} else if ok {
		in.LastClaim = &cl
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
	//
	// The path alone, deliberately: the tab is NOT baked in here. The
	// button sits outside the panel region, so a tab click never
	// re-renders it, and a value fixed at page-load time is the value
	// the page was LOADED with -- not the tab the operator is on. The
	// button takes the tab from the panel region at press time instead
	// (see the refresh button's hx-include).
	page.Path = r.URL.Path
	// The tab is resolved from the URL, and the strip is built over the
	// page's own path with it applied -- so a tab survives a reload, a
	// shared link and Back, and an unknown value renders the Overview
	// rather than failing (FR 7e463e31).
	page.Tab = taskDetailTabOf(r)
	page.Tabs = taskDetailTabsOf(r.URL.Path, page.Tab, page.Notes, page.NotesError, page.Deps, page.DepsError)
	// A tab click and a Refresh are the same route asked for different
	// things. HX-Target is what tells them apart: the tabs target the
	// panel region, the Refresh button targets the whole detail section.
	// Reading the target rather than guessing from the URL keeps the two
	// unambiguous -- a Refresh on ?tab=notes must re-render the whole
	// section, not splice a bare panel into it.
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

// tabSwapRequested reports whether this htmx request asked for the tab
// panel region specifically.
//
// htmx sends the resolved target's id in HX-Target, so the target is the
// request's own statement of which region it is replacing. A tab names
// the panel; the Refresh button names the whole section. Deciding on
// anything else -- the presence of ?tab=, say -- would make a Refresh
// taken while a non-default tab is open serve a bare panel, which the
// section swap would then splice in beside the page.
func tabSwapRequested(r *http.Request) bool {
	return r.Header.Get("HX-Target") == pages.TaskPanelAnchor
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
