// Unit tests for ListEntityNotesHandler/NoteEntityScopes (entity_note.go):
// scope is resolved from the entity itself, every note is returned
// regardless of lifecycle status, and bad ids/kinds map to 400/404. No
// Postgres -- in-memory fakes stand in for the stores.
package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// noteScopeProducts/... override only GetCurrentByID; other methods panic
// via the nil embedded interface.
type noteScopeProducts struct {
	store.ProductStore
	scopes map[uuid.UUID]uuid.UUID
}

func (f noteScopeProducts) GetCurrentByID(_ context.Context, id uuid.UUID) (store.Product, error) {
	scope, ok := f.scopes[id]
	if !ok {
		return store.Product{}, store.ErrNotFound
	}
	return store.Product{ID: id, ScopeID: scope}, nil
}

type noteScopeFeatureSets struct {
	store.FeatureSetStore
	scopes map[uuid.UUID]uuid.UUID
}

func (f noteScopeFeatureSets) GetCurrentByID(_ context.Context, id uuid.UUID) (store.FeatureSet, error) {
	scope, ok := f.scopes[id]
	if !ok {
		return store.FeatureSet{}, store.ErrNotFound
	}
	return store.FeatureSet{ID: id, ScopeID: scope}, nil
}

type noteScopeFeatures struct {
	store.FeatureStore
	scopes map[uuid.UUID]uuid.UUID
}

func (f noteScopeFeatures) GetCurrentByID(_ context.Context, id uuid.UUID) (store.Feature, error) {
	scope, ok := f.scopes[id]
	if !ok {
		return store.Feature{}, store.ErrNotFound
	}
	return store.Feature{ID: id, ScopeID: scope}, nil
}

type noteScopeRequirements struct {
	store.RequirementStore
	scopes map[uuid.UUID]uuid.UUID
}

func (f noteScopeRequirements) GetCurrentByID(_ context.Context, id uuid.UUID) (store.Requirement, error) {
	scope, ok := f.scopes[id]
	if !ok {
		return store.Requirement{}, store.ErrNotFound
	}
	return store.Requirement{ID: id, ScopeID: scope}, nil
}

type noteScopeDecisions struct {
	store.LoadBearingDecisionStore
	scopes map[uuid.UUID]uuid.UUID
}

func (f noteScopeDecisions) GetCurrentByID(_ context.Context, id uuid.UUID) (store.LoadBearingDecision, error) {
	scope, ok := f.scopes[id]
	if !ok {
		return store.LoadBearingDecision{}, store.ErrNotFound
	}
	return store.LoadBearingDecision{ID: id, ScopeID: scope}, nil
}

// noteScopesWith returns a NoteEntityScopes where every kind resolves id
// to scopeID.
func noteScopesWith(id, scopeID uuid.UUID) handlers.NoteEntityScopes {
	m := map[uuid.UUID]uuid.UUID{id: scopeID}
	return handlers.NoteEntityScopes{
		Products:     noteScopeProducts{scopes: m},
		FeatureSets:  noteScopeFeatureSets{scopes: m},
		Features:     noteScopeFeatures{scopes: m},
		Requirements: noteScopeRequirements{scopes: m},
		Decisions:    noteScopeDecisions{scopes: m},
	}
}

func serveEntityNotes(t *testing.T, kind store.NoteEntityKind, scopes handlers.NoteEntityScopes, tasks *fakeTaskStore, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /entities/{id}/notes", handlers.ListEntityNotesHandler(kind, scopes, tasks))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestListEntityNotesHandler_EveryKind_ResolvesScopeAndReturnsNotes proves
// each of the five kinds resolves scope from its own store and passes it
// through, and that non-"noted" statuses are still returned.
func TestListEntityNotesHandler_EveryKind_ResolvesScopeAndReturnsNotes(t *testing.T) {
	for _, kind := range []store.NoteEntityKind{
		store.NoteEntityKindProduct,
		store.NoteEntityKindFeatureSet,
		store.NoteEntityKindFeature,
		store.NoteEntityKindRequirement,
		store.NoteEntityKindLoadBearingDecision,
	} {
		t.Run(string(kind), func(t *testing.T) {
			entityID, scopeID := uuid.New(), uuid.New()
			first, second := uuid.New(), uuid.New()
			k := kind
			tasks := &fakeTaskStore{notesForEntity: []store.Note{
				{ID: first, ScopeID: scopeID, EntityKind: &k, EntityID: &entityID, Kind: store.NoteKindComment, Body: "round 1", CurrentStatus: store.NoteLifecycleStatusNoted},
				{ID: second, ScopeID: scopeID, EntityKind: &k, EntityID: &entityID, Kind: store.NoteKindScopeNote, Body: "round 2", CurrentStatus: store.NoteLifecycleStatusClosed},
			}}

			rec := serveEntityNotes(t, kind, noteScopesWith(entityID, scopeID), tasks, "/entities/"+entityID.String()+"/notes")

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, scopeID, tasks.gotListNotesForEntityScopeID, "scope must come from the entity's own row")
			assert.Equal(t, kind, tasks.gotListNotesForEntityKind)
			assert.Equal(t, entityID, tasks.gotListNotesForEntityID)

			var resp handlers.ListEntityNotesResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, string(kind), resp.EntityKind)
			assert.Equal(t, entityID.String(), resp.EntityID)
			require.Len(t, resp.Notes, 2)
			assert.Equal(t, first.String(), resp.Notes[0].ID)
			assert.Equal(t, "round 1", resp.Notes[0].Body)
			assert.Equal(t, "noted", resp.Notes[0].Status)
			assert.Equal(t, second.String(), resp.Notes[1].ID)
			assert.Equal(t, "scope-note", resp.Notes[1].Kind)
			assert.Equal(t, "closed", resp.Notes[1].Status)
		})
	}
}

func TestListEntityNotesHandler_NoNotes_ReturnsEmptyArray(t *testing.T) {
	entityID := uuid.New()
	rec := serveEntityNotes(t, store.NoteEntityKindFeatureSet, noteScopesWith(entityID, uuid.New()), &fakeTaskStore{}, "/entities/"+entityID.String()+"/notes")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"entity_kind": "feature_set", "entity_id": "`+entityID.String()+`", "notes": []}`, rec.Body.String())
}

func TestListEntityNotesHandler_InvalidID_Returns400(t *testing.T) {
	tasks := &fakeTaskStore{}
	rec := serveEntityNotes(t, store.NoteEntityKindProduct, noteScopesWith(uuid.New(), uuid.New()), tasks, "/entities/not-a-uuid/notes")

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotListNotesForEntityID, "an invalid id must never reach the store")
}

func TestListEntityNotesHandler_UnknownEntity_Returns404(t *testing.T) {
	tasks := &fakeTaskStore{}
	rec := serveEntityNotes(t, store.NoteEntityKindRequirement, noteScopesWith(uuid.New(), uuid.New()), tasks, "/entities/"+uuid.NewString()+"/notes")

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotListNotesForEntityID, "a missing entity must never reach the note query")
}

// milestone has no note target, so it is rejected as an unknown kind.
func TestListEntityNotesHandler_UnknownKind_Returns400(t *testing.T) {
	entityID := uuid.New()
	rec := serveEntityNotes(t, store.NoteEntityKind("milestone"), noteScopesWith(entityID, uuid.New()), &fakeTaskStore{}, "/entities/"+entityID.String()+"/notes")

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestListEntityNotesHandler_StoreError_Returns500(t *testing.T) {
	entityID := uuid.New()
	tasks := &fakeTaskStore{listNotesForEntityErr: errors.New("boom")}
	rec := serveEntityNotes(t, store.NoteEntityKindProduct, noteScopesWith(entityID, uuid.New()), tasks, "/entities/"+entityID.String()+"/notes")

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
}
