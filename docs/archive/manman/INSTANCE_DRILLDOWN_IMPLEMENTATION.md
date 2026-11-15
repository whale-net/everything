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
- [x] Create `GET /gameserver/instance/{instance_id}` endpoint
  - Uses `get_instance_with_commands()` from repository
  - Returns InstanceDetailsResponse with instance, config, defaults, config_commands
- [x] Create `POST /gameserver/instance/{instance_id}/command` endpoint
  - Validates instance is active (checks for CRASHED status)
  - Resolves command string from defaults or config_commands
  - Sends via existing stdin mechanism (CommandType.STDIN)
- [x] Create `GET /gameserver/{game_server_id}/commands` endpoint
  - Returns all visible GameServerCommand for game server
  - Used to populate add command modal dropdown
- [x] Create `POST /gameserver/config/{config_id}/command` endpoint
  - Creates new GameServerConfigCommands record
  - Handles IntegrityError for duplicate command+value

### Phase 2: Backend - Repository Methods
- [x] Add `DatabaseRepository.get_instance_with_commands(instance_id)`
  - Uses SQLAlchemy selectinload for eager loading
  - Joins GameServerInstance → GameServerConfig → GameServer
  - Queries command_defaults (via game_server_id)
  - Queries config_commands (via config_id)
  - Returns tuple: (instance, config, defaults, config_cmds)
- [x] Add `GameServerConfigRepository.get_commands_for_game_server(game_server_id)`
  - Filters GameServerCommand by game_server_id and is_visible=True
- [x] Add `GameServerConfigRepository.create_config_command(config_id, command_id, value, description)`
  - Creates GameServerConfigCommands record
  - Expunges before returning

### Phase 3: Frontend - Management UI Go Client
- [x] Regenerate OpenAPI client after API updates
  - Built: `bazel build //generated/go/manman:experience_api`
  - Synced: `./tools/scripts/sync_generated_clients.sh`
  - Generated new models: InstanceDetailsResponse, ExecuteCommandRequest, etc.
- [x] Add `getInstanceDetails(ctx, instanceID)` to api_client.go
- [x] Add `executeInstanceCommand(ctx, instanceID, request)` to api_client.go
- [x] Add `getAvailableCommands(ctx, gameServerID)` to api_client.go
- [x] Add `createConfigCommand(ctx, configID, request)` to api_client.go

### Phase 4: Frontend - Management UI Handlers
- [x] Add `handleInstancePage(w, r)` to handlers.go
  - Parses instance ID from URL path
  - Calls `getInstanceDetails()`
  - Renders instance.html with InstancePageData
- [x] Add `handleExecuteCommand(w, r)` to handlers.go
  - Parses form data: instance_id, command_type, command_id, custom_value
  - Uses NullableString for optional custom_value
  - Returns success message as HTMX fragment
- [x] Add `handleAddCommandModal(w, r)` to handlers.go
  - Gets game_server_id and config_id from query params
  - Fetches available commands
  - Renders add_command_modal.html
- [x] Add `handleCreateCommand(w, r)` to handlers.go
  - Parses form data: config_id, command_id, command_value, description
  - Uses NullableString for optional description
  - Triggers HX-Trigger: commandCreated for page refresh

### Phase 5: Frontend - Management UI Templates
- [x] Create `templates/instance.html`
  - Header: Back link, server name, instance/config IDs, status badge
  - Two-column layout: Commands panel + Console panel
  - Commands list: Renders both config_commands and command_defaults
  - Visual distinction: config commands (normal), defaults (muted with opacity 0.85)
  - Crashed state: Disables all execute buttons, adds .crashed class
  - Console panel: Dark theme with placeholder lorem ipsum
  - Add command button opens modal
- [x] Create `templates/add_command_modal.html`
  - Form with dropdown of available commands
  - Text input for command_value
  - Textarea for optional description
  - Submits via hx-post to /api/create-command
  - Triggers commandCreated event on success
- [x] Update `templates/servers.html`
  - Changed div.server-item to a.server-item with href="/instance/{{.InstanceID}}"
  - Made server list items clickable links
- [x] Update `templates/home.html`
  - Added hover states for server items
  - Added transition effects for smooth hover

### Phase 6: Styling
- [x] Add CSS for instance page
  - Two-column grid layout for commands and console
  - Command items with border-left color coding (blue for config, gray for defaults)
  - Console panel: #1e1e1e background, monospace font, scrollable
  - Disabled state: opacity 0.5, cursor not-allowed
  - Modal styling integrated into instance.html
- [x] Add responsive design patterns
- [x] Differentiate command types visually (completed with opacity and border colors)

### Phase 7: Testing & Validation
- [ ] Test API endpoints with running system
  - [ ] Verify instance details response structure
  - [ ] Test command execution with valid/invalid instances
  - [ ] Test command creation with duplicates
- [ ] Test UI flows
  - [ ] Click server → Navigate to instance page
  - [ ] Execute command → See success message
  - [ ] Add command via modal → See in list
  - [ ] View crashed server → Buttons disabled
- [ ] Test auto-refresh behavior (if implemented)
- [ ] Test error handling (404, 500, etc.)

### Phase 8: Documentation
- [x] Update this plan with actual implementation notes
- [ ] Add usage documentation for new features
- [ ] Document any design decisions or trade-offs

## Implementation Notes

### Technical Decisions Made

1. **Command Resolution Logic**
   - Execute command endpoint resolves the full command string server-side
   - Simple template substitution: `{value}` and `{map}` placeholders
   - Command sent to worker via existing CommandType.STDIN mechanism
   - Instance validation checks CRASHED status from latest status info

2. **Database Queries**
   - Used SQLAlchemy `selectinload()` for eager loading relationships
   - Queries command_defaults by joining through game_server
   - Queries config_commands directly by config_id
   - All objects expunged before returning to avoid session issues

3. **Go OpenAPI Client**
   - Generated client uses `NullableString` for optional fields
   - Must use `*experience_api.NewNullableString(&value)` for assignment
   - Fixed forward reference issues in Python by removing quotes from type hints

4. **HTMX Patterns**
   - Commands execute via `hx-post` with `hx-vals` for parameters
   - Success messages inserted into `#command-feedback` div
   - Modal content loaded via `hx-get` on modal open
   - `HX-Trigger: commandCreated` forces full page reload to show new command

5. **Visual Design**
   - Config commands: Standard styling, blue border-left
   - Default commands: opacity 0.85, gray border-left, "(default)" label
   - Crashed servers: opacity 0.5, buttons disabled
   - Console placeholder: Lorem ipsum in dark monospace panel

### Challenges Encountered

1. **Pydantic Forward References**
   - OpenAPI spec generation failed with `list["GameServerCommand"]` return type
   - Solution: Import at module level, remove quotes from type hints

2. **OpenAPI NullableString**
   - Go client generated NullableString type for optional fields
   - Cannot assign `*string` directly
   - Solution: Use `*experience_api.NewNullableString(&value)` wrapper

3. **Template Data Access**
   - Accessing outer scope in nested Go template loops
   - Solution: Use `$.Instance.Instance.EndDate` with `$` prefix for root scope

### Files Modified

**Backend**:
- `manman/src/host/api/shared/models.py` - Added 4 new request/response models
- `manman/src/host/api/experience/api.py` - Added 4 new endpoints
- `manman/src/repository/database.py` - Added 3 new repository methods
- `manman/src/models.py` - No changes (models already existed)

**Frontend Go**:
- `manman/management-ui/api_client.go` - Added 4 API wrapper methods
- `manman/management-ui/handlers.go` - Added 4 new handlers + 2 data structs
- `manman/management-ui/main.go` - Registered 4 new routes

**Frontend Templates**:
- `manman/management-ui/templates/instance.html` - New full page template
- `manman/management-ui/templates/add_command_modal.html` - New modal form
- `manman/management-ui/templates/servers.html` - Changed div to anchor tags
- `manman/management-ui/templates/home.html` - Added hover CSS

**Generated**:
- `generated/go/manman/experience_api/*` - Regenerated OpenAPI client (22 files)

### Next Steps for Testing

1. Start full environment with `tilt up --file=manman/Tiltfile`
2. Create test game server commands and defaults in database
3. Start a game server instance
4. Navigate to instance page and test:
   - Command execution
   - Adding custom commands
   - Crashed server state handling
5. Test error scenarios:
   - Non-existent instance ID
   - Duplicate command creation
   - Executing command on crashed server

---

**Implementation Status**: ✅ Complete (Phases 1-6)  
**Ready for Testing**: Yes  
**Breaking Changes**: None

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
