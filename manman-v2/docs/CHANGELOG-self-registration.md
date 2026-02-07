# Self-Registration Implementation - Changelog

## Summary

Implemented self-registration for ManManV2 host managers with stable server identification across restarts.

## Key Changes

### 1. Removed Legacy SERVER_ID Mode
- Host managers now always use self-registration
- No more pre-configured server IDs
- Simplifies deployment and configuration

### 2. Stable Server Identification
- **Environment-based naming**: `hostname-environment` (e.g., `myserver-production`)
- **Configurable names**: Optional `SERVER_NAME` override
- **Reuses existing servers**: Registration checks by name and reuses existing `server_id`
- **Preserves configurations**: ServerGameConfigs survive restarts

### 3. Multiple Managers Per Host Support
- Set different `ENVIRONMENT` values (dev, prod, staging)
- Each environment gets separate server record
- Perfect for running dev and prod on same physical host

### 4. Database Schema
- Added `environment` column to `servers` table
- Migration: `009_add_server_environment.up.sql`
- Indexed for efficient filtering by environment

### 5. Configuration Options

**Required:**
- `API_ADDRESS`: Control plane API endpoint
- `REGISTRATION_KEY`: Shared secret for registration
- `RABBITMQ_URL`: Message broker connection

**Recommended:**
- `ENVIRONMENT`: Deployment environment (dev/staging/prod)

**Optional:**
- `SERVER_NAME`: Override auto-generated name
- `DOCKER_SOCKET`: Docker daemon socket path

## Benefits

✅ **Stable identity**: Same server record across restarts
✅ **Config preservation**: ServerGameConfigs and port allocations persist
✅ **Multi-tenant friendly**: Run multiple environments on one host
✅ **Simple deployment**: No manual server ID assignment
✅ **Automatic recovery**: Orphan detection handles crashes

## Migration from Old Behavior

If you had UUID-based naming (hostname-uuid):
1. Set `ENVIRONMENT` to get stable names
2. Existing servers will be preserved if you match the old name
3. Clean up orphaned server records in database

## Documentation

- `self-registration.md`: Complete feature guide
- `server-identification.md`: How re-identification works
- `testing-self-registration.md`: Testing procedures

## Files Changed

**Code:**
- `manman/protos/messages.proto`: Added `environment` field to `Server`
- `manman/protos/api.proto`: Added `environment` to `RegisterServerRequest`
- `manman/models.go`: Added `Environment` field to `Server` struct
- `manman/host/main.go`: Removed legacy mode, stable name generation
- `manman/api/handlers/registration.go`: Store environment field
- `manman/api/handlers/server.go`: Include environment in proto conversion
- `manman/host/BUILD.bazel`: Updated dependencies

**Database:**
- `migrations/009_add_server_environment.up.sql`: Add environment column
- `migrations/009_add_server_environment.down.sql`: Rollback migration

**Documentation:**
- `docs/self-registration.md`: Feature guide
- `docs/server-identification.md`: Re-identification explanation
- `docs/testing-self-registration.md`: Testing guide

**Configuration:**
- `.env.example`: Updated with new options, removed SERVER_ID
