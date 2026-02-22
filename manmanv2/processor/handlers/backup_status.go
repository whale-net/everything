package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/host/rmq"
)

// BackupStatusHandler handles status.backup.* messages from the host-manager
type BackupStatusHandler struct {
	repo   *repository.Repository
	logger *slog.Logger
}

func NewBackupStatusHandler(repo *repository.Repository, logger *slog.Logger) *BackupStatusHandler {
	return &BackupStatusHandler{repo: repo, logger: logger}
}

func (h *BackupStatusHandler) Handle(ctx context.Context, routingKey string, body []byte) error {
	var msg rmq.BackupStatusUpdate
	if err := json.Unmarshal(body, &msg); err != nil {
		return &PermanentError{Err: fmt.Errorf("failed to unmarshal backup status: %w", err)}
	}

	h.logger.Info("processing backup status update", "backup_id", msg.BackupID, "status", msg.Status)

	if err := h.repo.Backups.UpdateStatus(ctx, msg.BackupID, msg.Status, msg.S3URL, msg.SizeBytes, msg.ErrorMessage); err != nil {
		return fmt.Errorf("failed to update backup status: %w", err)
	}

	// On completion, update last_backup_at on the BackupConfig
	if msg.Status == "completed" {
		backup, err := h.repo.Backups.Get(ctx, msg.BackupID)
		if err != nil {
			h.logger.Warn("failed to fetch backup for last_backup_at update", "backup_id", msg.BackupID, "error", err)
			return nil
		}
		if backup.BackupConfigID != nil {
			if err := h.repo.BackupConfigs.UpdateLastBackupAt(ctx, *backup.BackupConfigID, time.Now()); err != nil {
				h.logger.Warn("failed to update last_backup_at", "backup_config_id", *backup.BackupConfigID, "error", err)
			}
		}
	}

	return nil
}
