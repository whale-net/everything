# Migration History Tracking

## Overview

The migration library now tracks all migration attempts in a `migration_history` table, providing:

- **Complete audit trail** of all migration attempts (successes, failures, interruptions)
- **Safe recovery** from failed migrations with validation
- **Protection** against false recovery scenarios
- **Performance metrics** for each migration
- **Simple repository interface** for easy access to migration state

## Problem Solved

**Before**: When a migration failed (dirty=true) and users manually set dirty=false, the system incorrectly assumed the migration succeeded and tried to run the next one, leaving the database in an inconsistent state.

**After**: The system tracks all migration attempts and validates recovery decisions, preventing unsafe operations.

## Implementation

### Core Components

1. **`history.go`**: Core history tracker with low-level database operations
   - `HistoryTracker` struct
   - Methods: `RecordStart()`, `RecordSuccess()`, `RecordFailure()`, `GetLastAttempt()`, etc.

2. **`history_repo.go`**: Simplified repository interface for easy access
   - `HistoryRepo` struct
   - Simple methods: `IsVersionSafe()`, `GetStatus()`, `GetSuccessfulVersions()`
   - Clean, testable API

3. **`migrate.go`** (enhanced):
   - Added `tracker` field to `Runner`
   - New `UpWithTracking()` method runs migrations one-at-a-time with tracking
   - New `ForceWithValidation()` validates before forcing versions
   - New `History()` method exposes repository interface

4. **`cli.go`** (enhanced):
   - New flags: `--history`, `--history-limit`, `--force-dangerous`, `--tracked`
   - History display with formatted table output
   - Validated force operations by default

### Database Schema

The `migration_history` table is **automatically created** by the library (no app-specific migration needed):

```sql
CREATE TABLE migration_history (
    history_id BIGSERIAL PRIMARY KEY,
    version BIGINT NOT NULL,
    direction VARCHAR(4) NOT NULL,  -- 'up' or 'down'
    status VARCHAR(20) NOT NULL,    -- 'started', 'success', or 'failed'
    started_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP,
    duration_ms INTEGER,
    error_message TEXT,
    applied_by VARCHAR(255),
    created_at TIMESTAMP NOT NULL
);
```

### Key Features

#### 1. Automatic History Tracking

```go
runner := migrate.NewRunner(db, migrations, "migrations")

// Runs migrations with automatic history tracking
err := runner.UpWithTracking()
```

Each migration attempt is logged with:
- Start time
- End time
- Duration
- Success/failure status
- Error message (if failed)

#### 2. Simple Repository Access

```go
repo := runner.History()

// Check if version is safe to force to
safe, reason, err := repo.IsVersionSafe(5)

// Get detailed status
status, err := repo.GetStatus(5)

// Get recent history
entries, err := repo.GetRecentHistory(10)
```

#### 3. Validated Recovery

```go
// Safe force - validates against history
err := runner.ForceWithValidation(version, false)

// Dangerous force - skips validation (requires explicit flag)
err := runner.ForceWithValidation(version, true)
```

#### 4. CLI History Display

```bash
$ ./migrate --history

Migration History:
─────────────────────────────────────────────────────────────────────────────
ID         Version  Direction  Status     Duration     Started    Error
─────────────────────────────────────────────────────────────────────────────
125        5        up         success    145ms        10:45:23
124        4        up         success    89ms         10:45:22
123        3        up         failed     12ms         10:45:20   syntax error at line 42
122        3        up         started    -            10:44:15
─────────────────────────────────────────────────────────────────────────────
```

## Usage

### Running Migrations

```bash
# Default: run migrations with tracking
./migrate

# Legacy mode (no tracking)
./migrate --tracked=false

# View current version
./migrate --version
```

### Viewing History

```bash
# Show last 20 entries (default)
./migrate --history

# Show last 50 entries
./migrate --history --history-limit 50
```

### Recovery Operations

```bash
# Safe force (validates against history)
./migrate --force 3

# Dangerous force (skips validation)
./migrate --force 3 --force-dangerous
```

## Recovery Scenarios

### Scenario 1: Failed Migration

```bash
# Migration 5 fails
$ ./migrate
✓ Migration 1 completed (45ms)
✓ Migration 2 completed (89ms)
✓ Migration 3 completed (123ms)
✓ Migration 4 completed (67ms)
✗ Migration 5 failed: syntax error at line 42

# Check history
$ ./migrate --history
# Shows migration 5 status='failed'

# Fix migration file, then force back and retry
$ ./migrate --force 4  # Safe - version 4 was successful
$ ./migrate            # Re-run migration 5 with fix
✓ Migration 5 completed (92ms)
```

### Scenario 2: Interrupted Migration

```bash
# Migration gets killed
$ ./migrate
✓ Migration 1 completed (45ms)
^C

# Check history
$ ./migrate --history
# ID: 123, Version: 2, Status: started (never completed)

# Force back (requires --force-dangerous for interrupted migrations)
$ ./migrate --force 1 --force-dangerous
$ ./migrate  # Re-run
```

### Scenario 3: Protection from False Recovery

```bash
# Migration 5 fails
$ ./migrate
✗ Migration 5 failed: syntax error

# User tries to force without fixing
$ ./migrate --force 4
ERROR: cannot force to version 4: Last attempt of version 5 failed: syntax error
Use --force-dangerous to override (not recommended)

# System blocks unsafe operation!
```

## Benefits

1. **Complete Audit Trail**: Every migration attempt is logged
2. **Safe Recovery**: Validation prevents unsafe force operations
3. **Better Debugging**: See exactly what failed and when
4. **Performance Tracking**: Duration metrics for each migration
5. **Simple API**: Clean repository interface for programmatic access
6. **Backward Compatible**: Doesn't break existing functionality
7. **Automatic**: No manual setup required - history table created automatically
8. **Reusable**: Library-level implementation works across all apps

## Architecture Decisions

### Why Library-Level History Table?

The `migration_history` table is created **automatically by the library** rather than requiring app-specific migrations because:

1. **Reusability**: Every app using the library needs this table
2. **Consistency**: Ensures all apps have the same history schema
3. **Simplicity**: Apps don't need to add migration files for library infrastructure
4. **Automatic**: `EnsureHistoryTable()` creates it when first needed

### Why Repository Pattern?

The `HistoryRepo` interface provides:

1. **Simplicity**: Clean, easy-to-understand methods
2. **Testability**: Easy to mock for testing
3. **Type Safety**: Returns structured types instead of raw SQL rows
4. **Maintainability**: Changes to underlying implementation don't affect API

## Testing

```bash
# Build library
cd libs/go/migrate
go build

# Build migration binary
cd manman/migrate
go build

# Run migrations
./migrate

# View history
./migrate --history

# Test failure scenario (create intentional error)
echo "CREATE TABL broken (id INT);" > migrations/999_test.up.sql
./migrate  # Should fail and record in history
./migrate --history  # Should show failed attempt

# Cleanup
rm migrations/999_test.*
```

## See Also

- `example_usage.md` - Detailed usage examples
- `history.go` - Core history tracking implementation
- `history_repo.go` - Simplified repository interface
