#!/usr/bin/env bash
set -euo pipefail

# Seed L4D2 action definitions using the API
# Requires: grpcurl, python3

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=common.sh
source "${SCRIPT_DIR}/common.sh"

CONTROL_API_ADDR="${CONTROL_API_ADDR:-localhost:50052}"
USE_TLS="${USE_TLS:-auto}"
INSECURE_TLS="${INSECURE_TLS:-false}"

resolve_tls

echo "════════════════════════════════════════════════════"
echo "  Seeding Left 4 Dead 2 Game Actions"
echo "════════════════════════════════════════════════════"
echo "GRPC API:  ${CONTROL_API_ADDR}"
echo ""

require_grpcurl
setup_auth
test_api_connectivity

echo "Finding Left 4 Dead 2 game..."
game_id="$(find_game_id_by_name "Left 4 Dead 2")"
if [[ -z "${game_id}" ]]; then
  echo "✗ Left 4 Dead 2 game not found. Run load-l4d2-config.sh first."
  exit 1
fi
echo "✓ Found Left 4 Dead 2 game (ID: ${game_id})"
echo ""

# Create actions
echo "Creating actions..."

# 1. Change Map (with select field for official campaigns)
echo "  Creating 'Change Map' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "change_map",
    "label": "Change Map",
    "description": "Change to a different campaign map",
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
      "help_text": "Choose a campaign map to load"
    }
  ],
  "input_options": [
    {
      "value": "c1m1_hotel",
      "label": "Dead Center - Hotel",
      "display_order": 0,
      "is_default": true
    },
    {
      "value": "c2m1_highway",
      "label": "Dark Carnival - Highway",
      "display_order": 1
    },
    {
      "value": "c3m1_plankcountry",
      "label": "Swamp Fever - Plank Country",
      "display_order": 2
    },
    {
      "value": "c4m1_milltown_a",
      "label": "Hard Rain - Milltown",
      "display_order": 3
    },
    {
      "value": "c5m1_waterfront",
      "label": "The Parish - Waterfront",
      "display_order": 4
    },
    {
      "value": "c6m1_riverbank",
      "label": "The Passing - Riverbank",
      "display_order": 5
    },
    {
      "value": "c7m1_docks",
      "label": "The Sacrifice - Docks",
      "display_order": 6
    },
    {
      "value": "c8m1_apartment",
      "label": "No Mercy - Apartment",
      "display_order": 7
    },
    {
      "value": "c9m1_alleys",
      "label": "Crash Course - Alleys",
      "display_order": 8
    },
    {
      "value": "c10m1_caves",
      "label": "Death Toll - Caves",
      "display_order": 9
    },
    {
      "value": "c11m1_greenhouse",
      "label": "Dead Air - Greenhouse",
      "display_order": 10
    },
    {
      "value": "c12m1_hilltop",
      "label": "Blood Harvest - Hilltop",
      "display_order": 11
    },
    {
      "value": "c13m1_alpinecreek",
      "label": "Cold Stream - Alpine Creek",
      "display_order": 12
    }
  ]
}
EOF
)"

# 2. Change Game Mode
echo "  Creating 'Change Game Mode' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "change_mode",
    "label": "Change Game Mode",
    "description": "Switch to a different game mode",
    "command_template": "mp_gamemode {{.mode}}; changelevel {{.map}}",
    "display_order": 1,
    "group_name": "Game Mode",
    "button_style": "primary",
    "requires_confirmation": true,
    "confirmation_message": "This will change the game mode and reload the map. Continue?",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "mode",
      "label": "Select Mode",
      "field_type": "select",
      "required": true,
      "display_order": 0,
      "help_text": "Choose a game mode"
    },
    {
      "name": "map",
      "label": "Starting Map",
      "field_type": "text",
      "required": true,
      "placeholder": "e.g., c1m1_hotel",
      "display_order": 1,
      "help_text": "Map to load with new mode"
    }
  ],
  "input_options": [
    {
      "value": "coop",
      "label": "Co-op",
      "display_order": 0,
      "is_default": true
    },
    {
      "value": "versus",
      "label": "Versus",
      "display_order": 1
    },
    {
      "value": "survival",
      "label": "Survival",
      "display_order": 2
    },
    {
      "value": "scavenge",
      "label": "Scavenge",
      "display_order": 3
    },
    {
      "value": "realism",
      "label": "Realism",
      "display_order": 4
    },
    {
      "value": "teamversus",
      "label": "Team Versus",
      "display_order": 5
    },
    {
      "value": "teamscavenge",
      "label": "Team Scavenge",
      "display_order": 6
    }
  ]
}
EOF
)"

# 3. Set Difficulty
echo "  Creating 'Set Difficulty' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "set_difficulty",
    "label": "Set Difficulty",
    "description": "Change the game difficulty level",
    "command_template": "z_difficulty {{.difficulty}}",
    "display_order": 2,
    "group_name": "Game Settings",
    "button_style": "primary",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "difficulty",
      "label": "Difficulty Level",
      "field_type": "select",
      "required": true,
      "display_order": 0,
      "help_text": "Choose difficulty level"
    }
  ],
  "input_options": [
    {
      "value": "easy",
      "label": "Easy",
      "display_order": 0
    },
    {
      "value": "normal",
      "label": "Normal",
      "display_order": 1,
      "is_default": true
    },
    {
      "value": "hard",
      "label": "Advanced",
      "display_order": 2
    },
    {
      "value": "impossible",
      "label": "Expert",
      "display_order": 3
    }
  ]
}
EOF
)"

# 4. Restart Round
echo "  Creating 'Restart Round' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "restart_round",
    "label": "Restart Round",
    "description": "Restart the current round/chapter",
    "command_template": "mp_restartgame 1",
    "display_order": 3,
    "group_name": "Match Control",
    "button_style": "warning",
    "requires_confirmation": true,
    "confirmation_message": "This will restart the current round. Continue?",
    "enabled": true
  }
}
EOF
)"

# 5. Stop Server
echo "  Creating 'Stop Server' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "stop_server",
    "label": "Stop Server",
    "description": "Gracefully stop the L4D2 server",
    "command_template": "quit",
    "display_order": 4,
    "group_name": "Server Control",
    "button_style": "danger",
    "requires_confirmation": true,
    "confirmation_message": "This will stop the server and disconnect all players. Continue?",
    "enabled": true
  }
}
EOF
)"

# 6. Broadcast Preset Message
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
    "display_order": 5,
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
      "value": "Good luck survivors!",
      "label": "Good Luck",
      "display_order": 3
    },
    {
      "value": "Watch out for the Tank!",
      "label": "Tank Warning",
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

# 7. Custom Message
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
    "display_order": 6,
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
      "placeholder": "e.g., Good luck survivors!",
      "display_order": 0,
      "help_text": "Enter a message to broadcast to all players",
      "min_length": 1,
      "max_length": 256
    }
  ]
}
EOF
)"

# 8. Kick All Bots
echo "  Creating 'Kick All Bots' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "kick_bots",
    "label": "Kick All Bots",
    "description": "Remove all bot players from the server",
    "command_template": "sb_all_bot_game 0; kick all",
    "display_order": 7,
    "group_name": "Bot Management",
    "button_style": "warning",
    "requires_confirmation": true,
    "confirmation_message": "Are you sure you want to kick all bots?",
    "enabled": true
  }
}
EOF
)"

# 9. Toggle All Bot Team
echo "  Creating 'Toggle All Bot Team' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "toggle_bot_team",
    "label": "Toggle All Bot Team",
    "description": "Toggle between all bots or mixed human/bot team",
    "command_template": "sb_all_bot_team {{.enabled}}",
    "display_order": 8,
    "group_name": "Bot Management",
    "button_style": "primary",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "enabled",
      "label": "Bot Team Mode",
      "field_type": "select",
      "required": true,
      "display_order": 0,
      "help_text": "Enable or disable all-bot team"
    }
  ],
  "input_options": [
    {
      "value": "1",
      "label": "Enable (All Bots)",
      "display_order": 0
    },
    {
      "value": "0",
      "label": "Disable (Mixed)",
      "display_order": 1,
      "is_default": true
    }
  ]
}
EOF
)"

# 10. Set Max Players
echo "  Creating 'Set Max Players' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "set_maxplayers",
    "label": "Set Max Players",
    "description": "Change the maximum number of players allowed",
    "command_template": "maxplayers {{.count}}",
    "display_order": 9,
    "group_name": "Server Settings",
    "button_style": "primary",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "count",
      "label": "Max Players",
      "field_type": "select",
      "required": true,
      "display_order": 0,
      "help_text": "Maximum number of players"
    }
  ],
  "input_options": [
    {
      "value": "4",
      "label": "4 Players",
      "display_order": 0,
      "is_default": true
    },
    {
      "value": "8",
      "label": "8 Players",
      "display_order": 1
    },
    {
      "value": "12",
      "label": "12 Players",
      "display_order": 2
    },
    {
      "value": "16",
      "label": "16 Players",
      "display_order": 3
    },
    {
      "value": "18",
      "label": "18 Players",
      "display_order": 4
    }
  ]
}
EOF
)"

# 11. Director Force Panic Event
echo "  Creating 'Force Panic Event' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "force_panic",
    "label": "Force Panic Event",
    "description": "Trigger a panic event/crescendo",
    "command_template": "director_force_panic_event",
    "display_order": 10,
    "group_name": "Director Control",
    "button_style": "danger",
    "icon": "fa-exclamation-triangle",
    "enabled": true
  }
}
EOF
)"

# 12. Spawn Special Infected
echo "  Creating 'Spawn Special Infected' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "spawn_infected",
    "label": "Spawn Special Infected",
    "description": "Spawn a special infected near survivors",
    "command_template": "z_spawn {{.infected_type}}",
    "display_order": 11,
    "group_name": "Director Control",
    "button_style": "danger",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "infected_type",
      "label": "Infected Type",
      "field_type": "select",
      "required": true,
      "display_order": 0,
      "help_text": "Choose special infected to spawn"
    }
  ],
  "input_options": [
    {
      "value": "tank",
      "label": "Tank",
      "display_order": 0
    },
    {
      "value": "witch",
      "label": "Witch",
      "display_order": 1
    },
    {
      "value": "hunter",
      "label": "Hunter",
      "display_order": 2
    },
    {
      "value": "boomer",
      "label": "Boomer",
      "display_order": 3
    },
    {
      "value": "smoker",
      "label": "Smoker",
      "display_order": 4
    },
    {
      "value": "spitter",
      "label": "Spitter",
      "display_order": 5
    },
    {
      "value": "jockey",
      "label": "Jockey",
      "display_order": 6
    },
    {
      "value": "charger",
      "label": "Charger",
      "display_order": 7
    }
  ]
}
EOF
)"

# 13. Execute Config
echo "  Creating 'Execute Config' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "exec_config",
    "label": "Execute Config",
    "description": "Execute a server configuration file",
    "command_template": "exec {{.config_name}}",
    "display_order": 12,
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
)"

# 14. Give Item to Player
echo "  Creating 'Give Item' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "give_item",
    "label": "Give Item",
    "description": "Give an item to the player you are aiming at",
    "command_template": "give {{.item}}",
    "display_order": 13,
    "group_name": "Player Management",
    "button_style": "info",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "item",
      "label": "Item Type",
      "field_type": "select",
      "required": true,
      "display_order": 0,
      "help_text": "Choose item to give"
    }
  ],
  "input_options": [
    {
      "value": "health",
      "label": "First Aid Kit",
      "display_order": 0,
      "is_default": true
    },
    {
      "value": "pain_pills",
      "label": "Pain Pills",
      "display_order": 1
    },
    {
      "value": "adrenaline",
      "label": "Adrenaline",
      "display_order": 2
    },
    {
      "value": "defibrillator",
      "label": "Defibrillator",
      "display_order": 3
    },
    {
      "value": "rifle",
      "label": "Assault Rifle",
      "display_order": 4
    },
    {
      "value": "rifle_ak47",
      "label": "AK-47",
      "display_order": 5
    },
    {
      "value": "rifle_desert",
      "label": "Desert Rifle",
      "display_order": 6
    },
    {
      "value": "shotgun_chrome",
      "label": "Chrome Shotgun",
      "display_order": 7
    },
    {
      "value": "pumpshotgun",
      "label": "Pump Shotgun",
      "display_order": 8
    },
    {
      "value": "autoshotgun",
      "label": "Auto Shotgun",
      "display_order": 9
    },
    {
      "value": "sniper_military",
      "label": "Military Sniper",
      "display_order": 10
    },
    {
      "value": "pipe_bomb",
      "label": "Pipe Bomb",
      "display_order": 11
    },
    {
      "value": "molotov",
      "label": "Molotov",
      "display_order": 12
    },
    {
      "value": "vomitjar",
      "label": "Bile Jar",
      "display_order": 13
    }
  ]
}
EOF
)"

# 15. Enable/Disable God Mode
echo "  Creating 'Toggle God Mode' action..."
create_action "$(cat <<EOF
{
  "action": {
    "definition_level": "game",
    "entity_id": ${game_id},
    "name": "toggle_god",
    "label": "Toggle God Mode",
    "description": "Enable or disable god mode for testing",
    "command_template": "god {{.enabled}}",
    "display_order": 14,
    "group_name": "Debug/Testing",
    "button_style": "secondary",
    "enabled": true
  },
  "input_fields": [
    {
      "name": "enabled",
      "label": "God Mode",
      "field_type": "select",
      "required": true,
      "display_order": 0,
      "help_text": "Enable or disable god mode"
    }
  ],
  "input_options": [
    {
      "value": "1",
      "label": "Enable",
      "display_order": 0
    },
    {
      "value": "0",
      "label": "Disable",
      "display_order": 1,
      "is_default": true
    }
  ]
}
EOF
)"

echo ""
echo "✔ Left 4 Dead 2 actions seeded successfully!"
echo ""
echo "Summary:"
echo "  - Change Map (13 official campaign maps)"
echo "  - Change Game Mode (7 modes: coop, versus, survival, etc.)"
echo "  - Set Difficulty (easy, normal, advanced, expert)"
echo "  - Restart Round (with confirmation)"
echo "  - Stop Server (with confirmation)"
echo "  - Broadcast Message (6 preset options)"
echo "  - Custom Message (text input)"
echo "  - Kick All Bots (with confirmation)"
echo "  - Toggle All Bot Team (enable/disable)"
echo "  - Set Max Players (4, 8, 12, 16, 18)"
echo "  - Force Panic Event (director control)"
echo "  - Spawn Special Infected (8 types: tank, witch, hunter, etc.)"
echo "  - Execute Config (text input with confirmation)"
echo "  - Give Item (14 items: weapons, medkits, throwables)"
echo "  - Toggle God Mode (debug/testing)"
echo ""
