-- Add covering indexes for efficient queries
-- These indexes include commonly selected columns to avoid table lookups

-- Covering index for queries filtering by patch_level and entity_id
-- Includes patch_content and other commonly accessed columns
CREATE INDEX IF NOT EXISTS idx_config_patches_level_entity_covering
ON configuration_patches (patch_level, entity_id)
INCLUDE (strategy_id, patch_content, patch_format, created_at, updated_at);

-- Covering index for queries filtering by strategy_id and patch_level
-- Useful for getting all patches for a strategy at a specific level
CREATE INDEX IF NOT EXISTS idx_config_patches_strategy_level_covering
ON configuration_patches (strategy_id, patch_level)
INCLUDE (entity_id, patch_content, patch_format, created_at, updated_at);

-- Create views that expose patches as if they were separate tables
-- This provides a cleaner interface while maintaining the generic table design

-- View for GameConfig patches
CREATE OR REPLACE VIEW game_config_patches AS
SELECT
    patch_id,
    strategy_id,
    entity_id as game_config_id,
    patch_content,
    patch_format,
    created_at,
    updated_at
FROM configuration_patches
WHERE patch_level = 'game_config';

-- View for ServerGameConfig patches
CREATE OR REPLACE VIEW server_game_config_patches AS
SELECT
    patch_id,
    strategy_id,
    entity_id as server_game_config_id,
    patch_content,
    patch_format,
    created_at,
    updated_at
FROM configuration_patches
WHERE patch_level = 'server_game_config';

-- View for Session patches
CREATE OR REPLACE VIEW session_patches AS
SELECT
    patch_id,
    strategy_id,
    entity_id as session_id,
    patch_content,
    patch_format,
    created_at,
    updated_at
FROM configuration_patches
WHERE patch_level = 'session';

-- Create INSTEAD OF triggers to allow INSERT/UPDATE/DELETE through views
-- This makes the views fully functional like real tables

-- GameConfig patches insert trigger
CREATE OR REPLACE FUNCTION game_config_patches_insert()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO configuration_patches (strategy_id, patch_level, entity_id, patch_content, patch_format)
    VALUES (NEW.strategy_id, 'game_config', NEW.game_config_id, NEW.patch_content, NEW.patch_format)
    RETURNING patch_id, strategy_id, entity_id, patch_content, patch_format, created_at, updated_at
    INTO NEW.patch_id, NEW.strategy_id, NEW.game_config_id, NEW.patch_content, NEW.patch_format, NEW.created_at, NEW.updated_at;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER game_config_patches_insert_trigger
INSTEAD OF INSERT ON game_config_patches
FOR EACH ROW EXECUTE FUNCTION game_config_patches_insert();

-- GameConfig patches update trigger
CREATE OR REPLACE FUNCTION game_config_patches_update()
RETURNS TRIGGER AS $$
BEGIN
    UPDATE configuration_patches
    SET patch_content = NEW.patch_content,
        patch_format = NEW.patch_format,
        updated_at = CURRENT_TIMESTAMP
    WHERE patch_id = OLD.patch_id AND patch_level = 'game_config';
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER game_config_patches_update_trigger
INSTEAD OF UPDATE ON game_config_patches
FOR EACH ROW EXECUTE FUNCTION game_config_patches_update();

-- GameConfig patches delete trigger
CREATE OR REPLACE FUNCTION game_config_patches_delete()
RETURNS TRIGGER AS $$
BEGIN
    DELETE FROM configuration_patches
    WHERE patch_id = OLD.patch_id AND patch_level = 'game_config';
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER game_config_patches_delete_trigger
INSTEAD OF DELETE ON game_config_patches
FOR EACH ROW EXECUTE FUNCTION game_config_patches_delete();

-- ServerGameConfig patches insert trigger
CREATE OR REPLACE FUNCTION server_game_config_patches_insert()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO configuration_patches (strategy_id, patch_level, entity_id, patch_content, patch_format)
    VALUES (NEW.strategy_id, 'server_game_config', NEW.server_game_config_id, NEW.patch_content, NEW.patch_format)
    RETURNING patch_id, strategy_id, entity_id, patch_content, patch_format, created_at, updated_at
    INTO NEW.patch_id, NEW.strategy_id, NEW.server_game_config_id, NEW.patch_content, NEW.patch_format, NEW.created_at, NEW.updated_at;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER server_game_config_patches_insert_trigger
INSTEAD OF INSERT ON server_game_config_patches
FOR EACH ROW EXECUTE FUNCTION server_game_config_patches_insert();

-- ServerGameConfig patches update trigger
CREATE OR REPLACE FUNCTION server_game_config_patches_update()
RETURNS TRIGGER AS $$
BEGIN
    UPDATE configuration_patches
    SET patch_content = NEW.patch_content,
        patch_format = NEW.patch_format,
        updated_at = CURRENT_TIMESTAMP
    WHERE patch_id = OLD.patch_id AND patch_level = 'server_game_config';
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER server_game_config_patches_update_trigger
INSTEAD OF UPDATE ON server_game_config_patches
FOR EACH ROW EXECUTE FUNCTION server_game_config_patches_update();

-- ServerGameConfig patches delete trigger
CREATE OR REPLACE FUNCTION server_game_config_patches_delete()
RETURNS TRIGGER AS $$
BEGIN
    DELETE FROM configuration_patches
    WHERE patch_id = OLD.patch_id AND patch_level = 'server_game_config';
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER server_game_config_patches_delete_trigger
INSTEAD OF DELETE ON server_game_config_patches
FOR EACH ROW EXECUTE FUNCTION server_game_config_patches_delete();

-- Session patches insert trigger
CREATE OR REPLACE FUNCTION session_patches_insert()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO configuration_patches (strategy_id, patch_level, entity_id, patch_content, patch_format)
    VALUES (NEW.strategy_id, 'session', NEW.session_id, NEW.patch_content, NEW.patch_format)
    RETURNING patch_id, strategy_id, entity_id, patch_content, patch_format, created_at, updated_at
    INTO NEW.patch_id, NEW.strategy_id, NEW.session_id, NEW.patch_content, NEW.patch_format, NEW.created_at, NEW.updated_at;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER session_patches_insert_trigger
INSTEAD OF INSERT ON session_patches
FOR EACH ROW EXECUTE FUNCTION session_patches_insert();

-- Session patches update trigger
CREATE OR REPLACE FUNCTION session_patches_update()
RETURNS TRIGGER AS $$
BEGIN
    UPDATE configuration_patches
    SET patch_content = NEW.patch_content,
        patch_format = NEW.patch_format,
        updated_at = CURRENT_TIMESTAMP
    WHERE patch_id = OLD.patch_id AND patch_level = 'session';
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER session_patches_update_trigger
INSTEAD OF UPDATE ON session_patches
FOR EACH ROW EXECUTE FUNCTION session_patches_update();

-- Session patches delete trigger
CREATE OR REPLACE FUNCTION session_patches_delete()
RETURNS TRIGGER AS $$
BEGIN
    DELETE FROM configuration_patches
    WHERE patch_id = OLD.patch_id AND patch_level = 'session';
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER session_patches_delete_trigger
INSTEAD OF DELETE ON session_patches
FOR EACH ROW EXECUTE FUNCTION session_patches_delete();
