#!/bin/bash
# Seed script for Minecraft game actions
# Creates common Minecraft server actions for testing and server management

set -e

# Check if DATABASE_URL is set
if [ -z "$DATABASE_URL" ]; then
    echo "Error: DATABASE_URL environment variable is not set"
    exit 1
fi

echo "Seeding game actions for Minecraft..."

# Run SQL seed commands
psql "$DATABASE_URL" <<'EOF'

-- Minecraft Actions
-- Demonstrates simple buttons, select with presets, and parameterized input

-- Simple button: Save All
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style, icon)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1),
    'save_all',
    'Save World',
    'Save all chunks to disk',
    'save-all',
    0,
    'World Management',
    'success',
    'fa-save'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Simple button: Stop server
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style, requires_confirmation, confirmation_message)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1),
    'stop_server',
    'Stop Server',
    'Gracefully stop the Minecraft server',
    'stop',
    1,
    'Server Control',
    'danger',
    true,
    'This will stop the server and disconnect all players. Continue?'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Select button: Say (preset messages for testing)
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1),
    'say_preset',
    'Broadcast Message',
    'Send a preset message to all players',
    'say {{.message}}',
    2,
    'Communication',
    'info'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Add message selection input field for presets
INSERT INTO action_input_fields (action_id, name, label, field_type, required, display_order, help_text)
SELECT action_id, 'message', 'Select Message', 'select', true, 0, 'Choose a message to broadcast'
FROM action_definitions
WHERE name = 'say_preset'
  AND definition_level = 'game' AND entity_id = (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1)
ON CONFLICT (action_id, name) DO NOTHING;

-- Add preset message options
INSERT INTO action_input_options (field_id, value, label, display_order, is_default)
SELECT
    (SELECT field_id FROM action_input_fields aif
     JOIN action_definitions ad ON aif.action_id = ad.action_id
     WHERE ad.name = 'say_preset' AND aif.name = 'message'
       AND ad.game_id = (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1)
     LIMIT 1),
    value,
    label,
    display_order,
    is_default
FROM (VALUES
    ('Server will restart in 5 minutes!', 'Restart Warning (5 min)', 0, true),
    ('Server will restart in 1 minute. Please find a safe place!', 'Restart Warning (1 min)', 1, false),
    ('Server restart complete. Welcome back!', 'Restart Complete', 2, false),
    ('Backup in progress. Minor lag expected.', 'Backup Notice', 3, false),
    ('Event starting at spawn in 10 minutes!', 'Event Announcement', 4, false),
    ('Please report any bugs or issues to the admin.', 'Bug Report Reminder', 5, false)
) AS messages(value, label, display_order, is_default)
ON CONFLICT (field_id, value) DO NOTHING;

-- Parameterized button: Say (custom message)
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1),
    'say_custom',
    'Custom Message',
    'Send a custom message to all players',
    'say {{.custom_message}}',
    3,
    'Communication',
    'primary'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Add custom message input field
INSERT INTO action_input_fields (action_id, name, label, field_type, required, placeholder, display_order, help_text, min_length, max_length)
SELECT
    action_id,
    'custom_message',
    'Your Message',
    'text',
    true,
    'e.g., Welcome to the server!',
    0,
    'Enter a message to broadcast to all players',
    1,
    256
FROM action_definitions
WHERE name = 'say_custom'
  AND definition_level = 'game' AND entity_id = (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1)
ON CONFLICT (action_id, name) DO NOTHING;

-- Additional useful Minecraft commands

-- Simple button: List players
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1),
    'list_players',
    'List Players',
    'Show all online players',
    'list',
    4,
    'Server Info',
    'secondary'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Select button: Change gamemode
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1),
    'default_gamemode',
    'Set Default Gamemode',
    'Change the default gamemode for new players',
    'defaultgamemode {{.gamemode}}',
    5,
    'World Settings',
    'warning'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Add gamemode selection input field
INSERT INTO action_input_fields (action_id, name, label, field_type, required, display_order, help_text)
SELECT action_id, 'gamemode', 'Gamemode', 'select', true, 0, 'Select the default gamemode'
FROM action_definitions
WHERE name = 'default_gamemode'
  AND definition_level = 'game' AND entity_id = (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1)
ON CONFLICT (action_id, name) DO NOTHING;

-- Add gamemode options
INSERT INTO action_input_options (field_id, value, label, display_order, is_default)
SELECT
    (SELECT field_id FROM action_input_fields aif
     JOIN action_definitions ad ON aif.action_id = ad.action_id
     WHERE ad.name = 'default_gamemode' AND aif.name = 'gamemode'
       AND ad.game_id = (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1)
     LIMIT 1),
    value,
    label,
    display_order,
    is_default
FROM (VALUES
    ('survival', 'Survival', 0, true),
    ('creative', 'Creative', 1, false),
    ('adventure', 'Adventure', 2, false),
    ('spectator', 'Spectator', 3, false)
) AS gamemodes(value, label, display_order, is_default)
ON CONFLICT (field_id, value) DO NOTHING;

-- Select button: Change difficulty
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1),
    'difficulty',
    'Set Difficulty',
    'Change the world difficulty',
    'difficulty {{.level}}',
    6,
    'World Settings',
    'warning'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Add difficulty selection input field
INSERT INTO action_input_fields (action_id, name, label, field_type, required, display_order, help_text)
SELECT action_id, 'level', 'Difficulty Level', 'select', true, 0, 'Select the difficulty level'
FROM action_definitions
WHERE name = 'difficulty'
  AND definition_level = 'game' AND entity_id = (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1)
ON CONFLICT (action_id, name) DO NOTHING;

-- Add difficulty options
INSERT INTO action_input_options (field_id, value, label, display_order, is_default)
SELECT
    (SELECT field_id FROM action_input_fields aif
     JOIN action_definitions ad ON aif.action_id = ad.action_id
     WHERE ad.name = 'difficulty' AND aif.name = 'level'
       AND ad.game_id = (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1)
     LIMIT 1),
    value,
    label,
    display_order,
    is_default
FROM (VALUES
    ('peaceful', 'Peaceful', 0, false),
    ('easy', 'Easy', 1, false),
    ('normal', 'Normal', 2, true),
    ('hard', 'Hard', 3, false)
) AS difficulties(value, label, display_order, is_default)
ON CONFLICT (field_id, value) DO NOTHING;

-- Parameterized button: Set time
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1),
    'time_set',
    'Set Time',
    'Set the world time',
    'time set {{.time}}',
    7,
    'World Settings',
    'primary'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Add time selection input field
INSERT INTO action_input_fields (action_id, name, label, field_type, required, display_order, help_text)
SELECT action_id, 'time', 'Time', 'select', true, 0, 'Select the time of day'
FROM action_definitions
WHERE name = 'time_set'
  AND definition_level = 'game' AND entity_id = (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1)
ON CONFLICT (action_id, name) DO NOTHING;

-- Add time options
INSERT INTO action_input_options (field_id, value, label, display_order, is_default)
SELECT
    (SELECT field_id FROM action_input_fields aif
     JOIN action_definitions ad ON aif.action_id = ad.action_id
     WHERE ad.name = 'time_set' AND aif.name = 'time'
       AND ad.game_id = (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1)
     LIMIT 1),
    value,
    label,
    display_order,
    is_default
FROM (VALUES
    ('day', 'Day (1000)', 0, true),
    ('noon', 'Noon (6000)', 1, false),
    ('night', 'Night (13000)', 2, false),
    ('midnight', 'Midnight (18000)', 3, false)
) AS times(value, label, display_order, is_default)
ON CONFLICT (field_id, value) DO NOTHING;

-- Select button: Weather
INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template, display_order, group_name, button_style)
VALUES (
    'game',
    (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1),
    'weather',
    'Set Weather',
    'Change the weather',
    'weather {{.type}}',
    8,
    'World Settings',
    'info'
)
ON CONFLICT (definition_level, entity_id, name) DO NOTHING;

-- Add weather selection input field
INSERT INTO action_input_fields (action_id, name, label, field_type, required, display_order, help_text)
SELECT action_id, 'type', 'Weather Type', 'select', true, 0, 'Select the weather'
FROM action_definitions
WHERE name = 'weather'
  AND definition_level = 'game' AND entity_id = (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1)
ON CONFLICT (action_id, name) DO NOTHING;

-- Add weather options
INSERT INTO action_input_options (field_id, value, label, display_order, is_default)
SELECT
    (SELECT field_id FROM action_input_fields aif
     JOIN action_definitions ad ON aif.action_id = ad.action_id
     WHERE ad.name = 'weather' AND aif.name = 'type'
       AND ad.game_id = (SELECT game_id FROM games WHERE name = 'Minecraft' LIMIT 1)
     LIMIT 1),
    value,
    label,
    display_order,
    is_default
FROM (VALUES
    ('clear', 'Clear', 0, true),
    ('rain', 'Rain', 1, false),
    ('thunder', 'Thunder', 2, false)
) AS weather_types(value, label, display_order, is_default)
ON CONFLICT (field_id, value) DO NOTHING;

EOF

echo "Minecraft actions seeded successfully!"
