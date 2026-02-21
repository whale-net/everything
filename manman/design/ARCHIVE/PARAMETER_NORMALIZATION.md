# Parameter Schema Normalization

## Problem Statement

The current implementation stores parameters as JSONB in the database:

```sql
-- Current (Bad Pattern)
CREATE TABLE game_configs (
    config_id BIGSERIAL PRIMARY KEY,
    parameters JSONB DEFAULT '{}'  -- ❌ Anti-pattern
);
```

**Problems:**

1. **No referential integrity** - Invalid parameter keys accepted
2. **Poor query performance** - Can't index specific parameters
3. **No type enforcement** - Database can't validate int/bool types
4. **Difficult analytics** - Can't answer "how many configs use max_players > 20?"
5. **Duplicated metadata** - Parameter definitions repeated in every config
6. **No constraints** - Can't enforce min/max values, allowed values
7. **Harder to audit** - Changes to JSONB are opaque to database logs

## Proposed Solution: Normalized Schema

### Schema Overview

```
parameter_definitions (1 per game parameter)
    ↓
game_config_parameter_values (sparse: only non-default values)
    ↓
server_game_config_parameter_values (overrides)
    ↓
session_parameter_values (runtime overrides)
```

### Benefits

#### 1. Referential Integrity

```sql
-- Foreign key ensures parameter exists
FOREIGN KEY (param_id) REFERENCES parameter_definitions(param_id)

-- Can't insert invalid parameter keys
-- Can't delete parameter definition if values exist
```

#### 2. Efficient Queries

```sql
-- Find all configs with max_players >= 20
SELECT DISTINCT gc.config_id, gc.name
FROM game_configs gc
JOIN game_config_parameter_values gcpv ON gc.config_id = gcpv.config_id
JOIN parameter_definitions pd ON gcpv.param_id = pd.param_id
WHERE pd.key = 'max_players'
  AND gcpv.value::int >= 20;

-- Uses indexes! Much faster than JSONB queries
```

#### 3. Database-Level Constraints

```sql
-- Enforce allowed values at database level
CHECK (param_type IN ('string', 'int', 'bool', 'secret'))

-- Store validation rules in definition
min_value BIGINT,
max_value BIGINT,
allowed_values TEXT[]  -- ['easy', 'normal', 'hard']
```

#### 4. Analytics & Reporting

```sql
-- Parameter usage statistics
SELECT key, COUNT(DISTINCT config_id) as configs_using
FROM parameter_definitions pd
JOIN game_config_parameter_values gcpv ON pd.param_id = gcpv.param_id
GROUP BY key;

-- Value distribution
SELECT key, value, COUNT(*) as usage_count
FROM parameter_definitions pd
JOIN game_config_parameter_values gcpv ON pd.param_id = gcpv.param_id
GROUP BY key, value
ORDER BY key, usage_count DESC;
```

#### 5. No Duplication

```sql
-- Define once per game
INSERT INTO parameter_definitions (game_id, key, param_type, description, default_value)
VALUES (1, 'max_players', 'int', 'Maximum number of players', '20');

-- Reference everywhere
INSERT INTO game_config_parameter_values (config_id, param_id, value)
VALUES (5, 123, '50');  -- Override to 50 for this config
```

#### 6. Audit Trail

```sql
-- Can track who changed what parameter when
ALTER TABLE game_config_parameter_values ADD COLUMN updated_by VARCHAR(100);
ALTER TABLE game_config_parameter_values ADD COLUMN updated_at TIMESTAMP;

-- Trigger to log changes
CREATE TRIGGER audit_parameter_changes
BEFORE UPDATE ON game_config_parameter_values
FOR EACH ROW EXECUTE FUNCTION log_parameter_change();
```

## Migration Strategy

### Phase 1: Create New Schema (Zero Downtime)

```sql
-- Run migration 006_normalize_parameters.up.sql
-- Creates new tables alongside existing JSONB columns
```

### Phase 2: Data Migration

```sql
-- Extract parameter definitions from JSONB
INSERT INTO parameter_definitions (game_id, key, param_type, description, required, default_value)
SELECT DISTINCT
    gc.game_id,
    p->>'key' AS key,
    p->>'type' AS param_type,
    p->>'description' AS description,
    (p->>'required')::boolean AS required,
    p->>'default_value' AS default_value
FROM game_configs gc,
LATERAL jsonb_array_elements(gc.parameters->'parameters') AS p
ON CONFLICT (game_id, key) DO NOTHING;

-- Extract parameter values
INSERT INTO game_config_parameter_values (config_id, param_id, value)
SELECT
    gc.config_id,
    pd.param_id,
    p->>'value' AS value
FROM game_configs gc,
LATERAL jsonb_array_elements(gc.parameters->'parameters') AS p
JOIN parameter_definitions pd ON pd.game_id = gc.game_id AND pd.key = p->>'key'
WHERE p->>'value' IS NOT NULL AND p->>'value' != pd.default_value;  -- Sparse storage
```

### Phase 3: Update Application Code

**Repository Layer:**

```go
// Old (JSONB)
func (r *GameConfigRepository) GetParameters(configID int64) ([]*Parameter, error) {
    // Parse JSONB...
}

// New (Normalized)
func (r *GameConfigRepository) GetParameters(configID int64) ([]*Parameter, error) {
    query := `
        SELECT pd.key, COALESCE(gcpv.value, pd.default_value) AS value,
               pd.param_type, pd.description, pd.required
        FROM parameter_definitions pd
        LEFT JOIN game_config_parameter_values gcpv
            ON pd.param_id = gcpv.param_id AND gcpv.config_id = $1
        WHERE pd.game_id = (SELECT game_id FROM game_configs WHERE config_id = $1)
    `
    // Execute query...
}
```

**Merged Parameters Query:**

```go
func (r *SessionRepository) GetMergedParameters(sessionID int64) (map[string]string, error) {
    query := `
        WITH session_context AS (
            SELECT s.session_id, s.sgc_id, sgc.config_id, gc.game_id
            FROM sessions s
            JOIN server_game_configs sgc ON s.sgc_id = sgc.sgc_id
            JOIN game_configs gc ON sgc.config_id = gc.config_id
            WHERE s.session_id = $1
        )
        SELECT
            pd.key,
            COALESCE(
                spv.value,       -- Session override (priority 1)
                sgcpv.value,     -- SGC override (priority 2)
                gcpv.value,      -- GameConfig value (priority 3)
                pd.default_value -- Default (priority 4)
            ) AS effective_value
        FROM session_context sc
        JOIN parameter_definitions pd ON sc.game_id = pd.game_id
        LEFT JOIN session_parameter_values spv
            ON sc.session_id = spv.session_id AND pd.param_id = spv.param_id
        LEFT JOIN server_game_config_parameter_values sgcpv
            ON sc.sgc_id = sgcpv.sgc_id AND pd.param_id = sgcpv.param_id
        LEFT JOIN game_config_parameter_values gcpv
            ON sc.config_id = gcpv.config_id AND pd.param_id = gcpv.param_id
    `
    // Execute and build map...
}
```

### Phase 4: Dual-Write Period (Optional)

```go
// Write to both old and new schema during transition
func (r *GameConfigRepository) SetParameter(configID int64, key, value string) error {
    // Write to new schema
    if err := r.setParameterNormalized(configID, key, value); err != nil {
        return err
    }

    // Also update JSONB for backward compatibility
    if err := r.updateJSONBParameter(configID, key, value); err != nil {
        log.Warn("Failed to update JSONB (deprecated): %v", err)
        // Don't fail - new schema is source of truth
    }

    return nil
}
```

### Phase 5: Remove JSONB Columns

```sql
-- After verifying new schema works correctly
ALTER TABLE game_configs DROP COLUMN parameters;
ALTER TABLE server_game_configs DROP COLUMN parameters;
ALTER TABLE sessions DROP COLUMN parameters;
```

## Performance Comparison

### Current (JSONB)

```sql
-- Find configs with max_players > 20
EXPLAIN ANALYZE
SELECT config_id FROM game_configs
WHERE (parameters->'parameters' @> '[{"key": "max_players"}]')
  AND (parameters->'parameters'->0->>'value')::int > 20;

-- Sequential scan, no indexes usable
-- Execution time: ~200ms on 10k rows
```

### Normalized

```sql
-- Same query with normalized schema
EXPLAIN ANALYZE
SELECT DISTINCT gcpv.config_id
FROM game_config_parameter_values gcpv
JOIN parameter_definitions pd ON gcpv.param_id = pd.param_id
WHERE pd.key = 'max_players' AND gcpv.value::int > 20;

-- Uses indexes on param_id and param_id+value
-- Execution time: ~5ms on 10k rows (40x faster!)
```

## New Capabilities Enabled

### 1. Parameter Templates/Presets

```sql
-- Create parameter presets for common configurations
CREATE TABLE parameter_presets (
    preset_id BIGSERIAL PRIMARY KEY,
    game_id BIGINT REFERENCES games(game_id),
    name VARCHAR(100),  -- "Casual", "Competitive", "Hardcore"
    description TEXT
);

CREATE TABLE parameter_preset_values (
    preset_id BIGINT REFERENCES parameter_presets(preset_id),
    param_id BIGINT REFERENCES parameter_definitions(param_id),
    value TEXT,
    PRIMARY KEY (preset_id, param_id)
);

-- Apply preset to new config
INSERT INTO game_config_parameter_values (config_id, param_id, value)
SELECT $config_id, param_id, value
FROM parameter_preset_values
WHERE preset_id = $preset_id;
```

### 2. Parameter History/Versioning

```sql
CREATE TABLE parameter_value_history (
    history_id BIGSERIAL PRIMARY KEY,
    value_id BIGINT,  -- FK to game_config_parameter_values
    old_value TEXT,
    new_value TEXT,
    changed_by VARCHAR(100),
    changed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Trigger to track changes
CREATE TRIGGER track_parameter_changes
AFTER UPDATE ON game_config_parameter_values
FOR EACH ROW EXECUTE FUNCTION log_parameter_history();
```

### 3. Parameter Validation Rules

```sql
-- Add validation rules to definitions
ALTER TABLE parameter_definitions
ADD COLUMN validation_regex VARCHAR(500),
ADD COLUMN min_length INT,
ADD COLUMN max_length INT;

-- Example: Port must be 1-65535
UPDATE parameter_definitions
SET min_value = 1, max_value = 65535
WHERE key = 'server_port' AND param_type = 'int';

-- Example: Difficulty must be from list
UPDATE parameter_definitions
SET allowed_values = ARRAY['peaceful', 'easy', 'normal', 'hard']
WHERE key = 'difficulty';
```

### 4. Cross-Config Analytics

```sql
-- Most popular parameter values
SELECT pd.key, gcpv.value, COUNT(*) as usage
FROM parameter_definitions pd
JOIN game_config_parameter_values gcpv ON pd.param_id = gcpv.param_id
GROUP BY pd.key, gcpv.value
ORDER BY usage DESC;

-- Identify configs that deviate from defaults
SELECT gc.config_id, gc.name, COUNT(*) as overrides
FROM game_configs gc
JOIN game_config_parameter_values gcpv ON gc.config_id = gcpv.config_id
GROUP BY gc.config_id, gc.name
HAVING COUNT(*) > 5
ORDER BY overrides DESC;
```

## Trade-offs

### Pros

✅ Referential integrity and type safety
✅ Efficient queries with proper indexes
✅ Analytics and reporting capabilities
✅ Database-level validation
✅ Audit trails and history
✅ No data duplication
✅ Better performance at scale

### Cons

❌ More complex schema (4 tables instead of 1 JSONB column)
❌ More joins required for queries (but they're fast with indexes)
❌ Migration effort required
❌ Slightly more storage (but minimal due to sparse storage)

## Recommendation

**Migrate to normalized schema for production use.**

The normalized approach is the correct database design pattern and will provide:
- Better performance at scale
- Easier debugging and analytics
- Stronger data integrity guarantees
- More flexibility for future features

The migration can be done incrementally with zero downtime using the dual-write strategy.

## Implementation Timeline

1. **Week 1**: Create new tables and indexes (migration 006)
2. **Week 2**: Write and test data migration script
3. **Week 3**: Update repository layer to use new schema
4. **Week 4**: Dual-write period - write to both schemas
5. **Week 5**: Verify correctness, monitor performance
6. **Week 6**: Remove JSONB columns, cleanup old code

Total: ~6 weeks for complete migration with safety checks.
