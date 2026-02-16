# Self-Registration Implementation - Final Summary

## Completed Implementation

### Core Features

1. **Self-Registration Flow**
   - Host managers automatically register with control plane on startup
   - No manual server ID assignment required
   - Stable server identification across restarts

2. **Environment-Based Organization**
   - Optional `ENVIRONMENT` field (dev, staging, prod, etc.)
   - Enables multiple managers on same physical host
   - Servers grouped by environment for easy management

3. **Duplicate Prevention**
   - Registration fails if server with same name is already online
   - Prevents accidental duplicate managers
   - Error: `AlreadyExists` with clear message
   - Stale/crashed servers handled by status processor

4. **Configuration Preservation**
   - Same server name → same server_id → configs preserved
   - ServerGameConfigs survive restarts
   - Port allocations maintained
   - Session history intact

## Configuration

### Required Environment Variables

```bash
API_ADDRESS=localhost:50051          # Control plane API endpoint
RABBITMQ_URL=amqp://...             # Message broker connection
DOCKER_SOCKET=/var/run/docker.sock  # Docker daemon socket
```

### Optional Environment Variables

```bash
ENVIRONMENT=production               # Deployment environment tag
SERVER_NAME=gameserver-01           # Override auto-generated name
```

### Removed Variables

- ~~`SERVER_ID`~~ - Legacy mode removed
- ~~`REGISTRATION_KEY`~~ - Security placeholder removed (proper auth coming later)

## Server Naming Logic

```
IF SERVER_NAME is set:
    Use SERVER_NAME as-is
ELSE IF ENVIRONMENT is set:
    Use "hostname-environment" (stable across restarts)
ELSE:
    Use "hostname-{random-uuid}" (⚠️ creates new server each restart)
```

**Recommendation**: Always set `ENVIRONMENT` for stable naming.

## Registration Flow

```
1. Host starts → Generate/use server name
2. Call RegisterServer(name, capabilities, environment)
3. API checks database for existing server with this name
4. IF found AND status="online":
     → Return error "already online"
5. IF found AND status="offline":
     → Update to "online", return existing server_id (configs preserved!)
6. IF not found:
     → Create new server record, return new server_id
7. Host uses server_id for all operations
8. Send heartbeat every 5 seconds
```

## Multiple Managers on Same Host

**Example**: Run dev and prod on one physical server

```bash
# /etc/systemd/system/manmanv2-dev.service
Environment="ENVIRONMENT=dev"
Environment="API_ADDRESS=localhost:50051"
Environment="RABBITMQ_URL=amqp://user:pass@localhost:5672/manmanv2-dev"

# /etc/systemd/system/manmanv2-prod.service
Environment="ENVIRONMENT=production"
Environment="API_ADDRESS=localhost:50051"
Environment="RABBITMQ_URL=amqp://user:pass@localhost:5672/manmanv2-prod"
```

**Result:**
- Server 1: `myserver-dev` (server_id=1)
- Server 2: `myserver-production` (server_id=2)
- Each has separate configs, port ranges, sessions

## Duplicate Prevention

**Scenario**: Accidentally start same manager twice

```bash
# Terminal 1
ENVIRONMENT=prod ./host
# → Registers successfully (server_id=1)

# Terminal 2 (same host, same environment)
ENVIRONMENT=prod ./host
# → Error: "server 'myserver-prod' is already online (server_id=1)"
```

**Handling**:
- Manager exits with error
- Must stop first instance before starting new one
- Stale servers (crashed, no heartbeat) automatically marked offline by processor

## Error Handling

### Registration Errors

| Error | Description | Solution |
|-------|-------------|----------|
| `INVALID_ARGUMENT` | Name is empty | Set SERVER_NAME or ENVIRONMENT |
| `ALREADY_EXISTS` | Server already online | Stop existing manager first |
| `INTERNAL` | Database error | Check API logs |

### Recovery from Orphaned Server

If server is marked "online" but actually crashed:

1. Status processor detects stale server (no heartbeat)
2. Marks as "offline" after threshold (default: 10 seconds)
3. New registration attempt succeeds
4. Same server_id reused, configs preserved

## Database Schema Changes

### New Column

```sql
ALTER TABLE servers
ADD COLUMN environment VARCHAR(100);

CREATE INDEX idx_servers_environment ON servers(environment) WHERE environment IS NOT NULL;
```

### Migration

- Up: `migrations/009_add_server_environment.up.sql`
- Down: `migrations/009_add_server_environment.down.sql`

## API Changes

### RegisterServer RPC

**Request:**
```protobuf
message RegisterServerRequest {
  string name = 1;
  ServerCapabilities capabilities = 2;
  string environment = 3;  // Optional
}
```

**Response:**
```protobuf
message RegisterServerResponse {
  int64 server_id = 1;
  Server server = 2;
}
```

**Errors:**
- `INVALID_ARGUMENT`: Missing required fields
- `ALREADY_EXISTS`: Server already online
- `INTERNAL`: Database errors

### Server Message

```protobuf
message Server {
  int64 server_id = 1;
  string name = 2;
  string status = 3;
  int64 last_seen = 4;
  string environment = 5;  // New field
}
```

## Files Modified

### Protocol Buffers
- `manman/protos/api.proto` - Removed registration_key, kept environment
- `manman/protos/messages.proto` - Added environment to Server

### Go Code
- `manman/models.go` - Added Environment field
- `manman/api/handlers/registration.go` - Duplicate check, removed auth
- `manman/api/handlers/server.go` - Include environment in proto
- `manman/host/main.go` - Stable naming, removed registration_key

### Database
- `migrations/009_add_server_environment.up.sql` - Add column
- `migrations/009_add_server_environment.down.sql` - Rollback

### Configuration
- `.env.example` - Updated with new variables

### Documentation
- `docs/self-registration.md` - Feature guide
- `docs/server-identification.md` - Re-identification explained
- `docs/testing-self-registration.md` - Testing procedures
- `docs/CHANGELOG-self-registration.md` - Change log
- `docs/SUMMARY-self-registration.md` - This file

## Testing

### Basic Test

```bash
# Start manager
ENVIRONMENT=dev \
API_ADDRESS=localhost:50051 \
RABBITMQ_URL=amqp://rabbit:password@localhost:5672/manmanv2-dev \
./host

# Expected output:
# Starting ManManV2 Host Manager (self-registration mode)
# Registered as server 'myserver-dev'
# Successfully registered with control plane (server_id=1)
# ...
# ManManV2 Host Manager is running.
```

### Duplicate Test

```bash
# Start second instance (same environment)
ENVIRONMENT=dev \
API_ADDRESS=localhost:50051 \
RABBITMQ_URL=amqp://rabbit:password@localhost:5672/manmanv2-dev \
./host

# Expected error:
# Fatal error: failed to self-register: registration failed:
# rpc error: code = AlreadyExists desc = server 'myserver-dev'
# is already online (server_id=1). Cannot register duplicate instance.
```

### Restart Test

```bash
# Stop first instance (Ctrl+C)
# Start again with same environment
ENVIRONMENT=dev ./host

# Expected:
# Successfully registered with control plane (server_id=1)
# (Same server_id! Configs preserved!)
```

### Query Database

```sql
SELECT server_id, name, environment, status, last_seen
FROM servers
ORDER BY server_id;
```

## Future Enhancements

Intentionally deferred for later:

1. **Authentication/Authorization**
   - mTLS certificate-based auth
   - Token-based authentication
   - Integration with identity providers

2. **Advanced Features**
   - Server metadata/labels
   - Graceful deregistration API
   - Server groups/clusters
   - Geographic region tracking

## Deployment Checklist

- [ ] Run database migration `009_add_server_environment.up.sql`
- [ ] Update API server (no env vars needed)
- [ ] Update host manager configuration:
  - [ ] Remove `SERVER_ID` and `REGISTRATION_KEY`
  - [ ] Add `ENVIRONMENT` (recommended)
  - [ ] Keep `API_ADDRESS`, `RABBITMQ_URL`, `DOCKER_SOCKET`
- [ ] Test registration and duplicate prevention
- [ ] Monitor status processor for orphan cleanup
- [ ] Document your server naming scheme

## Known Limitations

1. **No authentication**: Anyone with network access can register servers
   - Mitigation: Use network policies/firewalls
   - Future: Add proper auth/authz

2. **Split-brain possible**: If status processor is down and server crashes
   - Server stays "online" in DB
   - New instance can't register
   - Mitigation: Manual DB update or wait for processor recovery

3. **Name conflicts**: Different physical hosts with same hostname+environment
   - Solution: Set explicit `SERVER_NAME`

## Support

For issues or questions:
- Check `docs/server-identification.md` for naming issues
- Check `docs/testing-self-registration.md` for test procedures
- Review API logs for registration errors
- Check status processor for orphan cleanup logs
