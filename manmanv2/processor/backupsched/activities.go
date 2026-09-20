package backupsched

import (
	"context"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
	s3lib "github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	manman "github.com/whale-net/everything/manmanv2/models"
)

// Activities holds the dependencies ListDueBackupConfigs and DispatchBackup
// need: the shared repository, the action-template repository, the RMQ
// publisher, and the S3 client — the same four scheduledBackupWorker
// (manmanv2/processor/backup_scheduler.go) holds today. The event-processor
// worker constructs one Activities value at startup and registers both
// methods with the Temporal worker.
type Activities struct {
	Repo       *repository.Repository
	ActionRepo *postgres.ActionRepository
	Publisher  *rmq.Publisher
	S3Client   *s3lib.Client
}

// ListDueBackupConfigs returns every enabled BackupConfig due for a backup
// as of now, backed by the existing BackupConfigRepository.ListDue query —
// unchanged, since that query is the cadence semantics FR16 must preserve.
func (a *Activities) ListDueBackupConfigs(ctx context.Context, now time.Time) ([]*manman.BackupConfig, error) {
	return a.Repo.BackupConfigs.ListDue(ctx, now)
}

// DispatchBackup ports scheduledBackupWorker.Work
// (manmanv2/processor/backup_scheduler.go) verbatim in behavior: skip
// disabled configs; resolve volume, eligible SGCs and their active session
// and server; render pre-backup action templates; create the Backup record
// with Status = pending and TriggerSource = scheduled; build the S3 key
// backups/<sgc_id>/<backup_config_id>/<backup_id>.tar.gz; and publish the
// BackupCommand payload to command.host.<server_id>.backup on the manman
// exchange (FR17, NFR5).
//
// Implementation phase ports the body; this scaffold registers the
// signature Activities.DispatchBackup so main.go can register it with the
// Temporal worker.
func (a *Activities) DispatchBackup(ctx context.Context, backupConfigID int64) error {
	return nil
}
