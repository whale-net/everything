// This file (issue #2727, FR11, FR12, C25) is the work-axis note-recording
// HTTP surface: POST /notes over store.TaskStore.RecordNote, gated behind
// RequireSession (NFR6, gate.go) -- and GET /tasks/{id}/notes over
// store.TaskStore.ListNotesForTask, ungated like every other read endpoint
// in this package, mirroring task_dependency.go's own gated-write/ungated-
// read pair for the same TaskStore-widening shape.
//
// RecordNoteHandler deliberately never checks whether the caller's session
// holds the target task's current claim -- FR11's "any Agent, claimant or
// not" rule -- and never accepts a status/state field in its request body
// (FR12: a note is a flat, immutable append with no lifecycle to set).
package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// recordNoteRequest is RecordNoteHandler's request body (FR11): exactly one
// of TaskID or EntityKind+EntityID must be set, Kind must be one of
// store.NoteKind's fixed enumeration, and Body is required. ScopeID and
// both subjects come from the gated session, never this body (NFR6).
type recordNoteRequest struct {
	TaskID     *string `json:"task_id"`
	EntityKind *string `json:"entity_kind"`
	EntityID   *string `json:"entity_id"`
	Kind       string  `json:"kind"`
	Body       string  `json:"body"`
}

// RecordNoteHandler returns the note-recording endpoint (FR11): POST
// /notes. Must be mounted behind RequireSession (NFR6) -- scope_id and both
// subjects always come from the caller's session, never the request body.
func RecordNoteHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req recordNoteRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		params := store.RecordNoteParams{
			ScopeID:    sess.ScopeID,
			Kind:       store.NoteKind(req.Kind),
			Body:       req.Body,
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		}

		if req.TaskID != nil {
			taskID, err := ParseUUIDField("task_id", *req.TaskID)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			params.TaskID = &taskID
		}
		if req.EntityKind != nil {
			kind := store.NoteEntityKind(*req.EntityKind)
			params.EntityKind = &kind
		}
		if req.EntityID != nil {
			entityID, err := ParseUUIDField("entity_id", *req.EntityID)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			params.EntityID = &entityID
		}

		note, err := tasks.RecordNote(r.Context(), params)
		if err != nil {
			writeNoteStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: note.ID.String()})
	}
}

// writeNoteStoreError maps a RecordNote error onto this package's one JSON
// error shape -- mirrors writeStoreError (types.go), extended with
// RecordNote's own named FR11/FR12 rejections.
func writeNoteStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound),
		errors.Is(err, store.ErrInvalidNoteTarget),
		errors.Is(err, store.ErrEmptyNoteBody),
		errors.Is(err, store.ErrUnknownNoteKind),
		errors.Is(err, store.ErrUnknownNoteEntityKind):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSONError(w, http.StatusInternalServerError, "failed to record note")
	}
}

// NoteWire is one entry of ListTaskNotesHandler's and ListEntityNotes'
// response bodies -- the wire shape of a store.Note row, including its current lifecycle Status
// (M5's C26, issue #2874, FR11) alongside the body wherever a note is
// returned.
type NoteWire struct {
	ID         string  `json:"id"`
	TaskID     *string `json:"task_id,omitempty"`
	EntityKind *string `json:"entity_kind,omitempty"`
	EntityID   *string `json:"entity_id,omitempty"`
	Kind       string  `json:"kind"`
	Body       string  `json:"body"`
	Status     string  `json:"status"`
}

func toNoteResponse(n store.Note) NoteWire {
	resp := NoteWire{
		ID:     n.ID.String(),
		Kind:   string(n.Kind),
		Body:   n.Body,
		Status: string(n.CurrentStatus),
	}
	if n.TaskID != nil {
		id := n.TaskID.String()
		resp.TaskID = &id
	}
	if n.EntityKind != nil {
		kind := string(*n.EntityKind)
		resp.EntityKind = &kind
	}
	if n.EntityID != nil {
		id := n.EntityID.String()
		resp.EntityID = &id
	}
	return resp
}

// listTaskNotesResponse is ListTaskNotesHandler's response body: taskID's
// recorded notes, ordered by CreatedAt.
type listTaskNotesResponse struct {
	Notes []NoteWire `json:"notes"`
}

// ListTaskNotesHandler returns the note-list read endpoint: GET
// /tasks/{id}/notes, ungated like every other read endpoint in this
// package. Resolves {id}'s own ScopeID via store.TaskStore.GetTaskByID
// before calling ListNotesForTask -- a read endpoint has no session to
// source scope_id from, mirroring ListTaskDependenciesHandler's own
// "resolve scope from the entity itself" posture (task_dependency.go).
func ListTaskNotesHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		task, err := tasks.GetTaskByID(r.Context(), taskID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "task not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		notes, err := tasks.ListNotesForTask(r.Context(), task.ScopeID, taskID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		resp := make([]NoteWire, len(notes))
		for i, n := range notes {
			resp[i] = toNoteResponse(n)
		}

		writeJSON(w, http.StatusOK, listTaskNotesResponse{Notes: resp})
	}
}
