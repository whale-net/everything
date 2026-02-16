#!/bin/bash
# Seed script for game actions
# Seeds example actions for Counter-Strike 2

set -e

# Check if DATABASE_URL is set
if [ -z "$DATABASE_URL" ]; then
    echo "Error: DATABASE_URL environment variable is not set"
    exit 1
fi

echo "Seeding game actions for Counter-Strike 2..."

# Run SQL seed commands
psql "$DATABASE_URL" <<'EOF'

-- Example Data for Counter-Strike 2
-- These actions demonstrate the three types: simple, select, and parameterized

-- Simple button: Save game
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style, icon)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Counter-Strike 2' LIMIT 1),
    'save_game',
    'Save Game',
    'Save the current game state',
    'save',
    0,
    'Game Control',
    'success',
    'fa-save'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Simple button: Kick all bots
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style, requires_confirmation, confirmation_message)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Counter-Strike 2' LIMIT 1),
    'kick_bots',
    'Kick All Bots',
    'Remove all bot players from the server',
    'bot_kick',
    2,
    'Admin',
    'danger',
    true,
    'Are you sure you want to kick all bots?'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Select button: Change map
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Counter-Strike 2' LIMIT 1),
    'change_map',
    'Change Map',
    'Change to a different map',
    'changelevel {{.map}}',
    1,
    'Map Selection',
    'primary'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Add map selection input field
INSERT INTO action_input_fields (action_id, name, label, field_type, required, display_order, help_text)
SELECT action_id, 'map', 'Select Map', 'select', true, 0, 'Choose a map to load'
FROM action_definitions
WHERE name = 'change_map'
  AND definition_level = 'game' AND entity_id = (SELECT game_id FROM games WHERE name = 'Counter-Strike 2' LIMIT 1)
ON CONFLICT (action_id, name) DO NOTHING;

-- Add map options
INSERT INTO action_input_options (field_id, value, label, display_order, is_default)
SELECT
    (SELECT field_id FROM action_input_fields aif
     JOIN action_definitions ad ON aif.action_id = ad.action_id
     WHERE ad.name = 'change_map' AND aif.name = 'map'
       AND ad.game_id = (SELECT game_id FROM games WHERE name = 'Counter-Strike 2' LIMIT 1)
     LIMIT 1),
    value,
    label,
    display_order,
    is_default
FROM (VALUES
    ('de_dust2', 'Dust 2', 0, true),
    ('de_mirage', 'Mirage', 1, false),
    ('de_inferno', 'Inferno', 2, false),
    ('de_nuke', 'Nuke', 3, false),
    ('de_overpass', 'Overpass', 4, false),
    ('de_ancient', 'Ancient', 5, false),
    ('de_anubis', 'Anubis', 6, false),
    ('de_vertigo', 'Vertigo', 7, false)
) AS maps(value, label, display_order, is_default)
ON CONFLICT (field_id, value) DO NOTHING;

-- Parameterized button: Host workshop map
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Counter-Strike 2' LIMIT 1),
    'host_workshop',
    'Host Workshop Map',
    'Host a map from Steam Workshop',
    'host_workshop_collection {{.workshop_id}}',
    3,
    'Workshop',
    'info'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Add workshop ID input field
INSERT INTO action_input_fields (action_id, name, label, field_type, required, placeholder, pattern, display_order, help_text)
SELECT
    action_id,
    'workshop_id',
    'Workshop Collection ID',
    'text',
    true,
    'e.g., 123456789',
    '^[0-9]+$',
    0,
    'Enter the Steam Workshop collection ID'
FROM action_definitions
WHERE name = 'host_workshop'
  AND definition_level = 'game' AND entity_id = (SELECT game_id FROM games WHERE name = 'Counter-Strike 2' LIMIT 1)
ON CONFLICT (action_id, name) DO NOTHING;

-- Parameterized button: Execute server command
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style, requires_confirmation, confirmation_message)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Counter-Strike 2' LIMIT 1),
    'exec_config',
    'Execute Config',
    'Execute a server configuration file',
    'exec {{.config_name}}',
    4,
    'Admin',
    'warning',
    true,
    'This will execute a server config file. Continue?'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

INSERT INTO action_input_fields (action_id, name, label, field_type, required, placeholder, display_order, help_text)
SELECT
    action_id,
    'config_name',
    'Config File Name',
    'text',
    true,
    'e.g., server.cfg',
    0,
    'Name of the config file (without path)'
FROM action_definitions
WHERE name = 'exec_config'
  AND definition_level = 'game' AND entity_id = (SELECT game_id FROM games WHERE name = 'Counter-Strike 2' LIMIT 1)
ON CONFLICT (action_id, name) DO NOTHING;

EOF

echo "Game actions seeded successfully!"
