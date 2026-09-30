// The read-only five-lane task board, reached from the delivery page and
// the task list. Reads through app.tasks / app.spec.
package main

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// taskBoardPageOf assembles the board view model. Implementation phase:
// five columns in store.CanonicalLaneOrder, cards placed by current lane.
func taskBoardPageOf(pid uuid.UUID, c taskContainer, tasks []store.TaskSummary, readErr error, now time.Time) pages.TaskBoardPage {
	return pages.TaskBoardPage{
		Path:         milestoneBoardPath(pid, c.ID),
		Name:         c.Name,
		LoadedAt:     now.UTC().Format(time.RFC3339),
		TasksPath:    milestoneTasksPath(pid, c.ID),
		DeliveryPath: deliveryPath(pid),
	}
}

// handleTaskBoard renders a milestone's or milepebble's task board.
func (app *App) handleTaskBoard(w http.ResponseWriter, r *http.Request) {
	pid, c, ok := app.resolveTaskRoute(w, r)
	if !ok {
		return
	}
	var tasks []store.TaskSummary
	var readErr error
	if len(c.Milepebbles) == 0 {
		tasks, readErr = app.tasks.ListTasksByMilestone(r.Context(), c.ID)
		if readErr != nil {
			logger.Error("task board read failed", "container", c.ID.String(), "error", readErr)
		}
	}
	body := pages.TaskBoard(taskBoardPageOf(pid, c, tasks, readErr, time.Now()))
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, body)
		return
	}
	renderShell(w, r, "Task board", specPath, body)
}
