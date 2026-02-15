-- Migration 020 Rollback: Restore original game-level + visibility overrides model

-- Drop updated views
DROP VIEW IF EXISTS action_counts_by_game;
DROP VIEW IF EXISTS action_with_inputs;
DROP VIEW IF EXISTS action_summary;

-- Recreate action_visibility_overrides table
CREATE TABLE action_visibility_overrides (
    override_id BIGSERIAL PRIMARY KEY,
    action_id BIGINT NOT NULL REFERENCES action_definitions(action_id) ON DELETE CASCADE,
    override_level VARCHAR(50) NOT NULL,
    entity_id BIGINT NOT NULL,
    enabled BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(action_id, override_level, entity_id),
    CHECK (override_level IN ('game_config', 'server_game_config', 'session'))
);

CREATE INDEX idx_action_visibility_level ON action_visibility_overrides(override_level, entity_id);
CREATE INDEX idx_action_visibility_action ON action_visibility_overrides(action_id);

CREATE TRIGGER update_action_visibility_overrides_updated_at
    BEFORE UPDATE ON action_visibility_overrides
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- Delete non-game-level actions (can't rollback to old schema with them)
DELETE FROM action_definitions WHERE definition_level != 'game';

-- Add back game_id column
ALTER TABLE action_definitions ADD COLUMN game_id BIGINT;

-- Copy entity_id back to game_id for game-level actions
UPDATE action_definitions SET game_id = entity_id WHERE definition_level = 'game';

-- Make game_id NOT NULL and add foreign key
ALTER TABLE action_definitions ALTER COLUMN game_id SET NOT NULL;
ALTER TABLE action_definitions ADD CONSTRAINT action_definitions_game_id_fkey
    FOREIGN KEY (game_id) REFERENCES games(game_id) ON DELETE CASCADE;

-- Drop new columns and constraints
ALTER TABLE action_definitions DROP CONSTRAINT check_action_definition_level;
ALTER TABLE action_definitions DROP CONSTRAINT action_definitions_level_entity_name_key;
ALTER TABLE action_definitions DROP COLUMN definition_level;
ALTER TABLE action_definitions DROP COLUMN entity_id;

-- Restore original unique constraint
ALTER TABLE action_definitions ADD CONSTRAINT action_definitions_game_id_name_key
    UNIQUE(game_id, name);

-- Restore original indexes
DROP INDEX IF EXISTS idx_action_definitions_level_entity;
DROP INDEX IF EXISTS idx_action_definitions_level_entity_enabled;
DROP INDEX IF EXISTS idx_action_definitions_display_order;

CREATE INDEX idx_action_definitions_game ON action_definitions(game_id);
CREATE INDEX idx_action_definitions_enabled ON action_definitions(game_id, enabled);
CREATE INDEX idx_action_definitions_order ON action_definitions(game_id, display_order);

-- Restore original views
CREATE VIEW action_summary AS
SELECT
    ad.*,
    COUNT(DISTINCT aif.field_id) as input_field_count,
    BOOL_OR(aif.required) as has_required_fields
FROM action_definitions ad
LEFT JOIN action_input_fields aif ON ad.action_id = aif.action_id
GROUP BY ad.action_id;

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
