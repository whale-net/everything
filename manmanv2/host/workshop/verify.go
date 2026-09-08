package workshop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/whale-net/everything/libs/go/docker"
)

// VerifyResult reports whether the addon's Workshop source still matches the
// content version a cache entry was built from.
type VerifyResult struct {
	Changed        bool
	ContentVersion string // the source's current version/timestamp
}

// verifyContainerPollInterval bounds how often VerifyWorkshopItem polls the verify
// container's status. Kept short relative to HandleDownloadCommand's download-container
// poll (1s) because a verify run does far less I/O than a full download and callers are
// waiting on it before deciding whether a download is even needed.
const verifyContainerPollInterval = 500 * time.Millisecond

// workshopManifestItemBlockPattern and workshopManifestFieldPattern extract a workshop
// item's manifest/timeupdated value from a SteamCMD appworkshop_<appid>.acf file. SteamCMD
// writes this VDF-format file into force_install_dir/steamapps/workshop/ as a side effect of
// +workshop_download_item, regardless of whether it actually transferred new bytes -- which
// is what makes it a reliable signal of the *current* content version even on a run where
// nothing changed. The ACF item blocks this package cares about ("WorkshopItemsInstalled"
// and "WorkshopItemDetails") are never nested more than one level deep, so a non-greedy
// brace-bounded regex is sufficient without a full VDF parser.
var (
	workshopManifestFieldPattern = regexp.MustCompile(`"manifest"\s+"([^"]+)"`)
	workshopTimeUpdatedPattern   = regexp.MustCompile(`"timeupdated"\s+"([^"]+)"`)
)

// VerifyWorkshopItem checks the addon's Workshop source (via SteamCMD
// workshop_status/workshop_download_item verify semantics against the same
// SteamCMD container path the orchestrator already drives) and reports
// whether its content version has changed since knownContentVersion.
//
// This primitive backs both the install-time verify/cache-refresh flow
// (#2184 FR8/FR9) and the Admin on-demand verify RPC, so it is deliberately
// independent of any install-flow state: it takes exactly the identifiers
// it needs and returns a plain result, with no side effects on the
// orchestrator's in-progress-download tracking or RMQ publishing.
//
// Implementation: this drives a short-lived SteamCMD container (mirroring
// HandleDownloadCommand's container lifecycle) with
// "+workshop_download_item <appid> <itemid> validate", pointed at a
// per-(steamAppID, workshopID) directory that persists across calls --
// unlike HandleDownloadCommand's per-invocation temp download dir. Because
// the directory persists, SteamCMD's own manifest bookkeeping in
// appworkshop_<appid>.acf lets an unchanged item's verify run cheaply
// (SteamCMD detects the on-disk manifest already matches the item's current
// manifest and does not re-transfer file contents), while a changed item is
// (re)synced during the same call. Either way, the resulting ACF file
// reports the item's *current* manifest/timeupdated value, which becomes
// ContentVersion; Changed is simply ContentVersion != knownContentVersion.
//
// A SteamCMD/RPC failure here is always surfaced as an error so callers can
// fall back to a plain download rather than failing the install (see
// Testing: "verify RPC/SteamCMD failure -> falls back to a plain download
// rather than failing the install").
func (do *DownloadOrchestrator) VerifyWorkshopItem(ctx context.Context, workshopID, steamAppID, knownContentVersion string) (VerifyResult, error) {
	if do.dockerClient == nil {
		return VerifyResult{}, fmt.Errorf("workshop verify: docker client is not configured")
	}
	if workshopID == "" || steamAppID == "" {
		return VerifyResult{}, fmt.Errorf("workshop verify: workshop_id and steam_app_id are required")
	}

	verifyInternalDir := do.getVerifyInternalDir(steamAppID, workshopID)
	if err := os.MkdirAll(verifyInternalDir, 0777); err != nil {
		return VerifyResult{}, fmt.Errorf("failed to create workshop verify directory: %w", err)
	}
	verifyHostDir := do.getVerifyHostDir(steamAppID, workshopID)
	containerName := do.getVerifyContainerName(steamAppID, workshopID)

	// Clean up any leftover container from a previous failed verify attempt.
	if existing, err := do.dockerClient.GetContainerStatus(ctx, containerName); err == nil && existing != nil {
		_ = do.dockerClient.RemoveContainer(ctx, existing.ContainerID, true)
	}

	if err := do.dockerClient.PullImage(ctx, steamCMDImage); err != nil {
		return VerifyResult{}, fmt.Errorf("failed to pull steamcmd image for workshop verify: %w", err)
	}

	const containerVerifyDir = "/tmp/workshop-verify"
	containerConfig := docker.ContainerConfig{
		Name:    containerName,
		Image:   steamCMDImage,
		Command: do.buildVerifySteamCMDCommand(steamAppID, workshopID, containerVerifyDir),
		Volumes: []string{fmt.Sprintf("%s:%s", verifyHostDir, containerVerifyDir)},
		Env:     []string{},
	}

	containerID, err := do.dockerClient.CreateContainer(ctx, containerConfig)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("failed to create workshop verify container: %w", err)
	}
	defer func() { _ = do.dockerClient.RemoveContainer(context.Background(), containerID, true) }()

	if err := do.dockerClient.StartContainer(ctx, containerID); err != nil {
		return VerifyResult{}, fmt.Errorf("failed to start workshop verify container: %w", err)
	}

	var exitCode int
	for {
		status, err := do.dockerClient.GetContainerStatus(ctx, containerID)
		if err != nil {
			return VerifyResult{}, fmt.Errorf("failed to get workshop verify container status: %w", err)
		}
		if !status.Running {
			exitCode = status.ExitCode
			break
		}
		select {
		case <-ctx.Done():
			return VerifyResult{}, ctx.Err()
		case <-time.After(verifyContainerPollInterval):
		}
	}

	if exitCode != 0 {
		return VerifyResult{}, fmt.Errorf("steamcmd workshop verify exited with code %d", exitCode)
	}

	contentVersion, err := readWorkshopManifestVersion(verifyInternalDir, steamAppID, workshopID)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("failed to read workshop manifest after verify: %w", err)
	}

	return VerifyResult{
		Changed:        contentVersion != knownContentVersion,
		ContentVersion: contentVersion,
	}, nil
}

// buildVerifySteamCMDCommand mirrors buildSteamCMDCommand but adds "validate" so SteamCMD
// checks the local manifest against the item's current one rather than unconditionally
// re-fetching -- this is the "cheap when unchanged" property VerifyWorkshopItem's doc
// comment describes.
func (do *DownloadOrchestrator) buildVerifySteamCMDCommand(steamAppID, workshopID, installPath string) []string {
	return []string{
		"+force_install_dir", installPath,
		"+login", "anonymous",
		"+workshop_download_item", steamAppID, workshopID, "validate",
		"+quit",
	}
}

// getVerifyContainerName returns the environment-aware container name for a workshop
// item's verify run. Distinct from getDownloadContainerName's SGC/addon-scoped naming --
// verify is addon-scoped only, independent of any particular SGC or install (see
// VerifyWorkshopItem's doc comment).
func (do *DownloadOrchestrator) getVerifyContainerName(steamAppID, workshopID string) string {
	if do.environment != "" {
		return fmt.Sprintf("workshop-verify-%s-%s-%s", do.environment, steamAppID, workshopID)
	}
	return fmt.Sprintf("workshop-verify-%s-%s", steamAppID, workshopID)
}

// getVerifyInternalDir and getVerifyHostDir return the (persistent, not per-invocation
// temp) directory SteamCMD's verify run uses, from this container's and Docker's
// perspective respectively. Persisting this directory across calls -- rather than a fresh
// temp dir per HandleDownloadCommand's download flow -- is what lets SteamCMD's own
// manifest check answer "did this change" cheaply.
func (do *DownloadOrchestrator) getVerifyInternalDir(steamAppID, workshopID string) string {
	return filepath.Join(do.internalDataDir, ".workshop-verify", steamAppID, workshopID)
}

func (do *DownloadOrchestrator) getVerifyHostDir(steamAppID, workshopID string) string {
	return filepath.Join(do.hostDataDir, ".workshop-verify", steamAppID, workshopID)
}

// readWorkshopManifestVersion reads verifyDir/steamapps/workshop/appworkshop_<steamAppID>.acf
// (written by SteamCMD as a side effect of +workshop_download_item) and extracts
// workshopID's current manifest id, falling back to its timeupdated timestamp if no
// manifest field is present. Either value is stable and comparable across calls, which is
// all VerifyWorkshopItem's Changed comparison needs.
func readWorkshopManifestVersion(verifyDir, steamAppID, workshopID string) (string, error) {
	acfPath := filepath.Join(verifyDir, "steamapps", "workshop", fmt.Sprintf("appworkshop_%s.acf", steamAppID))
	data, err := os.ReadFile(acfPath)
	if err != nil {
		return "", fmt.Errorf("failed to read workshop manifest %s: %w", acfPath, err)
	}

	version, ok := extractWorkshopContentVersion(string(data), workshopID)
	if !ok {
		return "", fmt.Errorf("workshop item %s not found in manifest %s", workshopID, acfPath)
	}
	return version, nil
}

// extractWorkshopContentVersion scans acfContent for a block keyed by workshopID (SteamCMD
// nests each installed item's details under its published-file ID in both the
// "WorkshopItemsInstalled" and "WorkshopItemDetails" sections) and returns its manifest id,
// or its timeupdated timestamp if no manifest field is present in that block.
func extractWorkshopContentVersion(acfContent, workshopID string) (string, bool) {
	itemBlockPattern := regexp.MustCompile(`(?s)"` + regexp.QuoteMeta(workshopID) + `"\s*\{([^{}]*)\}`)
	for _, match := range itemBlockPattern.FindAllStringSubmatch(acfContent, -1) {
		block := match[1]
		if m := workshopManifestFieldPattern.FindStringSubmatch(block); m != nil {
			return m[1], true
		}
		if m := workshopTimeUpdatedPattern.FindStringSubmatch(block); m != nil {
			return m[1], true
		}
	}
	return "", false
}
