package rmq

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
type StartSessionCommand struct {
	SessionID        int64                   `json:"session_id"`
	SGCID            int64                   `json:"sgc_id"`
	GameConfig       GameConfigMessage       `json:"game_config"`
	ServerGameConfig ServerGameConfigMessage `json:"server_game_config"`
	Force            bool                   `json:"force"`
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
