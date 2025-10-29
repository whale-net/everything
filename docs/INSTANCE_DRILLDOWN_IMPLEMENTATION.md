# Game Server Instance Drill-Down Page Implementation Plan

**Status**: Planning  
**Created**: 2025-10-28  
**Last Updated**: 2025-10-28

## Overview

Add a drill-down page for active game server instances that allows users to:
- View instance details and available commands
- Execute commands via the Experience API
- Add new custom commands
- View console output (streaming placeholder for now)
- See visual indicators for crashed servers (disabled interactions)

## Design

### Page Architecture

```
/instance/{instance_id}
├── Header: Server name, status, instance details
├── Command Panel
│   ├── Available Commands List (defaults + config-specific)
│   │   ├── Game Server Command Defaults (slightly muted style)
│   │   └── Config Commands (standard style)
│   └── Add Command Button → Modal
├── Console Output Panel (placeholder with lorem ipsum)
└── Status-aware interactions (disabled if crashed)
```

### Data Model Reference

The following models are already implemented in `manman/src/models.py`:

1. **GameServerCommand**: Reusable command templates (e.g., "change_map", "set_gamemode")
   - Fields: `game_server_command_id`, `game_server_id`, `name`, `command`, `description`, `is_visible`

2. **GameServerCommandDefaults**: Default command instances with values (shared across configs)
   - Fields: `game_server_command_default_id`, `game_server_command_id`, `command_value`, `description`, `is_visible`
   - Relationship: References `GameServerCommand`

3. **GameServerConfigCommands**: Config-specific command instances
   - Fields: `game_server_config_command_id`, `game_server_config_id`, `game_server_command_id`, `command_value`, `description`, `is_visible`
   - Relationships: References both `GameServerConfig` and `GameServerCommand`

### Visual Design

**Command Display**:
- **Config Commands**: Standard background (#f9fafb), full opacity
- **Default Commands**: Slightly muted (#f3f4f6), reduced opacity (0.9)
- Both show: command name/description, execute button

**Status Indicators**:
- Active server: Green outline, interactive buttons
- Crashed server: Red background, grayed-out disabled buttons

**Console Panel**:
- Dark background with monospace font
- Placeholder: Lorem ipsum text for now
- Future: Real-time streaming output

### HTMX Patterns

1. **Page Load**: GET `/instance/{instance_id}` → Full page render
2. **Command Execution**: POST button with `hx-post` → Status update
3. **Add Command Modal**: HTMX swap for modal content
4. **Auto-refresh**: `hx-trigger="every 5s"` for status updates

## API Design

### New Experience API Endpoints

#### 1. Get Instance Details with Commands
```
GET /gameserver/instance/{instance_id}

Response:
{
  "instance": {
    "game_server_instance_id": 1,
    "game_server_config_id": 2,
    "status_type": "ACTIVE",
    "start_date": "2025-10-28T10:00:00Z",
    "end_date": null,
    ...
  },
  "config": {
    "game_server_config_id": 2,
    "name": "CS:S Dust2 24/7",
    ...
  },
  "command_defaults": [
    {
      "game_server_command_default_id": 1,
      "game_server_command": {
        "game_server_command_id": 1,
        "name": "change_map",
        "command": "changelevel {map}",
        "description": "Change map"
      },
      "command_value": "de_dust2",
      "description": "Switch to Dust2"
    }
  ],
  "config_commands": [
    {
      "game_server_config_command_id": 1,
      "game_server_command": {
        "game_server_command_id": 2,
        "name": "set_cheats",
        "command": "sv_cheats {value}",
        "description": "Enable/disable cheats"
      },
      "command_value": "1",
      "description": "Enable cheats"
    }
  ]
}
```

#### 2. Execute Command by Instance
```
POST /gameserver/instance/{instance_id}/command

Request:
{
  "command_type": "default|config",  # Which list the command came from
  "command_id": 1,  # Either game_server_command_default_id or game_server_config_command_id
  "custom_value": "optional_override"  # Optional: override the stored command_value
}

Response:
{
  "status": "success",
  "message": "Command sent to instance 1",
  "command": "changelevel de_dust2"
}
```

#### 3. Get Available Commands (for modal)
```
GET /gameserver/{game_server_id}/commands

Response:
{
  "commands": [
    {
      "game_server_command_id": 1,
      "name": "change_map",
      "command": "changelevel {map}",
      "description": "Change map"
    },
    ...
  ]
}
```

#### 4. Create Config Command
```
POST /gameserver/config/{config_id}/command

Request:
{
  "game_server_command_id": 1,
  "command_value": "de_dust2",
  "description": "Switch to Dust2"
}

Response:
{
  "game_server_config_command_id": 5,
  "game_server_command_id": 1,
  "game_server_config_id": 2,
  "command_value": "de_dust2",
  "description": "Switch to Dust2"
}
```

## Implementation Checklist

### Phase 1: Backend - Experience API Endpoints
- [ ] Create `GET /gameserver/instance/{instance_id}` endpoint
  - [ ] Query instance from database
  - [ ] Join with config, command_defaults, config_commands
  - [ ] Return structured response with all data
- [ ] Create `POST /gameserver/instance/{instance_id}/command` endpoint
  - [ ] Validate instance is active (not crashed)
  - [ ] Parse command type and ID
  - [ ] Resolve command_value and construct final command
  - [ ] Send to worker via existing stdin mechanism
- [ ] Create `GET /gameserver/{game_server_id}/commands` endpoint
  - [ ] Query all visible commands for game server
  - [ ] Return list for modal dropdown
- [ ] Create `POST /gameserver/config/{config_id}/command` endpoint
  - [ ] Validate config exists
  - [ ] Create new GameServerConfigCommands record
  - [ ] Handle duplicate command+value (unique constraint)

### Phase 2: Backend - Repository Methods
- [ ] Add `DatabaseRepository.get_instance_with_commands(instance_id)`
  - [ ] Join GameServerInstance with GameServerConfig
  - [ ] Eager load command_defaults via config
  - [ ] Eager load config_commands via config
  - [ ] Return tuple: (instance, config, defaults, config_commands)
- [ ] Add `GameServerConfigRepository.get_commands_for_game_server(game_server_id)`
  - [ ] Query GameServerCommand filtered by game_server_id
  - [ ] Filter by is_visible=True
- [ ] Add `GameServerConfigRepository.create_config_command(config_id, command_id, value, description)`
  - [ ] Insert GameServerConfigCommands record
  - [ ] Handle IntegrityError for duplicates

### Phase 3: Frontend - Management UI Go Client
- [ ] Regenerate OpenAPI client after API updates
  - [ ] Run: `bazel build //generated/go/manman:experience_api`
  - [ ] Sync: `./tools/scripts/sync_generated_clients.sh`
- [ ] Add `getInstanceDetails(ctx, instanceID)` to api_client.go
  - [ ] Call new instance details endpoint
  - [ ] Parse response into Go structs
- [ ] Add `executeInstanceCommand(ctx, instanceID, request)` to api_client.go
- [ ] Add `getAvailableCommands(ctx, gameServerID)` to api_client.go
- [ ] Add `createConfigCommand(ctx, configID, request)` to api_client.go

### Phase 4: Frontend - Management UI Handlers
- [ ] Add `handleInstancePage(w, r)` to handlers.go
  - [ ] Parse instance ID from URL
  - [ ] Call `getInstanceDetails()`
  - [ ] Render instance.html template with data
  - [ ] Handle crashed status (pass to template)
- [ ] Add `handleExecuteCommand(w, r)` to handlers.go
  - [ ] Parse form data (command type, ID, optional custom value)
  - [ ] Call `executeInstanceCommand()`
  - [ ] Return success/error HTMX fragment
- [ ] Add `handleAddCommandModal(w, r)` to handlers.go
  - [ ] Get game_server_id from request
  - [ ] Call `getAvailableCommands()`
  - [ ] Render modal content with command list
- [ ] Add `handleCreateCommand(w, r)` to handlers.go
  - [ ] Parse form data (command_id, value, description)
  - [ ] Call `createConfigCommand()`
  - [ ] Trigger page refresh via HX-Trigger

### Phase 5: Frontend - Management UI Templates
- [ ] Create `templates/instance.html`
  - [ ] Header: Server name, instance ID, status badge
  - [ ] Commands section: Two lists (defaults + config)
  - [ ] Each command: name, description, execute button
  - [ ] Crashed status: Add .crashed class, disable buttons
  - [ ] Console panel: Dark background, placeholder text
  - [ ] Add command button: Opens modal
- [ ] Create `templates/instance_commands.html` (HTMX fragment)
  - [ ] Render command lists for auto-refresh
- [ ] Create `templates/add_command_modal.html`
  - [ ] Dropdown of available commands
  - [ ] Text input for command value
  - [ ] Text area for description
  - [ ] Submit button with `hx-post`
- [ ] Update `templates/servers.html`
  - [ ] Make server items clickable
  - [ ] Add link: `/instance/{.InstanceID}`
- [ ] Update `templates/home.html`
  - [ ] Ensure links to instance page work

### Phase 6: Styling
- [ ] Add CSS for instance page
  - [ ] Command panel layout (grid or flex)
  - [ ] Differentiate defaults (muted style) vs config commands
  - [ ] Console panel: dark theme, monospace font
  - [ ] Disabled state styling for crashed servers
  - [ ] Modal styling for add command
- [ ] Add responsive design for mobile
- [ ] Test visual hierarchy (ensure defaults are subtle but clear)

### Phase 7: Testing & Validation
- [ ] Test API endpoints with curl/httpie
  - [ ] Verify instance details response structure
  - [ ] Test command execution with valid/invalid instances
  - [ ] Test command creation with duplicates
- [ ] Test UI flows
  - [ ] Click server → Navigate to instance page
  - [ ] Execute command → See success message
  - [ ] Add command via modal → See in list
  - [ ] View crashed server → Buttons disabled
- [ ] Test auto-refresh behavior
- [ ] Test error handling (404, 500, etc.)

### Phase 8: Documentation
- [ ] Update this plan with actual implementation notes
- [ ] Document API endpoints in OpenAPI spec
- [ ] Add code comments for complex logic
- [ ] Update AGENTS.md if patterns change

## Implementation Notes

### How to Keep This Plan Updated

1. **Before starting a phase**: Review checklist items, adjust if needed
2. **While implementing**: Add sub-bullets with technical decisions
3. **After completing an item**: Check the box and add notes
4. **If blocked**: Add a `BLOCKED:` note with reason
5. **If design changes**: Update the Design section first, then checklist

### Example Updated Item
```markdown
- [x] Create `GET /gameserver/instance/{instance_id}` endpoint
  - Used SQLAlchemy selectinload for eager loading relationships
  - Added error handling for instance not found (404)
  - Response model: InstanceDetailsResponse (added to models.py)
  - DECISION: Always return defaults even if empty list
```

## Key Technical Decisions

### 1. Command Execution Flow
- Instance page sends command_id + type to API
- API resolves the full command string from database
- API sends resolved command to worker via existing `/gameserver/{id}/stdin`
- No direct access from UI to worker stdin (security)

### 2. Status Awareness
- Instance page re-fetches status every 5s (same as home page)
- If status becomes "crashed", buttons are disabled client-side
- API also validates instance is active before executing commands

### 3. Console Output
- Phase 1: Placeholder text only (lorem ipsum)
- Future: WebSocket or SSE for real-time streaming
- Data source: Worker stdout/stderr logs (not implemented yet)

### 4. Command Display Hierarchy
- Config commands are "first-class" (normal styling)
- Default commands are "fallback" (muted styling)
- Both are equally functional, styling is only for visual distinction

### 5. Modal vs Inline Form
- Using modal for "Add Command" to avoid cluttering main page
- HTMX handles modal content swapping
- Modal submission triggers full page refresh to show new command

## Dependencies

- Existing instance status logic (used in active servers list)
- Existing stdin command infrastructure (`/gameserver/{id}/stdin`)
- OpenAPI client regeneration tooling
- HTMX patterns already in use

## Future Enhancements (Out of Scope)

- Real console output streaming
- Command history/audit log
- Command scheduling/automation
- Bulk command execution across multiple instances
- Command templates with variable substitution UI

## Risks & Mitigation

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| OpenAPI spec changes break Go client | Medium | High | Regenerate client immediately after API changes, test |
| Command execution fails silently | Medium | Medium | Add proper error handling, return status in response |
| Crashed server allows command execution | Low | Medium | Validate instance status in API before execution |
| Modal UX is clunky with HTMX | Medium | Low | Test early, consider alternatives if needed |

## Success Criteria

- [ ] User can navigate from active servers to instance detail page
- [ ] User can see all available commands (defaults + config)
- [ ] User can execute commands with visual feedback
- [ ] User can add new config commands via modal
- [ ] Crashed servers show red and disable interactions
- [ ] Console panel displays placeholder text
- [ ] Page auto-refreshes status every 5 seconds
- [ ] No breaking changes to existing functionality

---

**Next Steps**: Begin Phase 1 - Create Experience API endpoints
