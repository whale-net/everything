-- Workshop Addon Management: workshop_addons table
-- Stores workshop addon metadata for game workshop content (Steam Workshop, etc.)

CREATE TABLE IF NOT EXISTS workshop_addons (
    addon_id BIGSERIAL PRIMARY KEY,
    game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
    workshop_id VARCHAR(255) NOT NULL,
    platform_type VARCHAR(50) NOT NULL DEFAULT 'steam_workshop',
    name VARCHAR(500) NOT NULL,
    description TEXT,
    file_size_bytes BIGINT,
    installation_path VARCHAR(1000),
    is_collection BOOLEAN NOT NULL DEFAULT FALSE,
    is_deprecated BOOLEAN NOT NULL DEFAULT FALSE,
    metadata JSONB DEFAULT '{}',
    last_updated TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(game_id, workshop_id, platform_type)
);

-- Indexes for efficient querying
CREATE INDEX IF NOT EXISTS idx_workshop_addons_game_id ON workshop_addons(game_id);
CREATE INDEX IF NOT EXISTS idx_workshop_addons_workshop_id ON workshop_addons(workshop_id);
CREATE INDEX IF NOT EXISTS idx_workshop_addons_platform ON workshop_addons(platform_type);
CREATE INDEX IF NOT EXISTS idx_workshop_addons_deprecated ON workshop_addons(is_deprecated) WHERE is_deprecated = FALSE;

-- Apply updated_at trigger
CREATE TRIGGER update_workshop_addons_updated_at BEFORE UPDATE ON workshop_addons
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
