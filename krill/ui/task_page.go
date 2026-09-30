// The read-only per-milestone/milepebble task list, reached from the
// delivery page. Reads through app.tasks / app.spec.
package main

import (
	"net/http"

	"github.com/google/uuid"
)

func milestoneTasksPath(pid, mid uuid.UUID) string {
	return productPath(pid) + "/milestones/" + mid.String() + "/tasks"
}

func milestoneBoardPath(pid, mid uuid.UUID) string {
	return productPath(pid) + "/milestones/" + mid.String() + "/board"
}

func taskDetailPath(pid, mid, tid uuid.UUID) string {
	return milestoneTasksPath(pid, mid) + "/" + tid.String()
}

// handleTaskList renders a milestone's or milepebble's task list.
// Scaffold stub; implemented in the next phase.
func (app *App) handleTaskList(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
