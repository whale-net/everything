package workshop

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/whale-net/everything/manmanv2"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/steam"
)

// RMQPublisher defines the interface for publishing messages to RabbitMQ
type RMQPublisher interface {
	PublishDownloadCommand(ctx context.Context, serverID int64, cmd *DownloadAddonCommand) error
	PublishRemoveCommand(ctx context.Context, serverID int64, cmd *RemoveAddonCommand) error
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

// WorkshopManager orchestrates workshop addon operations
type WorkshopManager struct {
	addonRepo        repository.WorkshopAddonRepository
	installationRepo repository.WorkshopInstallationRepository
	libraryRepo      repository.WorkshopLibraryRepository
	sgcRepo          repository.ServerGameConfigRepository
	gameConfigRepo   repository.GameConfigRepository
	strategyRepo     repository.ConfigurationStrategyRepository
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
	strategyRepo repository.ConfigurationStrategyRepository,
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
		strategyRepo:     strategyRepo,
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

	err = wm.rmqPublisher.PublishDownloadCommand(ctx, sgc.ServerID, downloadCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to publish download command: %w", err)
	}

	return installation, nil
}

// resolveInstallationPath determines the target path for addon installation
func (wm *WorkshopManager) resolveInstallationPath(ctx context.Context, sgc *manman.ServerGameConfig, addon *manman.WorkshopAddon) (string, error) {
	// Get game config to access game ID
	gameConfig, err := wm.gameConfigRepo.Get(ctx, sgc.GameConfigID)
	if err != nil {
		return "", fmt.Errorf("failed to get game config: %w", err)
	}

	// Get volume strategies for the game
	strategies, err := wm.strategyRepo.ListByGame(ctx, gameConfig.GameID)
	if err != nil {
		return "", fmt.Errorf("failed to list strategies: %w", err)
	}

	// Find volume strategy (should be only one or use apply_order)
	var volumeStrategy *manman.ConfigurationStrategy
	for _, s := range strategies {
		if s.StrategyType == manman.StrategyTypeVolume {
			volumeStrategy = s
			break
		}
	}

	if volumeStrategy == nil {
		return "", fmt.Errorf("no volume strategy found for game")
	}

	// Combine volume target path with addon installation path
	basePath := volumeStrategy.TargetPath
	if basePath == nil {
		return "", fmt.Errorf("volume strategy missing target_path")
	}

	addonPath := addon.InstallationPath
	if addonPath == nil || *addonPath == "" {
		return "", fmt.Errorf("addon missing installation_path")
	}

	// Resolve to absolute path
	fullPath := filepath.Join(*basePath, *addonPath)
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

	err = wm.rmqPublisher.PublishRemoveCommand(ctx, sgc.ServerID, removeCmd)
	if err != nil {
		return fmt.Errorf("failed to publish remove command: %w", err)
	}

	return nil
}
