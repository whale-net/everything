// Package backupsched is the Temporal-based periodic due-config scan and
// per-config backup dispatch scheduler, and the sole backup scheduler now
// that the earlier River-based one has been removed (FR16, FR17, NFR2). It
// runs inside the event-processor worker binary
// (//manmanv2/processor:event-processor) on task queue DefaultTaskQueue,
// per manmanv2/ARCHITECTURE.md's "Backup Scheduler (Temporal)" section and
// its one-task-queue-per-worker-binary convention.
//
// This package must not change cadence semantics, the S3 key scheme, the
// Backup record shape, or the command.host.<server_id>.backup message
// (NFR5) — those are load-bearing behaviors preserved from the River
// scheduler it replaced.
package backupsched

import "time"

// DefaultTaskQueue is the task queue this package's worker registration
// polls when TEMPORAL_TASK_QUEUE is unset, named after the worker binary
// that polls it (manmanv2/ARCHITECTURE.md's task-queue convention).
const DefaultTaskQueue = "manmanv2-processor"

// ScanScheduleID is the Temporal Schedule ID for the periodic due-config
// scan (BackupScanWorkflow), upserted at worker startup via
// libs/go/temporal.UpsertSchedule so a changed ScanInterval is applied on
// restart.
const ScanScheduleID = "manmanv2-backup-scan"

// ScanInterval is how often the scan schedule fires, matching the River
// scheduler's PeriodicInterval(1 * time.Minute) (FR16 cadence semantics).
const ScanInterval = time.Minute

// Activity names, registered explicitly in manmanv2/processor/main.go via
// activity.RegisterOptions{Name: ...} and referenced by name (not by method
// value) from workflow.go, so the workflow code has no compile-time
// dependency on the concrete *Activities receiver.
const (
	ActivityListDueBackupConfigs = "ListDueBackupConfigs"
	ActivityDispatchBackup       = "DispatchBackup"
)
