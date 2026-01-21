package manman

import (
	"database/sql/driver"
	"encoding/json"
	"time"
)

// JSONB is a custom type for PostgreSQL JSONB columns
type JSONB map[string]interface{}

// Value implements the driver.Valuer interface
func (j JSONB) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(j)
}

// Scan implements the sql.Scanner interface
func (j *JSONB) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return nil
	}
	return json.Unmarshal(bytes, j)
}

// Server represents a physical/virtual machine running the host manager
type Server struct {
	ServerID int64      `db:"server_id"`
	Name     string     `db:"name"`
	Status   string     `db:"status"`
	LastSeen *time.Time `db:"last_seen"`
}

// Game represents a game definition (e.g., Minecraft, Valheim)
type Game struct {
	GameID     int64   `db:"game_id"`
	Name       string  `db:"name"`
	SteamAppID *string `db:"steam_app_id"`
	Metadata   JSONB   `db:"metadata"`
}

// GameConfig represents a preset/template for running a game
type GameConfig struct {
	ConfigID     int64   `db:"config_id"`
	GameID       int64   `db:"game_id"`
	Name         string  `db:"name"`
	Image        string  `db:"image"`
	ArgsTemplate *string `db:"args_template"`
	EnvTemplate  JSONB   `db:"env_template"`
	Files        JSONB   `db:"files"`
	Parameters   JSONB   `db:"parameters"`
}

// ServerGameConfig represents a game configuration deployed on a specific server
type ServerGameConfig struct {
	SGCID        int64  `db:"sgc_id"`
	ServerID     int64  `db:"server_id"`
	GameConfigID int64  `db:"game_config_id"`
	PortBindings JSONB  `db:"port_bindings"`
	Parameters   JSONB  `db:"parameters"`
	Status       string `db:"status"`
}

// Session represents an execution of a ServerGameConfig
type Session struct {
	SessionID  int64      `db:"session_id"`
	SGCID      int64      `db:"sgc_id"`
	StartedAt  *time.Time `db:"started_at"`
	EndedAt    *time.Time `db:"ended_at"`
	ExitCode   *int       `db:"exit_code"`
	Status     string     `db:"status"`
	Parameters JSONB      `db:"parameters"`
}

// ServerPort represents port allocation tracking at server level
type ServerPort struct {
	ServerID    int64     `db:"server_id"`
	Port        int       `db:"port"`
	Protocol    string    `db:"protocol"`
	SGCID       *int64    `db:"sgc_id"`
	AllocatedAt time.Time `db:"allocated_at"`
}

// Status constants
const (
	ServerStatusOnline  = "online"
	ServerStatusOffline = "offline"

	SGCStatusActive   = "active"
	SGCStatusInactive = "inactive"

	SessionStatusPending   = "pending"
	SessionStatusStarting  = "starting"
	SessionStatusRunning   = "running"
	SessionStatusStopping  = "stopping"
	SessionStatusStopped   = "stopped"
	SessionStatusCrashed   = "crashed"
	SessionStatusCompleted = "completed"

	ProtocolTCP = "TCP"
	ProtocolUDP = "UDP"
)
