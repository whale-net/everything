# Game Actions Architecture

## Overview

Game actions are **configurable buttons** that send commands to running game sessions via stdin. Actions are defined per-game in the database and displayed dynamically on session detail pages.

## Action Types

1. **Simple** - One-click buttons (e.g., "Save Game" → sends `save`)
2. **Select** - Dropdown options (e.g., "Change Map" → choose from predefined maps)
3. **Parameterized** - User input required (e.g., "Host Workshop" → enter workshop ID)

## Data Model

```
action_definitions          - Core action metadata (name, label, command_template)
  ├─ action_input_fields    - Input fields for parameterized actions
  │   └─ action_input_options   - Options for select/radio fields
  ├─ action_visibility_overrides  - Hide actions at config/session levels
  └─ action_executions      - Audit log of all executions
```

## Execution Flow

1. **User clicks action** → UI sends `ExecuteAction` RPC
2. **Backend validates** → Check required fields, patterns, min/max values
3. **Template rendering** → `changelevel {{.map}}` → `changelevel de_dust2`
4. **Send to stdin** → Uses existing `SendInput` infrastructure
5. **Log execution** → Record in `action_executions` for audit

## Command Templates

Actions use Go template syntax for dynamic commands:

```
save                              # Simple: no variables
changelevel {{.map}}              # Select: predefined options
host_workshop_collection {{.id}}  # Parameterized: user input
exec {{.config_name}}             # Text input with validation
```

## Visibility System

Actions support 3-level visibility control (higher levels override lower):

- **Game Config** - Hide action for all deployments of a config
- **Server Game Config** - Hide action for a specific server deployment
- **Session** - Hide action for a single session instance

## Key Files

- **Migration**: `manman/migrate/migrations/018_game_actions.up.sql`
- **Models**: `manman/models.go` (ActionDefinition, ActionInputField, etc.)
- **Repository**: `manman/api/repository/postgres/action.go`
- **Handler**: `manman/api/handlers/action.go`
- **Protos**: `manman/protos/messages.proto`, `manman/protos/api.proto`
- **Seed Data**: `manman-v2/scripts/seed_actions.sh`

## Adding New Actions

Actions are managed via SQL:

```sql
-- 1. Create action definition
INSERT INTO action_definitions (game_id, name, label, command_template, ...)
VALUES (1, 'restart_round', 'Restart Round', 'mp_restartgame 1', ...);

-- 2. Add input fields (if parameterized)
INSERT INTO action_input_fields (action_id, name, label, field_type, required, ...)
SELECT action_id, 'delay', 'Delay (seconds)', 'number', true, ...
FROM action_definitions WHERE name = 'restart_round';

-- 3. Add options (if select/radio)
INSERT INTO action_input_options (field_id, value, label, display_order, ...)
VALUES (...);
```

## Future Enhancements

- Management UI for creating/editing actions (currently SQL-only)
- Action permissions/authorization
- Scheduled actions (cron-style)
- Action chaining (trigger multiple actions)
- Import/export action definitions (YAML/JSON)
