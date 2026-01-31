-- ============================================================================
-- Normalized Parameter Schema
-- ============================================================================
-- Replaces JSONB parameter storage with properly normalized tables
-- for better query performance, referential integrity, and validation

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

-- Apply updated_at trigger
CREATE TRIGGER update_parameter_definitions_updated_at BEFORE UPDATE ON parameter_definitions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

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
CREATE INDEX idx_gc_param_values_lookup ON game_config_parameter_values(param_id, value);

-- Apply updated_at trigger
CREATE TRIGGER update_gc_param_values_updated_at BEFORE UPDATE ON game_config_parameter_values
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

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

-- Apply updated_at trigger
CREATE TRIGGER update_sgc_param_values_updated_at BEFORE UPDATE ON server_game_config_parameter_values
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

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

-- ----------------------------------------------------------------------------
-- Helpful Views for Common Queries
-- ----------------------------------------------------------------------------

-- Parameter usage statistics
CREATE VIEW parameter_usage_stats AS
SELECT
    pd.key,
    pd.param_type,
    COUNT(DISTINCT gcpv.config_id) AS configs_using,
    COUNT(DISTINCT spv.session_id) AS sessions_using
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
