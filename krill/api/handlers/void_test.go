// Unit tests for the seven void endpoints (void.go): every one is gated
// like every other write endpoint, resolves scope_id and both LB4 subjects
// from the session rather than the body, and maps void's two named
// refusals to 409. No Postgres dependency -- fakeVoidStore stands in for
// store.VoidStore (krill/store/void_integration_test.go covers the real
// tombstone against Postgres).
package handlers_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

type voidCall struct {
	method     string
	scopeID    uuid.UUID
	id         uuid.UUID
	reason     *string
	acting     store.Subject
	onBehalfOf store.Subject
}

// fakeVoidStore records every call and, when refuses is set for a method,
// returns that error instead of succeeding.
type fakeVoidStore struct {
	calls   *[]voidCall
	refuses map[string]error
}

func (f fakeVoidStore) record(method string, scopeID, id uuid.UUID, reason *string, acting, onBehalfOf store.Subject) error {
	*f.calls = append(*f.calls, voidCall{method, scopeID, id, reason, acting, onBehalfOf})
	return f.refuses[method]
}

func (f fakeVoidStore) VoidProduct(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidProduct", s, id, r, a, o)
}

func (f fakeVoidStore) VoidFeatureSet(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidFeatureSet", s, id, r, a, o)
}

func (f fakeVoidStore) VoidFeature(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidFeature", s, id, r, a, o)
}

func (f fakeVoidStore) VoidRequirement(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidRequirement", s, id, r, a, o)
}

func (f fakeVoidStore) VoidPersona(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidPersona", s, id, r, a, o)
}

func (f fakeVoidStore) VoidNonGoal(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidNonGoal", s, id, r, a, o)
}

func (f fakeVoidStore) VoidLoadBearingDecision(_ context.Context, s uuid.UUID, id uuid.UUID, r *string, a, o store.Subject) error {
	return f.record("VoidLoadBearingDecision", s, id, r, a, o)
}

func (f fakeVoidStore) ListVoidEvents(_ context.Context, scopeID uuid.UUID, kind store.VoidedEntityKind) ([]store.VoidEvent, error) {
	return nil, nil
}

// doVoidRequest wraps handler in handlers.RequireSession(sessions) -- the
// exact wiring routes.go uses for all seven void endpoints -- and mounts it
// behind a "{id}" path so r.PathValue("id") is populated as a real request
// would populate it. sessionIDHeader == "" omits the header entirely.
func doVoidRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/void", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/void", bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// voidEndpoints is every registered void route, keyed by the store method it
// must reach. A route wired to the wrong handler -- or missing entirely --
// fails here.
var voidEndpoints = map[string]struct {
	path    string
	handler func(store.VoidStore) http.HandlerFunc
	method  string
}{
	"product":               {"/products/{id}/void", handlers.VoidProductHandler, "VoidProduct"},
	"feature_set":           {"/feature-sets/{id}/void", handlers.VoidFeatureSetHandler, "VoidFeatureSet"},
	"feature":               {"/features/{id}/void", handlers.VoidFeatureHandler, "VoidFeature"},
	"requirement":           {"/requirements/{id}/void", handlers.VoidRequirementHandler, "VoidRequirement"},
	"persona":               {"/personas/{id}/void", handlers.VoidPersonaHandler, "VoidPersona"},
	"non_goal":              {"/non-goals/{id}/void", handlers.VoidNonGoalHandler, "VoidNonGoal"},
	"load_bearing_decision": {"/load-bearing-decisions/{id}/void", handlers.VoidLoadBearingDecisionHandler, "VoidLoadBearingDecision"},
}

func TestVoidHandlers_NoSessionID_Rejected(t *testing.T) {
	sessions := newFakeSessionStore()
	for kind, ep := range voidEndpoints {
		t.Run(kind, func(t *testing.T) {
			voids := &fakeVoidStore{calls: &[]voidCall{}}
			rec := doVoidRequest(t, ep.handler(voids), sessions, "", uuid.New().String(), `{}`)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Empty(t, *voids.calls, "an ungated void must never reach VoidStore -- a tombstone with no attributable actor is unrecoverable")
		})
	}
}

func TestVoidHandlers_ValidSessionReachesStoreWithSessionScopeAndSubjects(t *testing.T) {
	for kind, ep := range voidEndpoints {
		t.Run(kind, func(t *testing.T) {
			sessions, scopeID, sessionIDStr := newTestSession(t)
			voids := &fakeVoidStore{calls: &[]voidCall{}}
			id := uuid.New()
			reason := "created against the wrong parent"

			rec := doVoidRequest(t, ep.handler(voids), sessions, sessionIDStr, id.String(),
				fmt.Sprintf(`{"reason": %q}`, reason))

			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			assert.JSONEq(t, fmt.Sprintf(`{"id": %q}`, id.String()), rec.Body.String(),
				"a void returns the unchanged surrogate id (LB2)")

			require.Len(t, *voids.calls, 1)
			call := (*voids.calls)[0]
			assert.Equal(t, ep.method, call.method, "the %s route must reach %s", ep.path, ep.method)
			assert.Equal(t, scopeID, call.scopeID, "scope_id comes from the session, never the body (LB1, NFR6)")
			assert.Equal(t, id, call.id)
			require.NotNil(t, call.reason)
			assert.Equal(t, reason, *call.reason, "the reason is passed through for the audit read")
			assert.Equal(t, store.SubjectKindService, call.acting.Kind, "the LB4 acting subject comes from the session")
			assert.Equal(t, call.acting, call.onBehalfOf)
		})
	}
}

func TestVoidHandlers_BadIDAndBadBody_RejectedBeforeStore(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	for label, tc := range map[string]struct {
		id, body string
		want     int
	}{
		"malformed id":       {"not-a-uuid", `{}`, http.StatusBadRequest},
		"unknown body field": {uuid.New().String(), `{"parent_id": "x"}`, http.StatusBadRequest},
		"malformed body":     {uuid.New().String(), `{"reason": 7}`, http.StatusBadRequest},
	} {
		t.Run(label, func(t *testing.T) {
			voids := &fakeVoidStore{calls: &[]voidCall{}}
			rec := doVoidRequest(t, handlers.VoidFeatureHandler(voids), sessions, sessionIDStr, tc.id, tc.body)
			assert.Equal(t, tc.want, rec.Code)
			assert.Empty(t, *voids.calls, "a rejected request must not reach the store")
		})
	}
}

// TestVoidHandlers_RefusalsReturn409 is the error-mapping half: a delivered
// or shipped entity, and an entity with a live child, are conflicts rather
// than malformed requests, and each body names the caller's next step.
func TestVoidHandlers_RefusalsReturn409(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	for label, refusal := range map[string]error{
		"delivered":     store.ErrEntityDelivered,
		"live children": store.ErrHasLiveChildren,
	} {
		t.Run(label, func(t *testing.T) {
			voids := &fakeVoidStore{calls: &[]voidCall{}, refuses: map[string]error{"VoidFeature": refusal}}
			rec := doVoidRequest(t, handlers.VoidFeatureHandler(voids), sessions, sessionIDStr, uuid.New().String(), `{}`)
			assert.Equal(t, http.StatusConflict, rec.Code)
			assert.Contains(t, rec.Body.String(), refusal.Error(),
				"the refusal message must reach the caller -- it says whether to amend or to void the children first")
		})
	}
}

func TestVoidHandlers_NotFoundIs400_UnknownErrorIs500(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	for label, tc := range map[string]struct {
		storeErr error
		want     int
	}{
		"not found":       {store.ErrNotFound, http.StatusBadRequest},
		"genuine failure": {errors.New("connection reset by peer"), http.StatusInternalServerError},
	} {
		t.Run(label, func(t *testing.T) {
			voids := &fakeVoidStore{calls: &[]voidCall{}, refuses: map[string]error{"VoidFeature": tc.storeErr}}
			rec := doVoidRequest(t, handlers.VoidFeatureHandler(voids), sessions, sessionIDStr, uuid.New().String(), `{}`)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}
