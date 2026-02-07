# Parameter System

## Overview

The ManManV2 parameter system provides a flexible, type-safe way to configure game servers across three levels:

1. **GameConfig** - Base parameter definitions with defaults
2. **ServerGameConfig** - Server-specific overrides
3. **Session** - Per-execution overrides

Parameters flow through the system with later levels overriding earlier ones:

```
GameConfig defaults → ServerGameConfig overrides → Session overrides = Final parameters
```

## Parameter Definition

Parameters are defined in GameConfig with full metadata:

```protobuf
message Parameter {
  string key = 1;           // Parameter name (e.g., "max_players")
  string value = 2;         // Current value
  string type = 3;          // "string" | "int" | "bool" | "secret"
  string description = 4;   // Human-readable description
  bool required = 5;        // Must be provided
  string default_value = 6; // Default if not specified
}
```

### Example: GameConfig with Parameters

```json
{
  "name": "Minecraft Server Config",
  "image": "minecraft:latest",
  "args_template": "--max-players={{max_players}} --difficulty={{difficulty}}",
  "parameters": [
    {
      "key": "max_players",
      "type": "int",
      "description": "Maximum number of players",
      "required": true,
      "default_value": "20"
    },
    {
      "key": "difficulty",
      "type": "string",
      "description": "Game difficulty (peaceful, easy, normal, hard)",
      "required": false,
      "default_value": "normal"
    },
    {
      "key": "pvp",
      "type": "bool",
      "description": "Enable player vs player combat",
      "required": false,
      "default_value": "false"
    },
    {
      "key": "server_password",
      "type": "secret",
      "description": "Server password (if password-protected)",
      "required": false,
      "default_value": ""
    }
  ]
}
```

## Parameter Types

### `string`
Any text value. No validation beyond presence checks.

**Examples:**
- `"difficulty": "hard"`
- `"world_name": "MyWorld"`
- `"motd": "Welcome to my server!"`

### `int`
Integer numbers. Validated to ensure the value parses as an integer.

**Examples:**
- `"max_players": "20"`
- `"view_distance": "10"`
- `"port": "25565"`

**Validation:**
- ✅ `"42"`, `"0"`, `"-5"`
- ❌ `"3.14"`, `"not_a_number"`, `""`

### `bool`
Boolean values. Accepts `true`/`false` (case-insensitive).

**Examples:**
- `"pvp": "true"`
- `"whitelist": "false"`

**Validation:**
- ✅ `"true"`, `"false"`, `"True"`, `"FALSE"`
- ❌ `"yes"`, `"no"`, `"1"`, `"0"`, `"maybe"`

### `secret`
Sensitive string values (passwords, API keys, etc.). Treated as strings but may receive special handling in UI/logs.

**Examples:**
- `"server_password": "super_secret_123"`
- `"rcon_password": "admin_pass"`
- `"api_key": "sk_live_abc123xyz"`

## Parameter Merging

Parameters are merged across three levels with later levels taking precedence:

### Example Flow

**1. GameConfig Definitions:**
```json
{
  "parameters": [
    {"key": "max_players", "default_value": "20"},
    {"key": "difficulty", "default_value": "normal"},
    {"key": "pvp", "default_value": "false"}
  ]
}
```

**2. ServerGameConfig Overrides:**
```json
{
  "parameters": {
    "max_players": "50",
    "difficulty": "hard"
  }
}
```

**3. Session Overrides:**
```json
{
  "parameters": {
    "max_players": "10",
    "pvp": "true"
  }
}
```

**4. Final Merged Parameters:**
```json
{
  "max_players": "10",     // From Session (latest)
  "difficulty": "hard",     // From ServerGameConfig
  "pvp": "true"             // From Session (latest)
}
```

## Template Rendering

Parameter values can be injected into configuration templates using `{{parameter_name}}` syntax.

### Args Template

```
--max-players={{max_players}} --difficulty={{difficulty}} --pvp={{pvp}}
```

**Rendered with merged parameters:**
```
--max-players=10 --difficulty=hard --pvp=true
```

### Environment Variables Template

```json
{
  "MAX_PLAYERS": "{{max_players}}",
  "DIFFICULTY": "{{difficulty}}",
  "SERVER_NAME": "{{server_name}}"
}
```

**Rendered:**
```json
{
  "MAX_PLAYERS": "10",
  "DIFFICULTY": "hard",
  "SERVER_NAME": "MyAwesomeServer"
}
```

### Missing Parameters

If a template references a parameter that doesn't exist, the placeholder is left unchanged:

```
--password={{server_password}}
```

If `server_password` is not provided:
```
--password={{server_password}}
```

This allows optional parameters to be safely omitted.

## Validation

### Required Parameters

Parameters marked as `required: true` must be provided at one of the three levels (GameConfig default, ServerGameConfig override, or Session override).

**Validation Error:**
```
parameter "max_players": required parameter is missing
```

### Type Validation

All parameter values are validated against their declared type.

**Integer Validation Error:**
```
parameter "max_players": must be an integer, got "twenty"
```

**Boolean Validation Error:**
```
parameter "pvp": must be a boolean (true/false), got "yes"
```

### Unknown Parameters

Parameters provided in ServerGameConfig or Session that aren't defined in GameConfig trigger warnings (not errors):

**Validation Warning:**
```
parameter "unknown_param": Unknown parameter - not defined in game config
```

This allows flexibility while alerting users to potential typos.

## API Usage

### Creating a GameConfig with Parameters

```bash
grpcurl -plaintext -d '{
  "game_id": 1,
  "name": "Minecraft Survival",
  "image": "minecraft:latest",
  "args_template": "--max-players={{max_players}} --difficulty={{difficulty}}",
  "parameters": [
    {
      "key": "max_players",
      "type": "int",
      "description": "Maximum players",
      "required": true,
      "default_value": "20"
    },
    {
      "key": "difficulty",
      "type": "string",
      "description": "Difficulty level",
      "required": false,
      "default_value": "normal"
    }
  ]
}' localhost:50051 manman.v1.ManManAPI/CreateGameConfig
```

### Deploying with Parameter Overrides

```bash
grpcurl -plaintext -d '{
  "server_id": 1,
  "game_config_id": 5,
  "parameters": {
    "max_players": "50",
    "difficulty": "hard"
  }
}' localhost:50051 manman.v1.ManManAPI/DeployGameConfig
```

### Starting Session with Per-Execution Overrides

```bash
grpcurl -plaintext -d '{
  "server_game_config_id": 10,
  "parameters": {
    "max_players": "10",
    "difficulty": "peaceful"
  }
}' localhost:50051 manman.v1.ManManAPI/StartSession
```

### Validating Deployment

```bash
grpcurl -plaintext -d '{
  "server_id": 1,
  "game_config_id": 5,
  "parameters": {
    "max_players": "not_a_number"
  }
}' localhost:50051 manman.v1.ManManAPI/ValidateDeployment
```

**Response:**
```json
{
  "valid": false,
  "issues": [
    {
      "severity": "VALIDATION_SEVERITY_ERROR",
      "field": "parameters.max_players",
      "message": "Invalid value for parameter 'max_players': must be an integer, got \"not_a_number\"",
      "suggestion": "Expected type: int"
    }
  ]
}
```

## Implementation

### Core Library

The parameter system is implemented in `libs/go/params/`:

- `MergeParams()` - Merge parameters across levels
- `ValidateParams()` - Validate types and required fields
- `RenderTemplate()` - Replace {{placeholders}} in templates
- `ConvertToType()` - Convert string values to typed values
- `GetMissingRequired()` - Find missing required parameters
- `GetUnknownParams()` - Find undefined parameters

### Integration Points

**1. Validation Handler** (`manman/api/handlers/validation.go`)
- Validates parameters before deployment
- Returns detailed error messages with suggestions

**2. Parameter Helpers** (`manman/api/handlers/param_helpers.go`)
- `MergeAndValidateParameters()` - Complete merge + validation flow
- `ValidateParametersWithDetails()` - Detailed validation issues
- `RenderGameConfigTemplates()` - Render args/env templates

**3. Session Handler** (planned)
- Will use `MergeAndValidateParameters()` when starting sessions
- Passes merged parameters to host manager via RabbitMQ

## Best Practices

### Defining Parameters

1. **Use descriptive keys**: `max_players` not `mp`
2. **Provide good descriptions**: Users will see these in validation errors
3. **Set reasonable defaults**: Make it easy to get started
4. **Mark truly required params as required**: Don't over-require
5. **Choose appropriate types**: Use `int` for numbers, `bool` for flags

### Using Parameters

1. **Override at the appropriate level**:
   - GameConfig: Universal defaults
   - ServerGameConfig: Server-specific settings (e.g., allocated ports)
   - Session: Per-execution tweaks (e.g., testing with fewer players)

2. **Template safely**: Use `{{param}}` syntax, not string interpolation
3. **Validate before starting**: Call `ValidateDeployment` RPC first
4. **Handle secrets carefully**: Mark sensitive params as `type: "secret"`

## Limitations

- Parameters are always string-typed in storage (JSONB map[string]string)
- Type conversion happens at validation/rendering time
- No support for complex types (arrays, objects)
- No parameter interdependencies (e.g., "if pvp=true, then whitelist required")

## Future Enhancements

- Parameter groups/categories for UI organization
- Conditional parameters (if/else logic)
- Parameter validation rules (regex, min/max, enum values)
- Parameter templates/presets (e.g., "casual", "competitive" profiles)
- Auto-discovery of parameters from Docker image metadata
