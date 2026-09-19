// Unit tests for CreateTaskHandler (task.go, issue #2719's Testing
// section). No Postgres dependency -- fakeTaskStore/fakeSessionStore
// stand in for the store interfaces; the real store-level branch coverage
// (milepebble/uncut-milestone/cut-milestone/lane-sequence validation)
// lives in krill/store/task_integration_test.go.
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// TestCreateTaskHandler_Success proves FR1/NFR6: a well-formed create
// returns the surrogate id, passes milestone_id/lane_sequence/
// starting_lane through unchanged, and writes scope_id and both LB4
// subjects from the session, never the request body.
func TestCreateTaskHandler_Success(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	milestoneID := uuid.New()

	tasks := &fakeTaskStore{}
	rec := doGatedRequest(t, handlers.CreateTaskHandler(tasks), sessions, sessionIDStr,
		`{"milestone_id": "`+milestoneID.String()+`", "title": "do the thing", "lane_sequence": ["Scaffold", "Implementation"], "starting_lane": "Scaffold"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ID)

	assert.Equal(t, scopeID, tasks.gotParams.ScopeID, "NFR6: scope_id must come from the session, not the request body")
	assert.Equal(t, milestoneID, tasks.gotParams.MilestoneID)
	assert.Equal(t, "do the thing", tasks.gotParams.Title)
	assert.Equal(t, []store.Lane{store.LaneScaffold, store.LaneImplementation}, tasks.gotParams.LaneSequence)
	assert.Equal(t, store.LaneScaffold, tasks.gotParams.StartingLane)
	assert.NotEmpty(t, tasks.gotParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
}

// TestCreateTaskHandler_MissingOrMalformedFields_Rejected proves a
// missing/empty/malformed milestone_id or an empty title is rejected
// before the store is ever called.
func TestCreateTaskHandler_MissingOrMalformedFields_Rejected(t *testing.T) {
	cases := map[string]string{
		"missing milestone_id":   `{"title": "do the thing", "lane_sequence": ["Scaffold"], "starting_lane": "Scaffold"}`,
		"empty milestone_id":     `{"milestone_id": "", "title": "do the thing", "lane_sequence": ["Scaffold"], "starting_lane": "Scaffold"}`,
		"malformed milestone_id": `{"milestone_id": "not-a-uuid", "title": "do the thing", "lane_sequence": ["Scaffold"], "starting_lane": "Scaffold"}`,
		"empty title":            `{"milestone_id": "` + uuid.New().String() + `", "title": "", "lane_sequence": ["Scaffold"], "starting_lane": "Scaffold"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{}
			rec := doGatedRequest(t, handlers.CreateTaskHandler(tasks), sessions, sessionIDStr, body)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "case %q: got body %q", name, rec.Body.String())
			assert.Equal(t, uuid.Nil, tasks.gotParams.ScopeID, "case %q: a validation failure must never reach the store", name)
		})
	}
}

// TestCreateTaskHandler_StoreRejection_Returns400 proves writeStoreError
// maps CreateTask's named FR1 rejections (store.ErrMilestoneHasMilepebbleCut,
// store.ErrInvalidLaneSequence, store.ErrStartingLaneNotInSequence) and
// store.ErrNotFound onto a 400, mirroring the entity-create handlers'
// own mapping.
func TestCreateTaskHandler_StoreRejection_Returns400(t *testing.T) {
	for name, err := range map[string]error{
		"unknown or non-delivery-target milestone_id": store.ErrNotFound,
		"milestone has a milepebble cut":               store.ErrMilestoneHasMilepebbleCut,
		"invalid lane sequence":                        store.ErrInvalidLaneSequence,
		"starting lane not in sequence":                store.ErrStartingLaneNotInSequence,
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{createErr: err}

			rec := doGatedRequest(t, handlers.CreateTaskHandler(tasks), sessions, sessionIDStr,
				`{"milestone_id": "`+uuid.New().String()+`", "title": "do the thing", "lane_sequence": ["Scaffold"], "starting_lane": "Scaffold"}`)

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

// TestCreateTaskHandler_NoSessionHeader_Rejected proves NFR6's write gate:
// POST /tasks with no X-Krill-Session-Id header is rejected before the
// body is ever parsed or the store is ever called.
func TestCreateTaskHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doGatedRequest(t, handlers.CreateTaskHandler(tasks), sessions, "",
		`{"milestone_id": "`+uuid.New().String()+`", "title": "do the thing", "lane_sequence": ["Scaffold"], "starting_lane": "Scaffold"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotParams.ScopeID, "a request with no session header must never reach the store")
}
