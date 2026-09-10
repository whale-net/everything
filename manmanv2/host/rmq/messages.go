package rmq

import "time"

// PortBindingMessage represents a container-to-host port mapping
type PortBindingMessage struct {
	ContainerPort int32  `json:"container_port"`
	HostPort      int32  `json:"host_port"`
	Protocol      string `json:"protocol"` // "TCP" | "UDP"
}

// VolumeMountMessage represents a persistent volume mount
type VolumeMountMessage struct {
	Name          string            `json:"name"`
	ContainerPath string            `json:"container_path"`
	HostSubpath   string            `json:"host_subpath,omitempty"`
	VolumeType    string            `json:"volume_type,omitempty"`
	Options       map[string]string `json:"options,omitempty"`
	IsEnabled     bool              `json:"is_enabled"`
}

// GameConfigMessage represents game configuration details
type GameConfigMessage struct {
	ConfigID      int64                  `json:"config_id"`
	Image         string                 `json:"image"`
	ArgsTemplate  string                 `json:"args_template"`
	EnvTemplate  map[string]string    `json:"env_template"`
	Entrypoint   []string             `json:"entrypoint"`
	Command       []string               `json:"command"`
	Volumes       []VolumeMountMessage   `json:"volumes"`
}

// ServerGameConfigMessage represents server-specific game configuration
type ServerGameConfigMessage struct {
	SGCID        int64                `json:"sgc_id"`
	PortBindings []PortBindingMessage `json:"port_bindings"`
}

// StartSessionCommand represents a command to start a session
//
// RenderedEnv (FR5) is the server-side rendered effective environment:
// the GameConfig env template merged with deployment-level overrides.
// Presence semantics: nil/absent means "not rendered" -- the host falls
// back to GameConfig.EnvTemplate; an empty (non-nil) map is present and
// authoritative -- an intentionally empty env is honored, not replaced
// by the template. Strictly additive (NFR2/LB1): no other field changes.
// The json tag must NOT be omitempty: an empty-but-present map carries
// meaning and must survive marshaling.
type StartSessionCommand struct {
	SessionID        int64                   `json:"session_id"`
	SGCID            int64                   `json:"sgc_id"`
	GameConfig       GameConfigMessage       `json:"game_config"`
	ServerGameConfig ServerGameConfigMessage `json:"server_game_config"`
	Force            bool                   `json:"force"`
	RenderedEnv      map[string]string       `json:"rendered_env"`
}

// StopSessionCommand represents a command to stop a session
type StopSessionCommand struct {
	SessionID int64 `json:"session_id"`
	Force     bool  `json:"force"`
}

// KillSessionCommand represents a command to kill a session
type KillSessionCommand struct {
	SessionID int64 `json:"session_id"`
}

// SendInputCommand represents a command to send stdin input to a running session
type SendInputCommand struct {
	SessionID int64  `json:"session_id"`
	Input     []byte `json:"input"`
}

// HostStatusUpdate represents a status update from the host
type HostStatusUpdate struct {
	ServerID int64  `json:"server_id"`
	Status   string `json:"status"` // "online" | "offline"
}

// SessionStatusUpdate represents a status update for a session
type SessionStatusUpdate struct {
	SessionID int64  `json:"session_id"`
	SGCID     int64  `json:"sgc_id"`
	Status    string `json:"status"` // "pending" | "starting" | "running" | "stopping" | "stopped" | "crashed"
	ExitCode  *int   `json:"exit_code,omitempty"`
}

// HealthUpdate represents a health/keepalive message with session metrics
type HealthUpdate struct {
	ServerID        int64           `json:"server_id"`
	SessionStats    *SessionStats   `json:"session_stats,omitempty"`
}

// SessionStats represents aggregated session statistics
type SessionStats struct {
	Total    int `json:"total"`
	Pending  int `json:"pending"`
	Starting int `json:"starting"`
	Running  int `json:"running"`
	Stopping int `json:"stopping"`
	Stopped  int `json:"stopped"`
	Crashed  int `json:"crashed"`
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

// InstallationStatusUpdate represents a status update for a workshop addon installation
type InstallationStatusUpdate struct {
	InstallationID  int64   `json:"installation_id"`
	Status          string  `json:"status"` // "pending" | "downloading" | "installed" | "failed" | "removed"
	ProgressPercent int     `json:"progress_percent"`
	ErrorMessage    *string `json:"error_message,omitempty"`
}

// RemoveAddonCommand represents a command to remove a workshop addon from disk
type RemoveAddonCommand struct {
	InstallationID   int64  `json:"installation_id"`
	SGCID            int64  `json:"sgc_id"`
	AddonID          int64  `json:"addon_id"`
	InstallationPath string `json:"installation_path"`
}

// BackupCommand instructs the host-manager to archive a volume sub-path and upload to S3
type BackupCommand struct {
	BackupID          int64     `json:"backup_id"`
	SGCID             int64     `json:"sgc_id"`
	VolumeType        string    `json:"volume_type"`         // "bind" or "named"
	VolumeHostPath    string    `json:"volume_host_path"`    // host path to volume root (bind volumes only)
	VolumeName        string    `json:"volume_name"`         // logical volume name (used to derive Docker named volume)
	BackupPath        string    `json:"backup_path"`         // relative path within volume to archive
	S3Key             string    `json:"s3_key"`              // pre-computed: backups/{sgc_id}/{config_id}/{backup_id}.tar.gz
	PresignedURL      string    `json:"presigned_url"`       // pre-signed PUT URL for direct upload
	PreActionCommands []string  `json:"pre_action_commands"` // pre-rendered commands to send to container stdin
	CreatedAt         time.Time `json:"created_at"`          // used to discard commands that queued too long
}

// BackupStatusUpdate reports the result of a backup operation back to the processor
type BackupStatusUpdate struct {
	BackupID     int64   `json:"backup_id"`
	S3URL        *string `json:"s3_url,omitempty"`
	SizeBytes    *int64  `json:"size_bytes,omitempty"`
	Status       string  `json:"status"` // "completed" | "failed"
	ErrorMessage *string `json:"error_message,omitempty"`
}

// WorkshopCacheStatusUpdate reports the outcome of an install-time Workshop
// verify/cache-refresh cycle (#2184, plan #2175 FR8/FR9/FR10). Published on
// the "status.host.<serverID>.workshop.cache" routing key, additive
// alongside (never replacing) the existing installation-status publishing
// above -- NFR3 forbids renaming a field, repurposing a routing key, or
// changing the shape of DownloadAddonCommand/InstallationStatusUpdate for
// existing consumers.
type WorkshopCacheStatusUpdate struct {
	ServerID       int64     `json:"server_id"`
	WorkshopID     string    `json:"workshop_id"`
	ContentVersion string    `json:"content_version"`
	CacheEntryID   int64     `json:"cache_entry_id"`
	Event          string    `json:"event"` // "verified_unchanged" | "refreshed" | "populated" | "present"
	SizeBytes      int64     `json:"size_bytes,omitempty"`
	VerifiedAt     time.Time `json:"verified_at"`
}

// VerifyCacheEntryCommand instructs the host-manager to run an on-demand
// SteamCMD verify of a specific cache entry against its Workshop source,
// independent of any install (#2186, plan #2175 FR11). Published on the new
// "command.host.<serverID>.workshop.cache_verify" routing key, additive
// alongside (never replacing) the existing workshop.download/workshop.remove
// commands above -- NFR3 forbids renaming a field, repurposing a routing
// key, or changing the shape of an existing command for existing consumers.
// The result is reported asynchronously on the existing
// "status.host.<serverID>.workshop.cache" key (#2184) via
// WorkshopCacheStatusUpdate -- this command has no dedicated reply message.
type VerifyCacheEntryCommand struct {
	CacheEntryID   int64  `json:"cache_entry_id"`
	WorkshopID     string `json:"workshop_id"`
	ContentVersion string `json:"content_version"`
	SteamAppID     string `json:"steam_app_id"`
}
