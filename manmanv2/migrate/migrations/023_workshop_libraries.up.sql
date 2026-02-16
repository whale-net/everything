-- Workshop Addon Management: workshop_libraries tables
-- Stores custom addon collections and library hierarchies

-- workshop_libraries: Custom collections of workshop addons
CREATE TABLE IF NOT EXISTS workshop_libraries (
    library_id BIGSERIAL PRIMARY KEY,
    game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    description TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(game_id, name)
);

-- Index for efficient game-based queries
CREATE INDEX IF NOT EXISTS idx_workshop_libraries_game_id ON workshop_libraries(game_id);

-- Apply updated_at trigger
CREATE TRIGGER update_workshop_libraries_updated_at BEFORE UPDATE ON workshop_libraries
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- workshop_library_addons: Junction table linking libraries to addons
CREATE TABLE IF NOT EXISTS workshop_library_addons (
    library_id BIGINT NOT NULL REFERENCES workshop_libraries(library_id) ON DELETE CASCADE,
    addon_id BIGINT NOT NULL REFERENCES workshop_addons(addon_id) ON DELETE CASCADE,
    display_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (library_id, addon_id)
);

-- Indexes for efficient querying
CREATE INDEX IF NOT EXISTS idx_workshop_library_addons_library_id ON workshop_library_addons(library_id);
CREATE INDEX IF NOT EXISTS idx_workshop_library_addons_addon_id ON workshop_library_addons(addon_id);

-- workshop_library_references: Library-to-library references for hierarchies
CREATE TABLE IF NOT EXISTS workshop_library_references (
    parent_library_id BIGINT NOT NULL REFERENCES workshop_libraries(library_id) ON DELETE CASCADE,
    child_library_id BIGINT NOT NULL REFERENCES workshop_libraries(library_id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (parent_library_id, child_library_id),
    CHECK (parent_library_id != child_library_id)
);

-- Indexes for efficient querying
CREATE INDEX IF NOT EXISTS idx_workshop_library_refs_parent ON workshop_library_references(parent_library_id);
CREATE INDEX IF NOT EXISTS idx_workshop_library_refs_child ON workshop_library_references(child_library_id);
