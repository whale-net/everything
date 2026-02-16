-- Remove 3rd party image support columns
ALTER TABLE game_configs
DROP COLUMN IF EXISTS entrypoint,
DROP COLUMN IF EXISTS command;
