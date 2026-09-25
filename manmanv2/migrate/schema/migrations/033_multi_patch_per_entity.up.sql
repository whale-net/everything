-- Drop the unique constraint enforcing one-patch-per-entity
ALTER TABLE configuration_patches
    DROP CONSTRAINT configuration_patches_strategy_id_patch_level_entity_id_key;

-- Add ordering column; default 0 so existing patches start at the same priority
ALTER TABLE configuration_patches
    ADD COLUMN patch_order INT NOT NULL DEFAULT 0;

-- Recreate lookup index to include patch_order for ordered retrieval
DROP INDEX IF EXISTS idx_config_patches_lookup;
CREATE INDEX idx_config_patches_lookup
    ON configuration_patches(strategy_id, patch_level, entity_id, patch_order);

-- Recreate covering indexes to include patch_order
DROP INDEX IF EXISTS idx_config_patches_level_entity_covering;
CREATE INDEX idx_config_patches_level_entity_covering
    ON configuration_patches(patch_level, entity_id)
    INCLUDE (strategy_id, patch_content, patch_format, patch_order, created_at, updated_at);

DROP INDEX IF EXISTS idx_config_patches_strategy_level_covering;
CREATE INDEX idx_config_patches_strategy_level_covering
    ON configuration_patches(strategy_id, patch_level)
    INCLUDE (entity_id, patch_content, patch_format, patch_order, created_at, updated_at);

-- Recreate views with explicit patch_order column
-- Must DROP CASCADE because INSTEAD OF triggers depend on view
DROP VIEW IF EXISTS game_config_patches CASCADE;
DROP VIEW IF EXISTS server_game_config_patches CASCADE;
DROP VIEW IF EXISTS session_patches CASCADE;

CREATE VIEW game_config_patches AS
    SELECT patch_id, strategy_id, entity_id AS game_config_id,
           patch_content, patch_format, patch_order, created_at, updated_at
    FROM configuration_patches WHERE patch_level = 'game_config';

CREATE VIEW server_game_config_patches AS
    SELECT patch_id, strategy_id, entity_id AS server_game_config_id,
           patch_content, patch_format, patch_order, created_at, updated_at
    FROM configuration_patches WHERE patch_level = 'server_game_config';

CREATE VIEW session_patches AS
    SELECT patch_id, strategy_id, entity_id AS session_id,
           patch_content, patch_format, patch_order, created_at, updated_at
    FROM configuration_patches WHERE patch_level = 'session';

-- Recreate INSTEAD OF triggers with patch_order support

-- GameConfig patches insert trigger
CREATE OR REPLACE FUNCTION game_config_patches_insert()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO configuration_patches (strategy_id, patch_level, entity_id, patch_content, patch_format, patch_order)
    VALUES (NEW.strategy_id, 'game_config', NEW.game_config_id, NEW.patch_content, NEW.patch_format, COALESCE(NEW.patch_order, 0))
    RETURNING patch_id, strategy_id, entity_id, patch_content, patch_format, patch_order, created_at, updated_at
    INTO NEW.patch_id, NEW.strategy_id, NEW.game_config_id, NEW.patch_content, NEW.patch_format, NEW.patch_order, NEW.created_at, NEW.updated_at;
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
        patch_order = NEW.patch_order,
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
    INSERT INTO configuration_patches (strategy_id, patch_level, entity_id, patch_content, patch_format, patch_order)
    VALUES (NEW.strategy_id, 'server_game_config', NEW.server_game_config_id, NEW.patch_content, NEW.patch_format, COALESCE(NEW.patch_order, 0))
    RETURNING patch_id, strategy_id, entity_id, patch_content, patch_format, patch_order, created_at, updated_at
    INTO NEW.patch_id, NEW.strategy_id, NEW.server_game_config_id, NEW.patch_content, NEW.patch_format, NEW.patch_order, NEW.created_at, NEW.updated_at;
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
        patch_order = NEW.patch_order,
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
    INSERT INTO configuration_patches (strategy_id, patch_level, entity_id, patch_content, patch_format, patch_order)
    VALUES (NEW.strategy_id, 'session', NEW.session_id, NEW.patch_content, NEW.patch_format, COALESCE(NEW.patch_order, 0))
    RETURNING patch_id, strategy_id, entity_id, patch_content, patch_format, patch_order, created_at, updated_at
    INTO NEW.patch_id, NEW.strategy_id, NEW.session_id, NEW.patch_content, NEW.patch_format, NEW.patch_order, NEW.created_at, NEW.updated_at;
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
        patch_order = NEW.patch_order,
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
