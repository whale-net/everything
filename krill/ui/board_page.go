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

// taskBoardPageOf assembles the board view model: five columns in
// store.CanonicalLaneOrder, each task a card in its current-lane column
// only. A non-nil readErr yields an error view, never empty columns.
func taskBoardPageOf(pid uuid.UUID, c taskContainer, tasks []store.TaskSummary, readErr error, now time.Time) pages.TaskBoardPage {
	page := pages.TaskBoardPage{
		Path:         milestoneBoardPath(pid, c.ID),
		Name:         c.Name,
		LoadedAt:     now.UTC().Format(time.RFC3339),
		TasksPath:    milestoneTasksPath(pid, c.ID),
		DeliveryPath: deliveryPath(pid),
	}
	if readErr != nil {
		page.Error = "The tasks could not be read. See the logs."
		return page
	}
	if len(c.Milepebbles) > 0 {
		for _, mp := range c.Milepebbles {
			page.Milepebbles = append(page.Milepebbles, pages.TaskMilepebbleLink{
				Name:      mp.Name,
				TasksPath: milestoneTasksPath(pid, mp.ID),
				BoardPath: milestoneBoardPath(pid, mp.ID),
			})
		}
		return page
	}
	page.Columns = make([]pages.BoardColumn, 0, len(store.CanonicalLaneOrder))
	for _, lane := range store.CanonicalLaneOrder {
		col := pages.BoardColumn{Lane: string(lane)}
		for _, t := range tasks {
			if t.CurrentLane == lane {
				col.Cards = append(col.Cards, taskRowOf(pid, c.ID, t, now))
			}
		}
		col.Count = len(col.Cards)
		page.Columns = append(page.Columns, col)
	}
	return page
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
	app.renderShell(w, r, "Task board", r.URL.Path, body)
}
