package repository

import (
	"context"
	"errors"
	"time"

	"github.com/whale-net/everything/manmanv2/models"
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
	// SetDrainState/ListByDrainState: host drain state (#2360, manmanv2 M6,
	// C29 groundwork). Inert here -- no cordon enforcement or eviction yet.
	// ListByDrainState is used by the dependent eviction task.
	SetDrainState(ctx context.Context, serverID int64, state string, requestedAt *time.Time) error
	ListByDrainState(ctx context.Context, state string) ([]*manman.Server, error)
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

	AddLibrary(ctx context.Context, sgcID, libraryID int64, presetID, volumeID *int64, installationPathOverride *string) error
	RemoveLibrary(ctx context.Context, sgcID, libraryID int64) error
	ListLibraries(ctx context.Context, sgcID int64) ([]*manman.WorkshopLibrary, error)
	GetSGCLibraryAttachments(ctx context.Context, sgcID int64) ([]*manman.SGCWorkshopLibrary, error)
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
	// UpdateSessionEndIfStatus is UpdateSessionEnd's compare-and-swap variant:
	// it only writes if the row's status still matches expectedStatus at write
	// time, so a stale read (e.g. checkStaleSessions' snapshot-then-later-write
	// tick) can never clobber a terminal transition that committed in between.
	UpdateSessionEndIfStatus(ctx context.Context, sessionID int64, expectedStatus string, newStatus string, endedAt time.Time, exitCode *int) (updated bool, err error)
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
	GetHistogramBySession(ctx context.Context, sessionID int64, bucketSeconds int64, startTime, endTime *int64) (map[int64]map[string]int32, error)
}

// BackupRepository defines operations for Backup entities
type BackupRepository interface {
	Create(ctx context.Context, backup *manman.Backup) (*manman.Backup, error)
	Get(ctx context.Context, backupID int64) (*manman.Backup, error)
	List(ctx context.Context, sgcID *int64, sessionID *int64, limit int, offset int) ([]*manman.Backup, error)
	Delete(ctx context.Context, backupID int64) error
	UpdateStatus(ctx context.Context, backupID int64, status string, s3URL *string, sizeBytes *int64, errMsg *string) error
}

// BackupConfigRepository defines operations for BackupConfig entities
type BackupConfigRepository interface {
	Create(ctx context.Context, cfg *manman.BackupConfig) (*manman.BackupConfig, error)
	Get(ctx context.Context, backupConfigID int64) (*manman.BackupConfig, error)
	List(ctx context.Context, volumeID int64) ([]*manman.BackupConfig, error)
	Update(ctx context.Context, cfg *manman.BackupConfig) error
	Delete(ctx context.Context, backupConfigID int64) error
	// ListDue returns enabled configs whose cadence has elapsed and whose SGC had an active session since last_backup_at
	ListDue(ctx context.Context, now time.Time) ([]*manman.BackupConfig, error)
	UpdateLastBackupAt(ctx context.Context, backupConfigID int64, t time.Time) error
	// Actions
	AddAction(ctx context.Context, backupConfigID, actionID int64, displayOrder int) error
	RemoveAction(ctx context.Context, backupConfigID, actionID int64) error
	ListActions(ctx context.Context, backupConfigID int64) ([]*manman.BackupConfigAction, error)
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

// ServerPortRangeRepository defines operations for a server's allowed
// host-port ranges (FR12, task #2095). Replace is replace-all semantics:
// the caller fetches the current set, modifies it, and stores the whole
// set back; an empty slice clears all ranges (unconstrained, SB-1.2).
type ServerPortRangeRepository interface {
	List(ctx context.Context, serverID int64) ([]*manman.ServerAllowedPortRange, error)
	Replace(ctx context.Context, serverID int64, ranges []*manman.ServerAllowedPortRange) ([]*manman.ServerAllowedPortRange, error)
}

// ConfigurationStrategyRepository defines operations for ConfigurationStrategy entities
type ConfigurationStrategyRepository interface {
	Create(ctx context.Context, strategy *manman.ConfigurationStrategy) (*manman.ConfigurationStrategy, error)
	Get(ctx context.Context, strategyID int64) (*manman.ConfigurationStrategy, error)
	ListByGame(ctx context.Context, gameID int64) ([]*manman.ConfigurationStrategy, error)
	Update(ctx context.Context, strategy *manman.ConfigurationStrategy) error
	Delete(ctx context.Context, strategyID int64) error
}

// GameConfigVolumeRepository defines operations for GameConfigVolume entities
type GameConfigVolumeRepository interface {
	Create(ctx context.Context, volume *manman.GameConfigVolume) (*manman.GameConfigVolume, error)
	Get(ctx context.Context, volumeID int64) (*manman.GameConfigVolume, error)
	ListByGameConfig(ctx context.Context, configID int64) ([]*manman.GameConfigVolume, error)
	Update(ctx context.Context, volume *manman.GameConfigVolume) error
	Delete(ctx context.Context, volumeID int64) error
}

// ConfigurationPatchRepository defines operations for ConfigurationPatch entities
type ConfigurationPatchRepository interface {
	Create(ctx context.Context, patch *manman.ConfigurationPatch) (*manman.ConfigurationPatch, error)
	Get(ctx context.Context, patchID int64) (*manman.ConfigurationPatch, error)
	GetByStrategyAndEntity(ctx context.Context, strategyID int64, patchLevel string, entityID int64) (*manman.ConfigurationPatch, error)
	ListByStrategyAndEntity(ctx context.Context, strategyID int64, patchLevel string, entityID int64) ([]*manman.ConfigurationPatch, error)
	List(ctx context.Context, strategyID *int64, patchLevel *string, entityID *int64) ([]*manman.ConfigurationPatch, error)
	Update(ctx context.Context, patch *manman.ConfigurationPatch) error
	Delete(ctx context.Context, patchID int64) error
}

// WorkshopAddonRepository defines operations for WorkshopAddon entities
type WorkshopAddonRepository interface {
	Create(ctx context.Context, addon *manman.WorkshopAddon) (*manman.WorkshopAddon, error)
	Get(ctx context.Context, addonID int64) (*manman.WorkshopAddon, error)
	GetByWorkshopID(ctx context.Context, gameID int64, workshopID string, platformType string) (*manman.WorkshopAddon, error)
	// GetByWorkshopIDAnyGame resolves a workshop_id to its addon without a
	// known game_id (#2186, plan #2175 FR11): a WorkshopCacheEntry's identity
	// is workshop_id + content_version only (NFR1, no game/addon linkage), so
	// the on-demand verify RPC has nothing but workshop_id to resolve the
	// addon's steam_app_id from. Returns (nil, nil) -- not an error -- if no
	// addon owns this workshop_id, mirroring GetCacheEntryByKey's
	// not-found convention.
	GetByWorkshopIDAnyGame(ctx context.Context, workshopID string) (*manman.WorkshopAddonWithGame, error)
	List(ctx context.Context, gameID *int64, includeDeprecated bool, limit, offset int) ([]*manman.WorkshopAddon, error)
	ListByCollectionID(ctx context.Context, collectionID int64) ([]*manman.WorkshopAddon, error)
	Update(ctx context.Context, addon *manman.WorkshopAddon) error
	Delete(ctx context.Context, addonID int64) error
}

// WorkshopInstallationRepository defines operations for installation tracking
type WorkshopInstallationRepository interface {
	Create(ctx context.Context, installation *manman.WorkshopInstallation) (*manman.WorkshopInstallation, error)
	Get(ctx context.Context, installationID int64) (*manman.WorkshopInstallation, error)
	GetBySGCAndAddon(ctx context.Context, sgcID, addonID int64) (*manman.WorkshopInstallation, error)
	List(ctx context.Context, limit, offset int) ([]*manman.WorkshopInstallation, error)
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
	ListAddons(ctx context.Context, libraryID int64) ([]*manman.WorkshopAddonWithGame, error)

	AddReference(ctx context.Context, parentLibraryID, childLibraryID int64) error
	RemoveReference(ctx context.Context, parentLibraryID, childLibraryID int64) error
	ListReferences(ctx context.Context, libraryID int64) ([]*manman.WorkshopLibrary, error)
	DetectCircularReference(ctx context.Context, parentLibraryID, childLibraryID int64) (bool, error)
}

// WorkshopBatchJobRepository defines operations for batch-operation
// persistence shared by Workshop collection bulk-add (FR1) and mixed-format
// batch create (FR2/FR3): the batch job header plus its per-item outcomes.
type WorkshopBatchJobRepository interface {
	CreateBatchJob(ctx context.Context, job *manman.WorkshopBatchJob) (*manman.WorkshopBatchJob, error)
	CreateBatchJobItems(ctx context.Context, batchJobID int64, items []*manman.WorkshopBatchJobItem) error
	UpdateBatchJobItemResult(ctx context.Context, batchJobItemID int64, status string, addonID *int64, errorMessage *string) error
	UpdateBatchJobStatus(ctx context.Context, batchJobID int64, status string, succeeded, failed int) error
	GetBatchJob(ctx context.Context, batchJobID int64) (*manman.WorkshopBatchJob, error)
	ListBatchJobItems(ctx context.Context, batchJobID int64) ([]*manman.WorkshopBatchJobItem, error)
	ListBatchJobs(ctx context.Context, gameID int64, limit int) ([]*manman.WorkshopBatchJob, error)
}

// ErrPendingRestartExists is returned by PendingRestartRepository.Create when
// a 'pending' record already exists for the target server_game_config_id --
// the pending_restarts_one_pending_per_sgc unique partial index (migration
// 036) is the actual FR10/NFR10 enforcement mechanism; this sentinel just
// lets callers distinguish "already in flight" from a real failure without
// inspecting a raw pgx/pgconn error.
var ErrPendingRestartExists = errors.New("pending restart already exists for this deployment")

// PendingRestartRepository defines operations for the durable pending-restart
// intent (Track B, durable restart): "a Start is pending for this deployment,
// gated on session <id>'s Stop reaching a terminal status". See
// manmanv2/ARCHITECTURE.md "Data Model" for why this is not SCD2.
type PendingRestartRepository interface {
	// Create inserts a new pending record. A unique-violation on
	// pending_restarts_one_pending_per_sgc must be returned as
	// ErrPendingRestartExists, not a raw pgx error, so the caller can treat
	// it as "already in flight" rather than a failure.
	Create(ctx context.Context, sgcID, gatingSessionID int64, stallDeadline time.Time) (*manman.PendingRestart, error)
	// ClaimForSession atomically transitions the pending record gated on
	// gatingSessionID to 'started' and returns it. Returns (nil, nil) when
	// there is no pending record to claim -- this single statement IS the
	// NFR10 idempotency guard, so it must be one UPDATE ... WHERE
	// status='pending' ... RETURNING *, never a SELECT-then-UPDATE.
	ClaimForSession(ctx context.Context, gatingSessionID int64) (*manman.PendingRestart, error)
	// MarkStarted records the session id the deferred Start produced.
	MarkStarted(ctx context.Context, pendingRestartID, startedSessionID int64) error
	// MarkFailed moves a 'pending' or 'started' record to 'failed' with a
	// reason. 'pending' covers RestartDeployment's own Stop-dispatch failing
	// right after Create (#1730); 'started' covers the deferred Start itself
	// failing after being claimed (#1731). Both are terminal failures of the
	// same intent, so they share one transition rather than two.
	MarkFailed(ctx context.Context, pendingRestartID int64, reason string) error
	// ExpireStalled atomically moves every 'pending' record past its
	// stall_deadline to 'expired' and returns them (FR11/NFR12).
	ExpireStalled(ctx context.Context, now time.Time) ([]*manman.PendingRestart, error)
	// GetLatestBySGCIDs returns the current non-resolved-or-recently-resolved
	// state per SGC for the operator-facing read path (FR12).
	GetLatestBySGCIDs(ctx context.Context, sgcIDs []int64) (map[int64]*manman.PendingRestart, error)
}

// AddonPathPresetRepository defines operations for game addon path presets
type AddonPathPresetRepository interface {
	Create(ctx context.Context, preset *manman.GameAddonPathPreset) (*manman.GameAddonPathPreset, error)
	Get(ctx context.Context, presetID int64) (*manman.GameAddonPathPreset, error)
	ListByGame(ctx context.Context, gameID int64) ([]*manman.GameAddonPathPreset, error)
	Update(ctx context.Context, preset *manman.GameAddonPathPreset) error
	Delete(ctx context.Context, presetID int64) error
}

// WorkshopCacheRepository defines operations for the content-addressed
// Workshop cache's identity and metadata (#2181, plan #2175, FR5/FR9).
// GetCacheEntryByKey/UpsertCacheEntry key exclusively on cache_key (which
// itself derives from workshop_id + content_version only, NFR1); no method
// here accepts or filters by sgc_id, server_id, deployment_id, or
// library_id (NFR2). Entries are append-only (FR9, not SCD2 -- see
// AGENTS.md § SCD2): DeleteCacheEntry exists only for explicit Admin
// eviction (FR12), never for the refresh/upsert path.
type WorkshopCacheRepository interface {
	GetCacheEntryByKey(ctx context.Context, cacheKey string) (*manman.WorkshopCacheEntry, error)
	// UpsertCacheEntry is an idempotent insert-or-return-existing on
	// cache_key: concurrent callers racing to cache the same
	// (workshop_id, content_version) converge on one row instead of
	// erroring (NFR4 foundation).
	UpsertCacheEntry(ctx context.Context, entry *manman.WorkshopCacheEntry) (*manman.WorkshopCacheEntry, error)
	ListCacheEntriesForWorkshopID(ctx context.Context, workshopID string) ([]*manman.WorkshopCacheEntry, error)
	GetCacheEntry(ctx context.Context, cacheEntryID int64) (*manman.WorkshopCacheEntry, error)
	TouchCacheEntryVerified(ctx context.Context, cacheEntryID int64, verifiedAt time.Time) error
	// DeleteCacheEntry is the explicit Admin eviction path (FR12) -- there is
	// no automatic garbage collection of superseded entries in this layer.
	DeleteCacheEntry(ctx context.Context, cacheEntryID int64) error
	UpsertHostPresence(ctx context.Context, cacheEntryID, serverID int64) error
	ListHostPresence(ctx context.Context, cacheEntryID int64) ([]*manman.WorkshopCacheHostPresence, error)
	// ListHostPresenceForCacheEntryIDs is the FR10 fleet-visibility read path:
	// one query for every entry's host presence, joined with the servers
	// table for display names, keyed by cache_entry_id. Callers with a version
	// history to render MUST use this instead of looping ListHostPresence per
	// entry -- that loop is exactly the per-host, per-entry fan-out FR10 rules
	// out, and the query count must not scale with entry count.
	ListHostPresenceForCacheEntryIDs(ctx context.Context, cacheEntryIDs []int64) (map[int64][]*manman.WorkshopCacheHostPresenceWithServer, error)
}

// Repository aggregates all repository interfaces
type Repository struct {
	Servers                 ServerRepository
	Games                   GameRepository
	GameConfigs             GameConfigRepository
	ServerGameConfigs       ServerGameConfigRepository
	Sessions                SessionRepository
	ServerCapabilities      ServerCapabilityRepository
	LogReferences           LogReferenceRepository
	Backups                 BackupRepository
	BackupConfigs           BackupConfigRepository
	ServerPorts             ServerPortRepository
	ServerPortRanges        ServerPortRangeRepository
	ConfigurationStrategies ConfigurationStrategyRepository
	ConfigurationPatches    ConfigurationPatchRepository
	GameConfigVolumes       GameConfigVolumeRepository
	WorkshopAddons          WorkshopAddonRepository
	WorkshopInstallations   WorkshopInstallationRepository
	WorkshopLibraries       WorkshopLibraryRepository
	WorkshopBatchJobs       WorkshopBatchJobRepository
	AddonPathPresets        AddonPathPresetRepository
	PendingRestarts         PendingRestartRepository
	WorkshopCache           WorkshopCacheRepository
	Actions                 interface{} // ActionRepository from postgres package
}
