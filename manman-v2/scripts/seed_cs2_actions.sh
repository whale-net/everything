#!/usr/bin/env bash
set -euo pipefail

# Seed CS2 action definitions using the API
# Requires: grpcurl, python3

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Default values
CONTROL_API_ADDR="${CONTROL_API_ADDR:-localhost:50052}"
USE_TLS="${USE_TLS:-auto}"
INSECURE_TLS="${INSECURE_TLS:-false}"

# Auto-detect TLS based on port (443 = TLS)
if [[ "${USE_TLS}" == "auto" ]]; then
  if [[ "${CONTROL_API_ADDR}" =~ :443$ ]]; then
    USE_TLS="true"
  else
    USE_TLS="false"
  fi
fi

grpc_call() {
  local addr="$1"
  local method="$2"
  local data="$3"

  local tls_flags=""
  if [[ "${USE_TLS}" == "true" ]]; then
    if [[ "${INSECURE_TLS}" == "true" ]]; then
      tls_flags="-insecure"
    fi
  else
    tls_flags="-plaintext"
  fi

  grpcurl ${tls_flags} \
    -import-path "${REPO_ROOT}" \
    -proto "${REPO_ROOT}/manmanv2/protos/api.proto" \
    -proto "${REPO_ROOT}/manmanv2/protos/messages.proto" \
    -d "${data}" \
    "${addr}" "${method}"
}

echo "════════════════════════════════════════════════════"
echo "  Seeding Counter-Strike 2 Game Actions"
echo "════════════════════════════════════════════════════"
echo "GRPC API:  ${CONTROL_API_ADDR}"
echo ""

# Check for grpcurl
if ! command -v grpcurl &> /dev/null; then
  echo "Error: grpcurl is not installed"
  echo "Install with: brew install grpcurl (macOS) or go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest"
  exit 1
fi

# Test API connectivity
echo "Testing API connectivity..."
TLS_FLAGS=""
if [[ "${USE_TLS}" == "true" ]]; then
  if [[ "${INSECURE_TLS}" == "true" ]]; then
    TLS_FLAGS="-insecure"
  fi
else
  TLS_FLAGS="-plaintext"
fi

if ! grpcurl ${TLS_FLAGS} "${CONTROL_API_ADDR}" list manman.v1.ManManAPI &> /dev/null; then
  echo "✗ Cannot connect to API at ${CONTROL_API_ADDR}"
  echo "Make sure the control plane is running and accessible"
  if [[ "${USE_TLS}" == "false" ]]; then
    echo "Hint: If the endpoint uses TLS, set USE_TLS=true"
  fi
  exit 1
fi
echo "✓ API is reachable"
echo ""

# Find CS2 game ID
echo "Finding Counter-Strike 2 game..."
games_resp="$(grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/ListGames" '{"page_size":100}')"
game_id="$(python3 -c 'import json,sys; data=json.loads(sys.stdin.read() or "{}"); games=data.get("games", []);
found="";
for g in games:
    name=(g.get("name") or "").strip()
    if name in ["Counter-Strike 2", "CS2"]:
        found=(g.get("gameId") or g.get("game_id") or "")
        break
print(found)' <<< "${games_resp}")"

if [[ -z "${game_id}" ]]; then
  echo "✗ Counter-Strike 2 game not found"
  echo "Please create the Counter-Strike 2 game first"
  exit 1
fi

echo "✓ Found Counter-Strike 2 game (ID: ${game_id})"
echo ""

# Create actions
echo "Creating actions..."

# 1. Change Map (with select field)
echo "  Creating 'Change Map' action..."
grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateActionDefinition" "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "change_map",
    "label": "Change Map",
    "description": "Change to a different map",
    "command_template": "changelevel {{.map}}",
    "display_order": 0,
    "group_name": "Map Control",
    "button_style": "primary",
    "icon": "fa-map",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "map",
      "label": "Select Map",
      "field_type": "select",
      "required": true,
      "display_order": 0,
      "help_text": "Choose a map to load"
    }
  ],
  "input_options": [
    {
      "value": "de_dust2",
      "label": "Dust II",
      "display_order": 0,
      "is_default": true
    },
    {
      "value": "de_mirage",
      "label": "Mirage",
      "display_order": 1
    },
    {
      "value": "de_inferno",
      "label": "Inferno",
      "display_order": 2
    },
    {
      "value": "de_nuke",
      "label": "Nuke",
      "display_order": 3
    },
    {
      "value": "de_overpass",
      "label": "Overpass",
      "display_order": 4
    },
    {
      "value": "de_ancient",
      "label": "Ancient",
      "display_order": 5
    },
    {
      "value": "de_anubis",
      "label": "Anubis",
      "display_order": 6
    },
    {
      "value": "de_vertigo",
      "label": "Vertigo",
      "display_order": 7
    }
  ]
}
EOF
)" > /dev/null 2>&1 && echo "    ✓ Created" || echo "    ⚠ Already exists or failed"

# 2. Restart Match
echo "  Creating 'Restart Match' action..."
grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateActionDefinition" "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "restart_match",
    "label": "Restart Match",
    "description": "Restart the current match after a delay",
    "command_template": "mp_restartgame {{.delay}}",
    "display_order": 1,
    "group_name": "Match Control",
    "button_style": "warning",
    "requires_confirmation": true,
    "confirmation_message": "This will restart the current match. Continue?",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "delay",
      "label": "Delay (seconds)",
      "field_type": "select",
      "required": true,
      "display_order": 0,
      "help_text": "Delay before restart"
    }
  ],
  "input_options": [
    {
      "value": "1",
      "label": "1 second",
      "display_order": 0,
      "is_default": true
    },
    {
      "value": "3",
      "label": "3 seconds",
      "display_order": 1
    },
    {
      "value": "5",
      "label": "5 seconds",
      "display_order": 2
    },
    {
      "value": "10",
      "label": "10 seconds",
      "display_order": 3
    }
  ]
}
EOF
)" > /dev/null 2>&1 && echo "    ✓ Created" || echo "    ⚠ Already exists or failed"

# 3. Stop Server
echo "  Creating 'Stop Server' action..."
grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateActionDefinition" "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "stop_server",
    "label": "Stop Server",
    "description": "Gracefully stop the CS2 server",
    "command_template": "quit",
    "display_order": 2,
    "group_name": "Server Control",
    "button_style": "danger",
    "requires_confirmation": true,
    "confirmation_message": "This will stop the server and disconnect all players. Continue?",
    "enabled": true
  }
}
EOF
)" > /dev/null 2>&1 && echo "    ✓ Created" || echo "    ⚠ Already exists or failed"

# 4. Broadcast Preset Message
echo "  Creating 'Broadcast Message' action..."
grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateActionDefinition" "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "say_preset",
    "label": "Broadcast Message",
    "description": "Send a preset message to all players",
    "command_template": "say {{.message}}",
    "display_order": 3,
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
      "value": "Server will restart in 1 minute!",
      "label": "Restart Warning (1 min)",
      "display_order": 1
    },
    {
      "value": "Server restart complete. Welcome back!",
      "label": "Restart Complete",
      "display_order": 2
    },
    {
      "value": "Match starting in 2 minutes. Get ready!",
      "label": "Match Starting Soon",
      "display_order": 3
    },
    {
      "value": "Tournament match begins in 10 minutes!",
      "label": "Tournament Announcement",
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
)" > /dev/null 2>&1 && echo "    ✓ Created" || echo "    ⚠ Already exists or failed"

# 5. Custom Message
echo "  Creating 'Custom Message' action..."
grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateActionDefinition" "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "say_custom",
    "label": "Custom Message",
    "description": "Send a custom message to all players",
    "command_template": "say {{.custom_message}}",
    "display_order": 4,
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
      "placeholder": "e.g., Good luck and have fun!",
      "display_order": 0,
      "help_text": "Enter a message to broadcast to all players",
      "min_length": 1,
      "max_length": 256
    }
  ]
}
EOF
)" > /dev/null 2>&1 && echo "    ✓ Created" || echo "    ⚠ Already exists or failed"

# 6. Kick All Bots
echo "  Creating 'Kick All Bots' action..."
grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateActionDefinition" "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "kick_bots",
    "label": "Kick All Bots",
    "description": "Remove all bot players from the server",
    "command_template": "bot_kick",
    "display_order": 5,
    "group_name": "Bot Management",
    "button_style": "warning",
    "requires_confirmation": true,
    "confirmation_message": "Are you sure you want to kick all bots?",
    "enabled": true
  }
}
EOF
)" > /dev/null 2>&1 && echo "    ✓ Created" || echo "    ⚠ Already exists or failed"

# 7. Host Workshop Map
echo "  Creating 'Host Workshop Map' action..."
grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateActionDefinition" "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "host_workshop_map",
    "label": "Host Workshop Map",
    "description": "Load a map from Steam Workshop",
    "command_template": "host_workshop_map {{.workshop_id}}",
    "display_order": 6,
    "group_name": "Workshop",
    "button_style": "info",
    "icon": "fa-steam",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "workshop_id",
      "label": "Workshop Map ID",
      "field_type": "text",
      "required": true,
      "placeholder": "e.g., 3070212801",
      "display_order": 0,
      "help_text": "Enter the Steam Workshop map ID",
      "pattern": "^[0-9]+$",
      "min_length": 1,
      "max_length": 20
    }
  ]
}
EOF
)" > /dev/null 2>&1 && echo "    ✓ Created" || echo "    ⚠ Already exists or failed"

# 8. Change Workshop Map (from collection)
echo "  Creating 'Change Workshop Map' action..."
grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateActionDefinition" "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "workshop_changelevel",
    "label": "Change Workshop Map",
    "description": "Change to a map from the workshop collection",
    "command_template": "ds_workshop_changelevel {{.map_name}}",
    "display_order": 7,
    "group_name": "Workshop",
    "button_style": "primary",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "map_name",
      "label": "Workshop Map Name",
      "field_type": "text",
      "required": true,
      "placeholder": "e.g., workshop/3070212801/de_custom",
      "display_order": 0,
      "help_text": "Enter the workshop map name (use ds_workshop_listmaps to see available maps)",
      "min_length": 1,
      "max_length": 128
    }
  ]
}
EOF
)" > /dev/null 2>&1 && echo "    ✓ Created" || echo "    ⚠ Already exists or failed"

# 9. List Workshop Maps
echo "  Creating 'List Workshop Maps' action..."
grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateActionDefinition" "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "list_workshop_maps",
    "label": "List Workshop Maps",
    "description": "Display all available workshop maps from the collection",
    "command_template": "ds_workshop_listmaps",
    "display_order": 8,
    "group_name": "Workshop",
    "button_style": "secondary",
    "enabled": true
  }
}
EOF
)" > /dev/null 2>&1 && echo "    ✓ Created" || echo "    ⚠ Already exists or failed"

# 10. Execute Config
echo "  Creating 'Execute Config' action..."
grpc_call "${CONTROL_API_ADDR}" "manman.v1.ManManAPI/CreateActionDefinition" "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "exec_config",
    "label": "Execute Config",
    "description": "Execute a server configuration file",
    "command_template": "exec {{.config_name}}",
    "display_order": 9,
    "group_name": "Server Control",
    "button_style": "warning",
    "requires_confirmation": true,
    "confirmation_message": "This will execute a server config file. Continue?",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "config_name",
      "label": "Config File Name",
      "field_type": "text",
      "required": true,
      "placeholder": "e.g., server.cfg",
      "display_order": 0,
      "help_text": "Name of the config file (without path)",
      "min_length": 1,
      "max_length": 128
    }
  ]
}
EOF
)" > /dev/null 2>&1 && echo "    ✓ Created" || echo "    ⚠ Already exists or failed"

echo ""
echo "✔ Counter-Strike 2 actions seeded successfully!"
echo ""
echo "Summary:"
echo "  - Change Map (select dropdown with 8 popular maps)"
echo "  - Restart Match (with delay options)"
echo "  - Stop Server (with confirmation)"
echo "  - Broadcast Message (select dropdown with 6 options)"
echo "  - Custom Message (text input)"
echo "  - Kick All Bots (with confirmation)"
echo "  - Host Workshop Map (text input for workshop ID)"
echo "  - Change Workshop Map (text input for map name)"
echo "  - List Workshop Maps (simple button)"
echo "  - Execute Config (text input with confirmation)"
echo ""
