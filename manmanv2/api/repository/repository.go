package repository

import (
	"context"
	"time"

	"github.com/whale-net/everything/manmanv2"
)

// ServerRepository defines operations for Server entities
type ServerRepository interface {
	Create(ctx context.Context, name string) (*manman.Server, error)
	Get(ctx context.Context, serverID int64) (*manman.Server, error)
	GetByName(ctx context.Context, name string) (*manman.Server, error)
	List(ctx context.Context, limit, offset int) ([]*manman.Server, error)
	Update(ctx context.Context, server *manman.Server) error
	Delete(ctx context.Context, serverID int64) error
	UpdateStatusAndLastSeen(ctx context.Context, serverID int64, status string, lastSeen time.Time) error
	UpdateLastSeen(ctx context.Context, serverID int64, lastSeen time.Time) error
	ListStaleServers(ctx context.Context, thresholdSeconds int) ([]*manman.Server, error)
	MarkServersOffline(ctx context.Context, serverIDs []int64) error
}

// GameRepository defines operations for Game entities
type GameRepository interface {
	Create(ctx context.Context, game *manman.Game) (*manman.Game, error)
	Get(ctx context.Context, gameID int64) (*manman.Game, error)
	List(ctx context.Context, limit, offset int) ([]*manman.Game, error)
	Update(ctx context.Context, game *manman.Game) error
	Delete(ctx context.Context, gameID int64) error
}

// GameConfigRepository defines operations for GameConfig entities
type GameConfigRepository interface {
	Create(ctx context.Context, config *manman.GameConfig) (*manman.GameConfig, error)
	Get(ctx context.Context, configID int64) (*manman.GameConfig, error)
	List(ctx context.Context, gameID *int64, limit, offset int) ([]*manman.GameConfig, error)
	Update(ctx context.Context, config *manman.GameConfig) error
	Delete(ctx context.Context, configID int64) error
}

// ServerGameConfigRepository defines operations for ServerGameConfig entities
type ServerGameConfigRepository interface {
	Create(ctx context.Context, sgc *manman.ServerGameConfig) (*manman.ServerGameConfig, error)
	Get(ctx context.Context, sgcID int64) (*manman.ServerGameConfig, error)
	List(ctx context.Context, serverID *int64, limit, offset int) ([]*manman.ServerGameConfig, error)
	Update(ctx context.Context, sgc *manman.ServerGameConfig) error
	Delete(ctx context.Context, sgcID int64) error
}

// SessionFilters defines filters for session queries
type SessionFilters struct {
	SGCID         *int64
	ServerID      *int64
	StatusFilter  []string
	StartedAfter  *time.Time
	StartedBefore *time.Time
	LiveOnly      bool
}

// SessionRepository defines operations for Session entities
type SessionRepository interface {
	Create(ctx context.Context, session *manman.Session) (*manman.Session, error)
	Get(ctx context.Context, sessionID int64) (*manman.Session, error)
	List(ctx context.Context, sgcID *int64, limit, offset int) ([]*manman.Session, error)
	ListWithFilters(ctx context.Context, filters *SessionFilters, limit, offset int) ([]*manman.Session, error)
	Update(ctx context.Context, session *manman.Session) error
	UpdateStatus(ctx context.Context, sessionID int64, status string) error
	UpdateSessionStart(ctx context.Context, sessionID int64, startedAt time.Time) error
	UpdateSessionEnd(ctx context.Context, sessionID int64, status string, endedAt time.Time, exitCode *int) error
	GetStaleSessions(ctx context.Context, threshold time.Duration) ([]*manman.Session, error)
	StopOtherSessionsForSGC(ctx context.Context, sessionID int64, sgcID int64) error
}

// ServerCapabilityRepository defines operations for ServerCapability entities
type ServerCapabilityRepository interface {
	Insert(ctx context.Context, cap *manman.ServerCapability) error
	Get(ctx context.Context, serverID int64) (*manman.ServerCapability, error)
}

// LogReferenceRepository defines operations for LogReference entities
type LogReferenceRepository interface {
	Create(ctx context.Context, logRef *manman.LogReference) error
	ListBySession(ctx context.Context, sessionID int64) ([]*manman.LogReference, error)
	ListBySessionAndTimeRange(ctx context.Context, sessionID int64, startTime, endTime time.Time) ([]*manman.LogReference, error)
	GetByMinute(ctx context.Context, sgcID int64, minuteTimestamp time.Time) (*manman.LogReference, error)
	UpdateState(ctx context.Context, logID int64, state string) error
	ListByTimeRange(ctx context.Context, sgcID int64, startTime, endTime time.Time) ([]*manman.LogReference, error)
	GetMinMaxTimes(ctx context.Context, sgcID int64) (minTime, maxTime *time.Time, err error)
	GetMinMaxTimesBySession(ctx context.Context, sessionID int64) (minTime, maxTime *time.Time, err error)
}

// BackupRepository defines operations for Backup entities
type BackupRepository interface {
	Create(ctx context.Context, backup *manman.Backup) (*manman.Backup, error)
	Get(ctx context.Context, backupID int64) (*manman.Backup, error)
	List(ctx context.Context, sgcID *int64, sessionID *int64, limit int, offset int) ([]*manman.Backup, error)
	Delete(ctx context.Context, backupID int64) error
}

// ServerPortRepository defines operations for port allocation management
type ServerPortRepository interface {
	AllocatePort(ctx context.Context, serverID int64, port int, protocol string, sessionID int64) error
	DeallocatePort(ctx context.Context, serverID int64, port int, protocol string) error
	IsPortAvailable(ctx context.Context, serverID int64, port int, protocol string) (bool, error)
	GetPortAllocation(ctx context.Context, serverID int64, port int, protocol string) (*manman.ServerPort, error)
	ListAllocatedPorts(ctx context.Context, serverID int64) ([]*manman.ServerPort, error)
	ListPortsBySessionID(ctx context.Context, sessionID int64) ([]*manman.ServerPort, error)
	DeallocatePortsBySessionID(ctx context.Context, sessionID int64) error
	AllocateMultiplePorts(ctx context.Context, serverID int64, portBindings []*manman.PortBinding, sessionID int64) error
	GetAvailablePortsInRange(ctx context.Context, serverID int64, protocol string, startPort, endPort, limit int) ([]int, error)
}

// ConfigurationStrategyRepository defines operations for ConfigurationStrategy entities
type ConfigurationStrategyRepository interface {
	Create(ctx context.Context, strategy *manman.ConfigurationStrategy) (*manman.ConfigurationStrategy, error)
	Get(ctx context.Context, strategyID int64) (*manman.ConfigurationStrategy, error)
	ListByGame(ctx context.Context, gameID int64) ([]*manman.ConfigurationStrategy, error)
	Update(ctx context.Context, strategy *manman.ConfigurationStrategy) error
	Delete(ctx context.Context, strategyID int64) error
}

// ConfigurationPatchRepository defines operations for ConfigurationPatch entities
type ConfigurationPatchRepository interface {
	Create(ctx context.Context, patch *manman.ConfigurationPatch) (*manman.ConfigurationPatch, error)
	Get(ctx context.Context, patchID int64) (*manman.ConfigurationPatch, error)
	GetByStrategyAndEntity(ctx context.Context, strategyID int64, patchLevel string, entityID int64) (*manman.ConfigurationPatch, error)
	List(ctx context.Context, strategyID *int64, patchLevel *string, entityID *int64) ([]*manman.ConfigurationPatch, error)
	Update(ctx context.Context, patch *manman.ConfigurationPatch) error
	Delete(ctx context.Context, patchID int64) error
}

// WorkshopAddonRepository defines operations for WorkshopAddon entities
type WorkshopAddonRepository interface {
	Create(ctx context.Context, addon *manman.WorkshopAddon) (*manman.WorkshopAddon, error)
	Get(ctx context.Context, addonID int64) (*manman.WorkshopAddon, error)
	GetByWorkshopID(ctx context.Context, gameID int64, workshopID string, platformType string) (*manman.WorkshopAddon, error)
	List(ctx context.Context, gameID *int64, includeDeprecated bool, limit, offset int) ([]*manman.WorkshopAddon, error)
	Update(ctx context.Context, addon *manman.WorkshopAddon) error
	Delete(ctx context.Context, addonID int64) error
}

// WorkshopInstallationRepository defines operations for installation tracking
type WorkshopInstallationRepository interface {
	Create(ctx context.Context, installation *manman.WorkshopInstallation) (*manman.WorkshopInstallation, error)
	Get(ctx context.Context, installationID int64) (*manman.WorkshopInstallation, error)
	GetBySGCAndAddon(ctx context.Context, sgcID, addonID int64) (*manman.WorkshopInstallation, error)
	ListBySGC(ctx context.Context, sgcID int64, limit, offset int) ([]*manman.WorkshopInstallation, error)
	ListByAddon(ctx context.Context, addonID int64, limit, offset int) ([]*manman.WorkshopInstallation, error)
	UpdateStatus(ctx context.Context, installationID int64, status string, errorMsg *string) error
	UpdateProgress(ctx context.Context, installationID int64, percent int) error
	Delete(ctx context.Context, installationID int64) error
}

// WorkshopLibraryRepository defines operations for library management
type WorkshopLibraryRepository interface {
	Create(ctx context.Context, library *manman.WorkshopLibrary) (*manman.WorkshopLibrary, error)
	Get(ctx context.Context, libraryID int64) (*manman.WorkshopLibrary, error)
	List(ctx context.Context, gameID *int64, limit, offset int) ([]*manman.WorkshopLibrary, error)
	Update(ctx context.Context, library *manman.WorkshopLibrary) error
	Delete(ctx context.Context, libraryID int64) error

	AddAddon(ctx context.Context, libraryID, addonID int64, displayOrder int) error
	RemoveAddon(ctx context.Context, libraryID, addonID int64) error
	ListAddons(ctx context.Context, libraryID int64) ([]*manman.WorkshopAddon, error)

	AddReference(ctx context.Context, parentLibraryID, childLibraryID int64) error
	RemoveReference(ctx context.Context, parentLibraryID, childLibraryID int64) error
	ListReferences(ctx context.Context, libraryID int64) ([]*manman.WorkshopLibrary, error)
	DetectCircularReference(ctx context.Context, parentLibraryID, childLibraryID int64) (bool, error)
}

// Repository aggregates all repository interfaces
type Repository struct {
	Servers                ServerRepository
	Games                  GameRepository
	GameConfigs            GameConfigRepository
	ServerGameConfigs      ServerGameConfigRepository
	Sessions               SessionRepository
	ServerCapabilities     ServerCapabilityRepository
	LogReferences          LogReferenceRepository
	Backups                BackupRepository
	ServerPorts            ServerPortRepository
	ConfigurationStrategies ConfigurationStrategyRepository
	ConfigurationPatches   ConfigurationPatchRepository
	WorkshopAddons         WorkshopAddonRepository
	WorkshopInstallations  WorkshopInstallationRepository
	WorkshopLibraries      WorkshopLibraryRepository
	Actions                interface{} // ActionRepository from postgres package
}
