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

// FleetGameStatus is the per-game row of the fleet-wide status summary
// (#2371, manmanv2 M6, FR5/NFR5): a point-in-time running-over-total
// deployment count across every host in the fleet, computed from existing
// ServerGameConfig/Session data. See
// SessionRepository.CountRunningDeploymentsByGame for the exact
// running/total definitions this carries -- this struct is deliberately a
// plain aggregate result, not a persisted row (no db tags needed beyond
// documentation, since it is built from a GROUP BY, not scanned 1:1 from a
// table).
type FleetGameStatus struct {
	GameID       int64
	GameName     string
	TotalCount   int32
	RunningCount int32
}
