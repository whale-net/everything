#!/usr/bin/env bash
set -euo pipefail

# Seed Satisfactory action definitions using the API.
# Requires: grpcurl, python3

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

CONTROL_API_ADDR="${CONTROL_API_ADDR:-localhost:50052}"
USE_TLS="${USE_TLS:-auto}"
INSECURE_TLS="${INSECURE_TLS:-false}"

resolve_tls

echo "════════════════════════════════════════════════════"
echo "  Seeding Satisfactory Game Actions"
echo "════════════════════════════════════════════════════"
echo "GRPC API:  ${CONTROL_API_ADDR}"
echo ""

require_grpcurl
setup_auth
test_api_connectivity

echo "Finding Satisfactory game..."
game_id="$(find_game_id_by_name "Satisfactory")"
if [[ -z "${game_id}" ]]; then
  echo "✗ Satisfactory game not found. Run load-satisfactory-config.sh first."
  exit 1
fi
echo "✓ Found Satisfactory game (ID: ${game_id})"
echo ""

echo "Creating actions..."

# 1. Kick player
echo "  Creating 'Kick Player' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "kick_player",
    "label": "Kick Player",
    "description": "Kick a player from the server",
    "command_template": "kick {{.player}}",
    "display_order": 0,
    "group_name": "Player Management",
    "button_style": "warning",
    "icon": "fa-user-xmark",
    "requires_confirmation": true,
    "confirmation_message": "Are you sure you want to kick {{.player}}?",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "player",
      "label": "Player Name",
      "field_type": "text",
      "required": true,
      "display_order": 0,
      "help_text": "Name of the player to kick"
    }
  ]
}
EOF
)"

# 2. Save game
echo "  Creating 'Save Game' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "save_game",
    "label": "Save Game",
    "description": "Force save the current game state to disk",
    "command_template": "server.SaveGame {{.save_name}}",
    "display_order": 1,
    "group_name": "World Management",
    "button_style": "success",
    "icon": "fa-floppy-disk",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "save_name",
      "label": "Save Name",
      "field_type": "text",
      "required": false,
      "display_order": 0,
      "help_text": "Name for the save file (leave blank for default)"
    }
  ]
}
EOF
)"

# 3. Set max players
echo "  Creating 'Set Max Players' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "set_max_players",
    "label": "Set Max Players",
    "description": "Change the maximum number of players allowed on the server",
    "command_template": "server.SetMaxPlayerCount {{.count}}",
    "display_order": 2,
    "group_name": "Server Settings",
    "button_style": "primary",
    "icon": "fa-users",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "count",
      "label": "Max Players",
      "field_type": "text",
      "required": true,
      "display_order": 0,
      "help_text": "Maximum number of concurrent players (1-4)"
    }
  ]
}
EOF
)"

# 4. Broadcast message
echo "  Creating 'Broadcast Message' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "broadcast_message",
    "label": "Broadcast Message",
    "description": "Send a message to all players on the server",
    "command_template": "server.BroadcastMessage {{.message}}",
    "display_order": 3,
    "group_name": "Communication",
    "button_style": "primary",
    "icon": "fa-comment",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "message",
      "label": "Message",
      "field_type": "text",
      "required": true,
      "display_order": 0,
      "help_text": "Message to broadcast to all players"
    }
  ]
}
EOF
)"

echo ""
echo "✔ Satisfactory actions seeded successfully!"
echo ""
echo "Summary:"
echo "  Game ID: ${game_id}"
echo "  Actions created:"
echo "    - kick_player       (Player Management)"
echo "    - save_game         (World Management)"
echo "    - set_max_players   (Server Settings)"
echo "    - broadcast_message (Communication)"
echo ""
