# Migration History Tracking - Usage Examples

## Simple Repository Access

The `HistoryRepo` provides a clean, simple interface for accessing migration state:

### Check if a version is safe to force to

```go
runner := migrate.NewRunner(db, migrations, "migrations")
repo := runner.History()

// Check if version 5 is safe
safe, reason, err := repo.IsVersionSafe(5)
if err != nil {
    log.Fatal(err)
}

if !safe {
    fmt.Printf("Not safe: %s\n", reason)
    // Output: "Not safe: Last attempt failed: syntax error at line 42"
}
```

### Get detailed status of a migration

```go
status, err := repo.GetStatus(5)
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Version: %d\n", status.Version)
fmt.Printf("Has been attempted: %v\n", status.HasBeenAttempted)
fmt.Printf("Last status: %s\n", status.LastStatus)
fmt.Printf("Is safe: %v\n", status.IsSafe)
if status.LastError != "" {
    fmt.Printf("Last error: %s\n", status.LastError)
}
```

### View recent migration history

```go
// Get last 10 migration attempts
entries, err := repo.GetRecentHistory(10)
if err != nil {
    log.Fatal(err)
}

for _, entry := range entries {
    fmt.Printf("v%d: %s (%s)\n", entry.Version, entry.Status, entry.Direction)
}
```

### Get all successful migrations

```go
versions, err := repo.GetSuccessfulVersions()
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Successfully completed versions: %v\n", versions)
// Output: Successfully completed versions: [1 2 3 4]
```

### Check if any history exists

```go
hasHistory, err := repo.HasAnyHistory()
if err != nil {
    log.Fatal(err)
}

if !hasHistory {
    fmt.Println("No migration history recorded yet")
}
```

## CLI Usage

### Run migrations with tracking (default)

```bash
./migrate
```

### View migration history

```bash
./migrate --history

# Limit number of entries
./migrate --history --history-limit 50
```

### Force version with validation

```bash
# Safe force (validates against history)
./migrate --force 3

# Dangerous force (skips validation)
./migrate --force 3 --force-dangerous
```

### Run without tracking (legacy mode)

```bash
./migrate --tracked=false
```

## Recovery Workflows

### Scenario 1: Migration fails with error

```bash
# Migration 5 fails
./migrate
# Error: migration 5 failed: syntax error

# Check what happened
./migrate --history
# ID    Version  Status   Error
# 123   5        failed   syntax error at line 42

# Fix the migration file, then force back
./migrate --force 4        # Safe - version 4 was successful
./migrate                  # Re-run migration 5 with fix
```

### Scenario 2: Interrupted migration

```bash
# Migration gets killed mid-execution
./migrate
^C

# Check status
./migrate --history
# ID    Version  Status    Started
# 124   6        started   10:30:15

# Version 6 never completed - force back
./migrate --force 5 --force-dangerous  # Required for interrupted migrations
./migrate                              # Re-run migration 6
```

### Scenario 3: False recovery attempt blocked

```bash
# Migration 5 fails
./migrate
# Error: migration 5 failed

# User tries to force without fixing
./migrate --force 4
# Error: cannot force to version 4: Last attempt of version 5 failed: syntax error
# Use --force-dangerous to override (not recommended)

# System prevents accidental data loss!
```

## Benefits

1. **Simple API**: `IsVersionSafe()`, `GetStatus()`, `GetRecentHistory()`
2. **Type-safe**: Returns structs with clear fields
3. **Error handling**: All methods return errors
4. **Readable**: Method names clearly express intent
5. **Testable**: Repository pattern makes testing easy

## Migration Status

A migration can be in one of three states:

- `success`: Migration completed successfully ✓
- `failed`: Migration failed with an error ✗
- `started`: Migration began but never finished (interrupted) ⚠️
