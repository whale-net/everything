-- Remove server-level allowed host-port ranges

DROP INDEX IF EXISTS idx_server_allowed_port_ranges_server;
DROP TABLE IF EXISTS server_allowed_port_ranges;
