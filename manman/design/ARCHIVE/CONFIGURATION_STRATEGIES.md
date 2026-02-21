# Configuration Strategy System

## Problem

Game servers have diverse configuration needs that don't fit into simple key-value parameters:

1. **CLI arguments**: `--max-players=20 --difficulty=hard`
2. **Environment variables**: `MAX_PLAYERS=20`
3. **Properties files**: `server.properties` with `max-players=20`
4. **JSON configs**: Nested structures like `{"server": {"maxPlayers": 20}}`
5. **YAML configs**: Multi-document, anchors, complex nesting
6. **INI files**: Sections like `[ServerSettings]\nMaxPlayers=20`
7. **Custom formats**: Lua, TOML, game-specific formats
8. **Multiple files**: Some games need 5+ config files

**Current limitation**: Only supports `args_template` and `env_template` - too simplistic.

## Solution: Configuration Strategy + Patch Layers

### Core Concept

Each parameter application is a **configuration strategy** that defines:
- **What** to apply (parameter values)
- **How** to apply (rendering strategy)
- **Where** to apply (target: CLI, env, file path)

Configurations are built through **patch layering**:

```
Base Template (from image or predefined)
  ↓ Apply GameConfig layer (default values)
  ↓ Apply ServerGameConfig layer (server-specific overrides)
  ↓ Apply Session layer (runtime overrides)
  ↓ Render final configuration
```

## Schema Design

### Configuration Strategies Table

```sql
CREATE TABLE configuration_strategies (
    strategy_id BIGSERIAL PRIMARY KEY,
    game_id BIGINT REFERENCES games(game_id) ON DELETE CASCADE,

    -- Strategy metadata
    name VARCHAR(100) NOT NULL,  -- "Server Properties", "CLI Args", "GameUserSettings.ini"
    description TEXT,

    -- Strategy type defines HOW to render
    strategy_type VARCHAR(50) NOT NULL CHECK (strategy_type IN (
        'cli_args',           -- Command line arguments
        'env_vars',           -- Environment variables
        'file_properties',    -- key=value properties file
        'file_json',          -- JSON file with merge/patch
        'file_yaml',          -- YAML file with merge
        'file_ini',           -- INI file with sections
        'file_xml',           -- XML file with XPath updates
        'file_lua',           -- Lua config file
        'file_custom'         -- Custom format with template
    )),

    -- Target location
    target_path TEXT,  -- File path like "/data/server.properties" or null for CLI/env

    -- Base template/content
    base_template TEXT,  -- Starting point before patches

    -- Rendering options (JSONB for flexibility)
    render_options JSONB DEFAULT '{}',  -- Format-specific options

    -- Ordering for multi-strategy configs
    apply_order INT DEFAULT 0,  -- Lower numbers applied first

    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(game_id, name)
);

CREATE INDEX idx_config_strategies_game_id ON configuration_strategies(game_id);
```

### Strategy Parameter Bindings

```sql
-- Links parameters to configuration strategies
CREATE TABLE strategy_parameter_bindings (
    binding_id BIGSERIAL PRIMARY KEY,
    strategy_id BIGINT REFERENCES configuration_strategies(strategy_id) ON DELETE CASCADE,
    param_id BIGINT REFERENCES parameter_definitions(param_id) ON DELETE CASCADE,

    -- How to apply this parameter in this strategy
    binding_type VARCHAR(50) NOT NULL CHECK (binding_type IN (
        'direct',        -- Use value as-is
        'template',      -- Use template with {{param}} substitution
        'json_path',     -- JSONPath like $.server.maxPlayers
        'xpath',         -- XPath for XML
        'ini_section'    -- INI section.key format
    )),

    -- Target location within the strategy
    target_key TEXT NOT NULL,  -- e.g., "max-players", "$.server.maxPlayers", "[ServerSettings]/MaxPlayers"

    -- Optional transformation template
    value_template TEXT,  -- e.g., "--{{key}}={{value}}" or "MaxPlayers={{value}}"

    -- Conditional application
    condition_expr TEXT,  -- e.g., "only_if:pvp=true"

    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(strategy_id, param_id)
);

CREATE INDEX idx_strategy_bindings_strategy_id ON strategy_parameter_bindings(strategy_id);
CREATE INDEX idx_strategy_bindings_param_id ON strategy_parameter_bindings(param_id);
```

### Configuration Patches

```sql
-- Stores configuration overrides at each level
CREATE TABLE configuration_patches (
    patch_id BIGSERIAL PRIMARY KEY,
    strategy_id BIGINT REFERENCES configuration_strategies(strategy_id) ON DELETE CASCADE,

    -- What level this patch applies to
    patch_level VARCHAR(50) NOT NULL CHECK (patch_level IN (
        'game_config',
        'server_game_config',
        'session'
    )),

    -- Which entity this patch belongs to
    entity_id BIGINT NOT NULL,  -- config_id, sgc_id, or session_id depending on patch_level

    -- Patch content (strategy-specific format)
    patch_content TEXT,  -- Could be JSON patch, YAML merge, or template
    patch_format VARCHAR(50) DEFAULT 'template',  -- 'json_merge_patch', 'json_patch', 'yaml_merge', 'template'

    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(strategy_id, patch_level, entity_id)
);

CREATE INDEX idx_config_patches_strategy ON configuration_patches(strategy_id);
CREATE INDEX idx_config_patches_entity ON configuration_patches(patch_level, entity_id);
```

## Examples

### Example 1: Minecraft Server

**Base Template** (server.properties):
```properties
# Minecraft Server Configuration
enable-jmx-monitoring=false
rcon.port=25575
level-seed=
gamemode=survival
enable-command-block=false
enable-query=false
level-name=world
motd=A Minecraft Server
pvp=true
difficulty=normal
max-players=20
spawn-protection=16
allow-flight=false
```

**Configuration Strategy**:
```sql
INSERT INTO configuration_strategies (
    game_id, name, strategy_type, target_path, base_template
) VALUES (
    1,
    'Server Properties',
    'file_properties',
    '/data/server.properties',
    '# Minecraft Server Configuration\nenable-jmx-monitoring=false\n...'  -- Full template above
);

-- Bind parameters to properties
INSERT INTO strategy_parameter_bindings (strategy_id, param_id, binding_type, target_key, value_template)
VALUES
    (1, 10, 'direct', 'max-players', 'max-players={{value}}'),
    (1, 11, 'direct', 'difficulty', 'difficulty={{value}}'),
    (1, 12, 'direct', 'pvp', 'pvp={{value}}'),
    (1, 13, 'direct', 'motd', 'motd={{value}}');
```

**GameConfig Patch**:
```sql
-- GameConfig sets defaults
INSERT INTO configuration_patches (strategy_id, patch_level, entity_id, patch_content)
VALUES (1, 'game_config', 5, 'max-players=20\ndifficulty=normal\npvp=true');
```

**Session Patch**:
```sql
-- Session overrides for testing
INSERT INTO configuration_patches (strategy_id, patch_level, entity_id, patch_content)
VALUES (1, 'session', 123, 'max-players=5\ndifficulty=peaceful');
```

**Final Rendered Config**:
```properties
# Minecraft Server Configuration
enable-jmx-monitoring=false
rcon.port=25575
level-seed=
gamemode=survival
enable-command-block=false
enable-query=false
level-name=world
motd=A Minecraft Server
pvp=true
difficulty=peaceful          # ← Session override
max-players=5                # ← Session override
spawn-protection=16
allow-flight=false
```

### Example 2: Valheim (Environment Variables)

```sql
INSERT INTO configuration_strategies (
    game_id, name, strategy_type, target_path, base_template
) VALUES (
    2,
    'Environment Variables',
    'env_vars',
    NULL,  -- No file path for env vars
    NULL   -- No base template needed
);

-- Bind parameters
INSERT INTO strategy_parameter_bindings (strategy_id, param_id, binding_type, target_key)
VALUES
    (2, 20, 'direct', 'SERVER_NAME'),
    (2, 21, 'direct', 'WORLD_NAME'),
    (2, 22, 'direct', 'SERVER_PASS');
```

### Example 3: ARK (Complex JSON Config)

**Base Template** (GameUserSettings.json):
```json
{
  "ServerSettings": {
    "SessionName": "My ARK Server",
    "ServerPassword": "",
    "MaxPlayers": 70,
    "DifficultyOffset": 1.0,
    "ServerPVE": false
  },
  "SessionSettings": {
    "SessionName": "My Session"
  }
}
```

**Strategy**:
```sql
INSERT INTO configuration_strategies (
    game_id, name, strategy_type, target_path, base_template, render_options
) VALUES (
    3,
    'Game User Settings',
    'file_json',
    '/data/GameUserSettings.json',
    '{"ServerSettings": {...}}',  -- Full JSON above
    '{"merge_strategy": "deep", "array_merge": "replace"}'::jsonb
);

-- Bind with JSON paths
INSERT INTO strategy_parameter_bindings (strategy_id, param_id, binding_type, target_key)
VALUES
    (3, 30, 'json_path', '$.ServerSettings.MaxPlayers'),
    (3, 31, 'json_path', '$.ServerSettings.ServerPassword'),
    (3, 32, 'json_path', '$.ServerSettings.ServerPVE');
```

**GameConfig Patch** (JSON Merge Patch format):
```sql
INSERT INTO configuration_patches (strategy_id, patch_level, entity_id, patch_content, patch_format)
VALUES (3, 'game_config', 10, '{
  "ServerSettings": {
    "MaxPlayers": 70,
    "ServerPVE": false
  }
}', 'json_merge_patch');
```

**Session Patch**:
```sql
INSERT INTO configuration_patches (strategy_id, patch_level, entity_id, patch_content, patch_format)
VALUES (3, 'session', 456, '{
  "ServerSettings": {
    "MaxPlayers": 10,
    "DifficultyOffset": 0.5
  }
}', 'json_merge_patch');
```

## Rendering Pipeline

### Step-by-Step Process

1. **Fetch Strategy** for a session
2. **Load Base Template** from `configuration_strategies.base_template`
3. **Apply Patches in Order**:
   - GameConfig patch (defaults)
   - ServerGameConfig patch (server-specific)
   - Session patch (runtime overrides)
4. **Render Final Output** based on `strategy_type`
5. **Return for Preview** or deployment

### Rendering Algorithm (Go)

```go
type ConfigRenderer struct {
    strategy *ConfigurationStrategy
    params   map[string]string  // Merged parameters
}

func (r *ConfigRenderer) Render() (string, error) {
    switch r.strategy.StrategyType {
    case "cli_args":
        return r.renderCLIArgs()
    case "env_vars":
        return r.renderEnvVars()
    case "file_properties":
        return r.renderPropertiesFile()
    case "file_json":
        return r.renderJSONFile()
    case "file_yaml":
        return r.renderYAMLFile()
    case "file_ini":
        return r.renderINIFile()
    default:
        return "", fmt.Errorf("unsupported strategy type: %s", r.strategy.StrategyType)
    }
}

func (r *ConfigRenderer) renderPropertiesFile() (string, error) {
    // Start with base template
    lines := strings.Split(r.strategy.BaseTemplate, "\n")

    // Apply patches layer by layer
    for _, patch := range r.getOrderedPatches() {
        lines = r.applyPropertiesPatch(lines, patch)
    }

    return strings.Join(lines, "\n"), nil
}

func (r *ConfigRenderer) renderJSONFile() (string, error) {
    // Parse base template
    var base map[string]interface{}
    json.Unmarshal([]byte(r.strategy.BaseTemplate), &base)

    // Apply JSON patches in order
    for _, patch := range r.getOrderedPatches() {
        switch patch.PatchFormat {
        case "json_merge_patch":
            base = r.applyJSONMergePatch(base, patch.Content)
        case "json_patch":
            base = r.applyJSONPatch(base, patch.Content)
        }
    }

    // Render final JSON
    output, _ := json.MarshalIndent(base, "", "  ")
    return string(output), nil
}
```

## Preview Endpoint

Users can preview rendered configs before starting a session:

```protobuf
message PreviewConfigurationRequest {
  int64 session_id = 1;  // Preview for this session
  map<string, string> parameter_overrides = 2;  // Try different values
}

message PreviewConfigurationResponse {
  repeated RenderedConfiguration configurations = 1;
}

message RenderedConfiguration {
  string strategy_name = 1;  // "Server Properties", "CLI Args", etc.
  string strategy_type = 2;  // "file_json", "env_vars", etc.
  string target_path = 3;    // Where it will be applied
  string rendered_content = 4;  // Final rendered configuration

  // Show the layering
  string base_content = 5;
  repeated PatchLayer patches = 6;
}

message PatchLayer {
  string level = 1;  // "game_config", "server_game_config", "session"
  string patch_content = 2;
}
```

**Example Preview Response**:

```json
{
  "configurations": [
    {
      "strategy_name": "Server Properties",
      "strategy_type": "file_properties",
      "target_path": "/data/server.properties",
      "rendered_content": "max-players=5\ndifficulty=peaceful\npvp=true\n...",
      "base_content": "max-players=20\ndifficulty=normal\n...",
      "patches": [
        {"level": "game_config", "patch_content": "max-players=20\ndifficulty=normal"},
        {"level": "session", "patch_content": "max-players=5\ndifficulty=peaceful"}
      ]
    },
    {
      "strategy_name": "CLI Arguments",
      "strategy_type": "cli_args",
      "target_path": null,
      "rendered_content": "--nogui --port=25565",
      "patches": [...]
    }
  ]
}
```

## Strategy Type Details

### CLI Args (`cli_args`)

**Target**: Container command
**Format**: Space-separated arguments

```
--max-players={{max_players}} --difficulty={{difficulty}} --port={{port}}
```

### Environment Variables (`env_vars`)

**Target**: Container environment
**Format**: KEY=value pairs

```
MAX_PLAYERS={{max_players}}
DIFFICULTY={{difficulty}}
SERVER_NAME={{server_name}}
```

### Properties File (`file_properties`)

**Target**: File like `server.properties`
**Format**: key=value lines

**Patch strategy**: Line-by-line replacement

### JSON File (`file_json`)

**Target**: `config.json`, `settings.json`, etc.
**Format**: JSON

**Patch strategies**:
- `json_merge_patch` (RFC 7386) - Deep merge
- `json_patch` (RFC 6902) - Array of operations

### YAML File (`file_yaml`)

**Target**: `config.yaml`, `values.yaml`, etc.
**Format**: YAML

**Patch strategy**: Strategic merge (Kubernetes-style)

### INI File (`file_ini`)

**Target**: `server.ini`, `GameUserSettings.ini`, etc.
**Format**: INI with sections

```ini
[ServerSettings]
MaxPlayers=70
ServerPassword=secret

[SessionSettings]
SessionName=My Game
```

**Patch strategy**: Section-aware merge

## Migration from Current System

### Phase 1: Create New Tables

```sql
-- Run migration 007_configuration_strategies.up.sql
CREATE TABLE configuration_strategies (...);
CREATE TABLE strategy_parameter_bindings (...);
CREATE TABLE configuration_patches (...);
```

### Phase 2: Migrate Existing Configs

```sql
-- Convert args_template to cli_args strategy
INSERT INTO configuration_strategies (game_id, name, strategy_type, base_template)
SELECT game_id, 'CLI Arguments', 'cli_args', args_template
FROM game_configs
WHERE args_template IS NOT NULL;

-- Convert env_template to env_vars strategy
INSERT INTO configuration_strategies (game_id, name, strategy_type, base_template)
SELECT game_id, 'Environment Variables', 'env_vars',
       array_to_string(array_agg(key || '={{' || key || '}}'), E'\n')
FROM game_configs, jsonb_each_text(env_template)
GROUP BY game_id;
```

### Phase 3: Update Application

- Add `ConfigRenderer` service
- Implement preview endpoint
- Update session startup to use strategies

### Phase 4: Deprecate Old Fields

```sql
ALTER TABLE game_configs DROP COLUMN args_template;
ALTER TABLE game_configs DROP COLUMN env_template;
```

## Benefits

✅ **Supports any config format** - JSON, YAML, INI, properties, custom
✅ **Patch-based layering** - Clear inheritance model
✅ **Preview before start** - Users see exactly what will run
✅ **Extensible** - Easy to add new strategy types
✅ **Normalized** - Works with normalized parameter schema
✅ **Multi-file support** - One strategy per file
✅ **Conditional logic** - Parameters can be conditional
✅ **Audit trail** - Can track what changed at each layer

## Future Enhancements

- **Strategy templates** - Reusable patterns for common games
- **Validation** - Validate rendered config against JSON schema
- **Diff view** - Show what changed between layers
- **Import from image** - Extract config from Docker image
- **Config versioning** - Track changes to strategies over time
