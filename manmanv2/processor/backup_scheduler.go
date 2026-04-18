package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"text/template"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/libs/go/rmq"
	s3lib "github.com/whale-net/everything/libs/go/s3"
	manman "github.com/whale-net/everything/manmanv2/models"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
)

const (
	backupTaskQueue  = "manmanv2-backup"
	backupScheduleID = "manmanv2-backup-scanner"
)

// ============================================================================
// Activities
// ============================================================================

type backupActivities struct {
	repo       *repository.Repository
	actionRepo *postgres.ActionRepository
	publisher  *rmq.Publisher
	s3Client   *s3lib.Client
	logger     *slog.Logger
}

// ListDueBackupConfigsActivity returns IDs of all enabled backup configs that are due.
func (a *backupActivities) ListDueBackupConfigsActivity(ctx context.Context) ([]int64, error) {
	configs, err := a.repo.BackupConfigs.ListDue(ctx, time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to list due backup configs: %w", err)
	}
	ids := make([]int64, len(configs))
	for i, cfg := range configs {
		ids[i] = cfg.BackupConfigID
	}
	return ids, nil
}

// ExecuteBackupActivity runs the full backup pipeline for one backup config:
// it iterates all eligible SGCs, creates backup records, presigns S3 URLs,
// and dispatches backup commands via RabbitMQ.
func (a *backupActivities) ExecuteBackupActivity(ctx context.Context, backupConfigID int64) error {
	cfg, err := a.repo.BackupConfigs.Get(ctx, backupConfigID)
	if err != nil {
		return fmt.Errorf("backup config %d not found: %w", backupConfigID, err)
	}
	if !cfg.Enabled {
		return nil
	}

	volume, err := a.repo.GameConfigVolumes.Get(ctx, cfg.VolumeID)
	if err != nil {
		return fmt.Errorf("volume %d not found: %w", cfg.VolumeID, err)
	}

	sgcs, err := a.repo.ServerGameConfigs.List(ctx, nil, 100, 0)
	if err != nil {
		return fmt.Errorf("failed to list SGCs: %w", err)
	}

	actions, _ := a.repo.BackupConfigs.ListActions(ctx, cfg.BackupConfigID)
	preActionCommands := make([]string, 0, len(actions))
	for _, bca := range actions {
		def, _, err := a.actionRepo.Get(ctx, bca.ActionID)
		if err != nil {
			a.logger.Warn("failed to get action definition, skipping", "action_id", bca.ActionID, "error", err)
			continue
		}
		rendered, err := renderSchedulerActionTemplate(def.CommandTemplate)
		if err != nil {
			a.logger.Warn("failed to render action template, skipping", "action_id", bca.ActionID, "error", err)
			continue
		}
		preActionCommands = append(preActionCommands, rendered)
	}

	hostPath := ""
	if volume.HostSubpath != nil {
		hostPath = *volume.HostSubpath
	}

	actLogger := activity.GetLogger(ctx)

	for _, sgc := range sgcs {
		if sgc.GameConfigID != volume.ConfigID {
			continue
		}

		sessions, err := a.repo.Sessions.List(ctx, &sgc.SGCID, 1, 0)
		if err != nil || len(sessions) == 0 {
			continue
		}

		server, err := a.repo.Servers.Get(ctx, sgc.ServerID)
		if err != nil {
			actLogger.Warn("failed to get server", "sgc_id", sgc.SGCID, "error", err)
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
		backup, err = a.repo.Backups.Create(ctx, backup)
		if err != nil {
			actLogger.Error("failed to create backup record", "sgc_id", sgc.SGCID, "error", err)
			continue
		}

		s3Key := fmt.Sprintf("backups/%d/%d/%d.tar.gz", sgc.SGCID, cfg.BackupConfigID, backup.BackupID)

		presignedURL, err := a.s3Client.PresignPutURL(ctx, s3Key, 1*time.Hour)
		if err != nil {
			actLogger.Error("failed to generate presigned URL", "backup_id", backup.BackupID, "error", err)
			_ = a.repo.Backups.UpdateStatus(ctx, backup.BackupID, manman.BackupStatusFailed, nil, nil, strPtr(err.Error()))
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
		if err := a.publisher.Publish(ctx, "manman", routingKey, cmd); err != nil {
			actLogger.Error("failed to dispatch backup command", "backup_id", backup.BackupID, "error", err)
		}
	}
	return nil
}

// ============================================================================
// Workflow
// ============================================================================

// BackupScanWorkflow lists all due backup configs and executes each backup.
// It is triggered on a schedule (every minute) via a Temporal Schedule.
func BackupScanWorkflow(ctx workflow.Context) error {
	logger := workflow.GetLogger(ctx)

	var acts *backupActivities

	var configIDs []int64
	scanOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	}
	if err := workflow.ExecuteActivity(
		workflow.WithActivityOptions(ctx, scanOpts),
		acts.ListDueBackupConfigsActivity,
	).Get(ctx, &configIDs); err != nil {
		return fmt.Errorf("list due backup configs: %w", err)
	}

	if len(configIDs) == 0 {
		return nil
	}

	backupOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 2,
		},
	}
	backupCtx := workflow.WithActivityOptions(ctx, backupOpts)

	for _, configID := range configIDs {
		if err := workflow.ExecuteActivity(backupCtx, acts.ExecuteBackupActivity, configID).Get(ctx, nil); err != nil {
			logger.Error("backup activity failed", "backup_config_id", configID, "error", err)
			// continue processing other configs
		}
	}

	return nil
}

// ============================================================================
// Startup
// ============================================================================

func startBackupScheduler(
	ctx context.Context,
	dbPool *pgxpool.Pool,
	repo *repository.Repository,
	rmqConn *rmq.Connection,
	s3Client *s3lib.Client,
	temporalHost string,
	logger *slog.Logger,
) (worker.Worker, client.Client, error) {
	publisher, err := rmq.NewPublisher(rmqConn)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create backup publisher: %w", err)
	}

	acts := &backupActivities{
		repo:       repo,
		actionRepo: postgres.NewActionRepository(dbPool),
		publisher:  publisher,
		s3Client:   s3Client,
		logger:     logger,
	}

	// Connect to Temporal
	temporalClient, err := client.Dial(client.Options{
		HostPort: temporalHost,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to Temporal: %w", err)
	}

	// Create and start worker
	w := worker.New(temporalClient, backupTaskQueue, worker.Options{})
	w.RegisterWorkflow(BackupScanWorkflow)
	w.RegisterActivity(acts)

	if err := w.Start(); err != nil {
		temporalClient.Close()
		return nil, nil, fmt.Errorf("failed to start Temporal worker: %w", err)
	}

	// Upsert schedule to run BackupScanWorkflow every minute
	if err := upsertBackupSchedule(ctx, temporalClient); err != nil {
		w.Stop()
		temporalClient.Close()
		return nil, nil, fmt.Errorf("failed to upsert backup schedule: %w", err)
	}

	logger.Info("backup scheduler started", "task_queue", backupTaskQueue, "schedule_id", backupScheduleID)
	return w, temporalClient, nil
}

// upsertBackupSchedule creates or updates the Temporal Schedule that triggers BackupScanWorkflow.
func upsertBackupSchedule(ctx context.Context, c client.Client) error {
	scheduleClient := c.ScheduleClient()
	handle := scheduleClient.GetHandle(ctx, backupScheduleID)

	scheduleSpec := client.ScheduleSpec{
		Intervals: []client.ScheduleIntervalSpec{
			{Every: time.Minute},
		},
	}
	scheduleAction := client.ScheduleWorkflowAction{
		ID:        backupScheduleID + "-run",
		Workflow:  BackupScanWorkflow,
		TaskQueue: backupTaskQueue,
	}

	_, err := handle.Describe(ctx)
	if err != nil {
		// Schedule doesn't exist — create it
		_, createErr := scheduleClient.Create(ctx, client.ScheduleOptions{
			ID:     backupScheduleID,
			Spec:   scheduleSpec,
			Action: &scheduleAction,
		})
		return createErr
	}

	// Schedule exists — update it
	return handle.Update(ctx, client.ScheduleUpdateOptions{
		DoUpdate: func(input client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
			input.Description.Schedule.Spec = &scheduleSpec
			input.Description.Schedule.Action = &scheduleAction
			return &client.ScheduleUpdate{Schedule: &input.Description.Schedule}, nil
		},
	})
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
