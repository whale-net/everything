// This file (issue #2719, FR1, C14) is the work-axis task-create HTTP
// surface: POST /tasks over store.TaskStore.CreateTask
// (krill/store/task.go). Mounted behind RequireSession (gate.go) in
// routes.go -- the LB4 subject pair and scope_id always come from the
// caller's session (sess.Acting/sess.OnBehalfOf/sess.ScopeID), never the
// request body (NFR6).
package handlers

import (
	"fmt"
	"net/http"

	"github.com/whale-net/everything/krill/store"
)

// createTaskRequest is CreateTaskHandler's request body (FR1).
// MilestoneID names either a milepebble or a milestone with no milepebble
// cut (NFR7/LB6) -- CreateTask resolves which. LaneSequence/StartingLane
// mirror store.Lane's fixed five-value vocabulary as wire strings.
type createTaskRequest struct {
	MilestoneID  string   `json:"milestone_id"`
	Title        string   `json:"title"`
	Body         *string  `json:"body"`
	LaneSequence []string `json:"lane_sequence"`
	StartingLane string   `json:"starting_lane"`
}

// CreateTaskHandler returns the task-create endpoint (FR1): POST /tasks.
// Must be mounted behind RequireSession.
func CreateTaskHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req createTaskRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		milestoneID, err := ParseUUIDField("milestone_id", req.MilestoneID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := RequireNonEmpty("title", req.Title); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		laneSequence := make([]store.Lane, len(req.LaneSequence))
		for i, l := range req.LaneSequence {
			laneSequence[i] = store.Lane(l)
		}

		task, err := tasks.CreateTask(r.Context(), store.CreateTaskParams{
			ScopeID:      sess.ScopeID,
			MilestoneID:  milestoneID,
			Title:        req.Title,
			Body:         req.Body,
			LaneSequence: laneSequence,
			StartingLane: store.Lane(req.StartingLane),
			Acting:       sess.Acting,
			OnBehalfOf:   sess.OnBehalfOf,
		})
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: task.ID.String()})
	}
}
