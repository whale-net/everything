-- Rollback ManManV2 Initial Schema

-- Drop triggers
DROP TRIGGER IF EXISTS update_sessions_updated_at ON sessions;
DROP TRIGGER IF EXISTS update_server_game_configs_updated_at ON server_game_configs;
DROP TRIGGER IF EXISTS update_game_configs_updated_at ON game_configs;
DROP TRIGGER IF EXISTS update_games_updated_at ON games;
DROP TRIGGER IF EXISTS update_servers_updated_at ON servers;

-- Drop trigger function
DROP FUNCTION IF EXISTS update_updated_at_column();

-- Drop tables in reverse order of dependencies
DROP TABLE IF EXISTS server_ports;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS server_game_configs;
DROP TABLE IF EXISTS game_configs;
DROP TABLE IF EXISTS games;
DROP TABLE IF EXISTS servers;
