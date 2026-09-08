-- Server-level allowed host-port ranges (#2080 / #2095, FR12 storage half).
-- Additive per NFR2/LB1: a server may have zero or more ranges; with no
-- rows, host-port assignment stays unconstrained (exactly the pre-038
-- behavior). Reuses the wire model's PortRange shape (start/end/protocol,
-- manmanv2/protos/messages.proto). ServerCapabilities.available_ports is
-- dormant and deliberately NOT built on.

CREATE TABLE IF NOT EXISTS server_allowed_port_ranges (
    range_id BIGSERIAL PRIMARY KEY,
    server_id BIGINT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
    start_port INTEGER NOT NULL,
    end_port INTEGER NOT NULL,
    protocol VARCHAR(16) NOT NULL DEFAULT 'TCP',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT server_allowed_port_ranges_port_order CHECK (start_port >= 1 AND end_port >= start_port),
    CONSTRAINT server_allowed_port_ranges_protocol CHECK (protocol IN ('TCP', 'UDP'))
);

CREATE INDEX IF NOT EXISTS idx_server_allowed_port_ranges_server
    ON server_allowed_port_ranges(server_id);
