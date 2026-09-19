// This file (issue #2719, FR1, C14) is the work-axis task-create HTTP
// surface: POST /tasks over store.TaskStore.CreateTask
// (krill/store/task.go). Must be mounted behind RequireSession (gate.go)
// once wired -- the LB4 subject pair and scope_id always come from the
// caller's session, never the request body (NFR6).
//
// Scaffold stage: the handler body is a stub returning 501; routes.go
// wiring and the store-backed body land in this issue's Implementation
// phase.
package handlers

import "net/http"

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

// CreateTaskHandler will return the task-create endpoint (FR1): POST
// /tasks. Scaffold stub -- see this file's doc comment.
func CreateTaskHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotImplemented, "CreateTaskHandler: not implemented -- see issue #2719's Implementation phase")
	}
}
