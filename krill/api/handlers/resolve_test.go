// Unit tests for the resolve endpoint (resolve.go): it is gated like every
// other write endpoint, resolves scope_id and both LB4 subjects from the
// session rather than the body, refuses an outcome outside the two-value
// set before reaching the store, and maps resolve's own refusal to 409
// through the package's shared switch. No Postgres dependency --
// fakeResolveStore stands in for store.ResolveStore
// (krill/store/resolve_integration_test.go covers the real SCD2 shapes
// against Postgres).
package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// t0 is a fixed timestamp, so a wire-form assertion on a rendered time is
// about the conversion and not about when the test happened to run.
var t0 = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// resolveCall records everything the handler passed to ResolveStore. Every
// field is asserted by TestResolveHandler_ValidSessionReachesStoreWithSessionScopeAndSubjects
// -- a recorded-but-unread field is how a scope leak or a dropped reason
// survives a green suite.
type resolveCall struct {
	scopeID    uuid.UUID
	id         uuid.UUID
	outcome    store.ResolveOutcome
	reason     *string
	acting     store.Subject
	onBehalfOf store.Subject
}

// fakeResolveStore records every call and, when err is set, returns it
// instead of succeeding. A promote returns a non-zero NonGoal so the
// handler's promote-response branch is exercised for real; a retire
// returns the zero NonGoal, which is what a tombstone leaves behind.
type fakeResolveStore struct {
	calls *[]resolveCall
	err   error
}

func (f fakeResolveStore) ResolveNonGoal(_ context.Context, scopeID, id uuid.UUID, outcome store.ResolveOutcome, reason *string, acting, onBehalfOf store.Subject) (store.NonGoal, error) {
	*f.calls = append(*f.calls, resolveCall{scopeID: scopeID, id: id, outcome: outcome, reason: reason, acting: acting, onBehalfOf: onBehalfOf})
	if f.err != nil {
		return store.NonGoal{}, f.err
	}
	if outcome == store.ResolveRetire {
		return store.NonGoal{}, nil
	}
	body := "carried across the re-kind"
	return store.NonGoal{
		ID:         id,
		ScopeID:    scopeID,
		ProductID:  uuid.New(),
		Kind:       store.NonGoalKindPermanent,
		Name:       "A deferred Non-Goal",
		Body:       &body,
		Position:   3,
		ValidFrom:  t0,
		RevisionID: uuid.New(),
	}, nil
}

func (f fakeResolveStore) ListNonGoalPromotions(_ context.Context, scopeID uuid.UUID, productID *uuid.UUID) ([]store.NonGoalPromotion, error) {
	*f.calls = append(*f.calls, resolveCall{scopeID: scopeID})
	return nil, nil
}

// doResolveRequest wraps handler in handlers.RequireSession(sessions) --
// the exact wiring routes.go uses -- and mounts it behind a "{id}" path so
// r.PathValue("id") is populated as a real request would populate it.
// sessionIDHeader == "" omits the header entirely.
func doResolveRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/resolve", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/resolve", bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestResolveHandler_NoSessionID_Rejected(t *testing.T) {
	sessions := newFakeSessionStore()
	resolve := &fakeResolveStore{calls: &[]resolveCall{}}
	rec := doResolveRequest(t, handlers.ResolveNonGoalHandler(resolve), sessions, "", uuid.New().String(), `{"outcome": "promote"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, *resolve.calls, "an ungated resolve must never reach ResolveStore -- a re-kind with no attributable actor is unrecoverable")
}

// TestResolveHandler_ValidSessionReachesStoreWithSessionScopeAndSubjects
// asserts every field the handler forwards. It runs both outcomes, because
// the two reach the store through the same call and only one of them is
// covered by a test that checks just one.
func TestResolveHandler_ValidSessionReachesStoreWithSessionScopeAndSubjects(t *testing.T) {
	for _, outcome := range []store.ResolveOutcome{store.ResolvePromote, store.ResolveRetire} {
		t.Run(string(outcome), func(t *testing.T) {
			sessions, scopeID, sessionIDStr := newTestSession(t)
			resolve := &fakeResolveStore{calls: &[]resolveCall{}}
			id := uuid.New()
			reason := "settled at signoff"

			rec := doResolveRequest(t, handlers.ResolveNonGoalHandler(resolve), sessions, sessionIDStr, id.String(),
				fmt.Sprintf(`{"outcome": %q, "reason": %q}`, string(outcome), reason))

			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

			require.Len(t, *resolve.calls, 1)
			call := (*resolve.calls)[0]
			assert.Equal(t, scopeID, call.scopeID, "scope_id comes from the session, never the body (LB1, NFR6)")
			assert.Equal(t, id, call.id)
			assert.Equal(t, outcome, call.outcome, "the outcome must reach the store untranslated")
			require.NotNil(t, call.reason, "a supplied reason must be forwarded, not dropped")
			assert.Equal(t, reason, *call.reason, "the reason is recorded in the resolution's history for the audit reads")
			assert.Equal(t, store.SubjectKindService, call.acting.Kind, "the LB4 acting subject comes from the session")
			assert.Equal(t, call.acting, call.onBehalfOf, "and both subjects travel together (LB4)")
		})
	}
}

// TestResolveHandler_ResponseShape pins what each outcome answers with. A
// promote returns the new current row, so the caller can see the kind it
// now carries without a second read; a retire leaves no current row, so
// the id alone is the whole answer and the zero NonGoal must not be
// serialised as if it were a row.
func TestResolveHandler_ResponseShape(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	id := uuid.New()

	t.Run("promote returns the new current row", func(t *testing.T) {
		resolve := &fakeResolveStore{calls: &[]resolveCall{}}
		rec := doResolveRequest(t, handlers.ResolveNonGoalHandler(resolve), sessions, sessionIDStr, id.String(), `{"outcome": "promote"}`)
		require.Equal(t, http.StatusOK, rec.Code)

		var got handlers.NonGoalWire
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, id.String(), got.ID, "the successor keeps the SAME surrogate id (LB2)")
		assert.Equal(t, string(store.NonGoalKindPermanent), got.Kind, "the caller can see the re-kind without a second read")
		assert.Nil(t, got.ValidTo, "a promoted row is current, so it carries no valid_to")
		require.NotNil(t, got.Body, "and the body survives the re-kind -- FR d0021a0f requires it be preserved")
		assert.Equal(t, "carried across the re-kind", *got.Body)
	})

	t.Run("retire returns the id alone", func(t *testing.T) {
		resolve := &fakeResolveStore{calls: &[]resolveCall{}}
		rec := doResolveRequest(t, handlers.ResolveNonGoalHandler(resolve), sessions, sessionIDStr, id.String(), `{"outcome": "retire"}`)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, fmt.Sprintf(`{"id": %q}`, id.String()), rec.Body.String(),
			"a retire leaves no current row, so there is nothing to describe beyond the id")
	})
}

// TestResolveHandler_UnknownOutcomeRejectedBeforeStore is the two-value
// set. The handler cannot let an unrecognised outcome reach the store,
// because guessing between "the row survives" and "the row is tombstoned"
// is the one thing a resolution must never do -- and a case-insensitive
// match would make PROMOTE silently work where the store accepts only
// the two exact values.
func TestResolveHandler_UnknownOutcomeRejectedBeforeStore(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	for label, outcome := range map[string]string{
		"missing":      "",
		"unknown":      "delete",
		"wrong case":   "PROMOTE",
		"near miss":    "retire ",
		"amend's verb": "amend",
		"void's verb":  "void",
	} {
		t.Run(label, func(t *testing.T) {
			resolve := &fakeResolveStore{calls: &[]resolveCall{}}
			rec := doResolveRequest(t, handlers.ResolveNonGoalHandler(resolve), sessions, sessionIDStr, uuid.New().String(),
				fmt.Sprintf(`{"outcome": %q}`, outcome))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "promote", "the error must name the two legal outcomes")
			assert.Contains(t, rec.Body.String(), "retire")
			assert.Empty(t, *resolve.calls, "an unrecognised outcome must be refused before the store, not defaulted inside it")
		})
	}
}

// TestResolveHandler_RefusalsReturn409 is the error-mapping half.
// ErrNotDeferred is a conflict, not a malformed request: the body was
// well-formed and the row is simply in the wrong state for it. The two
// void refusals are listed too, because a retire inherits both by running
// void's own close and must surface them identically.
func TestResolveHandler_RefusalsReturn409(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	for label, refusal := range map[string]error{
		"not deferred":  store.ErrNotDeferred,
		"delivered":     store.ErrEntityDelivered,
		"live children": store.ErrHasLiveChildren,
	} {
		t.Run(label, func(t *testing.T) {
			resolve := &fakeResolveStore{calls: &[]resolveCall{}, err: refusal}
			rec := doResolveRequest(t, handlers.ResolveNonGoalHandler(resolve), sessions, sessionIDStr, uuid.New().String(), `{"outcome": "retire"}`)
			assert.Equal(t, http.StatusConflict, rec.Code)
			assert.Contains(t, rec.Body.String(), refusal.Error(),
				"the refusal message must reach the caller -- it says why the row cannot be settled")
		})
	}
}

func TestResolveHandler_NotFoundIs400_UnknownErrorIs500(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	for label, tc := range map[string]struct {
		storeErr error
		want     int
	}{
		"not found":       {store.ErrNotFound, http.StatusBadRequest},
		"genuine failure": {errors.New("connection reset by peer"), http.StatusInternalServerError},
	} {
		t.Run(label, func(t *testing.T) {
			resolve := &fakeResolveStore{calls: &[]resolveCall{}, err: tc.storeErr}
			rec := doResolveRequest(t, handlers.ResolveNonGoalHandler(resolve), sessions, sessionIDStr, uuid.New().String(), `{"outcome": "promote"}`)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestResolveHandler_BadIDAndBadBody_RejectedBeforeStore(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	for label, tc := range map[string]struct {
		id, body string
	}{
		"malformed id":       {"not-a-uuid", `{"outcome": "promote"}`},
		"unknown body field": {uuid.New().String(), `{"outcome": "promote", "product_id": "x"}`},
		"malformed body":     {uuid.New().String(), `{"outcome": 7}`},
		"malformed reason":   {uuid.New().String(), `{"outcome": "promote", "reason": []}`},
	} {
		t.Run(label, func(t *testing.T) {
			resolve := &fakeResolveStore{calls: &[]resolveCall{}}
			rec := doResolveRequest(t, handlers.ResolveNonGoalHandler(resolve), sessions, sessionIDStr, tc.id, tc.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Empty(t, *resolve.calls, "a rejected request must not reach the store")
		})
	}
}

func TestResolveHandler_WrongMethodRejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	resolve := &fakeResolveStore{calls: &[]resolveCall{}}

	gated := handlers.RequireSession(sessions)(handlers.ResolveNonGoalHandler(resolve))
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/resolve", gated)
	req := httptest.NewRequest(http.MethodGet, "/probe/"+uuid.NewString()+"/resolve", nil)
	req.Header.Set("X-Krill-Session-Id", sessionIDStr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Empty(t, *resolve.calls)
}

// TestNewListNonGoalPromotionsResponse checks the audit read's wire form
// field by field, because a promotion record that drops its actor or its
// timestamp in serialisation is exactly the history FR 19123858 requires
// and exactly what nothing else would notice.
func TestNewListNonGoalPromotionsResponse(t *testing.T) {
	reason := "settled at signoff"
	promotions := []store.NonGoalPromotion{{
		ID:                  uuid.New(),
		ScopeID:             uuid.New(),
		NonGoalID:           uuid.New(),
		ProductID:           uuid.New(),
		FromKind:            store.NonGoalKindDeferred,
		ToKind:              store.NonGoalKindPermanent,
		Reason:              &reason,
		CreatedByActing:     store.Subject{Iss: "whale_net", Sub: "alex", Kind: store.SubjectKindHuman},
		CreatedByOnBehalfOf: store.Subject{Iss: "whale_net", Sub: "alex", Kind: store.SubjectKindHuman},
		CreatedAt:           t0,
	}}

	resp := handlers.NewListNonGoalPromotionsResponse(promotions)
	require.Len(t, resp.Promotions, 1)
	got := resp.Promotions[0]

	assert.Equal(t, promotions[0].ID.String(), got.ID)
	assert.Equal(t, promotions[0].NonGoalID.String(), got.NonGoalID)
	assert.Equal(t, promotions[0].ProductID.String(), got.ProductID)
	assert.Equal(t, string(store.NonGoalKindDeferred), got.FromKind, "the audit read must say which way the row moved")
	assert.Equal(t, string(store.NonGoalKindPermanent), got.ToKind)
	require.NotNil(t, got.Reason)
	assert.Equal(t, reason, *got.Reason)
	assert.Equal(t, "whale_net", got.CreatedByActingIss, "FR 19123858 requires the actor in the history")
	assert.Equal(t, "alex", got.CreatedByActingSub)
	assert.Equal(t, string(store.SubjectKindHuman), got.CreatedByActingKind)
	assert.Equal(t, "whale_net", got.CreatedByOnBehalfIs)
	assert.Equal(t, "alex", got.CreatedByOnBehalfSu)
	assert.Equal(t, string(store.SubjectKindHuman), got.CreatedByOnBehalfKi)
	assert.NotEmpty(t, got.CreatedAt, "and a timestamp -- the third leg of FR 19123858's history requirement")

	// An empty register serialises as [] rather than null, so a client
	// ranging over it does not have to special-case nil.
	empty := handlers.NewListNonGoalPromotionsResponse(nil)
	assert.NotNil(t, empty.Promotions)
	assert.Empty(t, empty.Promotions)
}
