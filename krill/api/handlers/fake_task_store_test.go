// fakeTaskStore is an in-memory store.TaskStore backing task_test.go:
// CreateTaskHandler (task.go, issue #2719) depends only on this
// interface, so handler-level tests never need a real Postgres (that is
// krill/store/task_integration_test.go's job). The DeclareDependency/
// ListDependencies/UnsatisfiedDependencies/GetTaskByID stubs below exist
// only so this fake keeps satisfying store.TaskStore now that
// task_dependency.go (issue #2720) widens it -- task_dependency_test.go's
// own Testing-phase task is what actually exercises them.
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

	declareErr          error
	gotDeclareParams    store.DeclareDependencyParams
	dependencies        []store.TaskDependency
	listDependenciesErr error
	unsatisfied         []uuid.UUID
	unsatisfiedErr      error
	getTaskByIDResult   store.Task
	getTaskByIDErr      error

	claimErr           error
	claimResult        store.Claim
	gotClaimParams     store.ClaimTaskParams
	getClaimByIDResult store.Claim
	getClaimByIDErr    error
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

func (f *fakeTaskStore) DeclareDependency(ctx context.Context, params store.DeclareDependencyParams) error {
	f.gotDeclareParams = params
	return f.declareErr
}

func (f *fakeTaskStore) ListDependencies(ctx context.Context, scopeID, taskID uuid.UUID) ([]store.TaskDependency, error) {
	return f.dependencies, f.listDependenciesErr
}

func (f *fakeTaskStore) UnsatisfiedDependencies(ctx context.Context, scopeID, taskID uuid.UUID) ([]uuid.UUID, error) {
	return f.unsatisfied, f.unsatisfiedErr
}

func (f *fakeTaskStore) GetTaskByID(ctx context.Context, id uuid.UUID) (store.Task, error) {
	return f.getTaskByIDResult, f.getTaskByIDErr
}

func (f *fakeTaskStore) ClaimTask(ctx context.Context, params store.ClaimTaskParams) (store.Claim, error) {
	f.gotClaimParams = params
	if f.claimErr != nil {
		return store.Claim{}, f.claimErr
	}
	return f.claimResult, nil
}

func (f *fakeTaskStore) GetClaimByID(ctx context.Context, id uuid.UUID) (store.Claim, error) {
	return f.getClaimByIDResult, f.getClaimByIDErr
}

var _ store.TaskStore = (*fakeTaskStore)(nil)
