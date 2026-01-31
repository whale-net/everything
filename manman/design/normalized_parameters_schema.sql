-- ============================================================================
-- Normalized Parameter Schema Design
-- ============================================================================
-- This schema replaces JSONB parameter storage with properly normalized tables

-- ----------------------------------------------------------------------------
-- Parameter Definitions
-- ----------------------------------------------------------------------------
-- Define parameters once per game with metadata
CREATE TABLE parameter_definitions (
    param_id BIGSERIAL PRIMARY KEY,
    game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
    key VARCHAR(100) NOT NULL,  -- e.g., "max_players"
    param_type VARCHAR(20) NOT NULL CHECK (param_type IN ('string', 'int', 'bool', 'secret')),
    description TEXT,
    required BOOLEAN NOT NULL DEFAULT false,
    default_value TEXT,

    -- Constraints for specific types (optional)
    min_value BIGINT,  -- For int type
    max_value BIGINT,  -- For int type
    allowed_values TEXT[],  -- For enum-like strings (e.g., ['easy', 'normal', 'hard'])

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(game_id, key)  -- Each parameter defined once per game
);

CREATE INDEX idx_param_defs_game_id ON parameter_definitions(game_id);
CREATE INDEX idx_param_defs_key ON parameter_definitions(key);

-- ----------------------------------------------------------------------------
-- GameConfig Parameter Values
-- ----------------------------------------------------------------------------
-- Store parameter values for each GameConfig
-- Only stores non-default values (sparse storage)
CREATE TABLE game_config_parameter_values (
    value_id BIGSERIAL PRIMARY KEY,
    config_id BIGINT NOT NULL REFERENCES game_configs(config_id) ON DELETE CASCADE,
    param_id BIGINT NOT NULL REFERENCES parameter_definitions(param_id) ON DELETE CASCADE,
    value TEXT NOT NULL,  -- Always stored as text, converted at application layer

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(config_id, param_id)  -- One value per parameter per config
);

CREATE INDEX idx_gc_param_values_config_id ON game_config_parameter_values(config_id);
CREATE INDEX idx_gc_param_values_param_id ON game_config_parameter_values(param_id);
-- Create indexes for common queries
CREATE INDEX idx_gc_param_values_lookup ON game_config_parameter_values(param_id, value);

-- ----------------------------------------------------------------------------
-- ServerGameConfig Parameter Values (Overrides)
-- ----------------------------------------------------------------------------
-- Server-specific overrides of GameConfig parameters
CREATE TABLE server_game_config_parameter_values (
    value_id BIGSERIAL PRIMARY KEY,
    sgc_id BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
    param_id BIGINT NOT NULL REFERENCES parameter_definitions(param_id) ON DELETE CASCADE,
    value TEXT NOT NULL,

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(sgc_id, param_id)
);

CREATE INDEX idx_sgc_param_values_sgc_id ON server_game_config_parameter_values(sgc_id);
CREATE INDEX idx_sgc_param_values_param_id ON server_game_config_parameter_values(param_id);

-- ----------------------------------------------------------------------------
-- Session Parameter Values (Runtime Overrides)
-- ----------------------------------------------------------------------------
-- Per-execution parameter overrides
CREATE TABLE session_parameter_values (
    value_id BIGSERIAL PRIMARY KEY,
    session_id BIGINT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    param_id BIGINT NOT NULL REFERENCES parameter_definitions(param_id) ON DELETE CASCADE,
    value TEXT NOT NULL,

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(session_id, param_id)
);

CREATE INDEX idx_session_param_values_session_id ON session_parameter_values(session_id);
CREATE INDEX idx_session_param_values_param_id ON session_parameter_values(param_id);

-- ============================================================================
-- Example Queries Enabled by Normalized Schema
-- ============================================================================

-- Find all GameConfigs with max_players >= 20
COMMENT ON TABLE game_config_parameter_values IS
'Example query: Find configs with max_players >= 20
SELECT DISTINCT gc.config_id, gc.name
FROM game_configs gc
JOIN game_config_parameter_values gcpv ON gc.config_id = gcpv.config_id
JOIN parameter_definitions pd ON gcpv.param_id = pd.param_id
WHERE pd.key = ''max_players''
  AND gcpv.value::int >= 20;';

-- Find all sessions for a specific game with PVP enabled
COMMENT ON TABLE session_parameter_values IS
'Example query: Find PVP sessions for a game
SELECT s.session_id, s.started_at
FROM sessions s
JOIN server_game_configs sgc ON s.sgc_id = sgc.sgc_id
JOIN game_configs gc ON sgc.game_config_id = gc.config_id
LEFT JOIN session_parameter_values spv ON s.session_id = spv.session_id
LEFT JOIN parameter_definitions pd ON spv.param_id = pd.param_id
WHERE gc.game_id = 1
  AND pd.key = ''pvp''
  AND spv.value = ''true'';';

-- Get merged parameters for a session (GameConfig → SGC → Session)
COMMENT ON TABLE parameter_definitions IS
'Example query: Get merged parameters for a session
WITH session_context AS (
  SELECT s.session_id, s.sgc_id, sgc.config_id, gc.game_id
  FROM sessions s
  JOIN server_game_configs sgc ON s.sgc_id = sgc.sgc_id
  JOIN game_configs gc ON sgc.config_id = gc.config_id
  WHERE s.session_id = 123
)
SELECT
  pd.key,
  COALESCE(
    spv.value,  -- Session override (highest priority)
    sgcpv.value,  -- ServerGameConfig override
    gcpv.value,  -- GameConfig value
    pd.default_value  -- Default from definition
  ) AS effective_value,
  pd.param_type,
  pd.description
FROM session_context sc
JOIN parameter_definitions pd ON sc.game_id = pd.game_id
LEFT JOIN session_parameter_values spv ON sc.session_id = spv.session_id AND pd.param_id = spv.param_id
LEFT JOIN server_game_config_parameter_values sgcpv ON sc.sgc_id = sgcpv.sgc_id AND pd.param_id = sgcpv.param_id
LEFT JOIN game_config_parameter_values gcpv ON sc.config_id = gcpv.config_id AND pd.param_id = gcpv.param_id
ORDER BY pd.key;';

-- ============================================================================
-- Migration Strategy
-- ============================================================================

COMMENT ON TABLE parameter_definitions IS
'Migration strategy from JSONB to normalized:
1. Create new tables (this file)
2. Write data migration script to:
   - Extract parameter definitions from game_configs.parameters JSONB
   - Populate parameter_definitions table
   - Extract parameter values from game_configs.parameters JSONB
   - Populate game_config_parameter_values table
   - Same for ServerGameConfig and Session
3. Update application code to use new schema
4. Remove old JSONB columns (game_configs.parameters, etc.)
5. Deploy with backward compatibility period if needed';

-- ============================================================================
-- Additional Features Enabled by Normalized Schema
-- ============================================================================

-- Parameter usage statistics
CREATE VIEW parameter_usage_stats AS
SELECT
    pd.key,
    pd.param_type,
    COUNT(DISTINCT gcpv.config_id) AS configs_using,
    COUNT(DISTINCT spv.session_id) AS sessions_using,
    COUNT(DISTINCT CASE WHEN pd.required THEN NULL ELSE gcpv.value_id END) AS optional_usage
FROM parameter_definitions pd
LEFT JOIN game_config_parameter_values gcpv ON pd.param_id = gcpv.param_id
LEFT JOIN session_parameter_values spv ON pd.param_id = spv.param_id
GROUP BY pd.param_id, pd.key, pd.param_type;

-- Find configs missing required parameters
CREATE VIEW configs_with_missing_required_params AS
SELECT
    gc.config_id,
    gc.name,
    pd.key AS missing_parameter,
    pd.description
FROM game_configs gc
CROSS JOIN parameter_definitions pd
WHERE pd.game_id = gc.game_id
  AND pd.required = true
  AND pd.default_value IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM game_config_parameter_values gcpv
    WHERE gcpv.config_id = gc.config_id AND gcpv.param_id = pd.param_id
  );

-- Parameter value distribution
CREATE VIEW parameter_value_distribution AS
SELECT
    pd.key,
    gcpv.value,
    COUNT(*) AS usage_count
FROM parameter_definitions pd
JOIN game_config_parameter_values gcpv ON pd.param_id = gcpv.param_id
GROUP BY pd.param_id, pd.key, gcpv.value
ORDER BY pd.key, usage_count DESC;
