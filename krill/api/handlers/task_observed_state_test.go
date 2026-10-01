// Cross-endpoint unit tests for the observed-state guard's HTTP surface
// (task_observed_state.go in krill/store). The four verbs' individual
// mapping tests live in task_cancel_test.go, task_release_test.go,
// task_requeue_test.go and task_escalate_test.go; this file covers the one
// property only a cross-endpoint view can show -- that all four agree on
// the status, so "the state changed since you looked" reads the same way
// whichever button the operator pressed. No Postgres: fakeTaskStore stands
// in for store.TaskStore.
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// TestObservedStateMismatch_SameStatusOnAllFourEndpoints proves the guard's
// refusal maps to one status across release, requeue, cancel and escalate:
// 409 on all four, never a 500 and never a status a legality refusal would
// also use for a different reason.
func TestObservedStateMismatch_SameStatusOnAllFourEndpoints(t *testing.T) {
	claimBody := `{"expected_claim_id":"` + uuid.New().String() + `"}`
	escalationBody := `{"expected_escalation_id":"` + uuid.New().String() + `"}`
	taskID := uuid.New().String()

	for name, call := range map[string]func(t *testing.T, tasks *fakeTaskStore, sessions store.SessionStore, sessionID string) *httptest.ResponseRecorder{
		"release": func(t *testing.T, tasks *fakeTaskStore, sessions store.SessionStore, sessionID string) *httptest.ResponseRecorder {
			t.Helper()
			return doReleaseTaskRequest(t, handlers.ReleaseTaskHandler(tasks, work.NewAssembler(tasks, slice.NewQuerier(nil))),
				sessions, sessionID, taskID, claimBody)
		},
		"requeue": func(t *testing.T, tasks *fakeTaskStore, sessions store.SessionStore, sessionID string) *httptest.ResponseRecorder {
			t.Helper()
			return doRequeueTaskRequest(t, handlers.RequeueTaskHandler(tasks, work.NewAssembler(tasks, slice.NewQuerier(nil))),
				sessions, sessionID, taskID, escalationBody)
		},
		"cancel": func(t *testing.T, tasks *fakeTaskStore, sessions store.SessionStore, sessionID string) *httptest.ResponseRecorder {
			t.Helper()
			return doCancelTaskRequest(t, handlers.CancelTaskHandler(tasks, work.NewAssembler(tasks, slice.NewQuerier(nil))),
				sessions, sessionID, taskID, escalationBody)
		},
		"escalate": func(t *testing.T, tasks *fakeTaskStore, sessions store.SessionStore, sessionID string) *httptest.ResponseRecorder {
			t.Helper()
			return doEscalateTaskRequest(t, handlers.EscalateTaskHandler(tasks, work.NewAssembler(tasks, slice.NewQuerier(nil))),
				sessions, sessionID, taskID, claimBody)
		},
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{
				cancelErr:   store.ErrObservedStateMismatch,
				releaseErr:  store.ErrObservedStateMismatch,
				requeueErr:  store.ErrObservedStateMismatch,
				escalateErr: store.ErrObservedStateMismatch,
			}

			rec := call(t, tasks, sessions, sessionIDStr)

			assert.Equal(t, http.StatusConflict, rec.Code, "a state change since the caller's read is a conflict on every verb alike: "+rec.Body.String())
			assert.Contains(t, rec.Body.String(), store.ErrObservedStateMismatch.Error(),
				"the body must name the observed-state guard's own refusal, so a client can tell it from a legality refusal sharing the status")
		})
	}
}

// TestObservedStateMismatch_DistinguishableFromLegalityRefusal proves the
// guard's refusal is not merely a 409: on the same endpoint and the same
// status as a legality refusal, its body still names the guard, so a client
// can tell "re-read and retry" from "this action is not legal".
func TestObservedStateMismatch_DistinguishableFromLegalityRefusal(t *testing.T) {
	type response struct {
		code int
		text string
	}
	call := func(t *testing.T, mismatch error, legality error) (response, response) {
		t.Helper()
		sessions, _, sessionIDStr := newTestSession(t)

		mismatchTasks := &fakeTaskStore{releaseErr: mismatch}
		mismatchRec := doReleaseTaskRequest(t, handlers.ReleaseTaskHandler(mismatchTasks, work.NewAssembler(mismatchTasks, slice.NewQuerier(nil))),
			sessions, sessionIDStr, uuid.New().String(), `{}`)

		legalityTasks := &fakeTaskStore{releaseErr: legality}
		legalityRec := doReleaseTaskRequest(t, handlers.ReleaseTaskHandler(legalityTasks, work.NewAssembler(legalityTasks, slice.NewQuerier(nil))),
			sessions, sessionIDStr, uuid.New().String(), `{}`)

		return response{mismatchRec.Code, mismatchRec.Body.String()}, response{legalityRec.Code, legalityRec.Body.String()}
	}

	mismatch, notClaimed := call(t, store.ErrObservedStateMismatch, store.ErrTaskNotClaimed)

	require.Equal(t, http.StatusConflict, mismatch.code)
	require.Equal(t, http.StatusConflict, notClaimed.code,
		"the legality refusal shares the status -- which is exactly why the bodies must differ")
	assert.NotEqual(t, mismatch.text, notClaimed.text,
		"a client must be able to tell 'the state changed since you looked' from 'the action is not legal'")
	assert.Contains(t, mismatch.text, store.ErrObservedStateMismatch.Error())
	assert.NotContains(t, mismatch.text, store.ErrTaskNotClaimed.Error())
}