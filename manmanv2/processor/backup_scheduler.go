package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"text/template"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/whale-net/everything/libs/go/rmq"
	s3lib "github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manmanv2/models"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
)

// ============================================================================
// Scan job: runs every minute, enqueues one ScheduledBackupJob per due config
// ============================================================================

type backupScanArgs struct{}

func (backupScanArgs) Kind() string { return "backup_scan" }

type backupScanWorker struct {
	river.WorkerDefaults[backupScanArgs]
	repo        *repository.Repository
	riverClient *river.Client[pgx.Tx]
	logger      *slog.Logger
}

func (w *backupScanWorker) Work(ctx context.Context, _ *river.Job[backupScanArgs]) error {
	due, err := w.repo.BackupConfigs.ListDue(ctx, time.Now())
	if err != nil {
		return fmt.Errorf("failed to list due backup configs: %w", err)
	}
	for _, cfg := range due {
		_, err := w.riverClient.Insert(ctx, scheduledBackupArgs{BackupConfigID: cfg.BackupConfigID}, &river.InsertOpts{
			UniqueOpts: river.UniqueOpts{
				ByArgs:   true,
				ByPeriod: time.Duration(cfg.CadenceMinutes) * time.Minute,
			},
		})
		if err != nil {
			w.logger.Error("failed to enqueue backup job", "backup_config_id", cfg.BackupConfigID, "error", err)
		}
	}
	return nil
}

// ============================================================================
// Backup job: archives one backup config across all eligible SGCs
// ============================================================================

type scheduledBackupArgs struct {
	BackupConfigID int64 `json:"backup_config_id"`
}

func (scheduledBackupArgs) Kind() string { return "scheduled_backup" }

type scheduledBackupWorker struct {
	river.WorkerDefaults[scheduledBackupArgs]
	repo       *repository.Repository
	actionRepo *postgres.ActionRepository
	publisher  *rmq.Publisher
	s3Client   *s3lib.Client
	logger     *slog.Logger
}

func (w *scheduledBackupWorker) Work(ctx context.Context, job *river.Job[scheduledBackupArgs]) error {
	cfg, err := w.repo.BackupConfigs.Get(ctx, job.Args.BackupConfigID)
	if err != nil {
		return fmt.Errorf("backup config %d not found: %w", job.Args.BackupConfigID, err)
	}
	if !cfg.Enabled {
		return nil
	}

	volume, err := w.repo.GameConfigVolumes.Get(ctx, cfg.VolumeID)
	if err != nil {
		return fmt.Errorf("volume %d not found: %w", cfg.VolumeID, err)
	}

	sgcs, err := w.repo.ServerGameConfigs.List(ctx, nil, 100, 0)
	if err != nil {
		return fmt.Errorf("failed to list SGCs: %w", err)
	}

	actions, _ := w.repo.BackupConfigs.ListActions(ctx, cfg.BackupConfigID)
	preActionCommands := make([]string, 0, len(actions))
	for _, a := range actions {
		def, _, err := w.actionRepo.Get(ctx, a.ActionID)
		if err != nil {
			w.logger.Warn("failed to get action definition, skipping", "action_id", a.ActionID, "error", err)
			continue
		}
		rendered, err := renderSchedulerActionTemplate(def.CommandTemplate)
		if err != nil {
			w.logger.Warn("failed to render action template, skipping", "action_id", a.ActionID, "error", err)
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

		sessions, err := w.repo.Sessions.List(ctx, &sgc.SGCID, 1, 0)
		if err != nil || len(sessions) == 0 {
			continue
		}

		server, err := w.repo.Servers.Get(ctx, sgc.ServerID)
		if err != nil {
			w.logger.Warn("failed to get server", "sgc_id", sgc.SGCID, "error", err)
			continue
		}

		backup := &manman.Backup{
			SessionID:          sessions[0].SessionID,
			ServerGameConfigID: sgc.SGCID,
			BackupConfigID:     &cfg.BackupConfigID,
			VolumeID:           &volume.VolumeID,
			Status:             manman.BackupStatusPending,
			CreatedAt:          time.Now(),
		}
		backup, err = w.repo.Backups.Create(ctx, backup)
		if err != nil {
			w.logger.Error("failed to create backup record", "sgc_id", sgc.SGCID, "error", err)
			continue
		}

		s3Key := fmt.Sprintf("backups/%d/%d/%d.tar.gz", sgc.SGCID, cfg.BackupConfigID, backup.BackupID)

		presignedURL, err := w.s3Client.PresignPutURL(ctx, s3Key, 1*time.Hour)
		if err != nil {
			w.logger.Error("failed to generate presigned URL", "backup_id", backup.BackupID, "error", err)
			_ = w.repo.Backups.UpdateStatus(ctx, backup.BackupID, manman.BackupStatusFailed, nil, nil, strPtr(err.Error()))
			continue
		}

		cmd := &hostrmq.BackupCommand{
			BackupID:          backup.BackupID,
			SGCID:             sgc.SGCID,
			VolumeHostPath:    hostPath,
			BackupPath:        cfg.BackupPath,
			S3Key:             s3Key,
			PresignedURL:      presignedURL,
			PreActionCommands: preActionCommands,
		}

		routingKey := fmt.Sprintf("command.host.%d.backup", server.ServerID)
		if err := w.publisher.Publish(ctx, "manman", routingKey, cmd); err != nil {
			w.logger.Error("failed to dispatch backup command", "backup_id", backup.BackupID, "error", err)
		}
	}
	return nil
}

// ============================================================================
// Startup
// ============================================================================

func startBackupScheduler(ctx context.Context, dbPool *pgxpool.Pool, repo *repository.Repository, rmqConn *rmq.Connection, s3Client *s3lib.Client, logger *slog.Logger) (*river.Client[pgx.Tx], error) {
	// Run River schema migrations
	migrator, err := rivermigrate.New(riverpgxv5.New(dbPool), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create river migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return nil, fmt.Errorf("failed to run river migrations: %w", err)
	}
	logger.Info("river schema migrations applied")

	publisher, err := rmq.NewPublisher(rmqConn)
	if err != nil {
		return nil, fmt.Errorf("failed to create backup publisher: %w", err)
	}

	actionRepo := postgres.NewActionRepository(dbPool)

	workers := river.NewWorkers()

	var riverClient *river.Client[pgx.Tx]

	scanWorker := &backupScanWorker{
		repo:   repo,
		logger: logger,
	}
	river.AddWorker(workers, scanWorker)
	river.AddWorker(workers, &scheduledBackupWorker{
		repo:       repo,
		actionRepo: actionRepo,
		publisher:  publisher,
		s3Client:   s3Client,
		logger:     logger,
	})

	riverClient, err = river.NewClient(riverpgxv5.New(dbPool), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 5},
		},
		Workers: workers,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(1*time.Minute),
				func() (river.JobArgs, *river.InsertOpts) {
					return backupScanArgs{}, nil
				},
				&river.PeriodicJobOpts{RunOnStart: true},
			),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create river client: %w", err)
	}

	// Wire the client reference into the scan worker
	scanWorker.riverClient = riverClient

	if err := riverClient.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start river client: %w", err)
	}

	logger.Info("backup scheduler started")
	return riverClient, nil
}

func renderSchedulerActionTemplate(tmplStr string) (string, error) {
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

func strPtr(s string) *string { return &s }
