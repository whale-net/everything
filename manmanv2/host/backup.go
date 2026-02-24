package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/whale-net/everything/manmanv2"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
)

// HandleBackup archives a volume sub-path and streams it to S3 via pre-signed URL.
func (h *CommandHandlerImpl) HandleBackup(ctx context.Context, cmd *hostrmq.BackupCommand) error {
	if cmd.PresignedURL == "" {
		return fmt.Errorf("presigned_url is empty for backup %d", cmd.BackupID)
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
	// VolumeHostPath is the host_subpath (e.g. "data"); build the full internal path:
	//   {internalDataDir}/sgc[-{env}]-{sgcID}/{hostSubpath}
	dirName := fmt.Sprintf("sgc-%d", cmd.SGCID)
	if h.environment != "" {
		dirName = fmt.Sprintf("sgc-%s-%d", h.environment, cmd.SGCID)
	}
	subPath := strings.TrimPrefix(cmd.VolumeHostPath, "/")
	if subPath == "" {
		return fail(fmt.Errorf("volume_host_path is empty"))
	}
	tarPath := filepath.Join(h.internalDataDir, dirName, subPath)
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

	// 3. Buffer tar output to temp file (needed for Content-Length on presigned PUT)
	tmpFile, err := os.CreateTemp("", "backup-*.tar.gz")
	if err != nil {
		_ = tarCmd.Process.Kill()
		return fail(fmt.Errorf("failed to create temp file: %w", err))
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, tarReader); err != nil {
		_ = tarCmd.Process.Kill()
		return fail(fmt.Errorf("failed to buffer tar output: %w", err))
	}
	if err := tarCmd.Wait(); err != nil {
		return fail(fmt.Errorf("tar exited with error: %w", err))
	}

	size, err := tmpFile.Seek(0, io.SeekEnd)
	if err != nil {
		return fail(fmt.Errorf("failed to get temp file size: %w", err))
	}
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("failed to rewind temp file: %w", err))
	}

	// 4. Upload to S3 via pre-signed PUT URL with known Content-Length
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, cmd.PresignedURL, tmpFile)
	if err != nil {
		return fail(fmt.Errorf("failed to create upload request: %w", err))
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.ContentLength = size

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_ = tarCmd.Process.Kill()
		return fail(fmt.Errorf("failed to upload to S3: %w", err))
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fail(fmt.Errorf("S3 upload returned status %d", resp.StatusCode))
	}

	s3URL := fmt.Sprintf("s3://%s", cmd.S3Key)
	slog.Info("backup completed", "backup_id", cmd.BackupID, "s3_url", s3URL)

	return h.publisher.PublishBackupStatus(ctx, &hostrmq.BackupStatusUpdate{
		BackupID: cmd.BackupID,
		S3URL:    &s3URL,
		Status:   manman.BackupStatusCompleted,
	})
}
