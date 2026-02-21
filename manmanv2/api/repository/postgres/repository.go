package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2/api/repository"
)

// NewRepository creates a new repository with PostgreSQL implementations
func NewRepository(ctx context.Context, connString string) (*repository.Repository, error) {
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Test connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &repository.Repository{
		Servers:                 NewServerRepository(pool),
		Games:                   NewGameRepository(pool),
		GameConfigs:             NewGameConfigRepository(pool),
		ServerGameConfigs:       NewServerGameConfigRepository(pool),
		Sessions:                NewSessionRepository(pool),
		ServerCapabilities:      NewServerCapabilityRepository(pool),
		LogReferences:           NewLogReferenceRepository(pool),
		Backups:                 NewBackupRepository(pool),
		ServerPorts:             NewServerPortRepository(pool),
		ConfigurationStrategies: NewConfigurationStrategyRepository(pool),
		ConfigurationPatches:    NewConfigurationPatchRepository(pool),
		GameConfigVolumes:       NewGameConfigVolumeRepository(pool),
		WorkshopAddons:          NewWorkshopAddonRepository(pool),
		WorkshopInstallations:   NewWorkshopInstallationRepository(pool),
		WorkshopLibraries:       NewWorkshopLibraryRepository(pool),
		AddonPathPresets:        NewAddonPathPresetRepository(pool),
		Actions:                 NewActionRepository(pool),
	}, nil
}
