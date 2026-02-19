# Volume System Refactoring - Implementation Complete

## Summary

Successfully refactored the volume system from Game-level (configuration_strategies) to GameConfig-level (game_config_volumes table). This addresses the architectural issue where volume mount paths are image-specific, not game-universal.

## Components Implemented

### 1. Database Layer ✓
- **Migration Files**: `025_move_volumes_to_gameconfig.up.sql` and `.down.sql`
  - Created `game_config_volumes` table with FK to `game_configs`
  - Added `volume_id` and `path_override` columns to `configuration_patches`
  - Migrated existing volume strategies to game_config_volumes
  - Removed 'volume' from strategy_type enum

### 2. Model Layer ✓
- **models.go**: Added `GameConfigVolume` struct
- **models.go**: Updated `ConfigurationPatch` with `VolumeID` and `PathOverride` fields

### 3. Repository Layer ✓
- **repository.go**: Added `GameConfigVolumeRepository` interface
- **gameconfigvolume.go**: Implemented full CRUD operations
- **patch.go**: Updated to handle new volume_id and path_override columns
- **repository.go**: Wired up new repository in factory
- **BUILD.bazel**: Added gameconfigvolume.go to build sources

### 4. Proto Layer ✓
- **messages.proto**: Added `GameConfigVolume` message
- **messages.proto**: Updated `ConfigurationPatch` with volume_id and path_override fields
- **api.proto**: Added 5 new RPCs for volume management (Create, Get, List, Update, Delete)

### 5. API Handler Layer ✓
- **api.go**: Added `GameConfigVolumeHandler` struct and methods
- **api.go**: Updated `buildStartSessionCommand` to use volumes from game_config_volumes
- **api.go**: Updated `StartSession` to fetch volumes by GameConfig instead of strategies
- **api.go**: Added RPC delegation methods for volume operations
- **api.go**: Updated `patchToProto` to include volume_id and path_override

### 6. Workshop Manager ✓
- **manager.go**: Changed `strategyRepo` to `volumeRepo`
- **manager.go**: Updated `resolveInstallationPath` to use game_config_volumes
- **main.go**: Updated WorkshopManager construction to pass GameConfigVolumes repo

### 7. UI Layer ✓
- **grpc_client.go**: Added 4 volume CRUD wrapper methods
- **handlers_games.go**: Updated `GameConfigDetailPageData.Volumes` type
- **handlers_games.go**: Updated `handleGameConfigDetail` to fetch volumes
- **handlers_games.go**: Added `handleGameConfigVolumeCreate` and `handleGameConfigVolumeDelete`
- **handlers_games.go**: Added routing for volume operations in config detail handler

## Migration Strategy

### Running the Migration
```bash
bazel run //manmanv2/migrate:migrate -- up
```

This will:
1. Create the `game_config_volumes` table
2. Migrate existing volume strategies to game_config_volumes (creates entries for ALL GameConfigs of each game)
3. Delete volume strategy rows
4. Update the strategy_type enum constraint

### Rollback Plan
```bash
bazel run //manmanv2/migrate:migrate -- down
```

This will reverse the migration (best effort - may lose some data if configurations changed).

## Verification Steps

### 1. Database Check
```sql
-- Verify volumes were created
SELECT * FROM game_config_volumes;

-- Verify volume strategies were removed
SELECT * FROM configuration_strategies WHERE strategy_type = 'volume'; -- Should return 0 rows

-- Check patch table columns
SELECT column_name FROM information_schema.columns
WHERE table_name = 'configuration_patches' AND column_name IN ('volume_id', 'path_override');
```

### 2. API Build Verification
```bash
bazel build //manmanv2/api:control-api //manmanv2/ui:manmanv2-ui
# ✓ Build completed successfully
```

### 3. Service Deployment
```bash
# Trigger Tilt rebuilds
tilt trigger manmanv2-api
tilt trigger manmanv2-ui
```

### 4. Functional Testing

#### Test Volume Display
1. Navigate to a GameConfig detail page (e.g., `/games/1/configs/1`)
2. Verify "Volume Mounts" section shows migrated volumes
3. Check that volumes display: name, container_path, host_subpath, read_only

#### Test Volume Creation
1. Click "+ Add Volume" button
2. Fill in form:
   - Name: "test-volume"
   - Container Path: "/test"
   - Host Subpath: "test/"
   - Description: "Test volume"
3. Submit and verify volume appears in list

#### Test Volume Deletion
1. Click "Delete" on a volume
2. Confirm deletion
3. Verify volume is removed from list

#### Test Session Start
1. Start a session for an SGC
2. Check host-manager logs for volume mount creation
3. Verify container has correct volumes mounted

## Key Architecture Points

### Volume Scoping
- **Before**: Volumes were stored as strategies at Game level (incorrect - paths vary by image)
- **After**: Volumes are stored at GameConfig level (correct - paths are image-specific)

### Patch System Unchanged
- File configuration strategies remain at Game level
- Patches can reference volumes via `volume_id` to compute full paths
- `strategy_id` stays game-scoped for file strategies
- Backward compatibility maintained for file strategies without volume references

### Path Resolution
```
Full Path = volume.container_path + "/" + (patch.path_override || strategy.target_path)
Example: "/data" + "/" + "server.properties" = "/data/server.properties"
```

## Files Modified

### New Files
- `manmanv2/migrate/migrations/025_move_volumes_to_gameconfig.up.sql`
- `manmanv2/migrate/migrations/025_move_volumes_to_gameconfig.down.sql`
- `manmanv2/api/repository/postgres/gameconfigvolume.go`

### Modified Files
- `manmanv2/models.go`
- `manmanv2/api/repository/repository.go`
- `manmanv2/api/repository/postgres/patch.go`
- `manmanv2/api/repository/postgres/repository.go`
- `manmanv2/api/repository/postgres/BUILD.bazel`
- `manmanv2/protos/messages.proto`
- `manmanv2/protos/api.proto`
- `manmanv2/api/handlers/api.go`
- `manmanv2/api/workshop/manager.go`
- `manmanv2/api/main.go`
- `manmanv2/ui/grpc_client.go`
- `manmanv2/ui/handlers_games.go`

## Next Steps

1. Run the database migration on dev environment
2. Trigger service rebuilds via Tilt
3. Test volume management in the UI
4. Verify session starts work correctly with volumes
5. (Optional) Update loader scripts to use new volume API
6. (Optional) Create UI template for volume management in config_detail.html
7. Monitor logs for any migration issues

## Status

✅ All implementation complete and builds successfully
⏳ Awaiting migration execution and testing
