#!/usr/bin/env bash
set -euo pipefail

# Seed Minecraft action definitions using the API
# Requires: grpcurl, python3

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

CONTROL_API_ADDR="${CONTROL_API_ADDR:-localhost:50052}"
USE_TLS="${USE_TLS:-auto}"
INSECURE_TLS="${INSECURE_TLS:-false}"

resolve_tls

echo "════════════════════════════════════════════════════"
echo "  Seeding Minecraft Game Actions"
echo "════════════════════════════════════════════════════"
echo "GRPC API:  ${CONTROL_API_ADDR}"
echo ""

require_grpcurl
setup_auth
test_api_connectivity

echo "Finding Minecraft game..."
game_id="$(find_game_id_by_name "Minecraft")"
if [[ -z "${game_id}" ]]; then
  echo "✗ Minecraft game not found. Run load-minecraft-config.sh first."
  exit 1
fi
echo "✓ Found Minecraft game (ID: ${game_id})"
echo ""

# Create actions
echo "Creating actions..."

# 1. Save World
echo "  Creating 'Save World' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "save_all",
    "label": "Save World",
    "description": "Save all chunks to disk",
    "command_template": "save-all",
    "display_order": 0,
    "group_name": "World Management",
    "button_style": "success",
    "icon": "fa-save",
    "enabled": true
  }
}
EOF
)"

# 2. Stop Server
echo "  Creating 'Stop Server' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "stop_server",
    "label": "Stop Server",
    "description": "Gracefully stop the Minecraft server",
    "command_template": "stop",
    "display_order": 1,
    "group_name": "Server Control",
    "button_style": "danger",
    "requires_confirmation": true,
    "confirmation_message": "This will stop the server and disconnect all players. Continue?",
    "enabled": true
  }
}
EOF
)"

# 3. Broadcast Preset Message (with select field and options)
echo "  Creating 'Broadcast Message' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "say_preset",
    "label": "Broadcast Message",
    "description": "Send a preset message to all players",
    "command_template": "say {{.message}}",
    "display_order": 2,
    "group_name": "Communication",
    "button_style": "info",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "message",
      "label": "Select Message",
      "field_type": "select",
      "required": true,
      "display_order": 0,
      "help_text": "Choose a message to broadcast"
    }
  ],
  "input_options": [
    {
      "value": "Server will restart in 5 minutes!",
      "label": "Restart Warning (5 min)",
      "display_order": 0,
      "is_default": true
    },
    {
      "value": "Server will restart in 1 minute. Please find a safe place!",
      "label": "Restart Warning (1 min)",
      "display_order": 1
    },
    {
      "value": "Server restart complete. Welcome back!",
      "label": "Restart Complete",
      "display_order": 2
    },
    {
      "value": "Backup in progress. Minor lag expected.",
      "label": "Backup Notice",
      "display_order": 3
    },
    {
      "value": "Event starting at spawn in 10 minutes!",
      "label": "Event Announcement",
      "display_order": 4
    },
    {
      "value": "Please report any bugs or issues to the admin.",
      "label": "Bug Report Reminder",
      "display_order": 5
    }
  ]
}
EOF
)"

# 4. Custom Message (with text input)
echo "  Creating 'Custom Message' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "say_custom",
    "label": "Custom Message",
    "description": "Send a custom message to all players",
    "command_template": "say {{.custom_message}}",
    "display_order": 3,
    "group_name": "Communication",
    "button_style": "primary",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "custom_message",
      "label": "Your Message",
      "field_type": "text",
      "required": true,
      "placeholder": "e.g., Welcome to the server!",
      "display_order": 0,
      "help_text": "Enter a message to broadcast to all players",
      "min_length": 1,
      "max_length": 256
    }
  ]
}
EOF
)"

echo ""
echo "✔ Minecraft actions seeded successfully!"
echo ""
echo "Summary:"
echo "  - Save World (simple button)"
echo "  - Stop Server (with confirmation)"
echo "  - Broadcast Message (select dropdown with 6 options)"
echo "  - Custom Message (text input)"
