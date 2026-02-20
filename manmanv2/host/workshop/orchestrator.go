package workshop

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/whale-net/everything/libs/go/docker"
	"github.com/whale-net/everything/manmanv2/host/rmq"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

// DownloadOrchestrator manages workshop addon download container lifecycle within host manager
type DownloadOrchestrator struct {
	dockerClient *docker.Client
	grpcClient   pb.ManManAPIClient
	serverID     int64
	environment  string
	hostDataDir  string
	maxConcurrent int
	semaphore    chan struct{}
	rmqPublisher InstallationStatusPublisher
	
	// In-progress download tracking to prevent duplicates
	inProgressMutex sync.RWMutex
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
	serverID int64,
	environment string,
	hostDataDir string,
	maxConcurrent int,
	rmqPublisher InstallationStatusPublisher,
) *DownloadOrchestrator {
	return &DownloadOrchestrator{
		dockerClient:        dockerClient,
		grpcClient:          grpcClient,
		serverID:            serverID,
		environment:         environment,
		hostDataDir:         hostDataDir,
		maxConcurrent:       maxConcurrent,
		semaphore:           make(chan struct{}, maxConcurrent),
		rmqPublisher:        rmqPublisher,
		inProgressDownloads: make(map[int64]bool),
	}
}

// HandleDownloadCommand processes download commands from RabbitMQ
func (do *DownloadOrchestrator) HandleDownloadCommand(ctx context.Context, cmd *DownloadAddonCommand) {
	logger := slog.With(
		"installation_id", cmd.InstallationID,
		"sgc_id", cmd.SGCID,
		"addon_id", cmd.AddonID,
		"workshop_id", cmd.WorkshopID,
	)

	// Check for duplicate in-progress downloads for this installation
	if do.isDownloadInProgress(cmd.InstallationID) {
		logger.Info("download already in progress, skipping duplicate")
		return
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
		return
	}

	// Build SteamCMD command
	steamCmd := do.buildSteamCMDCommand(cmd.SteamAppID, cmd.WorkshopID)

	// Create container
	containerID, err := do.dockerClient.CreateContainer(ctx, docker.ContainerConfig{
		Name:    containerName,
		Image:   "steamcmd/steamcmd:latest",
		Command: steamCmd,
		Volumes: volumeMounts,
		Env:     []string{},
	})
	if err != nil {
		logger.Error("failed to create download container", "error", err)
		do.handleDownloadError(ctx, cmd.InstallationID, err)
		return
	}

	// Start container
	err = do.dockerClient.StartContainer(ctx, containerID)
	if err != nil {
		logger.Error("failed to start download container", "error", err)
		do.handleDownloadError(ctx, cmd.InstallationID, err)
		return
	}

	// Monitor container logs for progress
	logReader, err := do.dockerClient.GetContainerLogs(ctx, containerID, true, "all")
	if err != nil {
		logger.Error("failed to get container logs", "error", err)
		do.handleDownloadError(ctx, cmd.InstallationID, err)
		return
	}
	defer logReader.Close()

	// Parse logs and update progress
	scanner := bufio.NewScanner(logReader)
	for scanner.Scan() {
		line := scanner.Text()
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
	} else {
		logger.Info("download completed successfully")
		do.publishStatus(ctx, cmd.InstallationID, InstallationStatusInstalled, 100, nil)
	}
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

// resolveVolumeMounts gets volume mounts from SGC configuration
func (do *DownloadOrchestrator) resolveVolumeMounts(ctx context.Context, sgcID int64) ([]string, error) {
	// Get SGC data directory on host
	sgcDataDir := do.getSGCHostDir(sgcID)

	// Mount the SGC data directory to the download container
	// This allows the download container to write to the same volumes as the game container
	return []string{
		fmt.Sprintf("%s:/data", sgcDataDir),
	}, nil
}

// buildSteamCMDCommand constructs the SteamCMD command for downloading
func (do *DownloadOrchestrator) buildSteamCMDCommand(steamAppID, workshopID string) []string {
	return []string{
		"/bin/bash",
		"-c",
		fmt.Sprintf(
			"steamcmd +login anonymous +workshop_download_item %s %s +quit",
			steamAppID,
			workshopID,
		),
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

// getSGCHostDir returns the host path for SGC data
func (do *DownloadOrchestrator) getSGCHostDir(sgcID int64) string {
	dirName := fmt.Sprintf("sgc-%d", sgcID)
	if do.environment != "" {
		dirName = fmt.Sprintf("sgc-%s-%d", do.environment, sgcID)
	}
	return filepath.Join(do.hostDataDir, dirName)
}

// handleDownloadError handles download errors
func (do *DownloadOrchestrator) handleDownloadError(ctx context.Context, installationID int64, err error) {
	errMsg := err.Error()
	do.publishStatus(ctx, installationID, InstallationStatusFailed, 0, &errMsg)
}

// EnsureLibraryAddonsInstalled downloads all library addons for an SGC before session start (blocking)
// Returns when all downloads complete or when context is cancelled
func (do *DownloadOrchestrator) EnsureLibraryAddonsInstalled(ctx context.Context, sgcID int64, heartbeatFn func()) error {
	logger := slog.With("sgc_id", sgcID)
	logger.Info("ensuring library addons are installed")

	// Call the API to get all addons that need to be installed for this SGC
	resp, err := do.grpcClient.ListSGCLibraries(ctx, &pb.ListSGCLibrariesRequest{
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
		addonsResp, err := do.grpcClient.GetLibraryAddons(ctx, &pb.GetLibraryAddonsRequest{
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
		childrenResp, err := do.grpcClient.GetChildLibraries(ctx, &pb.GetChildLibrariesRequest{
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
		installations, err := do.grpcClient.ListInstallations(ctx, &pb.ListInstallationsRequest{
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

		// Trigger installation via API (this creates the installation record)
		installResp, err := do.grpcClient.InstallAddon(ctx, &pb.InstallAddonRequest{
			SgcId:   sgcID,
			AddonId: addonID,
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
			SteamAppID:     addon.Metadata["steam_app_id"].(string), // TODO: proper type handling
			InstallPath:    installResp.Installation.InstallationPath,
		}

		// Download addon (blocking)
		do.HandleDownloadCommand(ctx, cmd)

		logger.Info("addon installation completed", "addon_id", addonID)
	}

	logger.Info("all library addons installed successfully")
	return nil
}
