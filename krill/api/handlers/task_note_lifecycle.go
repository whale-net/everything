// This file (issue #2874, root plan #2851, FR11, C25) is M5's note-
// lifecycle transition HTTP surface: POST /notes/{id}/lifecycle over
// store.TaskStore.TransitionNoteLifecycle, gated behind RequireSession
// (NFR6, gate.go) like every other write endpoint in this package --
// mirroring task_note.go's own RecordNoteHandler for the sibling
// TaskStore-widening shape. Deliberately NOT operator-only: FR11 says "any
// persona" may transition a note's lifecycle, so this handler is gated
// identically to RecordNoteHandler (the session requirement alone), never
// wrapped in an operator-only check the way console.go's queries will be.
package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// transitionNoteLifecycleRequest is TransitionNoteLifecycleHandler's
// request body (FR11): the destination Status, one of
// store.NoteLifecycleStatus's fixed enumeration. ScopeID and both subjects
// come from the gated session, never this body (NFR6).
type transitionNoteLifecycleRequest struct {
	Status string `json:"status"`
}

// TransitionNoteLifecycleHandler returns the note-lifecycle transition
// endpoint (FR11): POST /notes/{id}/lifecycle. Must be mounted behind
// RequireSession (NFR6) -- scope_id and both subjects always come from the
// caller's session, never the request body. Open to any persona holding a
// valid session -- never claim- or ownership-gated, mirroring
// store.TaskStore.TransitionNoteLifecycle's own posture.
func TransitionNoteLifecycleHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		noteID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req transitionNoteLifecycleRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		event, err := tasks.TransitionNoteLifecycle(r.Context(), store.TransitionNoteLifecycleParams{
			ScopeID:    sess.ScopeID,
			NoteID:     noteID,
			Status:     store.NoteLifecycleStatus(req.Status),
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		})
		if err != nil {
			writeNoteLifecycleStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: event.ID.String()})
	}
}

// writeNoteLifecycleStoreError maps a TransitionNoteLifecycle error onto
// this package's one JSON error shape -- mirrors writeNoteStoreError
// (task_note.go).
func writeNoteLifecycleStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound),
		errors.Is(err, store.ErrUnknownNoteLifecycleStatus):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSONError(w, http.StatusInternalServerError, "failed to transition note lifecycle")
	}
}
