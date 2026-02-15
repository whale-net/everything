-- Migration 018: Game Actions System
-- Adds configurable action buttons for game sessions with dynamic inputs

-- 1. Action Definitions
--    Core table defining available actions per game
CREATE TABLE action_definitions (
    action_id BIGSERIAL PRIMARY KEY,
    game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL, -- Unique identifier (e.g., 'save_game', 'change_map')
    label VARCHAR(200) NOT NULL, -- Display name (e.g., 'Save Game', 'Change Map')
    description TEXT,
    command_template TEXT NOT NULL, -- Go template syntax: 'changelevel {{.map}}'
    display_order INT NOT NULL DEFAULT 0,
    group_name VARCHAR(100), -- Group actions together (e.g., 'Game Control', 'Admin')
    button_style VARCHAR(50) DEFAULT 'primary', -- CSS class hint (primary, success, danger, etc.)
    icon VARCHAR(100), -- Optional icon class (e.g., 'fa-save')
    requires_confirmation BOOLEAN DEFAULT false,
    confirmation_message TEXT,
    enabled BOOLEAN DEFAULT true, -- Can be disabled without deletion
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(game_id, name),
    CHECK (name ~ '^[a-z0-9_]+$'), -- Snake_case validation
    CHECK (button_style IN ('primary', 'secondary', 'success', 'danger', 'warning', 'info', 'light', 'dark'))
);

CREATE INDEX idx_action_definitions_game ON action_definitions(game_id);
CREATE INDEX idx_action_definitions_enabled ON action_definitions(game_id, enabled);
CREATE INDEX idx_action_definitions_order ON action_definitions(game_id, display_order);

-- 2. Action Input Fields
--    Defines input fields for parameterized actions
CREATE TABLE action_input_fields (
    field_id BIGSERIAL PRIMARY KEY,
    action_id BIGINT NOT NULL REFERENCES action_definitions(action_id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL, -- Field identifier (e.g., 'map', 'workshop_id')
    label VARCHAR(200) NOT NULL, -- Display label (e.g., 'Select Map', 'Workshop ID')
    field_type VARCHAR(50) NOT NULL, -- text, number, select, textarea, checkbox, radio
    required BOOLEAN DEFAULT false,
    placeholder TEXT,
    help_text TEXT,
    default_value TEXT,
    display_order INT NOT NULL DEFAULT 0,
    -- Validation fields
    pattern VARCHAR(500), -- Regex pattern for validation
    min_value NUMERIC,
    max_value NUMERIC,
    min_length INT,
    max_length INT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(action_id, name),
    CHECK (name ~ '^[a-z0-9_]+$'),
    CHECK (field_type IN ('text', 'number', 'select', 'textarea', 'checkbox', 'radio', 'email', 'url'))
);

CREATE INDEX idx_action_input_fields_action ON action_input_fields(action_id);
CREATE INDEX idx_action_input_fields_order ON action_input_fields(action_id, display_order);

-- 3. Action Input Options
--    Defines options for select/radio field types
CREATE TABLE action_input_options (
    option_id BIGSERIAL PRIMARY KEY,
    field_id BIGINT NOT NULL REFERENCES action_input_fields(field_id) ON DELETE CASCADE,
    value TEXT NOT NULL, -- Actual value used in template (e.g., 'de_dust2')
    label VARCHAR(200) NOT NULL, -- Display label (e.g., 'Dust 2')
    display_order INT NOT NULL DEFAULT 0,
    is_default BOOLEAN DEFAULT false,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(field_id, value)
);

CREATE INDEX idx_action_input_options_field ON action_input_options(field_id);
CREATE INDEX idx_action_input_options_order ON action_input_options(field_id, display_order);

-- 4. Action Visibility Overrides
--    Controls action visibility at different configuration levels
CREATE TABLE action_visibility_overrides (
    override_id BIGSERIAL PRIMARY KEY,
    action_id BIGINT NOT NULL REFERENCES action_definitions(action_id) ON DELETE CASCADE,
    override_level VARCHAR(50) NOT NULL, -- 'game_config', 'server_game_config', 'session'
    entity_id BIGINT NOT NULL, -- ID of game_config, server_game_config, or session
    enabled BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(action_id, override_level, entity_id),
    CHECK (override_level IN ('game_config', 'server_game_config', 'session'))
);

CREATE INDEX idx_action_visibility_level ON action_visibility_overrides(override_level, entity_id);
CREATE INDEX idx_action_visibility_action ON action_visibility_overrides(action_id);

-- 5. Action Executions
--    Audit log of all action executions
CREATE TABLE action_executions (
    execution_id BIGSERIAL PRIMARY KEY,
    action_id BIGINT NOT NULL REFERENCES action_definitions(action_id) ON DELETE CASCADE,
    session_id BIGINT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    triggered_by VARCHAR(200), -- Username or system identifier
    input_values JSONB, -- User-provided input values: {"map": "de_dust2", "workshop_id": "123"}
    rendered_command TEXT NOT NULL, -- Final command sent to stdin
    status VARCHAR(50) NOT NULL DEFAULT 'success', -- success, failed, validation_error
    error_message TEXT,
    executed_at TIMESTAMPTZ DEFAULT NOW(),
    CHECK (status IN ('success', 'failed', 'validation_error'))
);

CREATE INDEX idx_action_executions_session ON action_executions(session_id, executed_at DESC);
CREATE INDEX idx_action_executions_action ON action_executions(action_id, executed_at DESC);
CREATE INDEX idx_action_executions_time ON action_executions(executed_at DESC);

-- Triggers for updated_at
CREATE TRIGGER update_action_definitions_updated_at
    BEFORE UPDATE ON action_definitions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_action_input_fields_updated_at
    BEFORE UPDATE ON action_input_fields
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_action_input_options_updated_at
    BEFORE UPDATE ON action_input_options
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_action_visibility_overrides_updated_at
    BEFORE UPDATE ON action_visibility_overrides
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- Helper Views

-- View: Actions with input field counts
CREATE VIEW action_summary AS
SELECT
    ad.*,
    COUNT(DISTINCT aif.field_id) as input_field_count,
    BOOL_OR(aif.required) as has_required_fields
FROM action_definitions ad
LEFT JOIN action_input_fields aif ON ad.action_id = aif.action_id
GROUP BY ad.action_id;

-- View: Actions with denormalized inputs (for quick lookups)
CREATE VIEW action_with_inputs AS
SELECT
    ad.action_id,
    ad.game_id,
    ad.name,
    ad.label,
    ad.description,
    ad.command_template,
    ad.display_order,
    ad.group_name,
    ad.button_style,
    ad.icon,
    ad.requires_confirmation,
    ad.confirmation_message,
    ad.enabled,
    json_agg(
        DISTINCT jsonb_build_object(
            'field_id', aif.field_id,
            'name', aif.name,
            'label', aif.label,
            'field_type', aif.field_type,
            'required', aif.required,
            'placeholder', aif.placeholder,
            'help_text', aif.help_text,
            'default_value', aif.default_value,
            'display_order', aif.display_order,
            'pattern', aif.pattern,
            'min_value', aif.min_value,
            'max_value', aif.max_value,
            'min_length', aif.min_length,
            'max_length', aif.max_length,
            'options', (
                SELECT json_agg(
                    jsonb_build_object(
                        'option_id', aio.option_id,
                        'value', aio.value,
                        'label', aio.label,
                        'display_order', aio.display_order,
                        'is_default', aio.is_default
                    ) ORDER BY aio.display_order
                )
                FROM action_input_options aio
                WHERE aio.field_id = aif.field_id
            )
        ) ORDER BY aif.display_order
    ) FILTER (WHERE aif.field_id IS NOT NULL) as input_fields
FROM action_definitions ad
LEFT JOIN action_input_fields aif ON ad.action_id = aif.action_id
GROUP BY ad.action_id;

-- View: Action counts by game
CREATE VIEW action_counts_by_game AS
SELECT
    g.game_id,
    g.name as game_name,
    COUNT(DISTINCT ad.action_id) as total_actions,
    COUNT(DISTINCT ad.action_id) FILTER (WHERE ad.enabled) as enabled_actions,
    COUNT(DISTINCT ae.execution_id) as total_executions
FROM games g
LEFT JOIN action_definitions ad ON g.game_id = ad.game_id
LEFT JOIN action_executions ae ON ad.action_id = ae.action_id
GROUP BY g.game_id, g.name;
