# ManManV2 gRPC API Testing

Quick reference for testing the manmanv2 control plane API using grpcurl.
See `/grpcurl` for generic grpcurl documentation.

## Setup

### Prerequisites
- **Tilt running**: `tilt up` from project root
- **grpcurl installed**: `brew install grpcurl` or `go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest`

### Connection Details (from Tilt)
- **API Address**: `localhost:50052` (gRPC, port-forwarded from container:50051)
- **Service Package**: `manman.v1`
- **Service Name**: `ManManAPI`
- **Proto Files**: `manmanv2/protos/api.proto`, `manmanv2/protos/messages.proto`

## Quick Commands

### Explore the API
```bash
# List all services
grpcurl -plaintext localhost:50052 list

# List ManManAPI methods
grpcurl -plaintext localhost:50052 list manman.v1.ManManAPI

# Describe a method
grpcurl -plaintext localhost:50052 describe manman.v1.ManManAPI.ListServers

# Describe a message type
grpcurl -plaintext localhost:50052 describe manman.v1.Server
```

## API Operations by Category

### Servers

```bash
# List all servers
grpcurl -plaintext localhost:50052 manman.v1.ManManAPI/ListServers

# Create a server
grpcurl -plaintext -d '{"name": "production-1"}' localhost:50052 manman.v1.ManManAPI/CreateServer

# Get a server (replace SERVER_ID with actual ID, e.g., 1)
grpcurl -plaintext -d '{"server_id": 1}' localhost:50052 manman.v1.ManManAPI/GetServer

# Update a server
grpcurl -plaintext -d '{"server_id": 1, "name": "prod-updated"}' localhost:50052 manman.v1.ManManAPI/UpdateServer

# Delete a server
grpcurl -plaintext -d '{"server_id": 1}' localhost:50052 manman.v1.ManManAPI/DeleteServer
```

### Games

```bash
# List games
grpcurl -plaintext localhost:50052 manman.v1.ManManAPI/ListGames

# Create a game
grpcurl -plaintext -d '{
  "name": "Counter-Strike 2",
  "steam_app_id": "730"
}' localhost:50052 manman.v1.ManManAPI/CreateGame

# Get a game
grpcurl -plaintext -d '{"game_id": 1}' localhost:50052 manman.v1.ManManAPI/GetGame

# Update a game
grpcurl -plaintext -d '{
  "game_id": 1,
  "name": "CS2 Updated",
  "steam_app_id": "730"
}' localhost:50052 manman.v1.ManManAPI/UpdateGame

# Delete a game
grpcurl -plaintext -d '{"game_id": 1}' localhost:50052 manman.v1.ManManAPI/DeleteGame
```

### Game Configs

```bash
# List configs for a game
grpcurl -plaintext -d '{"game_id": 1}' localhost:50052 manman.v1.ManManAPI/ListGameConfigs

# Get a specific config
grpcurl -plaintext -d '{"config_id": 1}' localhost:50052 manman.v1.ManManAPI/GetGameConfig

# Create a config
grpcurl -plaintext -d '{
  "game_id": 1,
  "name": "default",
  "image": "myregistry/cs2:latest",
  "args_template": "--port {{.port}} --maxplayers {{.maxplayers}}",
  "env_template": {
    "GAME_MODE": "competitive",
    "DIFFICULTY": "normal"
  }
}' localhost:50052 manman.v1.ManManAPI/CreateGameConfig

# Update a config
grpcurl -plaintext -d '{
  "config_id": 1,
  "name": "default-v2",
  "image": "myregistry/cs2:v2"
}' localhost:50052 manman.v1.ManManAPI/UpdateGameConfig

# Delete a config
grpcurl -plaintext -d '{"config_id": 1}' localhost:50052 manman.v1.ManManAPI/DeleteGameConfig
```

### Sessions

```bash
# List all sessions
grpcurl -plaintext localhost:50052 manman.v1.ManManAPI/ListSessions

# List live sessions only
grpcurl -plaintext -d '{"live_only": true}' localhost:50052 manman.v1.ManManAPI/ListSessions

# List sessions with pagination
grpcurl -plaintext -d '{"page_size": 20}' localhost:50052 manman.v1.ManManAPI/ListSessions

# Get a specific session
grpcurl -plaintext -d '{"session_id": 1}' localhost:50052 manman.v1.ManManAPI/GetSession

# Start a session
grpcurl -plaintext -d '{"server_game_config_id": 1}' localhost:50052 manman.v1.ManManAPI/StartSession

# Force start a session (kill existing if needed)
grpcurl -plaintext -d '{"server_game_config_id": 1, "force": true}' localhost:50052 manman.v1.ManManAPI/StartSession

# Stop a session
grpcurl -plaintext -d '{"session_id": 1}' localhost:50052 manman.v1.ManManAPI/StopSession
```

### Game Actions

```bash
# List available actions for a game
grpcurl -plaintext -d '{"game_id": 1}' localhost:50052 manman.v1.ManManAPI/ListActionDefinitions

# Get actions available in a session
grpcurl -plaintext -d '{"session_id": 1}' localhost:50052 manman.v1.ManManAPI/GetSessionActions

# Execute an action on a session
grpcurl -plaintext -d '{
  "session_id": 1,
  "action_id": 5,
  "input_values": {
    "map": "de_dust2"
  }
}' localhost:50052 manman.v1.ManManAPI/ExecuteAction

# Create an action definition
grpcurl -plaintext -d '{
  "action": {
    "name": "save_game",
    "label": "Save Game",
    "description": "Save the game state",
    "command_template": "save",
    "display_order": 0
  }
}' localhost:50052 manman.v1.ManManAPI/CreateActionDefinition

# Delete an action
grpcurl -plaintext -d '{"action_id": 1}' localhost:50052 manman.v1.ManManAPI/DeleteActionDefinition
```

### Backups

```bash
# Create a backup
grpcurl -plaintext -d '{
  "session_id": 1,
  "description": "Before map change"
}' localhost:50052 manman.v1.ManManAPI/CreateBackup

# List backups
grpcurl -plaintext localhost:50052 manman.v1.ManManAPI/ListBackups

# List backups for a session
grpcurl -plaintext -d '{"session_id": 1}' localhost:50052 manman.v1.ManManAPI/ListBackups

# Get a backup
grpcurl -plaintext -d '{"backup_id": 1}' localhost:50052 manman.v1.ManManAPI/GetBackup

# Delete a backup
grpcurl -plaintext -d '{"backup_id": 1}' localhost:50052 manman.v1.ManManAPI/DeleteBackup
```

### Deployments

```bash
# Deploy a game config to a server
grpcurl -plaintext -d '{
  "server_id": 1,
  "game_config_id": 1,
  "port_bindings": [
    {
      "container_port": 27015,
      "host_port": 27015
    }
  ]
}' localhost:50052 manman.v1.ManManAPI/DeployGameConfig

# Validate a deployment before deploying
grpcurl -plaintext -d '{
  "server_id": 1,
  "game_config_id": 1,
  "port_bindings": [
    {
      "container_port": 27015,
      "host_port": 27015
    }
  ]
}' localhost:50052 manman.v1.ManManAPI/ValidateDeployment
```

## Seed Data

Before testing, populate the database with sample data:

```bash
# Set database URL (adjust if needed)
export DATABASE_URL="postgres://postgres:password@localhost:5432/manmanv2"

# Run seed scripts for action definitions
./manmanv2/scripts/seed_cs2_actions.sh
./manmanv2/scripts/seed_minecraft_actions.sh
./manmanv2/scripts/seed_l4d2_actions.sh
```

## Advanced: Using Proto Files for Better Reflection

```bash
cd manmanv2

grpcurl -plaintext \
  -import-path ./protos \
  -proto api.proto \
  -proto messages.proto \
  localhost:50052 manman.v1.ManManAPI/ListServers
```

## Debugging

```bash
# Verbose output with full request/response details
grpcurl -v -plaintext localhost:50052 manman.v1.ManManAPI/ListServers

# Format JSON output with jq
grpcurl -plaintext localhost:50052 manman.v1.ManManAPI/ListServers | jq

# Count results
grpcurl -plaintext localhost:50052 manman.v1.ManManAPI/ListServers | jq '.servers | length'
```

## Related Services (from Tilt)

- **Log Processor**: `localhost:50053` (streaming logs)
- **UI**: `http://localhost:8080`
- **PostgreSQL**: `localhost:5432` (user: postgres, pass: password)
- **RabbitMQ**: `localhost:5672` (user: rabbit, pass: password)

See Tiltfile at `manmanv2/Tiltfile` for full configuration.
