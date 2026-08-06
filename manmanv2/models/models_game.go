package manman

import "time"

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
	Entrypoint   JSONB   `db:"entrypoint"` // []string stored as JSONB
	Command      JSONB   `db:"command"`    // []string stored as JSONB
}

// GameConfigVolume represents a volume mount configuration specific to a GameConfig
type GameConfigVolume struct {
	VolumeID      int64     `db:"volume_id"`
	ConfigID      int64     `db:"config_id"`
	Name          string    `db:"name"`
	Description   *string   `db:"description"`
	ContainerPath string    `db:"container_path"`
	HostSubpath   *string   `db:"host_subpath"`
	ReadOnly      bool      `db:"read_only"`
	VolumeType    string    `db:"volume_type"`
	IsEnabled     bool      `db:"is_enabled"`
	CreatedAt     time.Time `db:"created_at"`
}

// ServerGameConfig represents a game configuration deployed on a specific server
type ServerGameConfig struct {
	SGCID        int64  `db:"sgc_id"`
	ServerID     int64  `db:"server_id"`
	GameConfigID int64  `db:"game_config_id"`
	PortBindings JSONB  `db:"port_bindings"`
	Status       string `db:"status"`
}
