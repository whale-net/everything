package workshop

import (
	"archive/tar"
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DockerContainerClient is the subset of *docker.Client this package drives directly
// (download, verify, and helper-container lifecycles). Declaring it as an interface --
// rather than depending on *docker.Client concretely -- lets tests substitute a fake
// SteamCMD/docker double instead of requiring a live Docker daemon. *docker.Client already
// implements this exactly, so callers construct DownloadOrchestrator exactly as before.
type DockerContainerClient interface {
	CreateContainer(ctx context.Context, config docker.ContainerConfig) (string, error)
	StartContainer(ctx context.Context, containerID string) error
	PullImage(ctx context.Context, imageRef string) error
	RemoveContainer(ctx context.Context, containerID string, force bool) error
	GetContainerStatus(ctx context.Context, containerID string) (*docker.ContainerStatus, error)
	GetContainerLogs(ctx context.Context, containerID string, follow bool, tail string) (io.ReadCloser, error)
}

// DownloadOrchestrator manages workshop addon download container lifecycle within host manager
type DownloadOrchestrator struct {
	dockerClient    DockerContainerClient
	grpcClient      pb.ManManAPIClient
	workshopClient  pb.WorkshopServiceClient
	serverID        int64
	environment     string
	hostDataDir     string
	internalDataDir string // path where hostDataDir is mounted inside this container
	maxConcurrent   int
	semaphore       chan struct{}
	rmqPublisher    InstallationStatusPublisher
	cacheClient     *CacheClient // S3 cache read fast path (#2183); nil-safe if workshopClient is nil

	// In-progress download tracking to prevent duplicates
	inProgressMutex     sync.RWMutex
	inProgressDownloads map[int64]bool
}

// InstallationStatusPublisher defines the interface for publishing installation status updates
type InstallationStatusPublisher interface {
	PublishInstallationStatus(ctx context.Context, update *rmq.InstallationStatusUpdate) error
}

// WorkshopCachePublisher is the narrow publish surface the verify/cache-refresh flow
// (#2184) needs. Kept separate from InstallationStatusPublisher -- rather than adding a
// method to it -- so a caller (or test fake) that only wires up installation-status
// publishing does not have to implement a method it never uses:
// publishWorkshopCacheStatus type-asserts rmqPublisher against this interface and
// degrades to a no-op when it isn't satisfied. *rmq.Publisher (host/main.go's real
// wiring) implements both.
type WorkshopCachePublisher interface {
	PublishWorkshopCacheStatus(ctx context.Context, update *rmq.WorkshopCacheStatusUpdate) error
}

// Workshop cache status event names, mirrored from rmq.WorkshopCacheStatusUpdate's doc
// comment (#2184, plan #2175 FR8/FR9/FR10).
const (
	workshopCacheEventVerifiedUnchanged = "verified_unchanged"
	workshopCacheEventRefreshed         = "refreshed"
	workshopCacheEventPopulated         = "populated"
)

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
	dockerClient DockerContainerClient,
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
		cacheClient:         NewCacheClient(workshopClient, serverID, http.DefaultClient),
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

	// Defense in depth: a Steam Workshop collection is a grouping marker with no
	// downloadable content of its own (its children carry the actual items — see
	// WorkshopManager.InstallAddon, which fans out to children instead of dispatching
	// a download for the collection's own addon_id). Refuse rather than hand SteamCMD
	// a collection ID it cannot download.
	addonResp, addonErr := do.workshopClient.GetAddon(ctx, &pb.GetAddonRequest{AddonId: cmd.AddonID})
	if addonErr == nil && addonResp.Addon != nil && addonResp.Addon.IsCollection {
		err := fmt.Errorf("addon %d is a Steam Workshop collection, not downloadable content", cmd.AddonID)
		logger.Error("refusing to download collection addon", "error", err)
		do.handleDownloadError(ctx, cmd.InstallationID, err)
		return err
	}

	// Atomically check and mark to prevent duplicate concurrent downloads
	if do.checkAndMarkDownloadInProgress(cmd.InstallationID) {
		logger.Info("download already in progress, skipping duplicate")
		return nil
	}
	defer do.markDownloadComplete(cmd.InstallationID)

	// Acquire semaphore for concurrency control
	do.semaphore <- struct{}{}
	defer func() { <-do.semaphore }()

	logger.Info("starting workshop addon download")

	// Update status to downloading
	do.publishStatus(ctx, cmd.InstallationID, InstallationStatusDownloading, 0, nil)

	// FR8/FR9 verify-then-cache-or-refresh sequence (#2184), replacing #2183's plain
	// "cache-first, else download" ordering:
	//
	//  1. Ask VerifyWorkshopItem for the addon's *live* current content version.
	//  2. Unchanged (FR8): ask control-api for a cache hit at that exact version and, if
	//     present, serve it with no SteamCMD download at all -- the FR8 cost-saving claim.
	//     A miss here (first-ever install, or a version never cached before) is the
	//     ordinary fallthrough to the ordinary download+upload path below, not an error.
	//  3/5. Changed, or a miss despite being unchanged: the code below falls through into
	//     the pre-existing full SteamCMD download, and uploadToCache (called once that
	//     download has landed content on disk) uploads it as a **new** cache entry --
	//     never touching whatever entry existed under the addon's previous version (FR9).
	//
	// A verify failure (RPC/SteamCMD error) must never fail the install: resolveVerifyPlan
	// reports changed=true in that case, which simply routes straight to the ordinary
	// download path below with no cache read attempted.
	haveAddon := addonErr == nil && addonResp.Addon != nil
	var contentVersion string
	var changed bool
	if haveAddon {
		contentVersion, changed = do.resolveVerifyPlan(ctx, cmd, addonResp.Addon, logger)

		if !changed {
			if cacheEntryID, hit := do.tryServeFromCache(ctx, cmd, contentVersion, logger); hit {
				logger.Info("served workshop addon from S3 cache, skipping SteamCMD download", "content_version", contentVersion)
				do.publishWorkshopCacheStatus(ctx, cmd.WorkshopID, contentVersion, cacheEntryID, workshopCacheEventVerifiedUnchanged, 0)
				do.publishStatus(ctx, cmd.InstallationID, InstallationStatusInstalled, 100, nil)
				return nil
			}
		}
	}

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

	// Create temporary download directory for SteamCMD.
	// SteamCMD creates steamapps/workshop/content/<appid>/<workshopid>/ structure;
	// we extract from there to a staging subdir before installing to the target volume.
	tempSuffix := fmt.Sprintf("%d-%d", cmd.AddonID, time.Now().Unix())
	tempDownloadDir := filepath.Join(do.getSGCInternalDir(cmd.SGCID), ".workshop-temp", tempSuffix)
	if err := os.MkdirAll(tempDownloadDir, 0777); err != nil {
		logger.Error("failed to create temp download directory", "error", err)
		do.handleDownloadError(ctx, cmd.InstallationID, err)
		return err
	}
	defer os.RemoveAll(tempDownloadDir) // Clean up temp dir after download

	// Mount temp directory into container at /tmp/workshop-download.
	// tempHostDir and tempDownloadDir point to the same location from Docker's and
	// the host-manager's perspectives respectively.
	containerTempDir := "/tmp/workshop-download"
	tempHostDir := filepath.Join(do.getSGCHostDir(cmd.SGCID), ".workshop-temp", tempSuffix)
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

	// Always pull steamcmd image to ensure latest version is used
	// TODO: add cache fallback
	logger.Info("pulling steamcmd image", "image", steamCMDImage)
	const maxPullAttempts = 3
	var pullErr error
	for attempt := 1; attempt <= maxPullAttempts; attempt++ {
		pullErr = do.dockerClient.PullImage(ctx, steamCMDImage)
		if pullErr == nil {
			break
		}
		logger.Warn("failed to pull steamcmd image, retrying", "image", steamCMDImage, "attempt", attempt, "error", pullErr)
		if attempt < maxPullAttempts {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	if pullErr != nil {
		logger.Error("failed to pull steamcmd image after retries", "error", pullErr)
		do.handleDownloadError(ctx, cmd.InstallationID, pullErr)
		return pullErr
	}

	// Create container (image already pulled above)
	containerID, err := do.dockerClient.CreateContainer(ctx, containerConfig)
	if err != nil {
		logger.Error("failed to create download container", "error", err)
		do.handleDownloadError(ctx, cmd.InstallationID, err)
		return err
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

	// Resolve where this install path lives (bind-mount or named volume)
	target, err := do.resolveInstallTarget(ctx, cmd.SGCID, cmd.InstallPath)
	if err != nil {
		errMsg := fmt.Sprintf("failed to resolve install path: %v", err)
		logger.Error("extraction failed", "error", err, "container_path", cmd.InstallPath)
		do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	// Stage extracted files into a subdirectory of the temp dir so they are
	// accessible via the existing bind mount regardless of target volume type.
	stagingDir := filepath.Join(tempDownloadDir, "staging")
	if err := os.MkdirAll(stagingDir, 0777); err != nil {
		errMsg := fmt.Sprintf("failed to create staging directory: %v", err)
		logger.Error("extraction failed", "error", err)
		do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	// Move all files from SteamCMD's nested structure into the staging dir,
	// renaming legacy.bin → <workshopid>.vpk for Source engine games along the way.
	logger.Info("extracting workshop content", "from", steamContentDir, "to", stagingDir, "file_count", len(entries))

	if len(entries) == 1 && !entries[0].IsDir() {
		srcFile := filepath.Join(steamContentDir, entries[0].Name())
		filename := entries[0].Name()

		var dstFile string
		if strings.HasSuffix(filename, "_legacy.bin") {
			dstFile = filepath.Join(stagingDir, cmd.WorkshopID+".vpk")
			logger.Info("renaming legacy.bin to vpk", "from", filename, "to", cmd.WorkshopID+".vpk")
		} else {
			dstFile = filepath.Join(stagingDir, filename)
			logger.Info("keeping original filename", "file", filename)
		}

		if err := os.Rename(srcFile, dstFile); err != nil {
			if err := copyFile(srcFile, dstFile); err != nil {
				errMsg := fmt.Sprintf("failed to copy workshop file: %v", err)
				logger.Error("extraction failed", "error", err)
				do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
				return fmt.Errorf("%s", errMsg)
			}
			os.Remove(srcFile)
		}
	} else {
		if err := do.moveDirectory(steamContentDir, stagingDir); err != nil {
			errMsg := fmt.Sprintf("failed to extract workshop content: %v", err)
			logger.Error("extraction failed", "error", err)
			do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
			return fmt.Errorf("%s", errMsg)
		}
	}

	// Install staged files into the target volume.
	if target.isNamed {
		// Named volume: use a busybox helper container so Docker manages the copy natively.
		// The staging dir is bind-mounted and the named volume is mounted at its container path.
		stagingHostDir := filepath.Join(tempHostDir, "staging")
		destPath := target.ContainerPath
		if target.RelPath != "" {
			destPath = filepath.Join(destPath, target.RelPath)
		}
		copyCmd := fmt.Sprintf("mkdir -p %s && cp -r /tmp/workshop-staging/. %s/", destPath, destPath)
		helperConfig := docker.ContainerConfig{
			Name:    fmt.Sprintf("workshop-install-%s-%d-%d", do.environment, cmd.SGCID, cmd.AddonID),
			Image:   "busybox:latest",
			Command: []string{"sh", "-c", copyCmd},
			Volumes: []string{
				fmt.Sprintf("%s:/tmp/workshop-staging", stagingHostDir),
				fmt.Sprintf("%s:%s", target.VolumeName, target.ContainerPath),
			},
		}
		logger.Info("copying to named volume via helper container", "volume", target.VolumeName, "dest", destPath)
		if err := do.runHelperContainer(ctx, helperConfig); err != nil {
			errMsg := fmt.Sprintf("failed to copy files into named volume: %v", err)
			logger.Error("extraction failed", "error", err)
			do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
			return fmt.Errorf("%s", errMsg)
		}
	} else {
		// Bind-mount volume: merge files into the resolved host path.
		// Use copyDirectory rather than moveDirectory so that previously installed
		// addons sharing the same install path are not wiped out.
		installPath := target.BindPath
		if err := os.MkdirAll(installPath, 0777); err != nil {
			errMsg := fmt.Sprintf("failed to create install directory: %v", err)
			logger.Error("extraction failed", "error", err)
			do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
			return fmt.Errorf("%s", errMsg)
		}
		if err := do.copyDirectory(stagingDir, installPath); err != nil {
			errMsg := fmt.Sprintf("failed to copy files to install path: %v", err)
			logger.Error("extraction failed", "error", err)
			do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, &errMsg)
			return fmt.Errorf("%s", errMsg)
		}
	}

	// FR9 write side: package the content that just landed at stagingDir and upload it as
	// a **new** cache entry for (workshopID, contentVersion). This is best-effort with
	// respect to the install that already succeeded above -- see uploadToCache's doc
	// comment for why a failure here is only ever logged, never surfaced as an install
	// error or followed by a status publish (NFR4: no presence row, no completeness claim
	// on a failed upload).
	if haveAddon {
		do.uploadToCache(ctx, cmd.WorkshopID, stagingDir, contentVersion, changed, logger)
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

// checkAndMarkDownloadInProgress atomically checks if a download is in progress and marks it if not.
// Returns true if the download was already in progress (caller should skip).
func (do *DownloadOrchestrator) checkAndMarkDownloadInProgress(installationID int64) bool {
	do.inProgressMutex.Lock()
	defer do.inProgressMutex.Unlock()
	if do.inProgressDownloads[installationID] {
		return true
	}
	do.inProgressDownloads[installationID] = true
	return false
}

// markDownloadComplete marks a download as complete
func (do *DownloadOrchestrator) markDownloadComplete(installationID int64) {
	do.inProgressMutex.Lock()
	defer do.inProgressMutex.Unlock()
	delete(do.inProgressDownloads, installationID)
}

// HandleRemoveCommand removes workshop addon files from disk and publishes a status update.
func (do *DownloadOrchestrator) HandleRemoveCommand(ctx context.Context, cmd *rmq.RemoveAddonCommand) error {
	logger := slog.With(
		"installation_id", cmd.InstallationID,
		"sgc_id", cmd.SGCID,
		"addon_id", cmd.AddonID,
		"installation_path", cmd.InstallationPath,
	)
	logger.Info("removing workshop addon files")

	errMsg := func(s string) *string { return &s }

	if cmd.InstallationPath == "" {
		msg := "installation_path is empty, nothing to remove"
		logger.Warn(msg)
		do.publishStatus(ctx, cmd.InstallationID, InstallationStatusRemoved, 0, nil)
		return nil
	}

	if err := os.RemoveAll(cmd.InstallationPath); err != nil {
		e := fmt.Sprintf("failed to remove addon files: %v", err)
		logger.Error(e)
		do.publishStatus(ctx, cmd.InstallationID, InstallationStatusFailed, 0, errMsg(e))
		return fmt.Errorf("%s", e)
	}

	logger.Info("addon files removed successfully")
	do.publishStatus(ctx, cmd.InstallationID, InstallationStatusRemoved, 0, nil)
	return nil
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

// resolveVolumeMounts gets bind-mount volume mounts from SGC configuration for the SteamCMD
// download container, and ensures the target directories exist with world-writable permissions.
//
// Named volumes are excluded — the SteamCMD container downloads to a temp dir, and files
// are copied into named volumes afterwards via a separate helper container.
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

	// Build mount strings for bind-mount volumes only.
	// Named volumes are handled separately after download via a helper container.
	mounts := make([]string, 0, len(volumesResp.Volumes))
	for _, vol := range volumesResp.Volumes {
		if vol.VolumeType == "named" {
			continue
		}

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

// getNamedVolumeName returns the Docker named volume name for a given SGC and volume,
// using the same naming convention as the session manager.
func (do *DownloadOrchestrator) getNamedVolumeName(sgcID int64, volumeName string) string {
	if do.environment != "" {
		return fmt.Sprintf("manman-sgc-%s-%d-%s", do.environment, sgcID, volumeName)
	}
	return fmt.Sprintf("manman-sgc-%d-%s", sgcID, volumeName)
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

// installTarget describes where extracted workshop files should be placed.
// For bind-mount volumes the host-manager can write directly to BindPath.
// For named volumes files must be copied in via a Docker helper container.
type installTarget struct {
	// isNamed is true when the target volume is a Docker named volume.
	isNamed bool

	// BindPath is the host-accessible path to write to (bind-mount volumes only).
	BindPath string

	// VolumeName is the Docker named volume name (named volumes only).
	VolumeName string
	// ContainerPath is where the named volume is mounted inside game containers (named volumes only).
	ContainerPath string

	// RelPath is any sub-path within the volume root.
	RelPath string
}

// resolveInstallTarget looks up the volume that contains containerPath for the given SGC
// and returns an installTarget describing how to write files there.
func (do *DownloadOrchestrator) resolveInstallTarget(ctx context.Context, sgcID int64, containerPath string) (installTarget, error) {
	sgcInternalDir := do.getSGCInternalDir(sgcID)

	sgcResp, err := do.grpcClient.GetServerGameConfig(ctx, &pb.GetServerGameConfigRequest{
		ServerGameConfigId: sgcID,
	})
	if err != nil {
		return installTarget{}, fmt.Errorf("failed to get SGC %d: %w", sgcID, err)
	}

	volumesResp, err := do.grpcClient.ListGameConfigVolumes(ctx, &pb.ListGameConfigVolumesRequest{
		ConfigId: sgcResp.Config.GameConfigId,
	})
	if err != nil {
		return installTarget{}, fmt.Errorf("failed to list volumes: %w", err)
	}

	for _, vol := range volumesResp.Volumes {
		if !strings.HasPrefix(containerPath, vol.ContainerPath) {
			continue
		}

		relPath := strings.TrimPrefix(containerPath, vol.ContainerPath)
		relPath = strings.TrimPrefix(relPath, "/")

		if vol.VolumeType == "named" {
			return installTarget{
				isNamed:       true,
				VolumeName:    do.getNamedVolumeName(sgcID, vol.Name),
				ContainerPath: vol.ContainerPath,
				RelPath:       relPath,
			}, nil
		}

		// Bind-mount: resolve to the host-accessible internal path
		subPath := strings.TrimPrefix(vol.HostSubpath, "/")
		if subPath == "" {
			subPath = vol.Name
		}
		return installTarget{
			isNamed:  false,
			BindPath: filepath.Join(sgcInternalDir, subPath, relPath),
		}, nil
	}

	return installTarget{}, fmt.Errorf("no volume found for container path %s", containerPath)
}

// tryServeFromCache attempts to satisfy cmd entirely from the S3 Workshop cache via
// CacheClient.TryFetch, for the exact contentVersion the caller has already resolved
// (VerifyWorkshopItem's live-verified current version, per #2184 FR8 -- see
// resolveVerifyPlan). It returns ok == true, with the entry's cache_entry_id, only once
// the content has actually landed at the install target; any failure at any step returns
// ok == false so the caller falls back to the existing SteamCMD download path unchanged.
//
// The object is always fetched into an addon-scoped staging directory rather than
// directly into the (possibly shared) install target, then merged into place the same
// way the SteamCMD path already does -- via copyDirectory for bind mounts or the busybox
// helper container for named volumes -- so a cache hit can never wipe out other addons
// that already share the same install directory.
func (do *DownloadOrchestrator) tryServeFromCache(ctx context.Context, cmd *DownloadAddonCommand, contentVersion string, logger *slog.Logger) (cacheEntryID int64, ok bool) {
	if do.cacheClient == nil {
		return 0, false
	}

	target, err := do.resolveInstallTarget(ctx, cmd.SGCID, cmd.InstallPath)
	if err != nil {
		logger.Warn("failed to resolve install target for workshop cache fast path, falling back to SteamCMD", "error", err)
		return 0, false
	}

	cacheSuffix := fmt.Sprintf("%d-%d", cmd.AddonID, time.Now().UnixNano())
	cacheStagingInternal := filepath.Join(do.getSGCInternalDir(cmd.SGCID), ".workshop-cache-staging", cacheSuffix)
	cacheStagingHost := filepath.Join(do.getSGCHostDir(cmd.SGCID), ".workshop-cache-staging", cacheSuffix)
	if err := os.MkdirAll(filepath.Dir(cacheStagingInternal), 0777); err != nil {
		logger.Warn("failed to create workshop cache staging directory, falling back to SteamCMD", "error", err)
		return 0, false
	}
	defer os.RemoveAll(cacheStagingInternal)

	fetch, err := do.cacheClient.TryFetch(ctx, cmd.WorkshopID, contentVersion, cacheStagingInternal)
	if err != nil || !fetch.Hit {
		// CacheClient already logs the reason; a miss or transfer failure is the
		// ordinary fallback path, not a defect in this flow.
		return 0, false
	}

	if target.isNamed {
		destPath := target.ContainerPath
		if target.RelPath != "" {
			destPath = filepath.Join(destPath, target.RelPath)
		}
		copyCmd := fmt.Sprintf("mkdir -p %s && cp -r /tmp/workshop-cache-staging/. %s/", destPath, destPath)
		helperConfig := docker.ContainerConfig{
			Name:    fmt.Sprintf("workshop-cache-install-%s-%d-%d", do.environment, cmd.SGCID, cmd.AddonID),
			Image:   "busybox:latest",
			Command: []string{"sh", "-c", copyCmd},
			Volumes: []string{
				fmt.Sprintf("%s:/tmp/workshop-cache-staging", cacheStagingHost),
				fmt.Sprintf("%s:%s", target.VolumeName, target.ContainerPath),
			},
		}
		logger.Info("copying cached workshop content to named volume via helper container", "volume", target.VolumeName, "dest", destPath)
		if err := do.runHelperContainer(ctx, helperConfig); err != nil {
			logger.Warn("failed to copy cached workshop content into named volume, falling back to SteamCMD", "error", err)
			return 0, false
		}
		return fetch.CacheEntryID, true
	}

	if err := os.MkdirAll(target.BindPath, 0777); err != nil {
		logger.Warn("failed to create install directory for workshop cache fast path, falling back to SteamCMD", "error", err)
		return 0, false
	}
	if err := do.copyDirectory(cacheStagingInternal, target.BindPath); err != nil {
		logger.Warn("failed to copy cached workshop content to install path, falling back to SteamCMD", "error", err)
		return 0, false
	}
	return fetch.CacheEntryID, true
}

// resolveVerifyPlan runs the FR8/FR9 live verify against the addon's Workshop source and
// decides the content version this install should key its cache read/write against.
//
// A verify failure (RPC/SteamCMD error) must never fail the install (Testing: "verify
// RPC/SteamCMD failure -> falls back to a plain download rather than failing the
// install"): on failure this returns changed=true so the caller always falls straight
// through to a full download with no cache read attempted, using the addon's last-known
// control-api-synced version (addon.LastUpdated, Steam's time_updated) as its best-effort
// content identity for the eventual cache write.
func (do *DownloadOrchestrator) resolveVerifyPlan(ctx context.Context, cmd *DownloadAddonCommand, addon *pb.WorkshopAddon, logger *slog.Logger) (contentVersion string, changed bool) {
	knownVersion := strconv.FormatInt(addon.LastUpdated, 10)

	result, err := do.VerifyWorkshopItem(ctx, cmd.WorkshopID, cmd.SteamAppID, knownVersion)
	if err != nil {
		logger.Warn("workshop verify failed, falling back to a plain download", "error", err)
		return knownVersion, true
	}

	version := result.ContentVersion
	if version == "" {
		version = knownVersion
	}
	return version, result.Changed
}

// publishWorkshopCacheStatus publishes a WorkshopCacheStatusUpdate (#2184) if rmqPublisher
// also implements WorkshopCachePublisher, and is a silent no-op otherwise -- see
// WorkshopCachePublisher's doc comment for why this is a type assertion rather than a
// method on InstallationStatusPublisher.
func (do *DownloadOrchestrator) publishWorkshopCacheStatus(ctx context.Context, workshopID, contentVersion string, cacheEntryID int64, event string, sizeBytes int64) {
	publisher, ok := do.rmqPublisher.(WorkshopCachePublisher)
	if !ok {
		return
	}
	update := &rmq.WorkshopCacheStatusUpdate{
		ServerID:       do.serverID,
		WorkshopID:     workshopID,
		ContentVersion: contentVersion,
		CacheEntryID:   cacheEntryID,
		Event:          event,
		SizeBytes:      sizeBytes,
		VerifiedAt:     time.Now().UTC(),
	}
	if err := publisher.PublishWorkshopCacheStatus(ctx, update); err != nil {
		slog.Error("failed to publish workshop cache status", "workshop_id", workshopID, "event", event, "error", err)
	}
}

// uploadToCache implements FR9's write side: package the content that just landed at
// contentDir (an uncompressed tar, matching CacheClient's read-path expectation -- see
// cache_client.go's cacheObjectFormat) and PUT it to the presigned URL for
// (workshopID, contentVersion)'s cache entry, then publish the outcome on the workshop
// cache status key so control-api can record the entry and this host's presence (FR10).
//
// Takes workshopID directly (rather than a *DownloadAddonCommand) so the admin-triggered
// on-demand verify path (#2186, HandleVerifyCacheEntryCommand) can reuse this exact upload
// primitive for its own "changed" result -- it has no DownloadAddonCommand of its own,
// only a workshop_id/content_version pulled off the verify command.
//
// This is entirely best-effort with respect to the install that already succeeded by the
// time this is called: NFR4 requires that "a failed upload leaves no presence row and no
// claim that the entry is complete", so any failure here is logged and swallowed --
// never surfaced as an install error, and critically never followed by a status publish,
// since a publish is exactly the claim that must not be made on a failed upload. Two hosts
// racing to upload the same (workshopID, contentVersion) both resolve the same
// cache_entry_id/s3_key (control-api's GetCacheUploadURL, NFR4) and upload
// byte-identical content, so the ordinary last-writer-wins semantics of an S3 PUT are
// harmless here -- no distributed lock is used or needed (LB8).
func (do *DownloadOrchestrator) uploadToCache(ctx context.Context, workshopID, contentDir, contentVersion string, changed bool, logger *slog.Logger) {
	if do.workshopClient == nil {
		return
	}

	tarPath, size, err := tarDirectory(contentDir)
	if err != nil {
		logger.Warn("failed to package workshop content for cache upload", "error", err)
		return
	}
	defer os.Remove(tarPath)

	uploadResp, err := do.workshopClient.GetCacheUploadURL(ctx, &pb.GetCacheUploadURLRequest{
		ServerId:       do.serverID,
		WorkshopId:     workshopID,
		ContentVersion: contentVersion,
	})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.Unimplemented {
			// Older control-api or an unavailable relay: an expected deployment-skew
			// case, not a genuine error. The install itself already succeeded.
			logger.Info("control-api does not support workshop cache relay, skipping cache upload")
		} else {
			logger.Warn("failed to obtain workshop cache upload URL", "error", err)
		}
		return
	}

	if err := putFile(ctx, uploadResp.PresignedUrl, tarPath, size); err != nil {
		// A stale/expired presigned URL (or any other transfer failure) must never fail
		// the install, which already succeeded -- and, per NFR4, must not be followed by
		// a status publish (no presence row, no completeness claim).
		logger.Warn("workshop cache upload failed", "cache_entry_id", uploadResp.CacheEntryId, "error", scrubURL(err, uploadResp.PresignedUrl))
		return
	}

	event := workshopCacheEventPopulated
	if changed {
		event = workshopCacheEventRefreshed
	}
	do.publishWorkshopCacheStatus(ctx, workshopID, contentVersion, uploadResp.CacheEntryId, event, size)
	logger.Info("uploaded workshop content to cache", "cache_entry_id", uploadResp.CacheEntryId, "event", event, "size_bytes", size)
}

// tarDirectory packages srcDir into an uncompressed tar archive at a fresh temp file
// (the wire format CacheClient.TryFetch's read path expects, see cache_client.go's
// cacheObjectFormat) and returns its path and size. The caller owns removing the temp
// file once done with it.
func tarDirectory(srcDir string) (tarPath string, size int64, err error) {
	tmpFile, err := os.CreateTemp("", "workshop-cache-upload-*.tar")
	if err != nil {
		return "", 0, fmt.Errorf("failed to create temp file for cache upload: %w", err)
	}
	defer tmpFile.Close()

	tw := tar.NewWriter(tmpFile)
	walkErr := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(relPath)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if walkErr != nil {
		tw.Close()
		os.Remove(tmpFile.Name())
		return "", 0, fmt.Errorf("failed to package %s: %w", srcDir, walkErr)
	}
	if err := tw.Close(); err != nil {
		os.Remove(tmpFile.Name())
		return "", 0, fmt.Errorf("failed to finalize tar archive: %w", err)
	}

	fi, err := os.Stat(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		return "", 0, err
	}
	return tmpFile.Name(), fi.Size(), nil
}

// putFile uploads the file at localPath to presignedURL via HTTP PUT with a known
// Content-Length, following the same pattern as manmanv2/host/backup.go's presigned S3
// upload (GetBody set so the client can retry on HTTP/2 REFUSED_STREAM).
func putFile(ctx context.Context, presignedURL, localPath string, size int64) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open packaged content for upload: %w", err)
	}
	defer f.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, f)
	if err != nil {
		return fmt.Errorf("failed to build upload request: %w", err)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/x-tar")
	req.GetBody = func() (io.ReadCloser, error) {
		return os.Open(localPath)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upload returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// runHelperContainer creates, starts, waits for, and removes a short-lived container.
// Used to perform operations (e.g. copying files into a named volume) that require Docker's
// volume machinery rather than direct host filesystem access.
func (do *DownloadOrchestrator) runHelperContainer(ctx context.Context, config docker.ContainerConfig) error {
	// Clean up any leftover container from a previous failed attempt
	if existing, err := do.dockerClient.GetContainerStatus(ctx, config.Name); err == nil && existing != nil {
		_ = do.dockerClient.RemoveContainer(ctx, existing.ContainerID, true)
	}

	// Always pull helper image to ensure latest version is used
	// TODO: add cache fallback
	const maxHelperPullAttempts = 3
	var helperPullErr error
	for attempt := 1; attempt <= maxHelperPullAttempts; attempt++ {
		helperPullErr = do.dockerClient.PullImage(ctx, config.Image)
		if helperPullErr == nil {
			break
		}
		if attempt < maxHelperPullAttempts {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	if helperPullErr != nil {
		return fmt.Errorf("failed to pull helper image %s: %w", config.Image, helperPullErr)
	}

	containerID, err := do.dockerClient.CreateContainer(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to create helper container: %w", err)
	}

	if err := do.dockerClient.StartContainer(ctx, containerID); err != nil {
		_ = do.dockerClient.RemoveContainer(ctx, containerID, true)
		return fmt.Errorf("failed to start helper container: %w", err)
	}

	// Wait for completion
	for {
		status, err := do.dockerClient.GetContainerStatus(ctx, containerID)
		if err != nil {
			_ = do.dockerClient.RemoveContainer(ctx, containerID, true)
			return fmt.Errorf("failed to get helper container status: %w", err)
		}
		if !status.Running {
			_ = do.dockerClient.RemoveContainer(ctx, containerID, true)
			if status.ExitCode != 0 {
				return fmt.Errorf("helper container exited with code %d", status.ExitCode)
			}
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
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

	// Fetch library attachments (includes installation_path_override and preset_id per library)
	attachmentsResp, err := do.workshopClient.GetSGCLibraryAttachments(ctx, &pb.GetSGCLibraryAttachmentsRequest{
		SgcId: sgcID,
	})
	if err != nil {
		logger.Error("failed to get SGC library attachments", "error", err)
		return fmt.Errorf("failed to get SGC library attachments: %w", err)
	}

	if len(attachmentsResp.Attachments) == 0 {
		logger.Info("no libraries attached to SGC, skipping addon downloads")
		return nil
	}

	// Fetch library defaults (includes library's default preset_id)
	librariesResp, err := do.workshopClient.ListSGCLibraries(ctx, &pb.ListSGCLibrariesRequest{
		SgcId: sgcID,
	})
	if err != nil {
		logger.Warn("failed to list SGC libraries for defaults, proceeding without library presets", "error", err)
	}

	// Build map: libraryID → library default preset_id
	libraryDefaultPreset := make(map[int64]int64)
	if librariesResp != nil {
		for _, lib := range librariesResp.Libraries {
			if lib.PresetId != 0 {
				libraryDefaultPreset[lib.LibraryId] = lib.PresetId
			}
		}
	}

	// Build map: libraryID → effective overrides (attachment overrides take priority over library defaults)
	type libraryOverrides struct {
		pathOverride string
		presetID     int64 // effective: attachment's preset > library default preset
		volumeID     int64 // which volume to install into
	}
	libraryOpts := make(map[int64]libraryOverrides)
	for _, att := range attachmentsResp.Attachments {
		opts := libraryOverrides{
			pathOverride: att.InstallationPathOverride,
			presetID:     att.PresetId, // SGC attachment override
			volumeID:     att.VolumeId,
		}
		if opts.presetID == 0 {
			opts.presetID = libraryDefaultPreset[att.LibraryId] // fall back to library default
		}
		libraryOpts[att.LibraryId] = opts
	}

	logger.Info("found libraries attached to SGC", "count", len(attachmentsResp.Attachments))

	// Collect all unique addons from all libraries (including nested references).
	// Track the overrides that apply to each addon (inherited from the top-level library attachment).
	type addonEntry struct {
		addon        *pb.WorkshopAddon
		pathOverride string
		presetID     int64
		volumeID     int64
	}
	addonMap := make(map[int64]addonEntry)
	visited := make(map[int64]bool)

	type queueItem struct {
		libraryID    int64
		pathOverride string // inherited from the top-level SGC library attachment
		presetID     int64  // effective preset (attachment override or library default)
		volumeID     int64  // which volume to install into
	}
	queue := make([]queueItem, 0, len(attachmentsResp.Attachments))
	for _, att := range attachmentsResp.Attachments {
		opts := libraryOpts[att.LibraryId]
		queue = append(queue, queueItem{libraryID: att.LibraryId, pathOverride: opts.pathOverride, presetID: opts.presetID, volumeID: opts.volumeID})
	}

	// BFS to collect all addons from all libraries
	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		if visited[item.libraryID] {
			continue
		}
		visited[item.libraryID] = true

		// Get addons for this library
		addonsResp, err := do.workshopClient.GetLibraryAddons(ctx, &pb.GetLibraryAddonsRequest{
			LibraryId: item.libraryID,
		})
		if err != nil {
			logger.Warn("failed to get addons for library", "library_id", item.libraryID, "error", err)
			continue
		}

		for _, addon := range addonsResp.Addons {
			if _, exists := addonMap[addon.AddonId]; !exists {
				addonMap[addon.AddonId] = addonEntry{addon: addon, pathOverride: item.pathOverride, presetID: item.presetID, volumeID: item.volumeID}
			}
		}

		// Get child libraries (inherit parent's overrides)
		childrenResp, err := do.workshopClient.GetChildLibraries(ctx, &pb.GetChildLibrariesRequest{
			LibraryId: item.libraryID,
		})
		if err != nil {
			logger.Warn("failed to get child libraries", "library_id", item.libraryID, "error", err)
			continue
		}

		for _, child := range childrenResp.Libraries {
			queue = append(queue, queueItem{libraryID: child.LibraryId, pathOverride: item.pathOverride, presetID: item.presetID, volumeID: item.volumeID})
		}
	}

	addonIDs := addonMap

	if len(addonMap) == 0 {
		logger.Info("no addons found in libraries")
		return nil
	}

	logger.Info("found addons to install", "count", len(addonMap))

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
	for addonID, entry := range addonIDs {
		addon := entry.addon
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
			SgcId:                    sgcID,
			AddonId:                  addonID,
			SkipDispatch:             true,
			InstallationPathOverride: entry.pathOverride,
			PresetIdOverride:         entry.presetID,
			VolumeIdOverride:         entry.volumeID,
		})
		if err != nil {
			// Treat installation errors as non-fatal: a misconfigured addon (e.g. missing
			// installation path) should not prevent the session from starting. Log and skip.
			logger.Error("failed to trigger installation, skipping addon", "addon_id", addonID, "workshop_id", addon.WorkshopId, "error", err)
			continue
		}

		if installResp.Installation == nil {
			logger.Error("InstallAddon returned nil installation, skipping addon", "addon_id", addonID)
			continue
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
			logger.Error("failed to download addon, skipping", "addon_id", addonID, "workshop_id", addon.WorkshopId, "error", err)
			continue
		}

		logger.Info("addon installation completed", "addon_id", addonID)
	}

	logger.Info("all library addons installed successfully")
	return nil
}
