// POST /notes/{id}/amend over store.TaskStore.AmendNote: appends a new note
// row superseding {id}; the old row is retained. Session-gated like
// RecordNoteHandler; scope and subjects come from the session.
package handlers

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

type amendNoteRequest struct {
	Body string `json:"body"`
}

// AmendNoteHandler returns POST /notes/{id}/amend. Must be mounted behind
// RequireSession.
func AmendNoteHandler(tasks store.TaskStore) http.HandlerFunc {
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
		var req amendNoteRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		note, err := tasks.AmendNote(r.Context(), store.AmendNoteParams{
			ScopeID:    sess.ScopeID,
			NoteID:     noteID,
			Body:       req.Body,
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		})
		if err != nil {
			writeNoteStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, IDResponse{ID: note.ID.String()})
	}
}
