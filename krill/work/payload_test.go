// Unit coverage for Assembler.Assemble's two error branches that never
// need to reach slice.Querier's real store at all (issue #2721's Testing
// section): NFR1's cross-scope rejection and an unknown task id's named,
// loud failure. Both branches return before Assemble ever calls
// a.querier.GetMilestoneDeliversSlice (see payload.go), so a bare
// slice.NewQuerier(nil) is safe here -- exactly the technique
// slice_test.go's TestSliceRegister_MountsAllFourGranularities already
// uses for the same reason. Every other Testing-section bullet (spec
// slice content, the NFR4 byte-equal regression, the empty-dependency-list
// case, the claim-state read) needs a real assembled slice and therefore
// real Postgres -- see payload_integration_test.go.
package work_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// fakeTaskStore is an in-memory store.TaskStore -- mirrors
// krill/api/handlers/fake_task_store_test.go's own fake, duplicated here
// (rather than shared) because that one lives in an internal test package
// of a different Go package.
type fakeTaskStore struct {
	getTaskByIDResult store.Task
	getTaskByIDErr    error
}

func (f *fakeTaskStore) CreateTask(ctx context.Context, params store.CreateTaskParams) (store.Task, error) {
	panic("not used by this test")
}

func (f *fakeTaskStore) DeclareDependency(ctx context.Context, params store.DeclareDependencyParams) error {
	panic("not used by this test")
}

func (f *fakeTaskStore) ListDependencies(ctx context.Context, scopeID, taskID uuid.UUID) ([]store.TaskDependency, error) {
	panic("not used by this test -- both branches under test return before Assemble ever calls this")
}

func (f *fakeTaskStore) UnsatisfiedDependencies(ctx context.Context, scopeID, taskID uuid.UUID) ([]uuid.UUID, error) {
	panic("not used by this test")
}

func (f *fakeTaskStore) GetTaskByID(ctx context.Context, id uuid.UUID) (store.Task, error) {
	return f.getTaskByIDResult, f.getTaskByIDErr
}

func (f *fakeTaskStore) ClaimTask(ctx context.Context, params store.ClaimTaskParams) (store.Claim, error) {
	panic("not used by this test")
}

func (f *fakeTaskStore) GetClaimByID(ctx context.Context, id uuid.UUID) (store.Claim, error) {
	panic("not used by this test -- both branches under test return before Assemble ever calls this")
}

func (f *fakeTaskStore) Heartbeat(ctx context.Context, params store.HeartbeatParams) (store.LeaseState, error) {
	panic("not used by this test")
}

func (f *fakeTaskStore) CompleteTask(ctx context.Context, params store.CompleteTaskParams) (store.TaskLaneResult, error) {
	panic("not used by this test")
}

func (f *fakeTaskStore) ReclaimExpired(ctx context.Context, params store.ReclaimParams) (store.ReclaimResult, error) {
	panic("not used by this test")
}

func (f *fakeTaskStore) AbandonClaim(ctx context.Context, params store.AbandonParams) (store.AbandonClaimResult, error) {
	panic("not used by this test")
}

func (f *fakeTaskStore) RecordNote(ctx context.Context, params store.RecordNoteParams) (store.Note, error) {
	panic("not used by this test")
}

func (f *fakeTaskStore) ListNotesForTask(ctx context.Context, scopeID, taskID uuid.UUID) ([]store.Note, error) {
	panic("not used by this test -- both branches under test return before Assemble ever calls this")
}

func (f *fakeTaskStore) ListNotesForEntity(ctx context.Context, scopeID uuid.UUID, kind store.NoteEntityKind, entityID uuid.UUID) ([]store.Note, error) {
	panic("not used by this test")
}

func (f *fakeTaskStore) ListClaimedTasks(ctx context.Context, params store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	panic("not used by this test")
}

func (f *fakeTaskStore) GetEscalationEventByID(ctx context.Context, id uuid.UUID) (store.EscalationEvent, error) {
	panic("not used by this test -- both branches under test return before Assemble ever calls this")
}

var _ store.TaskStore = (*fakeTaskStore)(nil)

// TestAssemble_CrossScopeTaskID_NotFound proves NFR1: a taskID that
// resolves to a real task, but in a different scope than the caller's,
// must be rejected identically to an unknown id -- it must never leak that
// the id belongs to another scope, and it must never leak that scope's
// payload.
func TestAssemble_CrossScopeTaskID_NotFound(t *testing.T) {
	taskID := uuid.New()
	otherScope := uuid.New()
	callerScope := uuid.New()
	require.NotEqual(t, otherScope, callerScope)

	tasks := &fakeTaskStore{getTaskByIDResult: store.Task{ID: taskID, ScopeID: otherScope}}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	_, err := assembler.Assemble(context.Background(), callerScope, taskID)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNotFound, "a cross-scope task id must be rejected exactly like an unknown one (NFR1)")
	assert.Contains(t, err.Error(), taskID.String(), "the rejection must name which task id failed")
}

// TestAssemble_UnknownTaskID_NotFound proves an id naming no task row at
// all fails loud and named -- store.ErrNotFound, never a silently-empty
// Payload -- mirroring the FR21 conformance test's "fails loud and named
// rather than silently empty" posture this issue's Testing section cites.
func TestAssemble_UnknownTaskID_NotFound(t *testing.T) {
	unknown := uuid.New()
	scopeID := uuid.New()

	tasks := &fakeTaskStore{getTaskByIDErr: store.ErrNotFound}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	_, err := assembler.Assemble(context.Background(), scopeID, unknown)
	require.Error(t, err)
	assert.True(t, errors.Is(err, store.ErrNotFound), "an unknown task id must fail as store.ErrNotFound, not a generic/opaque error")
}
