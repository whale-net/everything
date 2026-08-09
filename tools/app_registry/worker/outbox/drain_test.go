package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/mocks"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/worker/writeback"
)

// fakeStore is a minimal in-memory repository.WritebackRepository double,
// scoped to exactly what Drainer needs -- ClaimBatch/MarkDone/MarkFailed.
// The real atomicity/claim-locking guarantees are covered against real
// Postgres in server/repository/postgres/postgres_integration_test.go; this
// fake exists only to observe what Drainer *does* with a claimed row.
type doneCall struct {
	WorkflowID string
	RunID      string
}

type fakeStore struct {
	rows []repository.WritebackOutbox
	done map[string]doneCall // outbox_id -> (workflow_id, run_id) MarkDone was called with
	fail map[string]string   // outbox_id -> error message
}

func newFakeStore(rows ...repository.WritebackOutbox) *fakeStore {
	return &fakeStore{rows: rows, done: map[string]doneCall{}, fail: map[string]string{}}
}

func (f *fakeStore) Enqueue(ctx context.Context, o repository.WritebackOutbox) (*repository.WritebackOutbox, error) {
	panic("not used by Drainer")
}

func (f *fakeStore) ClaimBatch(ctx context.Context, workerID string, limit int, staleAfter time.Duration) ([]repository.WritebackOutbox, error) {
	claimed := f.rows
	f.rows = nil
	return claimed, nil
}

func (f *fakeStore) MarkDone(ctx context.Context, outboxID, workflowID, runID string) error {
	f.done[outboxID] = doneCall{WorkflowID: workflowID, RunID: runID}
	return nil
}

func (f *fakeStore) MarkFailed(ctx context.Context, outboxID, errMsg string) error {
	f.fail[outboxID] = errMsg
	return nil
}

func (f *fakeStore) Get(ctx context.Context, outboxID string) (*repository.WritebackOutbox, error) {
	panic("not used by Drainer")
}

var _ repository.WritebackRepository = (*fakeStore)(nil)

func testRow() repository.WritebackOutbox {
	return repository.WritebackOutbox{
		OutboxID: "outbox-1", PromotionID: "promo-1", EnvironmentID: "env-1",
		EnvironmentKey: "dev", EventID: "event-1", StateHash: "hash-1",
	}
}

// TestDrainer_StartWorkflow_Success covers the ordinary path: ExecuteWorkflow
// succeeds, the row is marked done with the run id Temporal returned.
func TestDrainer_StartWorkflow_Success(t *testing.T) {
	row := testRow()
	store := newFakeStore(row)
	temporalClient := &mocks.Client{}
	run := &mocks.WorkflowRun{}
	run.On("GetID").Return(row.PromotionID)
	run.On("GetRunID").Return("run-abc")

	temporalClient.On("ExecuteWorkflow", mock.Anything, client.StartWorkflowOptions{
		ID: row.PromotionID, TaskQueue: writeback.TaskQueue,
	}, mock.Anything, writeback.WritebackInput{
		PromotionID: row.PromotionID, EnvironmentKey: row.EnvironmentKey, StateHash: row.StateHash,
	}).Return(run, nil)

	d := &Drainer{Store: store, Temporal: temporalClient, WorkerID: "test-worker", BatchSize: 10, StaleAfter: time.Hour, PollInterval: time.Millisecond}
	require.NoError(t, d.drainOnce(context.Background()))

	require.Equal(t, doneCall{WorkflowID: row.PromotionID, RunID: "run-abc"}, store.done[row.OutboxID])
	require.Empty(t, store.fail)
	temporalClient.AssertExpectations(t)
}

// TestDrainer_StartWorkflow_AlreadyStarted covers the redelivery case this
// package exists to make harmless: a worker killed mid-run leaves a row
// reclaimed while its WritebackWorkflow is still executing under the same
// workflow id. ExecuteWorkflow returns WorkflowExecutionAlreadyStarted, and
// Drainer must still mark the row done -- not failed, not stuck claimed --
// since the workflow this row asked for genuinely is running.
func TestDrainer_StartWorkflow_AlreadyStarted(t *testing.T) {
	row := testRow()
	store := newFakeStore(row)
	temporalClient := &mocks.Client{}
	temporalClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, &serviceerror.WorkflowExecutionAlreadyStarted{})

	d := &Drainer{Store: store, Temporal: temporalClient, WorkerID: "test-worker", BatchSize: 10, StaleAfter: time.Hour, PollInterval: time.Millisecond}
	require.NoError(t, d.drainOnce(context.Background()))

	require.Contains(t, store.done, row.OutboxID)
	require.Empty(t, store.fail)
}

// TestDrainer_StartWorkflow_OtherErrorMarksFailed covers a genuine failure
// (e.g. Temporal frontend unreachable): the row goes back to 'pending' (via
// MarkFailed) instead of being marked done, so the next drain pass retries
// it immediately rather than waiting out the staleness window.
func TestDrainer_StartWorkflow_OtherErrorMarksFailed(t *testing.T) {
	row := testRow()
	store := newFakeStore(row)
	temporalClient := &mocks.Client{}
	temporalClient.On("ExecuteWorkflow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil, errors.New("temporal frontend unreachable"))

	d := &Drainer{Store: store, Temporal: temporalClient, WorkerID: "test-worker", BatchSize: 10, StaleAfter: time.Hour, PollInterval: time.Millisecond}
	require.NoError(t, d.drainOnce(context.Background()))

	require.Empty(t, store.done)
	require.Contains(t, store.fail, row.OutboxID)
	require.Contains(t, store.fail[row.OutboxID], "unreachable")
}
