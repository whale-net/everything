// Internal (white-box) tests for Slice.handle (slice.go, issue #2491's
// Testing section: "Handler tests for the four endpoints"). All four
// registered routes -- GetFeatureSetSlice, GetFeatureSlice,
// GetRequirementSlice, GetProductSlice -- share this one adapter, so its
// id-parsing and error-mapping behavior is tested once here, generically,
// rather than reproving the same parse/call/encode sequence four times;
// slice_test.go (external, package handlers_test) separately proves all
// four routes in Register are actually wired to it. package handlers (not
// handlers_test) because sliceQueryFunc and handle are unexported.
package handlers

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

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// doSliceRequest drives one GET request through (&Slice{}).handle(query),
// with r.PathValue("id") set to idPathValue exactly as routes.go's
// "GET /slices/.../{id}" patterns would populate it.
func doSliceRequest(t *testing.T, query sliceQueryFunc, idPathValue string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /probe/{id}", (&Slice{}).handle(query))

	req := httptest.NewRequest(http.MethodGet, "/probe/"+idPathValue, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestSliceHandle_InvalidID_Returns400 proves a non-UUID path value is
// rejected before query is ever called.
func TestSliceHandle_InvalidID_Returns400(t *testing.T) {
	called := false
	query := func(ctx context.Context, id uuid.UUID) (slice.Document, error) {
		called = true
		return slice.Document{}, nil
	}

	rec := doSliceRequest(t, query, "not-a-uuid")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, called, "query must never be called for a malformed id")
}

// TestSliceHandle_StoreNotFound_Returns404 proves store.ErrNotFound maps to
// 404, not a generic 500.
func TestSliceHandle_StoreNotFound_Returns404(t *testing.T) {
	query := func(ctx context.Context, id uuid.UUID) (slice.Document, error) {
		return slice.Document{}, store.ErrNotFound
	}

	rec := doSliceRequest(t, query, uuid.NewString())

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestSliceHandle_OtherError_Returns500 proves a non-ErrNotFound failure
// maps to 500, distinct from the not-found case above.
func TestSliceHandle_OtherError_Returns500(t *testing.T) {
	query := func(ctx context.Context, id uuid.UUID) (slice.Document, error) {
		return slice.Document{}, errors.New("boom")
	}

	rec := doSliceRequest(t, query, uuid.NewString())

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// TestSliceHandle_Success_EncodesDocument proves the success path both
// requests a 200 and writes the exact slice.Document query returned,
// carrying the requested id's own EntityRef through untouched.
func TestSliceHandle_Success_EncodesDocument(t *testing.T) {
	id := uuid.New()
	revisionID := uuid.New()
	var gotID uuid.UUID

	query := func(ctx context.Context, reqID uuid.UUID) (slice.Document, error) {
		gotID = reqID
		return slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Requirements: []slice.RequirementEntity{{
				EntityRef: slice.EntityRef{ID: reqID, RevisionID: revisionID},
				Kind:      "FR",
				Name:      "FR1",
			}},
		}, nil
	}

	rec := doSliceRequest(t, query, id.String())

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, id, gotID, "the parsed path id must be passed through to query untouched")

	var doc slice.Document
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, slice.SchemaVersion, doc.SchemaVersion)
	require.Len(t, doc.Requirements, 1)
	assert.Equal(t, id, doc.Requirements[0].ID)
	assert.Equal(t, revisionID, doc.Requirements[0].RevisionID)
}
