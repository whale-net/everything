#!/usr/bin/env bash
set -euo pipefail

# Seed Arma Reforger action definitions using the API.
# Requires: grpcurl, python3

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

CONTROL_API_ADDR="${CONTROL_API_ADDR:-localhost:50052}"
USE_TLS="${USE_TLS:-auto}"
INSECURE_TLS="${INSECURE_TLS:-false}"

resolve_tls

echo "════════════════════════════════════════════════════"
echo "  Seeding Arma Reforger Game Actions"
echo "════════════════════════════════════════════════════"
echo "GRPC API:  ${CONTROL_API_ADDR}"
echo ""

require_grpcurl
setup_auth
test_api_connectivity

echo "Finding Arma Reforger game..."
game_id="$(find_game_id_by_name "Arma Reforger")"
if [[ -z "${game_id}" ]]; then
  echo "✗ Arma Reforger game not found. Run load-reforger-config.sh first."
  exit 1
fi
echo "✓ Found Arma Reforger game (ID: ${game_id})"
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
    "command_template": "#kick {{.player}}",
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
    "command_template": "#ban {{.player}}",
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

# 3. Shutdown server
echo "  Creating 'Shutdown Server' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "shutdown",
    "label": "Shutdown Server",
    "description": "Gracefully shut down the server",
    "command_template": "#shutdown",
    "display_order": 2,
    "group_name": "Server Control",
    "button_style": "danger",
    "icon": "fa-power-off",
    "requires_confirmation": true,
    "confirmation_message": "Are you sure you want to shut down the server?",
    "enabled": true
  }
}
EOF
)"

# 4. Change mission
echo "  Creating 'Change Mission' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "change_mission",
    "label": "Change Mission",
    "description": "Switch to a different mission/scenario",
    "command_template": "#mission {{.scenario_id}}",
    "display_order": 3,
    "group_name": "Server Control",
    "button_style": "primary",
    "icon": "fa-map",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "scenario_id",
      "label": "Scenario",
      "field_type": "select",
      "required": true,
      "display_order": 0,
      "help_text": "Select the mission to load"
    }
  ],
  "input_options": [
    {
      "value": "{ECC61978EDCC2B5A}Missions/23_Campaign.conf",
      "label": "Campaign",
      "display_order": 0,
      "is_default": true
    },
    {
      "value": "{59AD59368755F41A}Missions/21_GM_Eden.conf",
      "label": "Game Master - Eden",
      "display_order": 1
    },
    {
      "value": "{3F2E005F43DBD2F8}Missions/22_GM_Arland.conf",
      "label": "Game Master - Arland",
      "display_order": 2
    }
  ]
}
EOF
)"

# 5. Broadcast message
echo "  Creating 'Broadcast Message' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "say",
    "label": "Broadcast Message",
    "description": "Send a message to all players on the server",
    "command_template": "#say {{.message}}",
    "display_order": 4,
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
echo "✔ Arma Reforger actions seeded successfully!"
echo ""
echo "Summary:"
echo "  Game ID: ${game_id}"
echo "  Actions created:"
echo "    - kick_player    (Player Management)"
echo "    - ban_player     (Player Management)"
echo "    - shutdown       (Server Control)"
echo "    - change_mission (Server Control)"
echo "    - say            (Communication)"
echo ""
