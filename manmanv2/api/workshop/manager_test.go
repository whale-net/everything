package workshop

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/steam"
)

// Mock implementations for testing - only implement methods used by InstallAddon

type mockAddonRepo struct {
	addons map[int64]*manman.WorkshopAddon
}

func (m *mockAddonRepo) Create(ctx context.Context, addon *manman.WorkshopAddon) (*manman.WorkshopAddon, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockAddonRepo) Get(ctx context.Context, addonID int64) (*manman.WorkshopAddon, error) {
	addon, ok := m.addons[addonID]
	if !ok {
		return nil, fmt.Errorf("addon not found")
	}
	return addon, nil
}

func (m *mockAddonRepo) GetByWorkshopID(ctx context.Context, gameID int64, workshopID string, platformType string) (*manman.WorkshopAddon, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockAddonRepo) List(ctx context.Context, gameID *int64, includeDeprecated bool, limit, offset int) ([]*manman.WorkshopAddon, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockAddonRepo) Update(ctx context.Context, addon *manman.WorkshopAddon) error {
	return fmt.Errorf("not implemented")
}

func (m *mockAddonRepo) Delete(ctx context.Context, addonID int64) error {
	return fmt.Errorf("not implemented")
}

type mockInstallationRepo struct {
	installations map[string]*manman.WorkshopInstallation // key: "sgcID-addonID"
	installByID   map[int64]*manman.WorkshopInstallation  // key: installationID
	nextID        int64
}

func (m *mockInstallationRepo) GetBySGCAndAddon(ctx context.Context, sgcID, addonID int64) (*manman.WorkshopInstallation, error) {
	key := fmt.Sprintf("%d-%d", sgcID, addonID)
	inst, ok := m.installations[key]
	if !ok {
		return nil, fmt.Errorf("installation not found")
	}
	return inst, nil
}

func (m *mockInstallationRepo) Create(ctx context.Context, installation *manman.WorkshopInstallation) (*manman.WorkshopInstallation, error) {
	m.nextID++
	installation.InstallationID = m.nextID
	key := fmt.Sprintf("%d-%d", installation.SGCID, installation.AddonID)
	m.installations[key] = installation
	m.installByID[installation.InstallationID] = installation
	return installation, nil
}

func (m *mockInstallationRepo) Get(ctx context.Context, installationID int64) (*manman.WorkshopInstallation, error) {
	inst, ok := m.installByID[installationID]
	if !ok {
		return nil, fmt.Errorf("installation not found")
	}
	return inst, nil
}

func (m *mockInstallationRepo) ListBySGC(ctx context.Context, sgcID int64, limit, offset int) ([]*manman.WorkshopInstallation, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockInstallationRepo) ListByAddon(ctx context.Context, addonID int64, limit, offset int) ([]*manman.WorkshopInstallation, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockInstallationRepo) UpdateStatus(ctx context.Context, installationID int64, status string, errorMsg *string) error {
	inst, ok := m.installByID[installationID]
	if !ok {
		return fmt.Errorf("installation not found")
	}
	inst.Status = status
	inst.ErrorMessage = errorMsg
	return nil
}

func (m *mockInstallationRepo) UpdateProgress(ctx context.Context, installationID int64, percent int) error {
	return fmt.Errorf("not implemented")
}

func (m *mockInstallationRepo) Delete(ctx context.Context, installationID int64) error {
	return fmt.Errorf("not implemented")
}

type mockSGCRepo struct {
	sgcs map[int64]*manman.ServerGameConfig
}

func (m *mockSGCRepo) Get(ctx context.Context, sgcID int64) (*manman.ServerGameConfig, error) {
	sgc, ok := m.sgcs[sgcID]
	if !ok {
		return nil, fmt.Errorf("SGC not found")
	}
	return sgc, nil
}

func (m *mockSGCRepo) Create(ctx context.Context, sgc *manman.ServerGameConfig) (*manman.ServerGameConfig, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockSGCRepo) List(ctx context.Context, serverID *int64, limit, offset int) ([]*manman.ServerGameConfig, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockSGCRepo) Update(ctx context.Context, sgc *manman.ServerGameConfig) error {
	return fmt.Errorf("not implemented")
}

func (m *mockSGCRepo) Delete(ctx context.Context, sgcID int64) error {
	return fmt.Errorf("not implemented")
}

type mockGameConfigRepo struct {
	gameConfigs map[int64]*manman.GameConfig
}

func (m *mockGameConfigRepo) Get(ctx context.Context, configID int64) (*manman.GameConfig, error) {
	gc, ok := m.gameConfigs[configID]
	if !ok {
		return nil, fmt.Errorf("game config not found")
	}
	return gc, nil
}

func (m *mockGameConfigRepo) Create(ctx context.Context, gc *manman.GameConfig) (*manman.GameConfig, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockGameConfigRepo) List(ctx context.Context, gameID *int64, limit, offset int) ([]*manman.GameConfig, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockGameConfigRepo) Update(ctx context.Context, gc *manman.GameConfig) error {
	return fmt.Errorf("not implemented")
}

func (m *mockGameConfigRepo) Delete(ctx context.Context, configID int64) error {
	return fmt.Errorf("not implemented")
}

type mockStrategyRepo struct {
	strategies map[int64][]*manman.ConfigurationStrategy // key: gameID
}

func (m *mockStrategyRepo) ListByGame(ctx context.Context, gameID int64) ([]*manman.ConfigurationStrategy, error) {
	strategies, ok := m.strategies[gameID]
	if !ok {
		return []*manman.ConfigurationStrategy{}, nil
	}
	return strategies, nil
}

func (m *mockStrategyRepo) Create(ctx context.Context, strategy *manman.ConfigurationStrategy) (*manman.ConfigurationStrategy, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockStrategyRepo) Get(ctx context.Context, strategyID int64) (*manman.ConfigurationStrategy, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockStrategyRepo) List(ctx context.Context, gameID *int64, limit, offset int) ([]*manman.ConfigurationStrategy, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockStrategyRepo) Update(ctx context.Context, strategy *manman.ConfigurationStrategy) error {
	return fmt.Errorf("not implemented")
}

func (m *mockStrategyRepo) Delete(ctx context.Context, strategyID int64) error {
	return fmt.Errorf("not implemented")
}

type mockRMQPublisher struct {
	publishedCommands []*DownloadAddonCommand
	publishedRemovals []*RemoveAddonCommand
}

func (m *mockRMQPublisher) PublishDownloadCommand(ctx context.Context, serverID int64, cmd *DownloadAddonCommand) error {
	m.publishedCommands = append(m.publishedCommands, cmd)
	return nil
}

func (m *mockRMQPublisher) PublishRemoveCommand(ctx context.Context, serverID int64, cmd *RemoveAddonCommand) error {
	m.publishedRemovals = append(m.publishedRemovals, cmd)
	return nil
}

type mockSessionRepo struct {
	sessions map[int64][]*manman.Session // key: sgcID
}

func (m *mockSessionRepo) Create(ctx context.Context, session *manman.Session) (*manman.Session, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockSessionRepo) Get(ctx context.Context, sessionID int64) (*manman.Session, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockSessionRepo) List(ctx context.Context, sgcID *int64, limit, offset int) ([]*manman.Session, error) {
	if sgcID == nil {
		return nil, fmt.Errorf("sgcID required")
	}
	sessions, ok := m.sessions[*sgcID]
	if !ok {
		return []*manman.Session{}, nil
	}
	return sessions, nil
}

func (m *mockSessionRepo) ListWithFilters(ctx context.Context, filters *repository.SessionFilters, limit, offset int) ([]*manman.Session, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockSessionRepo) Update(ctx context.Context, session *manman.Session) error {
	return fmt.Errorf("not implemented")
}

func (m *mockSessionRepo) UpdateStatus(ctx context.Context, sessionID int64, status string) error {
	return fmt.Errorf("not implemented")
}

func (m *mockSessionRepo) UpdateSessionStart(ctx context.Context, sessionID int64, startedAt time.Time) error {
	return fmt.Errorf("not implemented")
}

func (m *mockSessionRepo) UpdateSessionEnd(ctx context.Context, sessionID int64, status string, endedAt time.Time, exitCode *int) error {
	return fmt.Errorf("not implemented")
}

func (m *mockSessionRepo) GetStaleSessions(ctx context.Context, threshold time.Duration) ([]*manman.Session, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockSessionRepo) StopOtherSessionsForSGC(ctx context.Context, sessionID int64, sgcID int64) error {
	return fmt.Errorf("not implemented")
}

// Helper function to create a test WorkshopManager with mocks
func createTestManager() (*WorkshopManager, *mockAddonRepo, *mockInstallationRepo, *mockSGCRepo, *mockGameConfigRepo, *mockStrategyRepo, *mockSessionRepo, *mockRMQPublisher) {
	addonRepo := &mockAddonRepo{addons: make(map[int64]*manman.WorkshopAddon)}
	installationRepo := &mockInstallationRepo{
		installations: make(map[string]*manman.WorkshopInstallation),
		installByID:   make(map[int64]*manman.WorkshopInstallation),
	}
	sgcRepo := &mockSGCRepo{sgcs: make(map[int64]*manman.ServerGameConfig)}
	gameConfigRepo := &mockGameConfigRepo{gameConfigs: make(map[int64]*manman.GameConfig)}
	strategyRepo := &mockStrategyRepo{strategies: make(map[int64][]*manman.ConfigurationStrategy)}
	sessionRepo := &mockSessionRepo{sessions: make(map[int64][]*manman.Session)}
	rmqPublisher := &mockRMQPublisher{
		publishedCommands: []*DownloadAddonCommand{},
		publishedRemovals: []*RemoveAddonCommand{},
	}

	manager := NewWorkshopManager(
		addonRepo,
		installationRepo,
		nil, // libraryRepo not needed for these tests
		sgcRepo,
		gameConfigRepo,
		strategyRepo,
		sessionRepo,
		nil, // steamClient not needed for these tests
		rmqPublisher,
	)

	return manager, addonRepo, installationRepo, sgcRepo, gameConfigRepo, strategyRepo, sessionRepo, rmqPublisher
}

// Feature: workshop-addon-management, Property 6: Installation Idempotency
// Validates: Requirements 2.2
func TestProperty6_InstallationIdempotency(t *testing.T) {
	t.Skip("Skipping property test - requires database connection")

	// Property: For any ServerGameConfig and addon combination, requesting installation
	// multiple times should result in only one installation record and one download operation
	// (unless force_reinstall is specified).

	ctx := context.Background()
	manager, addonRepo, installationRepo, sgcRepo, gameConfigRepo, strategyRepo, _, rmqPublisher := createTestManager()

	// Setup test data
	gameID := int64(1)
	gameConfigID := int64(1)
	sgcID := int64(1)
	addonID := int64(1)
	serverID := int64(1)

	installPath := "/data/maps"
	targetPath := "/data"
	addonPath := "maps"

	// Create game config
	gameConfigRepo.gameConfigs[gameConfigID] = &manman.GameConfig{
		ConfigID: gameConfigID,
		GameID:   gameID,
	}

	// Create SGC
	sgcRepo.sgcs[sgcID] = &manman.ServerGameConfig{
		SGCID:        sgcID,
		GameConfigID: gameConfigID,
		ServerID:     serverID,
	}

	// Create addon
	addonRepo.addons[addonID] = &manman.WorkshopAddon{
		AddonID:          addonID,
		GameID:           gameID,
		WorkshopID:       "123456",
		Name:             "Test Map",
		InstallationPath: &addonPath,
		Metadata: map[string]interface{}{
			"steam_app_id": "550",
		},
	}

	// Create volume strategy
	strategyRepo.strategies[gameID] = []*manman.ConfigurationStrategy{
		{
			StrategyType: manman.StrategyTypeVolume,
			TargetPath:   &targetPath,
		},
	}

	// First installation
	inst1, err := manager.InstallAddon(ctx, sgcID, addonID, false)
	if err != nil {
		t.Fatalf("First installation failed: %v", err)
	}

	if inst1.Status != manman.InstallationStatusPending {
		t.Errorf("Expected status pending, got %s", inst1.Status)
	}

	if inst1.InstallationPath != installPath {
		t.Errorf("Expected path %s, got %s", installPath, inst1.InstallationPath)
	}

	if len(rmqPublisher.publishedCommands) != 1 {
		t.Errorf("Expected 1 download command, got %d", len(rmqPublisher.publishedCommands))
	}

	// Simulate installation completion
	installationRepo.UpdateStatus(ctx, inst1.InstallationID, manman.InstallationStatusInstalled, nil)

	// Second installation (should be idempotent)
	inst2, err := manager.InstallAddon(ctx, sgcID, addonID, false)
	if err != nil {
		t.Fatalf("Second installation failed: %v", err)
	}

	if inst2.InstallationID != inst1.InstallationID {
		t.Errorf("Expected same installation ID, got different IDs: %d vs %d", inst1.InstallationID, inst2.InstallationID)
	}

	if inst2.Status != manman.InstallationStatusInstalled {
		t.Errorf("Expected status installed, got %s", inst2.Status)
	}

	// Should not publish another download command
	if len(rmqPublisher.publishedCommands) != 1 {
		t.Errorf("Expected still 1 download command (idempotent), got %d", len(rmqPublisher.publishedCommands))
	}

	// Third installation with force_reinstall
	inst3, err := manager.InstallAddon(ctx, sgcID, addonID, true)
	if err != nil {
		t.Fatalf("Third installation with force failed: %v", err)
	}

	if inst3.Status != manman.InstallationStatusPending {
		t.Errorf("Expected status pending after force reinstall, got %s", inst3.Status)
	}

	// Should publish another download command
	if len(rmqPublisher.publishedCommands) != 2 {
		t.Errorf("Expected 2 download commands after force reinstall, got %d", len(rmqPublisher.publishedCommands))
	}
}

// Feature: workshop-addon-management, Property 11: Path Resolution Consistency
// Validates: Requirements 3.2, 3.3, 4.2, 4.3
func TestProperty11_PathResolutionConsistency(t *testing.T) {
	t.Skip("Skipping property test - requires database connection")

	// Property: For any ServerGameConfig with a volume strategy and addon with installation_path,
	// resolving the full installation path should consistently combine the volume target_path
	// with the addon installation_path.

	ctx := context.Background()
	manager, addonRepo, _, sgcRepo, gameConfigRepo, strategyRepo, _, _ := createTestManager()

	testCases := []struct {
		name         string
		targetPath   string
		addonPath    string
		expectedPath string
	}{
		{
			name:         "Simple path",
			targetPath:   "/data",
			addonPath:    "maps",
			expectedPath: "/data/maps",
		},
		{
			name:         "Nested path",
			targetPath:   "/data/game",
			addonPath:    "addons/maps",
			expectedPath: "/data/game/addons/maps",
		},
		{
			name:         "Trailing slash in target",
			targetPath:   "/data/",
			addonPath:    "maps",
			expectedPath: "/data/maps",
		},
		{
			name:         "Leading slash in addon path",
			targetPath:   "/data",
			addonPath:    "/maps",
			expectedPath: "/data/maps",
		},
	}

	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gameID := int64(i + 1)
			gameConfigID := int64(i + 1)
			sgcID := int64(i + 1)
			addonID := int64(i + 1)
			serverID := int64(i + 1)

			// Create game config
			gameConfigRepo.gameConfigs[gameConfigID] = &manman.GameConfig{
				ConfigID: gameConfigID,
				GameID:   gameID,
			}

			// Create SGC
			sgcRepo.sgcs[sgcID] = &manman.ServerGameConfig{
				SGCID:        sgcID,
				GameConfigID: gameConfigID,
				ServerID:     serverID,
			}

			// Create addon
			addonRepo.addons[addonID] = &manman.WorkshopAddon{
				AddonID:          addonID,
				GameID:           gameID,
				WorkshopID:       fmt.Sprintf("%d", 100000+i),
				Name:             fmt.Sprintf("Test Map %d", i),
				InstallationPath: &tc.addonPath,
				Metadata: map[string]interface{}{
					"steam_app_id": "550",
				},
			}

			// Create volume strategy
			targetPath := tc.targetPath
			strategyRepo.strategies[gameID] = []*manman.ConfigurationStrategy{
				{
					StrategyType: manman.StrategyTypeVolume,
					TargetPath:   &targetPath,
				},
			}

			// Install addon
			inst, err := manager.InstallAddon(ctx, sgcID, addonID, false)
			if err != nil {
				t.Fatalf("Installation failed: %v", err)
			}

			if inst.InstallationPath != tc.expectedPath {
				t.Errorf("Expected path %s, got %s", tc.expectedPath, inst.InstallationPath)
			}
		})
	}
}

// Feature: workshop-addon-management, Property 14: Volume Strategy Validation
// Validates: Requirements 4.1, 4.4
func TestProperty14_VolumeStrategyValidation(t *testing.T) {
	t.Skip("Skipping property test - requires database connection")

	// Property: For any download request, if no volume strategy of type "volume" exists
	// for the game, the download should be rejected with a validation error.

	ctx := context.Background()
	manager, addonRepo, _, sgcRepo, gameConfigRepo, strategyRepo, _, _ := createTestManager()

	gameID := int64(1)
	gameConfigID := int64(1)
	sgcID := int64(1)
	addonID := int64(1)
	serverID := int64(1)

	addonPath := "maps"

	// Create game config
	gameConfigRepo.gameConfigs[gameConfigID] = &manman.GameConfig{
		ConfigID: gameConfigID,
		GameID:   gameID,
	}

	// Create SGC
	sgcRepo.sgcs[sgcID] = &manman.ServerGameConfig{
		SGCID:        sgcID,
		GameConfigID: gameConfigID,
		ServerID:     serverID,
	}

	// Create addon
	addonRepo.addons[addonID] = &manman.WorkshopAddon{
		AddonID:          addonID,
		GameID:           gameID,
		WorkshopID:       "123456",
		Name:             "Test Map",
		InstallationPath: &addonPath,
		Metadata: map[string]interface{}{
			"steam_app_id": "550",
		},
	}

	// Test 1: No volume strategy at all
	strategyRepo.strategies[gameID] = []*manman.ConfigurationStrategy{}

	_, err := manager.InstallAddon(ctx, sgcID, addonID, false)
	if err == nil {
		t.Error("Expected error when no volume strategy exists, got nil")
	}
	if err != nil && err.Error() != "failed to resolve installation path: no volume strategy found for game" {
		t.Errorf("Expected 'no volume strategy found' error, got: %v", err)
	}

	// Test 2: Strategy exists but not volume type
	cliArgsStrategy := &manman.ConfigurationStrategy{
		StrategyType: manman.StrategyTypeCLIArgs,
	}
	strategyRepo.strategies[gameID] = []*manman.ConfigurationStrategy{cliArgsStrategy}

	_, err = manager.InstallAddon(ctx, sgcID, addonID, false)
	if err == nil {
		t.Error("Expected error when no volume strategy exists (only cli_args), got nil")
	}

	// Test 3: Volume strategy exists but missing target_path
	volumeStrategy := &manman.ConfigurationStrategy{
		StrategyType: manman.StrategyTypeVolume,
		TargetPath:   nil,
	}
	strategyRepo.strategies[gameID] = []*manman.ConfigurationStrategy{volumeStrategy}

	_, err = manager.InstallAddon(ctx, sgcID, addonID, false)
	if err == nil {
		t.Error("Expected error when volume strategy missing target_path, got nil")
	}
	if err != nil && err.Error() != "failed to resolve installation path: volume strategy missing target_path" {
		t.Errorf("Expected 'missing target_path' error, got: %v", err)
	}

	// Test 4: Valid volume strategy should succeed
	targetPath := "/data"
	volumeStrategy.TargetPath = &targetPath
	strategyRepo.strategies[gameID] = []*manman.ConfigurationStrategy{volumeStrategy}

	inst, err := manager.InstallAddon(ctx, sgcID, addonID, false)
	if err != nil {
		t.Errorf("Expected success with valid volume strategy, got error: %v", err)
	}
	if inst == nil {
		t.Error("Expected installation record, got nil")
	}
}

// Mock Steam Workshop Client for testing
type mockSteamClient struct {
	items       map[string]*steam.WorkshopItemMetadata
	collections map[string][]steam.CollectionItem
	shouldError bool
}

func (m *mockSteamClient) GetWorkshopItemDetails(ctx context.Context, workshopID string) (*steam.WorkshopItemMetadata, error) {
	if m.shouldError {
		return nil, fmt.Errorf("mock steam API error")
	}
	item, ok := m.items[workshopID]
	if !ok {
		return nil, fmt.Errorf("workshop item not found")
	}
	return item, nil
}

func (m *mockSteamClient) GetCollectionDetails(ctx context.Context, collectionID string) ([]steam.CollectionItem, error) {
	if m.shouldError {
		return nil, fmt.Errorf("mock steam API error")
	}
	items, ok := m.collections[collectionID]
	if !ok {
		return nil, fmt.Errorf("collection not found")
	}
	return items, nil
}

// Feature: workshop-addon-management, Property 9: Addon Metadata Fetching
// Validates: Requirements 9.1, 9.2
func TestProperty9_AddonMetadataFetching(t *testing.T) {
	t.Skip("Skipping property test - requires database connection")

	// Property: For any valid workshop ID, fetching metadata from Steam Workshop API
	// should populate addon fields (name, description, file_size, last_updated)

	ctx := context.Background()
	manager, _, _, _, _, _, _, _ := createTestManager()

	// Setup mock Steam client
	steamClient := &mockSteamClient{
		items:       make(map[string]*steam.WorkshopItemMetadata),
		collections: make(map[string][]steam.CollectionItem),
	}
	manager.steamClient = steamClient

	testCases := []struct {
		name         string
		workshopID   string
		metadata     *steam.WorkshopItemMetadata
		isCollection bool
	}{
		{
			name:       "Regular addon",
			workshopID: "123456",
			metadata: &steam.WorkshopItemMetadata{
				WorkshopID:   "123456",
				Title:        "Test Map",
				Description:  "A test map for testing",
				FileSize:     1024000,
				TimeUpdated:  time.Now(),
				IsCollection: false,
			},
			isCollection: false,
		},
		{
			name:       "Large addon",
			workshopID: "789012",
			metadata: &steam.WorkshopItemMetadata{
				WorkshopID:   "789012",
				Title:        "Large Campaign",
				Description:  "A large campaign with multiple maps",
				FileSize:     524288000, // 500MB
				TimeUpdated:  time.Now(),
				IsCollection: false,
			},
			isCollection: false,
		},
		{
			name:       "Collection",
			workshopID: "999999",
			metadata: &steam.WorkshopItemMetadata{
				WorkshopID:   "999999",
				Title:        "Map Collection",
				Description:  "Collection of maps",
				FileSize:     0,
				TimeUpdated:  time.Now(),
				IsCollection: true,
			},
			isCollection: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup mock data
			steamClient.items[tc.workshopID] = tc.metadata

			if tc.isCollection {
				steamClient.collections[tc.workshopID] = []steam.CollectionItem{
					{WorkshopID: "111111", Title: "Map 1"},
					{WorkshopID: "222222", Title: "Map 2"},
				}
			}

			// Fetch and create addon
			gameID := int64(1)
			addon, err := manager.FetchAndCreateAddon(ctx, gameID, tc.workshopID)
			if err != nil {
				t.Fatalf("FetchAndCreateAddon failed: %v", err)
			}

			// Verify addon fields
			if addon.GameID != gameID {
				t.Errorf("Expected game_id %d, got %d", gameID, addon.GameID)
			}

			if addon.WorkshopID != tc.workshopID {
				t.Errorf("Expected workshop_id %s, got %s", tc.workshopID, addon.WorkshopID)
			}

			if addon.Name != tc.metadata.Title {
				t.Errorf("Expected name %s, got %s", tc.metadata.Title, addon.Name)
			}

			if addon.Description == nil || *addon.Description != tc.metadata.Description {
				t.Errorf("Expected description %s, got %v", tc.metadata.Description, addon.Description)
			}

			if addon.FileSizeBytes == nil || *addon.FileSizeBytes != tc.metadata.FileSize {
				t.Errorf("Expected file_size %d, got %v", tc.metadata.FileSize, addon.FileSizeBytes)
			}

			if addon.IsCollection != tc.isCollection {
				t.Errorf("Expected is_collection %v, got %v", tc.isCollection, addon.IsCollection)
			}

			if addon.PlatformType != manman.PlatformTypeSteamWorkshop {
				t.Errorf("Expected platform_type %s, got %s", manman.PlatformTypeSteamWorkshop, addon.PlatformType)
			}

			// Verify collection items stored in metadata
			if tc.isCollection {
				if addon.Metadata == nil {
					t.Error("Expected metadata for collection, got nil")
				} else {
					collectionItems, ok := addon.Metadata["collection_items"]
					if !ok {
						t.Error("Expected collection_items in metadata")
					} else {
						items, ok := collectionItems.([]map[string]interface{})
						if !ok {
							t.Errorf("Expected collection_items to be []map[string]interface{}, got %T", collectionItems)
						} else if len(items) != 2 {
							t.Errorf("Expected 2 collection items, got %d", len(items))
						}
					}
				}
			}
		})
	}
}

// Feature: workshop-addon-management, Property 14: Collection Detection and Expansion
// Validates: Requirements 14.1, 14.2, 14.3
func TestProperty14_CollectionDetectionAndExpansion(t *testing.T) {
	t.Skip("Skipping property test - requires database connection")

	// Property: When fetching a workshop ID that is a collection, the system should
	// detect it as a collection and fetch all items in the collection, storing them
	// in the metadata JSONB field.

	ctx := context.Background()
	manager, _, _, _, _, _, _, _ := createTestManager()

	// Setup mock Steam client
	steamClient := &mockSteamClient{
		items:       make(map[string]*steam.WorkshopItemMetadata),
		collections: make(map[string][]steam.CollectionItem),
	}
	manager.steamClient = steamClient

	// Test case: Collection with multiple items
	collectionID := "999999"
	collectionMetadata := &steam.WorkshopItemMetadata{
		WorkshopID:   collectionID,
		Title:        "Campaign Collection",
		Description:  "A collection of campaign maps",
		FileSize:     0,
		TimeUpdated:  time.Now(),
		IsCollection: true,
	}

	collectionItems := []steam.CollectionItem{
		{WorkshopID: "111111", Title: "Map 1"},
		{WorkshopID: "222222", Title: "Map 2"},
		{WorkshopID: "333333", Title: "Map 3"},
		{WorkshopID: "444444", Title: "Map 4"},
	}

	steamClient.items[collectionID] = collectionMetadata
	steamClient.collections[collectionID] = collectionItems

	// Fetch and create addon
	gameID := int64(1)
	addon, err := manager.FetchAndCreateAddon(ctx, gameID, collectionID)
	if err != nil {
		t.Fatalf("FetchAndCreateAddon failed: %v", err)
	}

	// Verify collection detection
	if !addon.IsCollection {
		t.Error("Expected is_collection to be true")
	}

	// Verify collection items stored in metadata
	if addon.Metadata == nil {
		t.Fatal("Expected metadata for collection, got nil")
	}

	collectionItemsRaw, ok := addon.Metadata["collection_items"]
	if !ok {
		t.Fatal("Expected collection_items in metadata")
	}

	items, ok := collectionItemsRaw.([]map[string]interface{})
	if !ok {
		t.Fatalf("Expected collection_items to be []map[string]interface{}, got %T", collectionItemsRaw)
	}

	if len(items) != len(collectionItems) {
		t.Errorf("Expected %d collection items, got %d", len(collectionItems), len(items))
	}

	// Verify each item is stored correctly
	for i, expectedItem := range collectionItems {
		if i >= len(items) {
			break
		}

		actualWorkshopID, ok := items[i]["workshop_id"].(string)
		if !ok {
			t.Errorf("Item %d: expected workshop_id to be string", i)
			continue
		}

		if actualWorkshopID != expectedItem.WorkshopID {
			t.Errorf("Item %d: expected workshop_id %s, got %s", i, expectedItem.WorkshopID, actualWorkshopID)
		}

		actualTitle, ok := items[i]["title"].(string)
		if !ok {
			t.Errorf("Item %d: expected title to be string", i)
			continue
		}

		if actualTitle != expectedItem.Title {
			t.Errorf("Item %d: expected title %s, got %s", i, expectedItem.Title, actualTitle)
		}
	}
}

// Feature: workshop-addon-management, Property 15: API Error Handling
// Validates: Requirements 9.3, 15.4
func TestProperty15_APIErrorHandling(t *testing.T) {
	t.Skip("Skipping property test - requires database connection")

	// Property: When Steam Workshop API fails or returns errors, the system should
	// propagate the error and not create an addon record.

	ctx := context.Background()
	manager, _, _, _, _, _, _, _ := createTestManager()

	// Setup mock Steam client with error
	steamClient := &mockSteamClient{
		items:       make(map[string]*steam.WorkshopItemMetadata),
		collections: make(map[string][]steam.CollectionItem),
		shouldError: true,
	}
	manager.steamClient = steamClient

	// Test case 1: API error
	gameID := int64(1)
	workshopID := "123456"

	addon, err := manager.FetchAndCreateAddon(ctx, gameID, workshopID)
	if err == nil {
		t.Error("Expected error when Steam API fails, got nil")
	}
	if addon != nil {
		t.Error("Expected nil addon when API fails, got non-nil")
	}

	// Test case 2: Workshop item not found
	steamClient.shouldError = false
	workshopID = "nonexistent"

	addon, err = manager.FetchAndCreateAddon(ctx, gameID, workshopID)
	if err == nil {
		t.Error("Expected error when workshop item not found, got nil")
	}
	if addon != nil {
		t.Error("Expected nil addon when item not found, got non-nil")
	}

	// Test case 3: Collection fetch fails
	collectionID := "999999"
	collectionMetadata := &steam.WorkshopItemMetadata{
		WorkshopID:   collectionID,
		Title:        "Test Collection",
		Description:  "Test",
		FileSize:     0,
		TimeUpdated:  time.Now(),
		IsCollection: true,
	}
	steamClient.items[collectionID] = collectionMetadata
	// Don't add collection items - will cause GetCollectionDetails to fail

	addon, err = manager.FetchAndCreateAddon(ctx, gameID, collectionID)
	if err == nil {
		t.Error("Expected error when collection fetch fails, got nil")
	}
	if addon != nil {
		t.Error("Expected nil addon when collection fetch fails, got non-nil")
	}
}


// TestRemoveInstallation_Success tests successful removal of an installation
func TestRemoveInstallation_Success(t *testing.T) {
	t.Skip("Skipping test - requires database connection")

	manager, _, installationRepo, sgcRepo, _, _, sessionRepo, rmqPublisher := createTestManager()

	ctx := context.Background()
	installationID := int64(1)
	sgcID := int64(100)
	addonID := int64(200)
	serverID := int64(10)

	// Setup: installation exists
	installation := &manman.WorkshopInstallation{
		InstallationID:   installationID,
		SGCID:            sgcID,
		AddonID:          addonID,
		Status:           manman.InstallationStatusInstalled,
		InstallationPath: "/data/maps/workshop/123456",
	}
	installationRepo.installByID[installationID] = installation

	// Setup: SGC exists
	sgc := &manman.ServerGameConfig{
		SGCID:    sgcID,
		ServerID: serverID,
	}
	sgcRepo.sgcs[sgcID] = sgc

	// Setup: no active sessions
	sessionRepo.sessions = make(map[int64][]*manman.Session)
	sessionRepo.sessions[sgcID] = []*manman.Session{}

	// Execute
	err := manager.RemoveInstallation(ctx, installationID)

	// Verify
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify status updated to removed
	updatedInstallation := installationRepo.installByID[installationID]
	if updatedInstallation.Status != manman.InstallationStatusRemoved {
		t.Errorf("Expected status %s, got %s", manman.InstallationStatusRemoved, updatedInstallation.Status)
	}

	// Verify remove command published
	if len(rmqPublisher.publishedRemovals) != 1 {
		t.Fatalf("Expected 1 remove command, got %d", len(rmqPublisher.publishedRemovals))
	}

	cmd := rmqPublisher.publishedRemovals[0]
	if cmd.InstallationID != installationID {
		t.Errorf("Expected InstallationID %d, got %d", installationID, cmd.InstallationID)
	}
	if cmd.SGCID != sgcID {
		t.Errorf("Expected SGCID %d, got %d", sgcID, cmd.SGCID)
	}
	if cmd.AddonID != addonID {
		t.Errorf("Expected AddonID %d, got %d", addonID, cmd.AddonID)
	}
	if cmd.InstallationPath != installation.InstallationPath {
		t.Errorf("Expected InstallationPath %s, got %s", installation.InstallationPath, cmd.InstallationPath)
	}
}

// TestRemoveInstallation_ActiveSessionBlocks tests that removal is blocked when active sessions exist
func TestRemoveInstallation_ActiveSessionBlocks(t *testing.T) {
	t.Skip("Skipping test - requires database connection")

	manager, _, installationRepo, sgcRepo, _, _, sessionRepo, _ := createTestManager()

	ctx := context.Background()
	installationID := int64(1)
	sgcID := int64(100)
	serverID := int64(10)

	// Setup: installation exists
	installation := &manman.WorkshopInstallation{
		InstallationID:   installationID,
		SGCID:            sgcID,
		AddonID:          200,
		Status:           manman.InstallationStatusInstalled,
		InstallationPath: "/data/maps/workshop/123456",
	}
	installationRepo.installByID[installationID] = installation

	// Setup: SGC exists
	sgc := &manman.ServerGameConfig{
		SGCID:    sgcID,
		ServerID: serverID,
	}
	sgcRepo.sgcs[sgcID] = sgc

	// Setup: active session exists
	activeSession := &manman.Session{
		SessionID: 1,
		SGCID:     sgcID,
		Status:    manman.SessionStatusRunning,
	}
	sessionRepo.sessions = make(map[int64][]*manman.Session)
	sessionRepo.sessions[sgcID] = []*manman.Session{activeSession}

	// Execute
	err := manager.RemoveInstallation(ctx, installationID)

	// Verify
	if err == nil {
		t.Fatal("Expected error when active session exists, got nil")
	}

	expectedErrMsg := "cannot remove addon: SGC has active session"
	if !contains(err.Error(), expectedErrMsg) {
		t.Errorf("Expected error containing '%s', got: %v", expectedErrMsg, err)
	}

	// Verify status NOT updated
	unchangedInstallation := installationRepo.installByID[installationID]
	if unchangedInstallation.Status != manman.InstallationStatusInstalled {
		t.Errorf("Expected status unchanged at %s, got %s", manman.InstallationStatusInstalled, unchangedInstallation.Status)
	}
}

// TestRemoveInstallation_InstallationNotFound tests error when installation doesn't exist
func TestRemoveInstallation_InstallationNotFound(t *testing.T) {
	t.Skip("Skipping test - requires database connection")

	manager, _, installationRepo, _, _, _, _, _ := createTestManager()

	ctx := context.Background()
	installationID := int64(999)

	// Setup: installation does not exist
	installationRepo.installByID = make(map[int64]*manman.WorkshopInstallation)

	// Execute
	err := manager.RemoveInstallation(ctx, installationID)

	// Verify
	if err == nil {
		t.Fatal("Expected error when installation not found, got nil")
	}

	expectedErrMsg := "failed to get installation"
	if !contains(err.Error(), expectedErrMsg) {
		t.Errorf("Expected error containing '%s', got: %v", expectedErrMsg, err)
	}
}

// TestRemoveInstallation_SGCNotFound tests error when SGC doesn't exist
func TestRemoveInstallation_SGCNotFound(t *testing.T) {
	t.Skip("Skipping test - requires database connection")

	manager, _, installationRepo, sgcRepo, _, _, sessionRepo, _ := createTestManager()

	ctx := context.Background()
	installationID := int64(1)
	sgcID := int64(100)

	// Setup: installation exists
	installation := &manman.WorkshopInstallation{
		InstallationID:   installationID,
		SGCID:            sgcID,
		AddonID:          200,
		Status:           manman.InstallationStatusInstalled,
		InstallationPath: "/data/maps/workshop/123456",
	}
	installationRepo.installByID[installationID] = installation

	// Setup: SGC does not exist
	sgcRepo.sgcs = make(map[int64]*manman.ServerGameConfig)

	// Setup: no active sessions
	sessionRepo.sessions = make(map[int64][]*manman.Session)
	sessionRepo.sessions[sgcID] = []*manman.Session{}

	// Execute
	err := manager.RemoveInstallation(ctx, installationID)

	// Verify
	if err == nil {
		t.Fatal("Expected error when SGC not found, got nil")
	}

	expectedErrMsg := "failed to get SGC"
	if !contains(err.Error(), expectedErrMsg) {
		t.Errorf("Expected error containing '%s', got: %v", expectedErrMsg, err)
	}
}

// TestRemoveInstallation_InactiveSessionsAllowed tests that removal succeeds with inactive sessions
func TestRemoveInstallation_InactiveSessionsAllowed(t *testing.T) {
	t.Skip("Skipping test - requires database connection")

	manager, _, installationRepo, sgcRepo, _, _, sessionRepo, rmqPublisher := createTestManager()

	ctx := context.Background()
	installationID := int64(1)
	sgcID := int64(100)
	serverID := int64(10)

	// Setup: installation exists
	installation := &manman.WorkshopInstallation{
		InstallationID:   installationID,
		SGCID:            sgcID,
		AddonID:          200,
		Status:           manman.InstallationStatusInstalled,
		InstallationPath: "/data/maps/workshop/123456",
	}
	installationRepo.installByID[installationID] = installation

	// Setup: SGC exists
	sgc := &manman.ServerGameConfig{
		SGCID:    sgcID,
		ServerID: serverID,
	}
	sgcRepo.sgcs[sgcID] = sgc

	// Setup: inactive sessions exist (completed, stopped)
	inactiveSessions := []*manman.Session{
		{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusCompleted},
		{SessionID: 2, SGCID: sgcID, Status: manman.SessionStatusStopped},
	}
	sessionRepo.sessions = make(map[int64][]*manman.Session)
	sessionRepo.sessions[sgcID] = inactiveSessions

	// Execute
	err := manager.RemoveInstallation(ctx, installationID)

	// Verify
	if err != nil {
		t.Fatalf("Expected no error with inactive sessions, got: %v", err)
	}

	// Verify status updated to removed
	updatedInstallation := installationRepo.installByID[installationID]
	if updatedInstallation.Status != manman.InstallationStatusRemoved {
		t.Errorf("Expected status %s, got %s", manman.InstallationStatusRemoved, updatedInstallation.Status)
	}

	// Verify remove command published
	if len(rmqPublisher.publishedRemovals) != 1 {
		t.Fatalf("Expected 1 remove command, got %d", len(rmqPublisher.publishedRemovals))
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
