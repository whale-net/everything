package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	s3lib "github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manmanv2"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
)

// HandleBackup archives a volume sub-path and streams it to S3.
func (h *CommandHandlerImpl) HandleBackup(ctx context.Context, cmd *hostrmq.BackupCommand) error {
	if h.s3Client == nil {
		return fmt.Errorf("s3 client not configured, cannot run backup %d", cmd.BackupID)
	}

	slog.Info("processing backup command", "backup_id", cmd.BackupID, "sgc_id", cmd.SGCID, "s3_key", cmd.S3Key)

	fail := func(err error) error {
		msg := err.Error()
		_ = h.publisher.PublishBackupStatus(ctx, &hostrmq.BackupStatusUpdate{
			BackupID:     cmd.BackupID,
			Status:       manman.BackupStatusFailed,
			ErrorMessage: &msg,
		})
		return err
	}

	// 1. Execute pre-backup actions via container stdin
	if len(cmd.PreActionCommands) > 0 {
		state, exists := h.sessionManager.GetSessionStateBySGCID(cmd.SGCID)
		if !exists {
			slog.Warn("no active session for pre-backup actions, skipping", "sgc_id", cmd.SGCID)
		} else {
			for _, cmdStr := range cmd.PreActionCommands {
				if err := h.sessionManager.SendInput(ctx, state.SessionID, []byte(cmdStr+"\n")); err != nil {
					slog.Warn("failed to send pre-backup action", "sgc_id", cmd.SGCID, "error", err)
				}
				time.Sleep(2 * time.Second)
			}
		}
	}

	// 2. Build tar command: stream to stdout
	tarPath := cmd.VolumeHostPath
	if tarPath == "" {
		return fail(fmt.Errorf("volume_host_path is empty"))
	}
	backupPath := cmd.BackupPath
	if backupPath == "" {
		backupPath = "."
	}

	tarCmd := exec.CommandContext(ctx, "tar", "-czf", "-", "-C", tarPath, backupPath)
	tarReader, err := tarCmd.StdoutPipe()
	if err != nil {
		return fail(fmt.Errorf("failed to create tar stdout pipe: %w", err))
	}
	if err := tarCmd.Start(); err != nil {
		return fail(fmt.Errorf("failed to start tar: %w", err))
	}

	// 3. Stream to S3 via multipart upload
	s3URL, err := h.s3Client.UploadStream(ctx, cmd.S3Key, tarReader, &s3lib.UploadOptions{
		ContentType: "application/gzip",
	})
	if err != nil {
		_ = tarCmd.Process.Kill()
		return fail(fmt.Errorf("failed to upload to S3: %w", err))
	}

	if err := tarCmd.Wait(); err != nil {
		return fail(fmt.Errorf("tar exited with error: %w", err))
	}

	slog.Info("backup completed", "backup_id", cmd.BackupID, "s3_url", s3URL)

	return h.publisher.PublishBackupStatus(ctx, &hostrmq.BackupStatusUpdate{
		BackupID: cmd.BackupID,
		S3URL:    &s3URL,
		Status:   manman.BackupStatusCompleted,
	})
}
