package backupsched

import (
	"bytes"
	"context"
	"fmt"
	"text/template"
	"time"

	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
	s3lib "github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
	manman "github.com/whale-net/everything/manmanv2/models"
)

// log is a package-level slog logger, not activity.GetLogger(ctx): tests
// call Activities methods directly with a plain context.Context (not
// through Temporal's activity execution machinery), and activity.GetLogger
// panics outside a real activity context -- see
// tools/app_registry/worker/release/activities.go's workerLog for the same
// precedent.
var log = logging.Get("manmanv2-backupsched")

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
// (manmanv2/processor/backup_scheduler.go) verbatim in behavior for one
// BackupConfig: skip disabled configs; resolve volume, eligible SGCs and
// their active session and server; render pre-backup action templates;
// create the Backup record with Status = pending and
// TriggerSource = scheduled; build the S3 key
// backups/<sgc_id>/<backup_config_id>/<backup_id>.tar.gz; and publish the
// BackupCommand payload to command.host.<server_id>.backup on the manman
// exchange (FR17, NFR5). Every field of BackupCommand this sets today is
// set exactly as scheduledBackupWorker.Work sets it, including leaving
// CreatedAt at its zero value -- see that function for the field-for-field
// mapping this must not drift from.
func (a *Activities) DispatchBackup(ctx context.Context, backupConfigID int64) error {
	cfg, err := a.Repo.BackupConfigs.Get(ctx, backupConfigID)
	if err != nil {
		return fmt.Errorf("backup config %d not found: %w", backupConfigID, err)
	}
	if !cfg.Enabled {
		log.InfoContext(ctx, "skipping disabled backup config", "backup_config_id", backupConfigID)
		return nil
	}

	volume, err := a.Repo.GameConfigVolumes.Get(ctx, cfg.VolumeID)
	if err != nil {
		return fmt.Errorf("volume %d not found: %w", cfg.VolumeID, err)
	}

	sgcs, err := a.Repo.ServerGameConfigs.List(ctx, nil, 100, 0)
	if err != nil {
		return fmt.Errorf("failed to list SGCs: %w", err)
	}

	actions, _ := a.Repo.BackupConfigs.ListActions(ctx, cfg.BackupConfigID)
	preActionCommands := make([]string, 0, len(actions))
	for _, act := range actions {
		def, _, err := a.ActionRepo.Get(ctx, act.ActionID)
		if err != nil {
			log.WarnContext(ctx, "failed to get action definition, skipping", "action_id", act.ActionID, "error", err)
			continue
		}
		rendered, err := renderActionTemplate(def.CommandTemplate)
		if err != nil {
			log.WarnContext(ctx, "failed to render action template, skipping", "action_id", act.ActionID, "error", err)
			continue
		}
		preActionCommands = append(preActionCommands, rendered)
	}

	hostPath := ""
	if volume.HostSubpath != nil {
		hostPath = *volume.HostSubpath
	}

	for _, sgc := range sgcs {
		if sgc.GameConfigID != volume.ConfigID {
			continue
		}

		sessions, err := a.Repo.Sessions.List(ctx, &sgc.SGCID, 1, 0)
		if err != nil || len(sessions) == 0 {
			continue
		}

		server, err := a.Repo.Servers.Get(ctx, sgc.ServerID)
		if err != nil {
			log.WarnContext(ctx, "failed to get server", "sgc_id", sgc.SGCID, "error", err)
			continue
		}

		backup := &manman.Backup{
			SessionID:          sessions[0].SessionID,
			ServerGameConfigID: sgc.SGCID,
			BackupConfigID:     &cfg.BackupConfigID,
			VolumeID:           &volume.VolumeID,
			Status:             manman.BackupStatusPending,
			TriggerSource:      manman.BackupTriggerSourceScheduled,
			CreatedAt:          time.Now(),
		}
		backup, err = a.Repo.Backups.Create(ctx, backup)
		if err != nil {
			log.ErrorContext(ctx, "failed to create backup record", "sgc_id", sgc.SGCID, "error", err)
			continue
		}

		s3Key := fmt.Sprintf("backups/%d/%d/%d.tar.gz", sgc.SGCID, cfg.BackupConfigID, backup.BackupID)

		presignedURL, err := a.S3Client.PresignPutURL(ctx, s3Key, 1*time.Hour)
		if err != nil {
			log.ErrorContext(ctx, "failed to generate presigned URL", "backup_id", backup.BackupID, "error", err)
			errMsg := err.Error()
			_ = a.Repo.Backups.UpdateStatus(ctx, backup.BackupID, manman.BackupStatusFailed, nil, nil, &errMsg)
			continue
		}

		cmd := &hostrmq.BackupCommand{
			BackupID:          backup.BackupID,
			SGCID:             sgc.SGCID,
			VolumeType:        volume.VolumeType,
			VolumeHostPath:    hostPath,
			VolumeName:        volume.Name,
			BackupPath:        cfg.BackupPath,
			S3Key:             s3Key,
			PresignedURL:      presignedURL,
			PreActionCommands: preActionCommands,
		}

		routingKey := fmt.Sprintf("command.host.%d.backup", server.ServerID)
		if err := a.Publisher.Publish(ctx, "manman", routingKey, cmd); err != nil {
			log.ErrorContext(ctx, "failed to dispatch backup command", "backup_id", backup.BackupID, "error", err)
		}
	}
	return nil
}

// renderActionTemplate ports renderSchedulerActionTemplate
// (manmanv2/processor/backup_scheduler.go) verbatim.
func renderActionTemplate(tmplStr string) (string, error) {
	tmpl, err := template.New("action").Parse(tmplStr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err != nil {
		return "", err
	}
	return buf.String(), nil
}
