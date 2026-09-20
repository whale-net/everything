package backupsched

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	manman "github.com/whale-net/everything/manmanv2/models"
)

// registerActivityStubs registers a placeholder function under each activity
// name this package's workflows dispatch by string (ActivityListDueBackupConfigs,
// ActivityDispatchBackup) -- OnActivity(name, ...) requires the name to
// already be a registered activity before it can be mocked, since it
// validates against the real signature (mirrors
// tools/app_registry/worker/release/workflow_test.go's
// registerActivityStubs). The bodies here are never reached: OnActivity's
// mock intercepts the call before the registered function runs.
func registerActivityStubs(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivityWithOptions(func(ctx context.Context, now time.Time) ([]*manman.BackupConfig, error) {
		return nil, nil
	}, activity.RegisterOptions{Name: ActivityListDueBackupConfigs})
	env.RegisterActivityWithOptions(func(ctx context.Context, backupConfigID int64) error {
		return nil
	}, activity.RegisterOptions{Name: ActivityDispatchBackup})
}

// TestBackupScanWorkflow_DispatchesOnePerDueConfig proves NFR1: every
// BackupConfig ListDueBackupConfigs returns is given its own
// DispatchBackupWorkflow child -- none silently skipped.
func TestBackupScanWorkflow_DispatchesOnePerDueConfig(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	due := []*manman.BackupConfig{
		{BackupConfigID: 10, CadenceMinutes: 5},
		{BackupConfigID: 20, CadenceMinutes: 5},
	}
	env.OnActivity(ActivityListDueBackupConfigs, mock.Anything, mock.Anything).Return(due, nil).Once()

	var mu sync.Mutex
	var dispatched []int64
	env.OnWorkflow(DispatchBackupWorkflow, mock.Anything, mock.Anything).Return(nil).
		Run(func(args mock.Arguments) {
			mu.Lock()
			dispatched = append(dispatched, args.Get(1).(int64))
			mu.Unlock()
		})

	env.ExecuteWorkflow(BackupScanWorkflow)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	sort.Slice(dispatched, func(i, j int) bool { return dispatched[i] < dispatched[j] })
	require.Equal(t, []int64{10, 20}, dispatched, "every due config must get its own dispatch, none skipped")
}

// TestBackupScanWorkflow_NoDueConfigs_NoDispatch proves the complementary
// case: zero due configs starts zero children.
func TestBackupScanWorkflow_NoDueConfigs_NoDispatch(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	env.OnActivity(ActivityListDueBackupConfigs, mock.Anything, mock.Anything).Return([]*manman.BackupConfig{}, nil).Once()

	var dispatchCount int
	env.OnWorkflow(DispatchBackupWorkflow, mock.Anything, mock.Anything).Return(nil).
		Run(func(args mock.Arguments) { dispatchCount++ })

	env.ExecuteWorkflow(BackupScanWorkflow)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 0, dispatchCount, "no due config must start zero DispatchBackupWorkflow children")
}

// TestDispatchBackupWorkflow_RetriesThenFailsWorkflow proves NFR1's other
// half: a DispatchBackup activity that keeps failing is retried per
// dispatchActivityOptions' RetryPolicy (MaximumAttempts: 5) rather than
// swallowed, and once retries are exhausted the failure surfaces as this
// workflow's own error instead of being silently dropped.
func TestDispatchBackupWorkflow_RetriesThenFailsWorkflow(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	var mu sync.Mutex
	var attempts int
	env.OnActivity(ActivityDispatchBackup, mock.Anything, int64(42)).
		Return(errors.New("s3 unreachable")).
		Run(func(args mock.Arguments) {
			mu.Lock()
			attempts++
			mu.Unlock()
		})

	env.ExecuteWorkflow(DispatchBackupWorkflow, int64(42))

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError(), "a DispatchBackup failure must fail DispatchBackupWorkflow once retries are exhausted, not be swallowed")
	require.Equal(t, 5, attempts, "must retry exactly dispatchActivityOptions' MaximumAttempts before giving up")
}

// TestDispatchBackupWorkflow_SucceedsAfterTransientFailure proves the retry
// actually helps: a transient failure followed by success still completes
// the workflow without error (NFR1 -- a transient S3/RMQ blip must not drop
// the config on the floor).
func TestDispatchBackupWorkflow_SucceedsAfterTransientFailure(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	env.OnActivity(ActivityDispatchBackup, mock.Anything, int64(7)).
		Return(errors.New("transient s3 blip")).Once()
	env.OnActivity(ActivityDispatchBackup, mock.Anything, int64(7)).
		Return(nil).Once()

	env.ExecuteWorkflow(DispatchBackupWorkflow, int64(7))

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
}

// TestDispatchWorkflowID_SameWindow_SameID drives DispatchWorkflowID
// directly for the same-window dedup case: two BackupScanWorkflow runs a
// few seconds apart inside the same cadence window compute the same child
// workflow id, so the second's ExecuteChildWorkflow collides against the
// first via WorkflowIDReusePolicy REJECT_DUPLICATE instead of double-
// dispatching. Exercised as a pure function here rather than as a full
// two-execution TestWorkflowEnvironment run, per the implementation note on
// DispatchWorkflowID/dispatchWindow's exported status: a real duplicate-
// child-workflow-id collision needs Temporal server-side id-reuse
// enforcement that TestWorkflowEnvironment's child-workflow mocking (see
// TestBackupScanWorkflow_DispatchesOnePerDueConfig's OnWorkflow use above)
// does not model.
func TestDispatchWorkflowID_SameWindow_SameID(t *testing.T) {
	const cadenceMinutes = 60
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	later := base.Add(3 * time.Minute) // still inside the same 60-minute window

	first := DispatchWorkflowID(501, cadenceMinutes, base)
	second := DispatchWorkflowID(501, cadenceMinutes, later)

	require.Equal(t, first, second, "two scans inside the same cadence window must compute the same dispatch workflow id")
}

// TestDispatchWorkflowID_DifferentWindow_DifferentID is the complementary
// case: once the cadence window has rolled over, the same BackupConfigID
// must get a fresh dispatch workflow id, so the config is dispatched again
// next cadence.
func TestDispatchWorkflowID_DifferentWindow_DifferentID(t *testing.T) {
	const cadenceMinutes = 60
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	nextWindow := base.Add(61 * time.Minute)

	first := DispatchWorkflowID(501, cadenceMinutes, base)
	second := DispatchWorkflowID(501, cadenceMinutes, nextWindow)

	require.NotEqual(t, first, second, "a new cadence window must get its own dispatch workflow id")
}

// TestDispatchWorkflowID_DistinctConfigsNeverCollide proves the id also
// discriminates on BackupConfigID within the very same window, so two
// configs due in the same scan never collide with each other.
func TestDispatchWorkflowID_DistinctConfigsNeverCollide(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	first := DispatchWorkflowID(501, 60, now)
	second := DispatchWorkflowID(502, 60, now)

	require.NotEqual(t, first, second)
}
