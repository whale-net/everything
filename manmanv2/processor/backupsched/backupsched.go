// Package backupsched is the Temporal-based replacement for the River
// periodic due-config scan and per-config backup dispatch in
// manmanv2/processor/backup_scheduler.go (FR16, FR17). It runs inside the
// event-processor worker binary (//manmanv2/processor:event-processor) on
// task queue DefaultTaskQueue, per manmanv2/ARCHITECTURE.md's "Backup
// Scheduler (Temporal)" section and its one-task-queue-per-worker-binary
// convention.
//
// River continues to run alongside this package until #2819 removes it —
// this package must not change cadence semantics, the S3 key scheme, the
// Backup record shape, or the command.host.<server_id>.backup message
// (NFR5).
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
