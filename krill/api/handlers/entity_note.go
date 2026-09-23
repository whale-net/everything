// Read-back path for notes recorded against a spec-axis entity (FR11):
// GET /{products,feature-sets,features,requirements,load-bearing-decisions}/{id}/notes
// over store.TaskStore.ListNotesForEntity. Ungated like every other read
// endpoint; scope_id is resolved from the entity itself, never caller input.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// NoteEntityScopes resolves a note-target entity's own ScopeID from its
// current row, one store per store.NoteEntityKind.
type NoteEntityScopes struct {
	Products     store.ProductStore
	FeatureSets  store.FeatureSetStore
	Features     store.FeatureStore
	Requirements store.RequirementStore
	Decisions    store.LoadBearingDecisionStore
}

// ScopeOf returns id's ScopeID for kind. Returns store.ErrUnknownNoteEntityKind
// for a kind outside the enumeration and store.ErrNotFound for a missing id.
func (s NoteEntityScopes) ScopeOf(ctx context.Context, kind store.NoteEntityKind, id uuid.UUID) (uuid.UUID, error) {
	switch kind {
	case store.NoteEntityKindProduct:
		row, err := s.Products.GetCurrentByID(ctx, id)
		return row.ScopeID, err
	case store.NoteEntityKindFeatureSet:
		row, err := s.FeatureSets.GetCurrentByID(ctx, id)
		return row.ScopeID, err
	case store.NoteEntityKindFeature:
		row, err := s.Features.GetCurrentByID(ctx, id)
		return row.ScopeID, err
	case store.NoteEntityKindRequirement:
		row, err := s.Requirements.GetCurrentByID(ctx, id)
		return row.ScopeID, err
	case store.NoteEntityKindLoadBearingDecision:
		row, err := s.Decisions.GetCurrentByID(ctx, id)
		return row.ScopeID, err
	default:
		return uuid.Nil, fmt.Errorf("%w: %q", store.ErrUnknownNoteEntityKind, kind)
	}
}

// ListEntityNotesResponse is the response body of GET /{kind}/{id}/notes
// and the list_entity_notes MCP tool: every note on the entity, oldest
// first, regardless of lifecycle status.
type ListEntityNotesResponse struct {
	EntityKind string     `json:"entity_kind"`
	EntityID   string     `json:"entity_id"`
	Notes      []NoteWire `json:"notes"`
}

// ListEntityNotes resolves (kind, id)'s scope and returns every note
// recorded against it. Shared by ListEntityNotesHandler and the MCP tool.
func ListEntityNotes(ctx context.Context, scopes NoteEntityScopes, tasks store.TaskStore, kind store.NoteEntityKind, id uuid.UUID) (ListEntityNotesResponse, error) {
	scopeID, err := scopes.ScopeOf(ctx, kind, id)
	if err != nil {
		return ListEntityNotesResponse{}, err
	}
	notes, err := tasks.ListNotesForEntity(ctx, scopeID, kind, id)
	if err != nil {
		return ListEntityNotesResponse{}, err
	}
	resp := ListEntityNotesResponse{
		EntityKind: string(kind),
		EntityID:   id.String(),
		Notes:      make([]NoteWire, len(notes)),
	}
	for i, n := range notes {
		resp.Notes[i] = toNoteResponse(n)
	}
	return resp, nil
}

// ListEntityNotesHandler returns GET /{kind-route}/{id}/notes for one fixed
// entity kind; routes.go mounts one per store.NoteEntityKind.
func ListEntityNotesHandler(kind store.NoteEntityKind, scopes NoteEntityScopes, tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		resp, err := ListEntityNotes(r.Context(), scopes, tasks, kind, id)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, resp)
		case errors.Is(err, store.ErrUnknownNoteEntityKind):
			writeJSONError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, store.ErrNotFound):
			writeJSONError(w, http.StatusNotFound, fmt.Sprintf("%s not found", kind))
		default:
			writeJSONError(w, http.StatusInternalServerError, "internal error")
		}
	}
}
