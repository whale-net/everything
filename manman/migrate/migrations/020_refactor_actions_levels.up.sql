-- Migration 020: Refactor actions to use level-based system (like patches)
-- Simplifies the model: instead of game-level actions + visibility overrides,
-- actions can be defined at game/config/sgc levels

-- Step 1: Add new columns to action_definitions
ALTER TABLE action_definitions
    ADD COLUMN definition_level VARCHAR(50) DEFAULT 'game',
    ADD COLUMN entity_id BIGINT;

-- Step 2: Set entity_id to game_id for existing rows (all are game-level)
UPDATE action_definitions SET entity_id = game_id;

-- Step 3: Make entity_id NOT NULL now that it's populated
ALTER TABLE action_definitions ALTER COLUMN entity_id SET NOT NULL;

-- Step 4: Add check constraint for valid levels
ALTER TABLE action_definitions
    ADD CONSTRAINT check_action_definition_level
    CHECK (definition_level IN ('game', 'game_config', 'server_game_config'));

-- Step 5: Drop the old game_id column (entity_id replaces it)
ALTER TABLE action_definitions DROP COLUMN game_id;

-- Step 6: Update unique constraint to use new columns
ALTER TABLE action_definitions
    DROP CONSTRAINT action_definitions_game_id_name_key;

ALTER TABLE action_definitions
    ADD CONSTRAINT action_definitions_level_entity_name_key
    UNIQUE (definition_level, entity_id, name);

-- Step 7: Drop the visibility overrides table (no longer needed)
DROP TABLE IF EXISTS action_visibility_overrides;

-- Step 8: Update indexes
DROP INDEX IF EXISTS idx_action_definitions_game;
DROP INDEX IF EXISTS idx_action_definitions_enabled;
DROP INDEX IF EXISTS idx_action_definitions_order;

CREATE INDEX idx_action_definitions_level_entity ON action_definitions(definition_level, entity_id);
CREATE INDEX idx_action_definitions_level_entity_enabled ON action_definitions(definition_level, entity_id, enabled);
CREATE INDEX idx_action_definitions_display_order ON action_definitions(definition_level, entity_id, display_order);

-- Step 9: Update views to use new schema
DROP VIEW IF EXISTS action_summary;
DROP VIEW IF EXISTS action_with_inputs;
DROP VIEW IF EXISTS action_counts_by_game;

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
    ad.definition_level,
    ad.entity_id,
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
        jsonb_build_object(
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

-- View: Action counts by game (updated to count across all levels)
CREATE VIEW action_counts_by_game AS
SELECT
    g.game_id,
    g.name as game_name,
    COUNT(DISTINCT ad.action_id) FILTER (WHERE ad.definition_level = 'game') as game_level_actions,
    COUNT(DISTINCT ad.action_id) as total_actions,
    COUNT(DISTINCT ad.action_id) FILTER (WHERE ad.enabled) as enabled_actions,
    COUNT(DISTINCT ae.execution_id) as total_executions
FROM games g
LEFT JOIN action_definitions ad ON (
    (ad.definition_level = 'game' AND ad.entity_id = g.game_id)
    OR (ad.definition_level = 'game_config' AND ad.entity_id IN (
        SELECT config_id FROM game_configs WHERE game_id = g.game_id
    ))
    OR (ad.definition_level = 'server_game_config' AND ad.entity_id IN (
        SELECT sgc_id FROM server_game_configs sgc
        JOIN game_configs gc ON sgc.game_config_id = gc.config_id
        WHERE gc.game_id = g.game_id
    ))
)
LEFT JOIN action_executions ae ON ad.action_id = ae.action_id
GROUP BY g.game_id, g.name;

COMMENT ON TABLE action_definitions IS 'Actions can be defined at game, game_config, or server_game_config levels. When resolving actions for a session, all three levels are merged (game baseline + config additions + sgc additions).';
COMMENT ON COLUMN action_definitions.definition_level IS 'Level at which this action is defined: game (baseline), game_config (config-specific), or server_game_config (deployment-specific)';
COMMENT ON COLUMN action_definitions.entity_id IS 'ID of the entity: game_id, config_id, or sgc_id depending on definition_level';
