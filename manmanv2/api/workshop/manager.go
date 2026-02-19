package workshop

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/whale-net/everything/manmanv2"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/steam"
)

// RMQPublisher defines the interface for publishing messages to RabbitMQ
type RMQPublisher interface {
	Publish(ctx context.Context, exchange, routingKey string, body interface{}) error
}

// SteamClient defines the interface for Steam Workshop API operations
type SteamClient interface {
	GetWorkshopItemDetails(ctx context.Context, workshopID string) (*steam.WorkshopItemMetadata, error)
	GetCollectionDetails(ctx context.Context, collectionID string) ([]steam.CollectionItem, error)
}

// DownloadAddonCommand represents a command to download a workshop addon
type DownloadAddonCommand struct {
	InstallationID int64  `json:"installation_id"`
	SGCID          int64  `json:"sgc_id"`
	AddonID        int64  `json:"addon_id"`
	WorkshopID     string `json:"workshop_id"`
	SteamAppID     string `json:"steam_app_id"`
	InstallPath    string `json:"install_path"`
}

// RemoveAddonCommand represents a command to remove a workshop addon
type RemoveAddonCommand struct {
	InstallationID   int64  `json:"installation_id"`
	SGCID            int64  `json:"sgc_id"`
	AddonID          int64  `json:"addon_id"`
	InstallationPath string `json:"installation_path"`
}

// WorkshopManagerInterface defines the interface for workshop addon operations
type WorkshopManagerInterface interface {
	InstallAddon(ctx context.Context, sgcID, addonID int64, forceReinstall bool) (*manman.WorkshopInstallation, error)
	RemoveInstallation(ctx context.Context, installationID int64) error
	FetchMetadata(ctx context.Context, gameID int64, workshopID string) (*manman.WorkshopAddon, error)
	EnsureLibraryAddonsInstalled(ctx context.Context, sgcID int64) error
}

// WorkshopManager orchestrates workshop addon operations
type WorkshopManager struct {
	addonRepo        repository.WorkshopAddonRepository
	installationRepo repository.WorkshopInstallationRepository
	libraryRepo      repository.WorkshopLibraryRepository
	sgcRepo          repository.ServerGameConfigRepository
	gameConfigRepo   repository.GameConfigRepository
	volumeRepo       repository.GameConfigVolumeRepository
	sessionRepo      repository.SessionRepository
	steamClient      SteamClient
	rmqPublisher     RMQPublisher
}

// NewWorkshopManager creates a new WorkshopManager instance
func NewWorkshopManager(
	addonRepo repository.WorkshopAddonRepository,
	installationRepo repository.WorkshopInstallationRepository,
	libraryRepo repository.WorkshopLibraryRepository,
	sgcRepo repository.ServerGameConfigRepository,
	gameConfigRepo repository.GameConfigRepository,
	volumeRepo repository.GameConfigVolumeRepository,
	sessionRepo repository.SessionRepository,
	steamClient SteamClient,
	rmqPublisher RMQPublisher,
) *WorkshopManager {
	return &WorkshopManager{
		addonRepo:        addonRepo,
		installationRepo: installationRepo,
		libraryRepo:      libraryRepo,
		sgcRepo:          sgcRepo,
		gameConfigRepo:   gameConfigRepo,
		volumeRepo:       volumeRepo,
		sessionRepo:      sessionRepo,
		steamClient:      steamClient,
		rmqPublisher:     rmqPublisher,
	}
}

// InstallAddon downloads and installs an addon to a ServerGameConfig
func (wm *WorkshopManager) InstallAddon(ctx context.Context, sgcID, addonID int64, forceReinstall bool) (*manman.WorkshopInstallation, error) {
	// 1. Check if already installed
	existing, err := wm.installationRepo.GetBySGCAndAddon(ctx, sgcID, addonID)
	if err == nil && existing.Status == manman.InstallationStatusInstalled && !forceReinstall {
		return existing, nil // Already installed
	}

	// 2. Get addon details
	addon, err := wm.addonRepo.Get(ctx, addonID)
	if err != nil {
		return nil, fmt.Errorf("failed to get addon: %w", err)
	}

	// 3. Get SGC and resolve volume paths
	sgc, err := wm.sgcRepo.Get(ctx, sgcID)
	if err != nil {
		return nil, fmt.Errorf("failed to get SGC: %w", err)
	}

	// 4. Resolve installation path from volume strategies
	installPath, err := wm.resolveInstallationPath(ctx, sgc, addon)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve installation path: %w", err)
	}

	// 5. Create or update installation record
	installation := &manman.WorkshopInstallation{
		SGCID:            sgcID,
		AddonID:          addonID,
		Status:           manman.InstallationStatusPending,
		InstallationPath: installPath,
		ProgressPercent:  0,
	}

	if existing != nil {
		installation.InstallationID = existing.InstallationID
		err = wm.installationRepo.UpdateStatus(ctx, installation.InstallationID, manman.InstallationStatusPending, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to update installation status: %w", err)
		}
		installation = existing
		installation.Status = manman.InstallationStatusPending
	} else {
		installation, err = wm.installationRepo.Create(ctx, installation)
		if err != nil {
			return nil, fmt.Errorf("failed to create installation record: %w", err)
		}
	}

	// 6. Publish download command to RabbitMQ for host manager
	steamAppID := ""
	if addon.Metadata != nil {
		if appID, ok := addon.Metadata["steam_app_id"].(string); ok {
			steamAppID = appID
		}
	}

	downloadCmd := &DownloadAddonCommand{
		InstallationID: installation.InstallationID,
		SGCID:          sgcID,
		AddonID:        addonID,
		WorkshopID:     addon.WorkshopID,
		SteamAppID:     steamAppID,
		InstallPath:    installPath,
	}

	routingKey := fmt.Sprintf("command.host.%d.workshop.download", sgc.ServerID)
	err = wm.rmqPublisher.Publish(ctx, "manman.commands", routingKey, downloadCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to publish download command: %w", err)
	}

	return installation, nil
}

// resolveInstallationPath determines the target path for addon installation
func (wm *WorkshopManager) resolveInstallationPath(ctx context.Context, sgc *manman.ServerGameConfig, addon *manman.WorkshopAddon) (string, error) {
	// Get volumes for this GameConfig
	volumes, err := wm.volumeRepo.ListByGameConfig(ctx, sgc.GameConfigID)
	if err != nil {
		return "", fmt.Errorf("failed to get volumes: %w", err)
	}

	if len(volumes) == 0 {
		return "", fmt.Errorf("no volumes configured for GameConfig %d", sgc.GameConfigID)
	}

	// Use first volume's container_path as base (or could match by name if addon specifies)
	basePath := volumes[0].ContainerPath

	addonPath := addon.InstallationPath
	if addonPath == nil || *addonPath == "" {
		return "", fmt.Errorf("addon missing installation_path")
	}

	// Resolve to absolute path
	fullPath := filepath.Join(basePath, *addonPath)
	return fullPath, nil
}

// FetchMetadata fetches metadata from Steam Workshop without creating a database record
func (wm *WorkshopManager) FetchMetadata(ctx context.Context, gameID int64, workshopID string) (*manman.WorkshopAddon, error) {
	// Fetch metadata from Steam Workshop API
	metadata, err := wm.steamClient.GetWorkshopItemDetails(ctx, workshopID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch workshop metadata: %w", err)
	}

	addon := &manman.WorkshopAddon{
		GameID:        gameID,
		WorkshopID:    workshopID,
		PlatformType:  manman.PlatformTypeSteamWorkshop,
		Name:          metadata.Title,
		Description:   &metadata.Description,
		FileSizeBytes: &metadata.FileSize,
		IsCollection:  metadata.IsCollection,
		LastUpdated:   &metadata.TimeUpdated,
	}

	// If collection, fetch all items
	if metadata.IsCollection {
		items, err := wm.steamClient.GetCollectionDetails(ctx, workshopID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch collection details: %w", err)
		}

		// Store collection items in metadata
		collectionItems := make([]map[string]interface{}, len(items))
		for i, item := range items {
			collectionItems[i] = map[string]interface{}{
				"workshop_id": item.WorkshopID,
				"title":       item.Title,
			}
		}
		addon.Metadata = map[string]interface{}{
			"collection_items": collectionItems,
		}
	}

	// Return addon without persisting to database
	return addon, nil
}

// EnsureLibraryAddonsInstalled pre-installs all addons from libraries attached to an SGC.
// It collects all unique addon IDs (recursively via library references), triggers installs
// for any not yet installed, and polls until they complete or timeout.
// Returns nil even if some installs fail — failures are visible in the UI.
func (wm *WorkshopManager) EnsureLibraryAddonsInstalled(ctx context.Context, sgcID int64) error {
	// 1. List libraries attached to this SGC
	libraries, err := wm.sgcRepo.ListLibraries(ctx, sgcID)
	if err != nil {
		return fmt.Errorf("failed to list SGC libraries: %w", err)
	}
	if len(libraries) == 0 {
		return nil
	}

	// 2. BFS collect all unique addon IDs from all libraries (including nested references)
	addonIDs := make(map[int64]struct{})
	visited := make(map[int64]struct{})
	queue := make([]int64, 0, len(libraries))
	for _, lib := range libraries {
		queue = append(queue, lib.LibraryID)
	}

	for len(queue) > 0 {
		libID := queue[0]
		queue = queue[1:]

		if _, seen := visited[libID]; seen {
			continue
		}
		visited[libID] = struct{}{}

		// Collect direct addons
		addons, err := wm.libraryRepo.ListAddons(ctx, libID)
		if err != nil {
			log.Printf("Warning: failed to list addons for library %d: %v", libID, err)
		} else {
			for _, a := range addons {
				addonIDs[a.AddonID] = struct{}{}
			}
		}

		// Enqueue child libraries
		children, err := wm.libraryRepo.ListReferences(ctx, libID)
		if err != nil {
			log.Printf("Warning: failed to list child libraries for library %d: %v", libID, err)
		} else {
			for _, child := range children {
				queue = append(queue, child.LibraryID)
			}
		}
	}

	if len(addonIDs) == 0 {
		return nil
	}

	// 3. Trigger installs for addons not yet installed
	var triggered []int64
	for addonID := range addonIDs {
		existing, err := wm.installationRepo.GetBySGCAndAddon(ctx, sgcID, addonID)
		if err == nil && existing.Status == manman.InstallationStatusInstalled {
			continue // already installed
		}

		if _, err := wm.InstallAddon(ctx, sgcID, addonID, false); err != nil {
			log.Printf("Warning: failed to trigger install for SGC %d addon %d: %v", sgcID, addonID, err)
			continue
		}
		triggered = append(triggered, addonID)
	}

	if len(triggered) == 0 {
		return nil
	}

	// 4. Poll until all triggered addons are installed or failed (up to 90s)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		allDone := true
		for _, addonID := range triggered {
			inst, err := wm.installationRepo.GetBySGCAndAddon(ctx, sgcID, addonID)
			if err != nil {
				allDone = false
				continue
			}
			if inst.Status != manman.InstallationStatusInstalled && inst.Status != manman.InstallationStatusFailed && inst.Status != manman.InstallationStatusRemoved {
				allDone = false
			}
		}

		if allDone {
			break
		}
	}

	return nil
}

// FetchAndCreateAddon fetches metadata from Steam Workshop and creates addon
func (wm *WorkshopManager) FetchAndCreateAddon(ctx context.Context, gameID int64, workshopID string) (*manman.WorkshopAddon, error) {
	// Fetch metadata using FetchMetadata
	addon, err := wm.FetchMetadata(ctx, gameID, workshopID)
	if err != nil {
		return nil, err
	}

	// Create addon in database
	return wm.addonRepo.Create(ctx, addon)
}

// RemoveInstallation removes an installed addon from a ServerGameConfig
func (wm *WorkshopManager) RemoveInstallation(ctx context.Context, installationID int64) error {
	// Get installation record
	installation, err := wm.installationRepo.Get(ctx, installationID)
	if err != nil {
		return fmt.Errorf("failed to get installation: %w", err)
	}

	// Validate no active sessions using the addon (Requirement 10.5)
	sessions, err := wm.sessionRepo.List(ctx, &installation.SGCID, 100, 0)
	if err != nil {
		return fmt.Errorf("failed to list sessions for SGC: %w", err)
	}

	// Check if any sessions are active
	for _, session := range sessions {
		if session.IsActive() {
			return fmt.Errorf("cannot remove addon: SGC has active session (session_id=%d, status=%s)", session.SessionID, session.Status)
		}
	}

	// Get SGC to determine server ID for RabbitMQ routing
	sgc, err := wm.sgcRepo.Get(ctx, installation.SGCID)
	if err != nil {
		return fmt.Errorf("failed to get SGC: %w", err)
	}

	// Update status to removed
	err = wm.installationRepo.UpdateStatus(ctx, installationID, manman.InstallationStatusRemoved, nil)
	if err != nil {
		return fmt.Errorf("failed to update installation status: %w", err)
	}

	// Publish removal command to host manager to delete files
	removeCmd := &RemoveAddonCommand{
		InstallationID:   installationID,
		SGCID:            installation.SGCID,
		AddonID:          installation.AddonID,
		InstallationPath: installation.InstallationPath,
	}

	routingKey := fmt.Sprintf("command.host.%d.workshop.remove", sgc.ServerID)
	err = wm.rmqPublisher.Publish(ctx, "manman.commands", routingKey, removeCmd)
	if err != nil {
		return fmt.Errorf("failed to publish remove command: %w", err)
	}

	return nil
}
