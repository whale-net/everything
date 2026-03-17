package manman

import "time"

// ConfigurationStrategy defines how to render configuration for a game
type ConfigurationStrategy struct {
	StrategyID    int64     `db:"strategy_id"`
	GameID        int64     `db:"game_id"`
	Name          string    `db:"name"`
	Description   *string   `db:"description"`
	StrategyType  string    `db:"strategy_type"`
	TargetPath    *string   `db:"target_path"`
	BaseTemplate  *string   `db:"base_template"`
	RenderOptions JSONB     `db:"render_options"`
	ApplyOrder    int       `db:"apply_order"`
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
}

// ConfigurationPatch stores configuration overrides at different levels
type ConfigurationPatch struct {
	PatchID      int64     `db:"patch_id"`
	StrategyID   int64     `db:"strategy_id"`
	PatchLevel   string    `db:"patch_level"`
	EntityID     int64     `db:"entity_id"`
	PatchContent *string   `db:"patch_content"`
	PatchFormat  string    `db:"patch_format"`
	VolumeID     *int64    `db:"volume_id"`     // Optional FK to game_config_volumes
	PathOverride *string   `db:"path_override"` // Optional relative path override
	PatchOrder   int       `db:"patch_order"`   // Application order (lower = earlier = lower priority)
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}
