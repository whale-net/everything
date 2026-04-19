package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/manmanv2/api/repository"
)

// NewRepository creates a new repository from an existing connection pool.
func NewRepository(pool *pgxpool.Pool) *repository.Repository {
	return &repository.Repository{
		Servers:                 NewServerRepository(pool),
		Games:                   NewGameRepository(pool),
		GameConfigs:             NewGameConfigRepository(pool),
		ServerGameConfigs:       NewServerGameConfigRepository(pool),
		Sessions:                NewSessionRepository(pool),
		ServerCapabilities:      NewServerCapabilityRepository(pool),
		LogReferences:           NewLogReferenceRepository(pool),
		Backups:                 NewBackupRepository(pool),
		BackupConfigs:           NewBackupConfigRepository(pool),
		ServerPorts:             NewServerPortRepository(pool),
		ConfigurationStrategies: NewConfigurationStrategyRepository(pool),
		ConfigurationPatches:    NewConfigurationPatchRepository(pool),
		GameConfigVolumes:       NewGameConfigVolumeRepository(pool),
		WorkshopAddons:          NewWorkshopAddonRepository(pool),
		WorkshopInstallations:   NewWorkshopInstallationRepository(pool),
		WorkshopLibraries:       NewWorkshopLibraryRepository(pool),
		AddonPathPresets:        NewAddonPathPresetRepository(pool),
		RestartSchedules:        NewRestartScheduleRepository(pool),
		Actions:                 NewActionRepository(pool),
	}
}
