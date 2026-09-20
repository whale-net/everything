package backupsched

import (
	"errors"
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	manman "github.com/whale-net/everything/manmanv2/models"
)

// dispatchActivityOptions bounds DispatchBackup's retry: a transient S3/RMQ
// failure retries a few times with backoff instead of dropping a due config
// on the floor (NFR1), but a permanent failure (e.g. an unknown backup
// config id) still surfaces as a failed workflow rather than retrying
// forever.
func dispatchActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    5,
		},
	}
}

// dispatchWindow buckets now into a cadenceMinutes-wide window, mirroring
// River's UniqueOpts{ByArgs, ByPeriod: cadence}: since a dispatched backup's
// BackupConfig.LastBackupAt only advances once the host reports completion
// (manmanv2/processor/handlers/backup_status.go), a config stays "due"
// (BackupConfigRepository.ListDue) for every scan between dispatch and
// completion -- without this bucketing, a slow backup would be
// re-dispatched on every 1-minute scan instead of once per cadence.
func dispatchWindow(now time.Time, cadenceMinutes int) int64 {
	cadence := time.Duration(cadenceMinutes) * time.Minute
	if cadence <= 0 {
		cadence = time.Minute
	}
	return now.Unix() / int64(cadence.Seconds())
}

// DispatchWorkflowID returns the deterministic DispatchBackupWorkflow id for
// backupConfigID's current cadence window as of now: "backup-dispatch-<id>-
// <window>". Two BackupScanWorkflow runs inside the same window compute the
// same id, so the second child-workflow start collides
// (WorkflowIDReusePolicy REJECT_DUPLICATE below) instead of double-
// dispatching the same config.
func DispatchWorkflowID(backupConfigID int64, cadenceMinutes int, now time.Time) string {
	return fmt.Sprintf("backup-dispatch-%d-%d", backupConfigID, dispatchWindow(now, cadenceMinutes))
}

// BackupScanWorkflow is the body of the manmanv2-backup-scan Schedule
// (ScanScheduleID): it lists every BackupConfig due for a backup and starts
// one DispatchBackupWorkflow child per due config, keyed so a config due in
// the same cadence window is not dispatched twice (FR16).
//
// Every due config is given a child workflow -- see the loop below and
// DispatchWorkflowID's doc comment for how the same-window collision is
// detected and tolerated rather than treated as this workflow's failure
// (NFR1: no due config is silently skipped by this workflow itself; a
// config's own DispatchBackupWorkflow can still fail after retries, which
// is a distinct, independently observable failure -- see that workflow's
// doc comment).
func BackupScanWorkflow(ctx workflow.Context) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: time.Second,
			MaximumAttempts: 3,
		},
	})

	now := workflow.Now(ctx)

	var due []*manman.BackupConfig
	if err := workflow.ExecuteActivity(ctx, ActivityListDueBackupConfigs, now).Get(ctx, &due); err != nil {
		return fmt.Errorf("list due backup configs: %w", err)
	}

	logger := workflow.GetLogger(ctx)
	logger.Info("backup scan found due configs", "count", len(due))

	type startedChild struct {
		backupConfigID int64
		future         workflow.ChildWorkflowFuture
	}
	started := make([]startedChild, 0, len(due))

	for _, cfg := range due {
		childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			WorkflowID:            DispatchWorkflowID(cfg.BackupConfigID, cfg.CadenceMinutes, now),
			WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
			ParentClosePolicy:     enumspb.PARENT_CLOSE_POLICY_ABANDON,
		})
		future := workflow.ExecuteChildWorkflow(childCtx, DispatchBackupWorkflow, cfg.BackupConfigID)
		started = append(started, startedChild{backupConfigID: cfg.BackupConfigID, future: future})
	}

	// Wait for each child to start (not to finish -- ParentClosePolicy
	// ABANDON lets a dispatch keep running past this scan's own
	// completion) so a same-window collision is observed and logged here
	// rather than left as an unobserved child-start error.
	for _, sc := range started {
		var childExec workflow.Execution
		if err := sc.future.GetChildWorkflowExecution().Get(ctx, &childExec); err != nil {
			var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
			if errors.As(err, &alreadyStarted) {
				logger.Info("backup dispatch already in flight for this cadence window, skipping", "backup_config_id", sc.backupConfigID)
				continue
			}
			return fmt.Errorf("start backup dispatch for config %d: %w", sc.backupConfigID, err)
		}
	}

	return nil
}

// DispatchBackupWorkflow ports scheduledBackupWorker.Work
// (manmanv2/processor/backup_scheduler.go) to a single DispatchBackup
// activity execution for one BackupConfig (FR17). dispatchActivityOptions
// bounds the activity's retry so a transient S3/RMQ failure retries instead
// of dropping the config on the floor (NFR1); once retries are exhausted
// the error propagates as this workflow's own failure rather than being
// swallowed.
func DispatchBackupWorkflow(ctx workflow.Context, backupConfigID int64) error {
	ctx = workflow.WithActivityOptions(ctx, dispatchActivityOptions())
	return workflow.ExecuteActivity(ctx, ActivityDispatchBackup, backupConfigID).Get(ctx, nil)
}
