#!/usr/bin/env bash
set -euo pipefail

# This script adds a server.properties configuration strategy for Minecraft
# It creates a file_properties strategy that will render server.properties

API_URL="${API_URL:-localhost:50051}"

echo "Adding server.properties configuration strategy for Minecraft..."

# Get game ID
game_id=$(grpcurl -plaintext -d '{"name": "Minecraft"}' "$API_URL" manman.v1.ManManAPI/ListGames | jq -r '.games[] | select(.name == "Minecraft") | .gameId')

if [[ -z "$game_id" ]]; then
    echo "Error: Minecraft game not found"
    exit 1
fi

echo "Found Minecraft game (ID: $game_id)"

# Create server.properties strategy
echo "Creating server.properties strategy..."
strategy_payload="$(cat <<EOF
{
  "game_id": ${game_id},
  "name": "server.properties",
  "description": "Minecraft server configuration file",
  "strategy_type": "file_properties",
  "target_path": "/data/server.properties",
  "base_template": "# Minecraft Server Properties\nmotd=A Minecraft Server\nmax-players=20\ndifficulty=normal\npvp=true\nspawn-monsters=true\nview-distance=10\nonline-mode=true\ngamemode=survival\nallow-nether=true",
  "apply_order": 2
}
EOF
)"

strategy_id=$(grpcurl -plaintext -d "$strategy_payload" "$API_URL" manman.v1.ManManAPI/CreateConfigurationStrategy | jq -r '.strategy.strategyId')

if [[ -z "$strategy_id" || "$strategy_id" == "null" ]]; then
    echo "Error: Failed to create server.properties strategy"
    exit 1
fi

echo "Created server.properties strategy (ID: $strategy_id)"
echo ""
echo "✅ Server properties strategy created successfully!"
echo ""
echo "To test:"
echo "1. Stop any running Minecraft sessions"
echo "2. Start a new session - it should create /data/server.properties"
echo "3. Check the container's /data directory for the file"
