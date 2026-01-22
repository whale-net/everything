package rmq

// StartSessionCommand represents a command to start a session
type StartSessionCommand struct {
	SessionID int64                  `json:"session_id"`
	SGCID     int64                  `json:"sgc_id"`
	GameConfig map[string]interface{} `json:"game_config"`
	ServerGameConfig map[string]interface{} `json:"server_game_config"`
	Parameters map[string]interface{} `json:"parameters"`
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

// HealthUpdate represents a health/keepalive message
type HealthUpdate struct {
	ServerID int64 `json:"server_id"`
}
