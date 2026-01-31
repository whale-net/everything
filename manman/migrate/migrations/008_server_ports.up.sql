-- Create server_ports table for port allocation tracking
CREATE TABLE IF NOT EXISTS server_ports (
    server_id BIGINT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
    port INTEGER NOT NULL CHECK (port >= 1 AND port <= 65535),
    protocol VARCHAR(10) NOT NULL CHECK (protocol IN ('TCP', 'UDP')),
    sgc_id BIGINT REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
    allocated_at TIMESTAMP NOT NULL DEFAULT NOW(),

    -- Unique constraint: each port+protocol combination on a server can only be allocated once
    CONSTRAINT pk_server_ports PRIMARY KEY (server_id, port, protocol),

    -- Index for querying by SGCID
    CONSTRAINT idx_server_ports_sgc_id UNIQUE (sgc_id, server_id, port, protocol)
);

-- Index for efficient queries by SGCID
CREATE INDEX idx_server_ports_sgc_id ON server_ports(sgc_id);

-- Index for efficient availability checks
CREATE INDEX idx_server_ports_server_id ON server_ports(server_id);

-- Comments for documentation
COMMENT ON TABLE server_ports IS 'Tracks port allocations on servers to prevent conflicts between ServerGameConfigs';
COMMENT ON COLUMN server_ports.server_id IS 'Server where the port is allocated';
COMMENT ON COLUMN server_ports.port IS 'Port number (1-65535)';
COMMENT ON COLUMN server_ports.protocol IS 'TCP or UDP';
COMMENT ON COLUMN server_ports.sgc_id IS 'ServerGameConfig that owns this port allocation';
COMMENT ON COLUMN server_ports.allocated_at IS 'When the port was allocated';
