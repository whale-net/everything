-- ============================================================================
-- Configuration Strategies Migration
-- ============================================================================
-- Replaces simple args_template/env_template with flexible configuration
-- strategy system supporting CLI args, env vars, and various file formats
-- with patch-based layering (GameConfig → ServerGameConfig → Session)

-- ----------------------------------------------------------------------------
-- Configuration Strategies
-- ----------------------------------------------------------------------------
-- Define how to render configuration for a game
CREATE TABLE configuration_strategies (
    strategy_id BIGSERIAL PRIMARY KEY,
    game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,

    -- Strategy metadata
    name VARCHAR(100) NOT NULL,  -- "Server Properties", "CLI Args", etc.
    description TEXT,

    -- Strategy type defines HOW to render
    strategy_type VARCHAR(50) NOT NULL CHECK (strategy_type IN (
        'cli_args',           -- Command line arguments
        'env_vars',           -- Environment variables
        'file_properties',    -- key=value properties file
        'file_json',          -- JSON file with merge/patch
        'file_yaml',          -- YAML file with merge
        'file_ini',           -- INI file with sections
        'file_xml',           -- XML file with XPath updates
        'file_lua',           -- Lua config file
        'file_custom'         -- Custom format with template
    )),

    -- Target location
    target_path TEXT,  -- File path like "/data/server.properties" or null for CLI/env

    -- Base template/content
    base_template TEXT,  -- Starting point before patches

    -- Rendering options (JSONB for flexibility)
    render_options JSONB DEFAULT '{}',  -- Format-specific options

    -- Ordering for multi-strategy configs
    apply_order INT DEFAULT 0,  -- Lower numbers applied first

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(game_id, name)
);

CREATE INDEX IF NOT EXISTS idx_config_strategies_game_id ON configuration_strategies(game_id);
CREATE INDEX IF NOT EXISTS idx_config_strategies_apply_order ON configuration_strategies(game_id, apply_order);

-- ----------------------------------------------------------------------------
-- Strategy Parameter Bindings
-- ----------------------------------------------------------------------------
-- Links parameters to configuration strategies
CREATE TABLE strategy_parameter_bindings (
    binding_id BIGSERIAL PRIMARY KEY,
    strategy_id BIGINT NOT NULL REFERENCES configuration_strategies(strategy_id) ON DELETE CASCADE,
    param_id BIGINT NOT NULL REFERENCES parameter_definitions(param_id) ON DELETE CASCADE,

    -- How to apply this parameter in this strategy
    binding_type VARCHAR(50) NOT NULL CHECK (binding_type IN (
        'direct',        -- Use value as-is
        'template',      -- Use template with {{param}} substitution
        'json_path',     -- JSONPath like $.server.maxPlayers
        'xpath',         -- XPath for XML
        'ini_section'    -- INI section.key format
    )),

    -- Target location within the strategy
    target_key TEXT NOT NULL,  -- e.g., "max-players", "$.server.maxPlayers", "[ServerSettings]/MaxPlayers"

    -- Optional transformation template
    value_template TEXT,  -- e.g., "--{{key}}={{value}}" or "MaxPlayers={{value}}"

    -- Conditional application
    condition_expr TEXT,  -- e.g., "only_if:pvp=true"

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(strategy_id, param_id)
);

CREATE INDEX IF NOT EXISTS idx_strategy_bindings_strategy_id ON strategy_parameter_bindings(strategy_id);
CREATE INDEX IF NOT EXISTS idx_strategy_bindings_param_id ON strategy_parameter_bindings(param_id);

-- ----------------------------------------------------------------------------
-- Configuration Patches
-- ----------------------------------------------------------------------------
-- Stores configuration overrides at each level
CREATE TABLE configuration_patches (
    patch_id BIGSERIAL PRIMARY KEY,
    strategy_id BIGINT NOT NULL REFERENCES configuration_strategies(strategy_id) ON DELETE CASCADE,

    -- What level this patch applies to
    patch_level VARCHAR(50) NOT NULL CHECK (patch_level IN (
        'game_config',
        'server_game_config',
        'session'
    )),

    -- Which entity this patch belongs to
    entity_id BIGINT NOT NULL,  -- config_id, sgc_id, or session_id depending on patch_level

    -- Patch content (strategy-specific format)
    patch_content TEXT,  -- Could be JSON patch, YAML merge, or template
    patch_format VARCHAR(50) DEFAULT 'template',  -- 'json_merge_patch', 'json_patch', 'yaml_merge', 'template'

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(strategy_id, patch_level, entity_id)
);

CREATE INDEX IF NOT EXISTS idx_config_patches_strategy ON configuration_patches(strategy_id);
CREATE INDEX IF NOT EXISTS idx_config_patches_entity ON configuration_patches(patch_level, entity_id);
CREATE INDEX IF NOT EXISTS idx_config_patches_lookup ON configuration_patches(strategy_id, patch_level, entity_id);
