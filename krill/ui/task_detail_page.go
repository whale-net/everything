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

func taskDetailNotFound(w http.ResponseWriter, r *http.Request, c taskContainer, pid uuid.UUID) {
	renderSpecStatus(w, r, http.StatusNotFound, pages.StatusPage{
		Title:    "Not found",
		Detail:   "No task with that id belongs to this milestone or milepebble.",
		BackHref: milestoneTasksPath(pid, c.ID),
		BackText: "Back to " + c.Name + " tasks",
	})
}

// taskDetailInputs are the reads composed into one detail view; each
// section carries its own error so a partial failure stays visible.
type taskDetailInputs struct {
	Task      store.Task
	Claim     *store.Claim
	Deps      []store.TaskDependency
	DepTasks  map[uuid.UUID]store.Task
	DepsErr   error
	Notes     []store.Note
	NotesErr  error
	Slice     slice.Document
	SliceErr  error
}

func taskState(t store.Task, now time.Time) string {
	switch {
	case t.CancelledAt != nil:
		return "cancelled"
	case t.CurrentEscalationID != nil:
		return "escalated"
	case t.CurrentClaimID != nil && t.LeaseExpiresAt != nil && !t.LeaseExpiresAt.After(now):
		return "lease expired"
	}
	return "active"
}

// taskDetailPageOf assembles the detail view model. now is injected so a
// lease's expiry is judged against the read time.
func taskDetailPageOf(pid uuid.UUID, c taskContainer, in taskDetailInputs, now time.Time) pages.TaskDetailPage {
	t := in.Task
	summary := store.TaskSummary{
		ID: t.ID, Title: t.Title, CurrentLane: t.CurrentLane, AttemptCount: t.AttemptCount,
		CurrentClaimID: t.CurrentClaimID, LeaseExpiresAt: t.LeaseExpiresAt,
		CurrentEscalationID: t.CurrentEscalationID, CancelledAt: t.CancelledAt,
	}
	page := pages.TaskDetailPage{
		Path:          taskDetailPath(pid, c.ID, t.ID),
		ID:            t.ID.String(),
		Title:         t.Title,
		Lane:          string(t.CurrentLane),
		Attempts:      taskAttemptsLabel(t.AttemptCount),
		LoadedAt:      now.UTC().Format(time.RFC3339),
		Badges:        taskStateBadges(summary, now),
		State:         taskState(t, now),
		TasksPath:     milestoneTasksPath(pid, c.ID),
		BoardPath:     milestoneBoardPath(pid, c.ID),
		ContainerName: c.Name,
	}
	if t.Body != nil {
		page.Body = *t.Body
	}
	if t.CurrentEscalationID != nil {
		page.Escalation = "escalation " + t.CurrentEscalationID.String()
	}
	for _, l := range t.LaneSequence {
		page.LaneSequence = append(page.LaneSequence, string(l))
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

// handleTaskDetail renders one task's detail.
func (app *App) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	pid, c, ok := app.resolveTaskRoute(w, r)
	if !ok {
		return
	}
	tid, err := uuid.Parse(r.PathValue("tid"))
	if err != nil {
		taskDetailNotFound(w, r, c, pid)
		return
	}
	ctx := r.Context()
	task, err := app.tasks.GetTaskByID(ctx, tid)
	if err != nil || task.MilestoneID != c.ID {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			logger.Error("task read failed", "task", tid.String(), "error", err)
			renderSpecStatus(w, r, http.StatusInternalServerError, pages.StatusPage{
				Title:    "Could not load the task",
				Detail:   "The task could not be read. See the logs.",
				BackHref: milestoneTasksPath(pid, c.ID),
				BackText: "Back to " + c.Name + " tasks",
			})
			return
		}
		taskDetailNotFound(w, r, c, pid)
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
	if sr, ok := app.spec.(taskSliceReader); ok {
		in.Slice, in.SliceErr = sr.MilestoneDeliversSlice(ctx, task.MilestoneID)
	} else {
		in.SliceErr = fmt.Errorf("spec reader has no task slice read")
	}
	if in.SliceErr != nil {
		logger.Error("task slice read failed", "task", tid.String(), "error", in.SliceErr)
	}

	body := pages.TaskDetail(taskDetailPageOf(pid, c, in, time.Now()))
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, body)
		return
	}
	renderShell(w, r, "Task", specPath, body)
}
