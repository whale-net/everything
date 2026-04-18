#!/usr/bin/env bash
set -euo pipefail

# Seed Necesse action definitions using the API.
# Requires: grpcurl, python3

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

CONTROL_API_ADDR="${CONTROL_API_ADDR:-localhost:50052}"
USE_TLS="${USE_TLS:-auto}"
INSECURE_TLS="${INSECURE_TLS:-false}"

resolve_tls

echo "════════════════════════════════════════════════════"
echo "  Seeding Necesse Game Actions"
echo "════════════════════════════════════════════════════"
echo "GRPC API:  ${CONTROL_API_ADDR}"
echo ""

require_grpcurl
setup_auth
test_api_connectivity

echo "Finding Necesse game..."
game_id="$(find_game_id_by_name "Necesse")"
if [[ -z "${game_id}" ]]; then
  echo "✗ Necesse game not found. Run load-necesse-config.sh first."
  exit 1
fi
echo "✓ Found Necesse game (ID: ${game_id})"
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

# 2. Ban player
echo "  Creating 'Ban Player' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "ban_player",
    "label": "Ban Player",
    "description": "Ban a player from the server",
    "command_template": "ban {{.player}}",
    "display_order": 1,
    "group_name": "Player Management",
    "button_style": "danger",
    "icon": "fa-ban",
    "requires_confirmation": true,
    "confirmation_message": "Are you sure you want to ban {{.player}}?",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "player",
      "label": "Player Name",
      "field_type": "text",
      "required": true,
      "display_order": 0,
      "help_text": "Name of the player to ban"
    }
  ]
}
EOF
)"

# 3. Save world
echo "  Creating 'Save World' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "save_world",
    "label": "Save World",
    "description": "Force save the current world state to disk",
    "command_template": "save",
    "display_order": 2,
    "group_name": "World Management",
    "button_style": "success",
    "icon": "fa-floppy-disk",
    "enabled": true
  }
}
EOF
)"

# 4. Say (broadcast message)
echo "  Creating 'Broadcast Message' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "say",
    "label": "Broadcast Message",
    "description": "Send a message to all players on the server",
    "command_template": "say {{.message}}",
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
echo "✔ Necesse actions seeded successfully!"
echo ""
echo "Summary:"
echo "  Game ID: ${game_id}"
echo "  Actions created:"
echo "    - kick_player  (Player Management)"
echo "    - ban_player   (Player Management)"
echo "    - save_world   (World Management)"
echo "    - say          (Communication)"
echo ""
