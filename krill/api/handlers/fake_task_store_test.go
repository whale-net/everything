// fakeTaskStore is an in-memory store.TaskStore backing task_test.go:
// CreateTaskHandler (task.go, issue #2719) depends only on this
// interface, so handler-level tests never need a real Postgres (that is
// krill/store/task_integration_test.go's job).
package handlers_test

import (
	"context"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// fakeTaskStore records the last CreateTask call's params (so a test can
// assert scope_id/subject-pair pass-through, NFR1/NFR6) and can be told
// to fail with a fixed error.
type fakeTaskStore struct {
	createErr error

	gotParams store.CreateTaskParams
}

func (f *fakeTaskStore) CreateTask(ctx context.Context, params store.CreateTaskParams) (store.Task, error) {
	f.gotParams = params
	if f.createErr != nil {
		return store.Task{}, f.createErr
	}
	return store.Task{
		ID:                  uuid.New(),
		ScopeID:             params.ScopeID,
		MilestoneID:         params.MilestoneID,
		Title:               params.Title,
		Body:                params.Body,
		LaneSequence:        params.LaneSequence,
		CurrentLane:         params.StartingLane,
		CreatedByActing:     params.Acting,
		CreatedByOnBehalfOf: params.OnBehalfOf,
	}, nil
}

var _ store.TaskStore = (*fakeTaskStore)(nil)
