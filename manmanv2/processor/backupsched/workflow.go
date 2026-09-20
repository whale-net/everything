package backupsched

import (
	"go.temporal.io/sdk/workflow"
)

// BackupScanWorkflow is the body of the manmanv2-backup-scan Schedule
// (ScanScheduleID): it lists every BackupConfig due for a backup and starts
// one DispatchBackupWorkflow child per due config, keyed so a config due in
// the same cadence window is not dispatched twice.
//
// Implementation phase wires the ListDueBackupConfigs activity call and the
// child-workflow dispatch loop (FR16); this scaffold registers the
// signature so the worker binary and its BUILD target compile end to end.
func BackupScanWorkflow(ctx workflow.Context) error {
	return nil
}

// DispatchBackupWorkflow ports scheduledBackupWorker.Work
// (manmanv2/processor/backup_scheduler.go) to a single DispatchBackup
// activity execution for one BackupConfig (FR17).
//
// Implementation phase wires the ExecuteActivity call and its retry policy
// (NFR1); this scaffold registers the signature so BackupScanWorkflow has a
// child workflow to start.
func DispatchBackupWorkflow(ctx workflow.Context, backupConfigID int64) error {
	return nil
}
