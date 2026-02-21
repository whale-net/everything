CREATE TABLE IF NOT EXISTS sgc_workshop_libraries (
    sgc_id     BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
    library_id BIGINT NOT NULL REFERENCES workshop_libraries(library_id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (sgc_id, library_id)
);
CREATE INDEX IF NOT EXISTS idx_sgc_workshop_libraries_sgc_id ON sgc_workshop_libraries(sgc_id);
CREATE INDEX IF NOT EXISTS idx_sgc_workshop_libraries_library_id ON sgc_workshop_libraries(library_id);
