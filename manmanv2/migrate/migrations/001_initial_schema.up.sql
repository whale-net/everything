-- ManManV2 Initial Schema
-- Creates tables for game server management

-- Server: Physical/virtual machines running host managers
CREATE TABLE IF NOT EXISTS servers (
    server_id BIGSERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL UNIQUE,
    status VARCHAR(50) NOT NULL DEFAULT 'offline',
    last_seen TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_servers_status ON servers(status);
CREATE INDEX IF NOT EXISTS idx_servers_last_seen ON servers(last_seen);

-- Game: Game definitions (e.g., Minecraft, Valheim)
CREATE TABLE IF NOT EXISTS games (
    game_id BIGSERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL UNIQUE,
    steam_app_id VARCHAR(50),
    metadata JSONB DEFAULT '{}',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_games_name ON games(name);
CREATE INDEX IF NOT EXISTS idx_games_steam_app_id ON games(steam_app_id) WHERE steam_app_id IS NOT NULL;

-- GameConfig: Presets/templates for running games
CREATE TABLE IF NOT EXISTS game_configs (
    config_id BIGSERIAL PRIMARY KEY,
    game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    image VARCHAR(500) NOT NULL,
    args_template TEXT,
    env_template JSONB DEFAULT '{}',
    files JSONB DEFAULT '{}',
    parameters JSONB DEFAULT '{}',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(game_id, name)
);

CREATE INDEX IF NOT EXISTS idx_game_configs_game_id ON game_configs(game_id);

-- ServerGameConfig: Game configs deployed on specific servers
CREATE TABLE IF NOT EXISTS server_game_configs (
    sgc_id BIGSERIAL PRIMARY KEY,
    server_id BIGINT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
    game_config_id BIGINT NOT NULL REFERENCES game_configs(config_id) ON DELETE CASCADE,
    port_bindings JSONB DEFAULT '{}',
    parameters JSONB DEFAULT '{}',
    status VARCHAR(50) NOT NULL DEFAULT 'inactive',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(server_id, game_config_id)
);

CREATE INDEX IF NOT EXISTS idx_sgc_server_id ON server_game_configs(server_id);
CREATE INDEX IF NOT EXISTS idx_sgc_game_config_id ON server_game_configs(game_config_id);
CREATE INDEX IF NOT EXISTS idx_sgc_status ON server_game_configs(status);

-- Session: Executions of ServerGameConfigs
CREATE TABLE IF NOT EXISTS sessions (
    session_id BIGSERIAL PRIMARY KEY,
    sgc_id BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
    started_at TIMESTAMP,
    ended_at TIMESTAMP,
    exit_code INTEGER,
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    parameters JSONB DEFAULT '{}',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_sessions_sgc_id ON sessions(sgc_id);
CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status);
CREATE INDEX IF NOT EXISTS idx_sessions_started_at ON sessions(started_at);

-- ServerPort: Port allocation tracking
CREATE TABLE IF NOT EXISTS server_ports (
    server_id BIGINT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
    port INTEGER NOT NULL,
    protocol VARCHAR(10) NOT NULL CHECK (protocol IN ('TCP', 'UDP')),
    sgc_id BIGINT REFERENCES server_game_configs(sgc_id) ON DELETE SET NULL,
    allocated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (server_id, port, protocol)
);

CREATE INDEX IF NOT EXISTS idx_server_ports_sgc_id ON server_ports(sgc_id) WHERE sgc_id IS NOT NULL;

-- Updated_at trigger function
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ language 'plpgsql';

-- Apply updated_at triggers
CREATE TRIGGER update_servers_updated_at BEFORE UPDATE ON servers
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_games_updated_at BEFORE UPDATE ON games
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_game_configs_updated_at BEFORE UPDATE ON game_configs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_server_game_configs_updated_at BEFORE UPDATE ON server_game_configs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_sessions_updated_at BEFORE UPDATE ON sessions
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
