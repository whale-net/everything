// This file (issue #2941) is the work-axis per-milestone task discovery
// surface: GET /milestones/{id}/tasks lists every task scoped to one
// milestone_ref row (a milepebble, or a milestone with no milepebble cut),
// so a caller can find task ids and their lane/claim state without already
// knowing them. Ungated like every other read endpoint in this package.
package handlers

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// TaskSummaryWire is the wire shape of one store.TaskSummary. Exported so
// krill/mcp/tools' list_tasks tool returns this exact shape (LB7).
type TaskSummaryWire struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CurrentLane  string `json:"current_lane"`
	AttemptCount int    `json:"attempt_count"`
	HasLiveClaim bool   `json:"has_live_claim"`
}

// ListTasksResponse is ListTasksHandler's response body.
type ListTasksResponse struct {
	Tasks []TaskSummaryWire `json:"tasks"`
}

// NewListTasksResponse maps store.TaskStore.ListTasksByMilestone's result
// onto its wire shape -- shared by ListTasksHandler and the list_tasks MCP
// tool.
func NewListTasksResponse(tasks []store.TaskSummary) ListTasksResponse {
	wire := make([]TaskSummaryWire, len(tasks))
	for i, t := range tasks {
		wire[i] = TaskSummaryWire{
			ID:           t.ID.String(),
			Title:        t.Title,
			CurrentLane:  string(t.CurrentLane),
			AttemptCount: t.AttemptCount,
			HasLiveClaim: t.HasLiveClaim,
		}
	}
	return ListTasksResponse{Tasks: wire}
}

// ListTasksHandler returns the per-milestone task list endpoint: GET
// /milestones/{id}/tasks. Mirrors ListMilepebblesHandler's shape
// (milestone.go).
func ListTasksHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		milestoneID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		list, err := tasks.ListTasksByMilestone(r.Context(), milestoneID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, NewListTasksResponse(list))
	}
}
