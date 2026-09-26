-- Workshop Addon Management: workshop_installations table
-- Tracks workshop addon installations per ServerGameConfig

CREATE TABLE IF NOT EXISTS workshop_installations (
    installation_id BIGSERIAL PRIMARY KEY,
    sgc_id BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
    addon_id BIGINT NOT NULL REFERENCES workshop_addons(addon_id) ON DELETE CASCADE,
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    installation_path VARCHAR(1000) NOT NULL,
    progress_percent INTEGER DEFAULT 0 CHECK (progress_percent >= 0 AND progress_percent <= 100),
    error_message TEXT,
    download_started_at TIMESTAMP,
    download_completed_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(sgc_id, addon_id)
);

-- Indexes for efficient querying
CREATE INDEX IF NOT EXISTS idx_workshop_installations_sgc_id ON workshop_installations(sgc_id);
CREATE INDEX IF NOT EXISTS idx_workshop_installations_addon_id ON workshop_installations(addon_id);
CREATE INDEX IF NOT EXISTS idx_workshop_installations_status ON workshop_installations(status);

-- Apply updated_at trigger
CREATE TRIGGER update_workshop_installations_updated_at BEFORE UPDATE ON workshop_installations
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- Status values: 'pending', 'downloading', 'installed', 'failed', 'removed'
