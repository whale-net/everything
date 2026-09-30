// The read-only task detail page, reached from the task list. Composes
// store.GetTaskByID + ListDependencies + ListNotesForTask and the task's
// spec slice; issues no write.
package main

import (
	"net/http"

	"github.com/whale-net/everything/krill/ui/pages"
)

// handleTaskDetail renders one task's detail (scaffold: not yet implemented).
func (app *App) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Task", specPath, pages.TaskDetail(pages.TaskDetailPage{}))
}
