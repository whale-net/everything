// fakeTaskStore is an in-memory store.TaskStore backing task_test.go:
// CreateTaskHandler (task.go, issue #2719) depends only on this
// interface, so handler-level tests never need a real Postgres (that is
// krill/store/task_integration_test.go's job). The DeclareDependency/
// ListDependencies/UnsatisfiedDependencies/GetTaskByID stubs below exist
// only so this fake keeps satisfying store.TaskStore now that
// task_dependency.go (issue #2720) widens it -- task_dependency_test.go's
// own Testing-phase task is what actually exercises them. The
// RecordNote/ListNotesForTask/ListNotesForEntity stubs exist for the same
// reason now that task_note.go (issue #2727) widens it again --
// task_note_test.go's own later Testing-phase task exercises them. The
// Heartbeat stub exists for the same reason now that task_lease.go (issue
// #2723) widens it once more -- task_lease_test.go exercises it. The
// CompleteTask stub exists for the same reason now that task_complete.go
// (issue #2725) widens it once more -- task_complete_test.go's own Testing
// section is what actually exercises it. The ReclaimExpired stub exists for
// the same reason now that task_reclaim.go (issue #2724) widens it again --
// task_reclaim_test.go exercises it. The AbandonClaim stub exists for the
// same reason now that task_abandon.go (issue #2726) widens it once more --
// task_abandon_test.go's own Testing-phase task is what actually exercises
// it. The ListClaimedTasks stub exists for the same reason now that
// console.go (issue #2869) widens it once more -- console_test.go's own
// later Testing-phase task exercises it. The GetEscalationEventByID stub
// exists for the same reason now that task_escalation.go/task_complete.go
// (issue #2870) widen it once more -- task_complete_test.go's own Testing
// section is what actually exercises it. The CancelTask/
// ListCancelledTasks stubs exist for the same reason now that
// task_cancel.go/console.go (issue #2873) widen it once more --
// task_cancel_test.go/console_test.go exercise them. The
// TransitionNoteLifecycle/ListOpenNotes stubs exist for the same reason now
// that task_note_lifecycle.go/task_note_console.go (issue #2874) widen it
// once more -- task_note_lifecycle_test.go/console_test.go's own later
// Testing-phase task exercises them. The ReleaseLease/EscalateTask stubs
// exist for the same reason now that task_release.go/task_escalate.go
// (issue #2872) widen it once more -- task_release_test.go/
// task_escalate_test.go's own later Testing-phase task exercises them. The
// ListEscalatedTasks stub exists for the same reason now that console.go
// (issue #2875, FR5) widens it once more -- console_test.go's own Testing-
// phase task exercises it. The RequeueTask stub exists for the same reason
// now that task_requeue.go (issue #2876) widens it once more --
// task_requeue_test.go's own later Testing-phase task exercises it.
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

	heartbeatErr       error
	heartbeatResult    store.LeaseState
	gotHeartbeatParams store.HeartbeatParams

	completeErr       error
	completeResult    store.TaskLaneResult
	gotCompleteParams store.CompleteTaskParams

	reclaimErr       error
	reclaimResult    store.ReclaimResult
	gotReclaimParams store.ReclaimParams

	abandonErr       error
	abandonResult    store.AbandonClaimResult
	gotAbandonParams store.AbandonParams

	recordNoteErr         error
	gotRecordNoteParams   store.RecordNoteParams
	recordNoteResult      store.Note
	notesForTask          []store.Note
	listNotesForTaskErr   error
	notesForEntity        []store.Note
	listNotesForEntityErr error

	listClaimedTasksErr       error
	listClaimedTasksResult    store.Page[store.ClaimedTaskRow]
	gotListClaimedTasksParams store.ListClaimedTasksParams

	getEscalationEventByIDResult store.EscalationEvent
	getEscalationEventByIDErr    error

	cancelErr           error
	cancelResult        store.CancelResult
	gotCancelParams     store.CancelTaskParams
	listCancelledErr    error
	listCancelledResult store.Page[store.CancelledTaskRow]
	gotListCancelled    store.ListCancelledTasksParams

	transitionNoteLifecycleErr       error
	transitionNoteLifecycleResult    store.NoteLifecycleEvent
	gotTransitionNoteLifecycleParams store.TransitionNoteLifecycleParams

	listOpenNotesErr       error
	listOpenNotesResult    store.Page[store.OpenNoteRow]
	gotListOpenNotesParams store.ListOpenNotesParams

	releaseErr       error
	releaseResult    store.ReleaseResult
	gotReleaseParams store.ReleaseParams

	escalateErr       error
	escalateResult    store.EscalateResult
	gotEscalateParams store.EscalateParams

	listEscalatedErr    error
	listEscalatedResult store.Page[store.EscalatedTaskRow]
	gotListEscalated    store.ListEscalatedTasksParams

	requeueErr       error
	requeueResult    store.RequeueResult
	gotRequeueParams store.RequeueParams
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

func (f *fakeTaskStore) Heartbeat(ctx context.Context, params store.HeartbeatParams) (store.LeaseState, error) {
	f.gotHeartbeatParams = params
	if f.heartbeatErr != nil {
		return store.LeaseState{}, f.heartbeatErr
	}
	return f.heartbeatResult, nil
}

func (f *fakeTaskStore) CompleteTask(ctx context.Context, params store.CompleteTaskParams) (store.TaskLaneResult, error) {
	f.gotCompleteParams = params
	if f.completeErr != nil {
		return store.TaskLaneResult{}, f.completeErr
	}
	return f.completeResult, nil
}

func (f *fakeTaskStore) ReclaimExpired(ctx context.Context, params store.ReclaimParams) (store.ReclaimResult, error) {
	f.gotReclaimParams = params
	if f.reclaimErr != nil {
		return store.ReclaimResult{}, f.reclaimErr
	}
	return f.reclaimResult, nil
}

func (f *fakeTaskStore) AbandonClaim(ctx context.Context, params store.AbandonParams) (store.AbandonClaimResult, error) {
	f.gotAbandonParams = params
	if f.abandonErr != nil {
		return store.AbandonClaimResult{}, f.abandonErr
	}
	return f.abandonResult, nil
}

func (f *fakeTaskStore) RecordNote(ctx context.Context, params store.RecordNoteParams) (store.Note, error) {
	f.gotRecordNoteParams = params
	if f.recordNoteErr != nil {
		return store.Note{}, f.recordNoteErr
	}
	return f.recordNoteResult, nil
}

func (f *fakeTaskStore) ListNotesForTask(ctx context.Context, scopeID, taskID uuid.UUID) ([]store.Note, error) {
	return f.notesForTask, f.listNotesForTaskErr
}

func (f *fakeTaskStore) ListNotesForEntity(ctx context.Context, scopeID uuid.UUID, kind store.NoteEntityKind, entityID uuid.UUID) ([]store.Note, error) {
	return f.notesForEntity, f.listNotesForEntityErr
}

func (f *fakeTaskStore) ListClaimedTasks(ctx context.Context, params store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	f.gotListClaimedTasksParams = params
	if f.listClaimedTasksErr != nil {
		return store.Page[store.ClaimedTaskRow]{}, f.listClaimedTasksErr
	}
	return f.listClaimedTasksResult, nil
}

func (f *fakeTaskStore) GetEscalationEventByID(ctx context.Context, id uuid.UUID) (store.EscalationEvent, error) {
	return f.getEscalationEventByIDResult, f.getEscalationEventByIDErr
}

func (f *fakeTaskStore) CancelTask(ctx context.Context, params store.CancelTaskParams) (store.CancelResult, error) {
	f.gotCancelParams = params
	if f.cancelErr != nil {
		return store.CancelResult{}, f.cancelErr
	}
	return f.cancelResult, nil
}

func (f *fakeTaskStore) ListCancelledTasks(ctx context.Context, params store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	f.gotListCancelled = params
	if f.listCancelledErr != nil {
		return store.Page[store.CancelledTaskRow]{}, f.listCancelledErr
	}
	return f.listCancelledResult, nil
}

func (f *fakeTaskStore) TransitionNoteLifecycle(ctx context.Context, params store.TransitionNoteLifecycleParams) (store.NoteLifecycleEvent, error) {
	f.gotTransitionNoteLifecycleParams = params
	if f.transitionNoteLifecycleErr != nil {
		return store.NoteLifecycleEvent{}, f.transitionNoteLifecycleErr
	}
	return f.transitionNoteLifecycleResult, nil
}

func (f *fakeTaskStore) ListOpenNotes(ctx context.Context, params store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	f.gotListOpenNotesParams = params
	if f.listOpenNotesErr != nil {
		return store.Page[store.OpenNoteRow]{}, f.listOpenNotesErr
	}
	return f.listOpenNotesResult, nil
}

func (f *fakeTaskStore) ReleaseLease(ctx context.Context, params store.ReleaseParams) (store.ReleaseResult, error) {
	f.gotReleaseParams = params
	if f.releaseErr != nil {
		return store.ReleaseResult{}, f.releaseErr
	}
	return f.releaseResult, nil
}

func (f *fakeTaskStore) EscalateTask(ctx context.Context, params store.EscalateParams) (store.EscalateResult, error) {
	f.gotEscalateParams = params
	if f.escalateErr != nil {
		return store.EscalateResult{}, f.escalateErr
	}
	return f.escalateResult, nil
}

func (f *fakeTaskStore) ListEscalatedTasks(ctx context.Context, params store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	f.gotListEscalated = params
	if f.listEscalatedErr != nil {
		return store.Page[store.EscalatedTaskRow]{}, f.listEscalatedErr
	}
	return f.listEscalatedResult, nil
}

func (f *fakeTaskStore) RequeueTask(ctx context.Context, params store.RequeueParams) (store.RequeueResult, error) {
	f.gotRequeueParams = params
	if f.requeueErr != nil {
		return store.RequeueResult{}, f.requeueErr
	}
	return f.requeueResult, nil
}

var _ store.TaskStore = (*fakeTaskStore)(nil)
