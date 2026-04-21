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

	"github.com/whale-net/everything/libs/go/docker"
	"github.com/whale-net/everything/manmanv2/models"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
)

// HandleBackup archives a volume sub-path and streams it to S3 via pre-signed URL.
func (h *CommandHandlerImpl) HandleBackup(ctx context.Context, cmd *hostrmq.BackupCommand) error {
	if cmd.PresignedURL == "" {
		return fmt.Errorf("presigned_url is empty for backup %d", cmd.BackupID)
	}

	slog.Info("processing backup command", "backup_id", cmd.BackupID, "sgc_id", cmd.SGCID, "s3_key", cmd.S3Key, "volume_type", cmd.VolumeType)

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

	// 2. Resolve the directory to tar; cleanup removes any temp dir used for named volumes.
	tarPath, cleanup, err := h.resolveBackupSourceDir(ctx, cmd)
	if err != nil {
		return fail(err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	backupPath := cmd.BackupPath
	if backupPath == "" {
		backupPath = "."
	}

	// 3. Build tar command: stream to stdout
	tarCmd := exec.CommandContext(ctx, "tar", "-czf", "-", "-C", tarPath, backupPath)
	tarReader, err := tarCmd.StdoutPipe()
	if err != nil {
		return fail(fmt.Errorf("failed to create tar stdout pipe: %w", err))
	}
	if err := tarCmd.Start(); err != nil {
		return fail(fmt.Errorf("failed to start tar: %w", err))
	}

	// 4. Buffer tar output to temp file (needed for Content-Length on presigned PUT)
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

	// 5. Upload to S3 via pre-signed PUT URL with known Content-Length
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, cmd.PresignedURL, io.NopCloser(tmpFile))
	if err != nil {
		return fail(fmt.Errorf("failed to create upload request: %w", err))
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.ContentLength = size
	// GetBody allows the HTTP client to retry on HTTP/2 REFUSED_STREAM
	req.GetBody = func() (io.ReadCloser, error) {
		if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		return io.NopCloser(tmpFile), nil
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fail(fmt.Errorf("failed to upload to S3: %w", err))
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fail(fmt.Errorf("S3 upload returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody))))
	}

	s3URL := fmt.Sprintf("s3://%s", cmd.S3Key)
	slog.Info("backup completed", "backup_id", cmd.BackupID, "s3_url", s3URL)

	return h.publisher.PublishBackupStatus(ctx, &hostrmq.BackupStatusUpdate{
		BackupID: cmd.BackupID,
		S3URL:    &s3URL,
		Status:   manman.BackupStatusCompleted,
	})
}

// resolveBackupSourceDir returns the directory path to pass to tar and an optional cleanup func.
// For bind-mount volumes it returns the host-accessible path directly.
// For named volumes it copies the volume contents into a temporary directory via a busybox
// helper container (the same pattern used by the workshop orchestrator for installs).
func (h *CommandHandlerImpl) resolveBackupSourceDir(ctx context.Context, cmd *hostrmq.BackupCommand) (tarPath string, cleanup func(), err error) {
	if cmd.VolumeType != "named" {
		// Bind-mount: derive the internal path from host_subpath. If host_subpath is not
		// set, fall back to the volume name to match other code paths that treat it as the
		// default bind-mount subdirectory.
		subPath := strings.TrimPrefix(cmd.VolumeHostPath, "/")
		if subPath == "" {
			subPath = strings.TrimPrefix(cmd.VolumeName, "/")
		}
		if subPath == "" {
			return "", nil, fmt.Errorf("volume_host_path and volume_name are empty for bind-mount backup %d", cmd.BackupID)
		}
		dirName := fmt.Sprintf("sgc-%d", cmd.SGCID)
		if h.environment != "" {
			dirName = fmt.Sprintf("sgc-%s-%d", h.environment, cmd.SGCID)
		}
		return filepath.Join(h.internalDataDir, dirName, subPath), nil, nil
	}

	// Named volume: Docker manages the storage location, so direct host filesystem access is
	// not available. Use a busybox helper container to copy the volume contents into a
	// temporary bind-mount directory that this process can then tar.
	if cmd.VolumeName == "" {
		return "", nil, fmt.Errorf("volume_name is empty for named-volume backup %d", cmd.BackupID)
	}
	dockerVolumeName := h.getNamedVolumeName(cmd.SGCID, cmd.VolumeName)

	// Create staging dir under internalDataDir so we can read the files directly.
	// Compute its equivalent host path for use as a Docker bind-mount source.
	stagingInternal, err := os.MkdirTemp(h.internalDataDir, fmt.Sprintf("backup-%d-*", cmd.BackupID))
	if err != nil {
		return "", nil, fmt.Errorf("failed to create backup staging dir: %w", err)
	}
	if err := os.Chmod(stagingInternal, 0777); err != nil {
		os.RemoveAll(stagingInternal)
		return "", nil, fmt.Errorf("failed to chmod backup staging dir: %w", err)
	}
	cleanup = func() { os.RemoveAll(stagingInternal) }

	internalBase := filepath.Clean(h.internalDataDir)
	stagingPath := filepath.Clean(stagingInternal)
	relPath, err := filepath.Rel(internalBase, stagingPath)
	if err != nil {
		return "", cleanup, fmt.Errorf("failed to resolve backup staging dir %q relative to %q: %w", stagingInternal, h.internalDataDir, err)
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) {
		return "", cleanup, fmt.Errorf("backup staging dir %q escapes internal data dir %q", stagingInternal, h.internalDataDir)
	}
	stagingHost := filepath.Join(h.hostDataDir, relPath)

	helperConfig := docker.ContainerConfig{
		Name:    fmt.Sprintf("backup-extract-%d-%d", cmd.SGCID, cmd.BackupID),
		Image:   "busybox:latest",
		Command: []string{"sh", "-c", "cp -a /vol/. /staging/"},
		Volumes: []string{
			fmt.Sprintf("%s:/vol", dockerVolumeName),
			fmt.Sprintf("%s:/staging", stagingHost),
		},
	}

	slog.Info("extracting named volume via helper container", "volume", dockerVolumeName, "staging", stagingHost)
	if err := h.runBackupHelperContainer(ctx, helperConfig); err != nil {
		return "", cleanup, fmt.Errorf("failed to extract named volume %s: %w", dockerVolumeName, err)
	}

	return stagingInternal, cleanup, nil
}

// getNamedVolumeName mirrors the naming convention in the session manager and workshop orchestrator.
func (h *CommandHandlerImpl) getNamedVolumeName(sgcID int64, volumeName string) string {
	if h.environment != "" {
		return fmt.Sprintf("manman-sgc-%s-%d-%s", h.environment, sgcID, volumeName)
	}
	return fmt.Sprintf("manman-sgc-%d-%s", sgcID, volumeName)
}

// runBackupHelperContainer creates, starts, waits for, and removes a short-lived container.
func (h *CommandHandlerImpl) runBackupHelperContainer(ctx context.Context, config docker.ContainerConfig) error {
	if existing, err := h.dockerClient.GetContainerStatus(ctx, config.Name); err == nil && existing != nil {
		_ = h.dockerClient.RemoveContainer(ctx, existing.ContainerID, true)
	}

	const maxPullAttempts = 3
	var pullErr error
	for attempt := 1; attempt <= maxPullAttempts; attempt++ {
		pullErr = h.dockerClient.PullImage(ctx, config.Image)
		if pullErr == nil {
			break
		}
		if attempt < maxPullAttempts {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	if pullErr != nil {
		return fmt.Errorf("failed to pull helper image %s: %w", config.Image, pullErr)
	}

	containerID, err := h.dockerClient.CreateContainer(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to create helper container: %w", err)
	}

	if err := h.dockerClient.StartContainer(ctx, containerID); err != nil {
		_ = h.dockerClient.RemoveContainer(ctx, containerID, true)
		return fmt.Errorf("failed to start helper container: %w", err)
	}

	for {
		status, err := h.dockerClient.GetContainerStatus(ctx, containerID)
		if err != nil {
			_ = h.dockerClient.RemoveContainer(ctx, containerID, true)
			return fmt.Errorf("failed to get helper container status: %w", err)
		}
		if !status.Running {
			_ = h.dockerClient.RemoveContainer(ctx, containerID, true)
			if status.ExitCode != 0 {
				return fmt.Errorf("helper container exited with code %d", status.ExitCode)
			}
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}
