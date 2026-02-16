-- Add support for 3rd party Docker images by allowing entrypoint/command overrides

ALTER TABLE game_configs
ADD COLUMN IF NOT EXISTS entrypoint JSONB DEFAULT NULL,
ADD COLUMN IF NOT EXISTS command JSONB DEFAULT NULL;

COMMENT ON COLUMN game_configs.entrypoint IS 'Override Docker ENTRYPOINT (array of strings stored as JSONB). Use for 3rd party images with incompatible entrypoints.';
COMMENT ON COLUMN game_configs.command IS 'Override Docker CMD (array of strings stored as JSONB). Alternative to args_template for complex commands.';

-- Note: args_template and command serve similar purposes but for different use cases:
-- - args_template: Template string with {{param}} substitution (e.g., "--max-players={{max_players}}")
-- - command: Raw command array for compatibility with Docker CMD override
