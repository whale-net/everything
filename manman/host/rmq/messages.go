package rmq

// PortBindingMessage represents a container-to-host port mapping
type PortBindingMessage struct {
	ContainerPort int32  `json:"container_port"`
	HostPort      int32  `json:"host_port"`
	Protocol      string `json:"protocol"` // "TCP" | "UDP"
}

// FileTemplateMessage represents a file to be created in the container
type FileTemplateMessage struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Mode       string `json:"mode"`
	IsTemplate bool   `json:"is_template"`
}

// ParameterMessage represents a configurable parameter
type ParameterMessage struct {
	Key          string `json:"key"`
	Value        string `json:"value"`
	Type         string `json:"type"`
	Description  string `json:"description"`
	Required     bool   `json:"required"`
	DefaultValue string `json:"default_value"`
}

// GameConfigMessage represents game configuration details
type GameConfigMessage struct {
	ConfigID     int64                  `json:"config_id"`
	Image        string                 `json:"image"`
	ArgsTemplate string                 `json:"args_template"`
	EnvTemplate  map[string]string      `json:"env_template"`
	Files        []FileTemplateMessage  `json:"files"`
	Parameters   []ParameterMessage     `json:"parameters"`
}

// ServerGameConfigMessage represents server-specific game configuration
type ServerGameConfigMessage struct {
	SGCID        int64                  `json:"sgc_id"`
	PortBindings []PortBindingMessage   `json:"port_bindings"`
	Parameters   map[string]string      `json:"parameters"`
}

// StartSessionCommand represents a command to start a session
type StartSessionCommand struct {
	SessionID        int64                   `json:"session_id"`
	SGCID            int64                   `json:"sgc_id"`
	GameConfig       GameConfigMessage       `json:"game_config"`
	ServerGameConfig ServerGameConfigMessage `json:"server_game_config"`
	Parameters       map[string]string       `json:"parameters"`
	Force            bool                    `json:"force"`
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
