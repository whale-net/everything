package workshop

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/whale-net/everything/libs/go/docker"
	"github.com/whale-net/everything/manmanv2/host/rmq"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

// DownloadOrchestrator manages workshop addon download container lifecycle within host manager
type DownloadOrchestrator struct {
	dockerClient    *docker.Client
	grpcClient      pb.ManManAPIClient
	workshopClient  pb.WorkshopServiceClient
	serverID        int64
	environment     string
	hostDataDir     string
	internalDataDir string // path where hostDataDir is mounted inside this container
	maxConcurrent   int
	semaphore       chan struct{}
	rmqPublisher    InstallationStatusPublisher

	// In-progress download tracking to prevent duplicates
	inProgressMutex     sync.RWMutex
	inProgressDownloads map[int64]bool
}

// InstallationStatusPublisher defines the interface for publishing installation status updates
type InstallationStatusPublisher interface {
	PublishInstallationStatus(ctx context.Context, update *rmq.InstallationStatusUpdate) error
}

// DownloadAddonCommand is received via RabbitMQ from control plane
type DownloadAddonCommand struct {
	InstallationID int64  `json:"installation_id"`
	SGCID          int64  `json:"sgc_id"`
	AddonID        int64  `json:"addon_id"`
	WorkshopID     string `json:"workshop_id"`
	SteamAppID     string `json:"steam_app_id"`
	InstallPath    string `json:"install_path"`
}

// Installation status constants
const (
	InstallationStatusPending     = "pending"
	InstallationStatusDownloading = "downloading"
	InstallationStatusInstalled   = "installed"
	InstallationStatusFailed      = "failed"
	InstallationStatusRemoved     = "removed"
)

// NewDownloadOrchestrator creates a new download orchestrator
func NewDownloadOrchestrator(
	dockerClient *docker.Client,
	grpcClient pb.ManManAPIClient,
	workshopClient pb.WorkshopServiceClient,
	serverID int64,
	environment string,
	hostDataDir string,
	internalDataDir string,
	maxConcurrent int,
	rmqPublisher InstallationStatusPublisher,
) *DownloadOrchestrator {
	return &DownloadOrchestrator{
		dockerClient:        dockerClient,
		grpcClient:          grpcClient,
		workshopClient:      workshopClient,
		serverID:            serverID,
		environment:         environment,
		hostDataDir:         hostDataDir,
		internalDataDir:     internalDataDir,
		maxConcurrent:       maxConcurrent,
		semaphore:           make(chan struct{}, maxConcurrent),
		rmqPublisher:        rmqPublisher,
		inProgressDownloads: make(map[int64]bool),
	}
}

const steamCMDImage = "steamcmd/steamcmd:latest"

// HandleDownloadCommand processes download commands from RabbitMQ
func (do *DownloadOrchestrator) HandleDownloadCommand(ctx context.Context, cmd *DownloadAddonCommand) error {
	logger := slog.With(
		"installation_id", cmd.InstallationID,
		"sgc_id", cmd.SGCID,
		"addon_id", cmd.AddonID,
		"workshop_id", cmd.WorkshopID,
	)

	// Check for duplicate in-progress downloads for this installation
	if do.isDownloadInProgress(cmd.InstallationID) {
		logger.Info("download already in progress, skipping duplicate")
		return nil
	}

	// Mark download as in progress
	do.markDownloadInProgress(cmd.InstallationID)
	defer do.markDownloadComplete(cmd.InstallationID)

	// Acquire semaphore for concurrency control
	do.semaphore <- struct{}{}
	defer func() { <-do.semaphore }()

	logger.Info("starting workshop addon download")

	// Update status to downloading
	do.publishStatus(ctx, cmd.InstallationID, InstallationStatusDownloading, 0, nil)

	// Build download container configuration with environment-aware naming
	containerName := do.getDownloadContainerName(cmd.SGCID, cmd.AddonID)

	// Check if container already exists (cleanup from previous failed attempt)
	existing, err := do.dockerClient.GetContainerStatus(ctx, containerName)
	if err == nil && existing != nil {
		logger.Info("cleaning up existing download container", "container_name", containerName)
		_ = do.dockerClient.RemoveContainer(ctx, existing.ContainerID, true)
	}

	// Resolve volume mounts from SGC
	volumeMounts, err := do.resolveVolumeMounts(ctx, cmd.SGCID)
	if err != nil {
		logger.Error("failed to resolve volume mounts", "error", err)
		do.handleDownloadError(ctx, cmd.InstallationID, err)
		return err
	}

	// Create temporary download directory for SteamCMD
	// SteamCMD creates steamapps/workshop/content/<appid>/<workshopid>/ structure
	// We'll extract from there to the final install path
	tempDownloadDir := filepath.Join(do.getSGCInternalDir(cmd.SGCID), ".workshop-temp", fmt.Sprintf("%d-%d", cmd.AddonID, time.Now().Unix()))
	if err := os.MkdirAll(tempDownloadDir, 0777); err != nil {
		logger.Error("failed to create temp download directory", "error", err)
		do.handleDownloadError(ctx, cmd.InstallationID, err)
		return err
	}
	defer os.RemoveAll(tempDownloadDir) // Clean up temp dir after download

	// Mount temp directory into container at /tmp/workshop-download
	// This allows SteamCMD to write there and us to read from the same location
	containerTempDir := "/tmp/workshop-download"
	tempHostDir := filepath.Join(do.getSGCHostDir(cmd.SGCID), ".workshop-temp", fmt.Sprintf("%d-%d", cmd.AddonID, time.Now().Unix()))
	volumeMounts = append(volumeMounts, fmt.Sprintf("%s:%s", tempHostDir, containerTempDir))

	// Build SteamCMD command with container temp directory
	steamCmd := do.buildSteamCMDCommand(cmd.SteamAppID, cmd.WorkshopID, containerTempDir)

	containerConfig := docker.ContainerConfig{
		Name:    containerName,
		Image:   steamCMDImage,
		Command: steamCmd,
		Volumes: volumeMounts,
		Env:     []string{},
	}

	// Create container, pulling image if needed
	containerID, err := do.dockerClient.CreateContainer(ctx, containerConfig)
	if err != nil {
		if strings.Contains(err.Error(), "No such image") {
			logger.Info("pulling steamcmd image", "image", steamCMDImage)
			if pullErr := do.dockerClient.PullImage(ctx, steamCMDImage); pullErr != nil {
				logger.Error("failed to pull steamcmd image", "error", pullErr)
				do.handleDownloadError(ctx, cmd.InstallationID, pullErr)
				return pullErr
			}
			containerID, err = do.dockerClient.CreateContainer(ctx, containerConfig)
		}
		if err != nil {
			logger.Error("failed to create download container", "error", err)
			do.handleDownloadError(ctx, cmd.InstallationID, err)
			return err
		}
	}

	// Start container
	err = do.dockerClient.StartContainer(ctx, containerID)
	if err != nil {
		logger.Error("failed to start download container", "error", err)
		do.handleDownloadError(ctx, cmd.InstallationID, err)
		return err
	}

	// Monitor container logs for progress.
	// Docker returns a multiplexed stream with 8-byte binary headers per message.
	// We use stdcopy.StdCopy to demultiplex it before scanning for text lines.
	logReader, err := do.dockerClient.GetContainerLogs(ctx, containerID, true, "all")
	if err != nil {
		logger.Error("failed to get container logs", "error", err)
		do.handleDownloadError(ctx, cmd.InstallationID, err)
		return err
	}
	defer logReader.Close()

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		_, _ = stdcopy.StdCopy(pw, pw, logReader)
	}()

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		line := scanner.Text()
		logger.Info("steamcmd", "line", line)
		if progress := do.parseProgress(line); progress > 0 {
			do.publishStatus(ctx, cmd.InstallationID, InstallationStatusDownloading, progress, nil)
		}
	}

	// Wait for container to complete by checking status
	var exitCode int
	for {
		status, err := do.dockerClient.GetContainerStatus(ctx, containerID)
		if err != nil {
			logger.Error("failed to get container status", "error", err)
			break
		}
		if !status.Running {
			exitCode = status.ExitCode
			break
		}
		time.Sleep(1 * time.Second)
	}

	// Clean up container
	_ = do.dockerClient.RemoveContainer(ctx, containerID, true)

	// Update installation status
	if exitCode != 0 {
		errMsg := fmt.Sprintf("download failed with exit code %d", exitCode)
		logger.Error("download failed", "exit_code", exitCode)
		do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	// Extract files from SteamCMD's nested structure to final install path
	steamContentDir := filepath.Join(tempDownloadDir, "steamapps", "workshop", "content", cmd.SteamAppID, cmd.WorkshopID)
	
	// Check if directory exists and has content
	entries, err := os.ReadDir(steamContentDir)
	if err != nil {
		errMsg := fmt.Sprintf("downloaded content not found at expected path: %s (error: %v)", steamContentDir, err)
		logger.Error("extraction failed", "error", errMsg, "temp_dir", tempDownloadDir)
		do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
		return fmt.Errorf("%s", errMsg)
	}
	if len(entries) == 0 {
		errMsg := fmt.Sprintf("downloaded content directory is empty: %s", steamContentDir)
		logger.Error("extraction failed", "error", errMsg, "temp_dir", tempDownloadDir)
		do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	// Convert container path to internal path for file operations
	// cmd.InstallPath is a container path (e.g., /data/maps/addon_name)
	// We need to resolve it to the internal path where we can actually write files
	internalInstallPath, err := do.resolveContainerPathToInternal(ctx, cmd.SGCID, cmd.InstallPath)
	if err != nil {
		errMsg := fmt.Sprintf("failed to resolve install path: %v", err)
		logger.Error("extraction failed", "error", err, "container_path", cmd.InstallPath)
		do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	// Ensure parent directory of install path exists
	installDir := filepath.Dir(internalInstallPath)
	if err := os.MkdirAll(installDir, 0777); err != nil {
		errMsg := fmt.Sprintf("failed to create install directory: %v", err)
		logger.Error("extraction failed", "error", err, "install_dir", installDir)
		do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	// Move all files from steam content directory to install path
	logger.Info("extracting workshop content", "from", steamContentDir, "to", internalInstallPath, "file_count", len(entries))
	
	// Handle single file - check if it needs renaming based on SteamCMD's legacy format
	// SteamCMD downloads workshop items with internal Steam filenames (e.g., "16796113200273194991_legacy.bin")
	// Different games expect different formats:
	// - Source engine games (L4D2, CS:GO, etc.): Expect .vpk files named <workshopid>.vpk
	// - Garry's Mod: Expects .gma files
	// - Other games: May expect .bsp, .pak, or other formats
	//
	// The "_legacy.bin" suffix indicates SteamCMD's generic wrapper format.
	// For Source engine games, these are actually VPK archives with the wrong extension.
	// Reference: https://steamcommunity.com/app/550/discussions/3/7340374598287174069/
	if len(entries) == 1 && !entries[0].IsDir() {
		srcFile := filepath.Join(steamContentDir, entries[0].Name())
		filename := entries[0].Name()
		
		// Check if this is a legacy.bin file that needs renaming
		// For now, we handle Source engine games (L4D2, etc.) by renaming to .vpk
		// TODO: Add game-specific detection based on steam_app_id for other formats:
		//   - Garry's Mod (4000): .gma
		//   - Other Source games: .vpk
		//   - Non-Source games: Keep original or extract
		var dstFile string
		if strings.HasSuffix(filename, "_legacy.bin") {
			// Rename to <workshopid>.vpk for Source engine games
			// The workshop ID is the canonical identifier that games use to load content
			dstFile = filepath.Join(internalInstallPath, cmd.WorkshopID+".vpk")
			logger.Info("renaming legacy.bin to vpk", "from", filename, "to", cmd.WorkshopID+".vpk")
		} else {
			// Keep original filename for files that don't use the legacy format
			dstFile = filepath.Join(internalInstallPath, filename)
			logger.Info("keeping original filename", "file", filename)
		}
		
		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(dstFile), 0777); err != nil {
			errMsg := fmt.Sprintf("failed to create install directory: %v", err)
			logger.Error("extraction failed", "error", err)
			do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
			return fmt.Errorf("%s", errMsg)
		}
		
		// Move the file
		if err := os.Rename(srcFile, dstFile); err != nil {
			// If rename fails (cross-device), copy then delete
			if err := copyFile(srcFile, dstFile); err != nil {
				errMsg := fmt.Sprintf("failed to copy workshop file: %v", err)
				logger.Error("extraction failed", "error", err)
				do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
				return fmt.Errorf("%s", errMsg)
			}
			os.Remove(srcFile)
		}
	} else {
		// Multiple files or directory - move entire directory structure as-is
		// Workshop items with multiple files (models, textures, sounds, etc.) should
		// preserve their directory structure exactly as the creator uploaded them
		if err := do.moveDirectory(steamContentDir, internalInstallPath); err != nil {
			errMsg := fmt.Sprintf("failed to extract workshop content: %v", err)
			logger.Error("extraction failed", "error", err)
			do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
			return fmt.Errorf("%s", errMsg)
		}
	}

	logger.Info("download completed successfully")
	do.publishStatus(ctx, cmd.InstallationID, InstallationStatusInstalled, 100, nil)
	return nil
}

// getDownloadContainerName generates environment-aware container name
func (do *DownloadOrchestrator) getDownloadContainerName(sgcID, addonID int64) string {
	if do.environment != "" {
		return fmt.Sprintf("workshop-download-%s-%d-%d", do.environment, sgcID, addonID)
	}
	return fmt.Sprintf("workshop-download-%d-%d", sgcID, addonID)
}

// isDownloadInProgress checks if a download is already in progress
func (do *DownloadOrchestrator) isDownloadInProgress(installationID int64) bool {
	do.inProgressMutex.RLock()
	defer do.inProgressMutex.RUnlock()
	return do.inProgressDownloads[installationID]
}

// markDownloadInProgress marks a download as in progress
func (do *DownloadOrchestrator) markDownloadInProgress(installationID int64) {
	do.inProgressMutex.Lock()
	defer do.inProgressMutex.Unlock()
	do.inProgressDownloads[installationID] = true
}

// markDownloadComplete marks a download as complete
func (do *DownloadOrchestrator) markDownloadComplete(installationID int64) {
	do.inProgressMutex.Lock()
	defer do.inProgressMutex.Unlock()
	delete(do.inProgressDownloads, installationID)
}

// publishStatus sends status updates back to control plane via RabbitMQ
func (do *DownloadOrchestrator) publishStatus(ctx context.Context, installationID int64, status string, progress int, errorMsg *string) {
	update := &rmq.InstallationStatusUpdate{
		InstallationID:  installationID,
		Status:          status,
		ProgressPercent: progress,
		ErrorMessage:    errorMsg,
	}
	if err := do.rmqPublisher.PublishInstallationStatus(ctx, update); err != nil {
		slog.Error("failed to publish installation status", "installation_id", installationID, "error", err)
	}
}

// resolveVolumeMounts gets volume mounts from SGC configuration, matching the game container's
// mount scheme, and ensures the target directories exist with world-writable permissions.
//
// Directories are created at the internal path (inside this container) so they are visible
// to Docker on the host. We use 0777 so any container user (including steam, UID 1000) can write.
func (do *DownloadOrchestrator) resolveVolumeMounts(ctx context.Context, sgcID int64) ([]string, error) {
	sgcHostDir := do.getSGCHostDir(sgcID)
	sgcInternalDir := do.getSGCInternalDir(sgcID)

	// Look up the SGC to get its game_config_id
	sgcResp, err := do.grpcClient.GetServerGameConfig(ctx, &pb.GetServerGameConfigRequest{
		ServerGameConfigId: sgcID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get SGC %d: %w", sgcID, err)
	}

	// List volumes for this game config
	volumesResp, err := do.grpcClient.ListGameConfigVolumes(ctx, &pb.ListGameConfigVolumesRequest{
		ConfigId: sgcResp.Config.GameConfigId,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes for game config %d: %w", sgcResp.Config.GameConfigId, err)
	}

	if len(volumesResp.Volumes) == 0 {
		return nil, fmt.Errorf("no volumes configured for game config %d (SGC %d)", sgcResp.Config.GameConfigId, sgcID)
	}

	// Build mount strings matching the game container's volume mount scheme:
	//   sgcHostDir/<host_subpath>:<container_path>
	// Also create the directories now with 0777 so steamcmd (steam user, UID 1000) can write.
	mounts := make([]string, 0, len(volumesResp.Volumes))
	for _, vol := range volumesResp.Volumes {
		subPath := strings.TrimPrefix(vol.HostSubpath, "/")
		if subPath == "" {
			subPath = vol.Name
		}

		// Create the directory at the internal path (accessible inside this container)
		internalPath := filepath.Join(sgcInternalDir, subPath)
		if err := os.MkdirAll(internalPath, 0777); err != nil {
			return nil, fmt.Errorf("failed to create volume directory %s: %w", internalPath, err)
		}

		hostPath := filepath.Join(sgcHostDir, subPath)
		mounts = append(mounts, fmt.Sprintf("%s:%s", hostPath, vol.ContainerPath))
	}

	return mounts, nil
}

// buildSteamCMDCommand constructs the SteamCMD command for downloading.
// The steamcmd/steamcmd image has ENTRYPOINT ["steamcmd"], so these args are passed
// directly to steamcmd — no bash wrapper needed or wanted.
func (do *DownloadOrchestrator) buildSteamCMDCommand(steamAppID, workshopID, installPath string) []string {
	return []string{
		"+force_install_dir", installPath,
		"+login", "anonymous",
		"+workshop_download_item", steamAppID, workshopID,
		"+quit",
	}
}

// parseProgress extracts download progress from SteamCMD output
func (do *DownloadOrchestrator) parseProgress(logLine string) int {
	// SteamCMD outputs progress like: "Downloading item 123456 ... 45%"
	re := regexp.MustCompile(`(\d+)%`)
	matches := re.FindStringSubmatch(logLine)
	if len(matches) > 1 {
		percent, _ := strconv.Atoi(matches[1])
		return percent
	}
	return 0
}

// getSGCHostDir returns the host path for SGC data (used as Docker bind mount source)
func (do *DownloadOrchestrator) getSGCHostDir(sgcID int64) string {
	dirName := fmt.Sprintf("sgc-%d", sgcID)
	if do.environment != "" {
		dirName = fmt.Sprintf("sgc-%s-%d", do.environment, sgcID)
	}
	return filepath.Join(do.hostDataDir, dirName)
}

// getSGCInternalDir returns the path inside this container where SGC data is accessible.
// hostDataDir is mounted at internalDataDir, so volume directories can be created here.
func (do *DownloadOrchestrator) getSGCInternalDir(sgcID int64) string {
	dirName := fmt.Sprintf("sgc-%d", sgcID)
	if do.environment != "" {
		dirName = fmt.Sprintf("sgc-%s-%d", do.environment, sgcID)
	}
	return filepath.Join(do.internalDataDir, dirName)
}

// resolveContainerPathToInternal converts a container path to the internal path
// where the host-manager can access it. This looks up the volume configuration
// to find which volume contains the path and maps it to the internal directory.
func (do *DownloadOrchestrator) resolveContainerPathToInternal(ctx context.Context, sgcID int64, containerPath string) (string, error) {
	sgcInternalDir := do.getSGCInternalDir(sgcID)

	// Get SGC to look up game config
	sgcResp, err := do.grpcClient.GetServerGameConfig(ctx, &pb.GetServerGameConfigRequest{
		ServerGameConfigId: sgcID,
	})
	if err != nil {
		return "", fmt.Errorf("failed to get SGC %d: %w", sgcID, err)
	}

	// List volumes for this game config
	volumesResp, err := do.grpcClient.ListGameConfigVolumes(ctx, &pb.ListGameConfigVolumesRequest{
		ConfigId: sgcResp.Config.GameConfigId,
	})
	if err != nil {
		return "", fmt.Errorf("failed to list volumes: %w", err)
	}

	// Find which volume contains this container path
	for _, vol := range volumesResp.Volumes {
		// Check if containerPath starts with this volume's container path
		if strings.HasPrefix(containerPath, vol.ContainerPath) {
			// Get the relative path within the volume
			relPath := strings.TrimPrefix(containerPath, vol.ContainerPath)
			relPath = strings.TrimPrefix(relPath, "/")

			// Build internal path: sgcInternalDir/<host_subpath>/<relPath>
			subPath := strings.TrimPrefix(vol.HostSubpath, "/")
			if subPath == "" {
				subPath = vol.Name
			}
			return filepath.Join(sgcInternalDir, subPath, relPath), nil
		}
	}

	return "", fmt.Errorf("no volume found for container path %s", containerPath)
}

// handleDownloadError handles download errors
func (do *DownloadOrchestrator) handleDownloadError(ctx context.Context, installationID int64, err error) {
	errMsg := err.Error()
	do.publishStatus(ctx, installationID, InstallationStatusFailed, 0, &errMsg)
}

// moveDirectory moves all contents from src to dst, preserving directory structure
func (do *DownloadOrchestrator) moveDirectory(src, dst string) error {
	// Check if destination already exists and remove it
	if _, err := os.Stat(dst); err == nil {
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("failed to remove existing destination: %w", err)
		}
	}

	// Try simple rename first (works if on same filesystem)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// If rename fails, copy recursively then remove source
	if err := do.copyDirectory(src, dst); err != nil {
		return fmt.Errorf("failed to copy directory: %w", err)
	}

	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("failed to remove source after copy: %w", err)
	}

	return nil
}

// copyDirectory recursively copies a directory
func (do *DownloadOrchestrator) copyDirectory(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Calculate destination path
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}

		// Copy file
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.Create(dstPath)
		if err != nil {
			return err
		}
		defer dstFile.Close()

		if _, err := io.Copy(dstFile, srcFile); err != nil {
			return err
		}

		return os.Chmod(dstPath, info.Mode())
	})
}

// copyFile copies a single file from src to dst
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	// Copy permissions
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, srcInfo.Mode())
}

// EnsureLibraryAddonsInstalled downloads all library addons for an SGC before session start (blocking)
// Returns when all downloads complete or when context is cancelled
func (do *DownloadOrchestrator) EnsureLibraryAddonsInstalled(ctx context.Context, sgcID int64, heartbeatFn func()) error {
	logger := slog.With("sgc_id", sgcID)
	logger.Info("ensuring library addons are installed")

	// Call the API to get all addons that need to be installed for this SGC
	resp, err := do.workshopClient.ListSGCLibraries(ctx, &pb.ListSGCLibrariesRequest{
		SgcId: sgcID,
	})
	if err != nil {
		logger.Error("failed to list SGC libraries", "error", err)
		return fmt.Errorf("failed to list SGC libraries: %w", err)
	}

	if len(resp.Libraries) == 0 {
		logger.Info("no libraries attached to SGC, skipping addon downloads")
		return nil
	}

	logger.Info("found libraries attached to SGC", "count", len(resp.Libraries))

	// Collect all unique addon IDs from all libraries (including nested references)
	addonIDs := make(map[int64]*pb.WorkshopAddon)
	visited := make(map[int64]bool)
	queue := make([]int64, 0, len(resp.Libraries))
	for _, lib := range resp.Libraries {
		queue = append(queue, lib.LibraryId)
	}

	// BFS to collect all addons from all libraries
	for len(queue) > 0 {
		libID := queue[0]
		queue = queue[1:]

		if visited[libID] {
			continue
		}
		visited[libID] = true

		// Get addons for this library
		addonsResp, err := do.workshopClient.GetLibraryAddons(ctx, &pb.GetLibraryAddonsRequest{
			LibraryId: libID,
		})
		if err != nil {
			logger.Warn("failed to get addons for library", "library_id", libID, "error", err)
			continue
		}

		for _, addon := range addonsResp.Addons {
			addonIDs[addon.AddonId] = addon
		}

		// Get child libraries
		childrenResp, err := do.workshopClient.GetChildLibraries(ctx, &pb.GetChildLibrariesRequest{
			LibraryId: libID,
		})
		if err != nil {
			logger.Warn("failed to get child libraries", "library_id", libID, "error", err)
			continue
		}

		for _, child := range childrenResp.Libraries {
			queue = append(queue, child.LibraryId)
		}
	}

	if len(addonIDs) == 0 {
		logger.Info("no addons found in libraries")
		return nil
	}

	logger.Info("found addons to install", "count", len(addonIDs))

	// Start heartbeat ticker
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Start heartbeat goroutine
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()

	go func() {
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if heartbeatFn != nil {
					heartbeatFn()
				}
			}
		}
	}()

	// Download each addon (sequentially for now, can parallelize later)
	for addonID, addon := range addonIDs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		logger.Info("checking addon installation", "addon_id", addonID, "workshop_id", addon.WorkshopId)

		// Check if already installed
		installations, err := do.workshopClient.ListInstallations(ctx, &pb.ListInstallationsRequest{
			SgcId:   sgcID,
			AddonId: addonID,
		})
		if err != nil {
			logger.Warn("failed to check installation status", "addon_id", addonID, "error", err)
			// Continue anyway, we'll try to install
		} else if len(installations.Installations) > 0 {
			inst := installations.Installations[0]
			if inst.Status == InstallationStatusInstalled {
				logger.Info("addon already installed, skipping", "addon_id", addonID)
				continue
			}
		}

		// Trigger installation via API (creates/updates the record; we handle the download below)
		installResp, err := do.workshopClient.InstallAddon(ctx, &pb.InstallAddonRequest{
			SgcId:        sgcID,
			AddonId:      addonID,
			SkipDispatch: true,
		})
		if err != nil {
			logger.Error("failed to trigger installation", "addon_id", addonID, "error", err)
			return fmt.Errorf("failed to trigger installation for addon %d: %w", addonID, err)
		}

		// Now handle the download synchronously
		cmd := &DownloadAddonCommand{
			InstallationID: installResp.Installation.InstallationId,
			SGCID:          sgcID,
			AddonID:        addonID,
			WorkshopID:     addon.WorkshopId,
			SteamAppID:     addon.SteamAppId,
			InstallPath:    installResp.Installation.InstallationPath,
		}

		// Download addon (blocking)
		if err := do.HandleDownloadCommand(ctx, cmd); err != nil {
			return fmt.Errorf("failed to download addon %d: %w", addonID, err)
		}

		logger.Info("addon installation completed", "addon_id", addonID)
	}

	logger.Info("all library addons installed successfully")
	return nil
}
