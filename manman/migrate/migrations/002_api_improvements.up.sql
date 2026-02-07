-- Server capabilities table for tracking resource availability (time series)
CREATE TABLE IF NOT EXISTS server_capabilities (
    capability_id BIGSERIAL PRIMARY KEY,
    server_id BIGINT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
    total_memory_mb INT NOT NULL,
    available_memory_mb INT NOT NULL,
    cpu_cores INT NOT NULL,
    available_cpu_millicores INT NOT NULL,
    docker_version VARCHAR(50),
    recorded_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Index for querying latest capabilities by server
CREATE INDEX IF NOT EXISTS idx_server_capabilities_server_recorded ON server_capabilities(server_id, recorded_at DESC);

-- Log references table for tracking log files (local storage)
CREATE TABLE IF NOT EXISTS log_references (
    log_id BIGSERIAL PRIMARY KEY,
    session_id BIGINT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    file_path TEXT NOT NULL,  -- Local file path (stub for S3)
    start_time TIMESTAMP NOT NULL,
    end_time TIMESTAMP NOT NULL,
    line_count INT NOT NULL,
    source VARCHAR(20) NOT NULL,  -- "stdout" | "stderr" | "wrapper"
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Indexes for log references
CREATE INDEX IF NOT EXISTS idx_log_refs_session_id ON log_references(session_id);
CREATE INDEX IF NOT EXISTS idx_log_refs_start_time ON log_references(start_time);
