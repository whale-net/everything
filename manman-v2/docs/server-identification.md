# Server Identification and Re-Registration

## Overview

ManManV2 host managers self-register with the control plane on startup. To preserve server configurations and associations across restarts, the system uses **stable server names** for re-identification.

## How Server Re-Identification Works

### Registration Flow

1. **Host startup**: Manager starts and needs a `server_id`
2. **Name generation**: Creates or uses configured server name
3. **Registration call**: Sends `RegisterServer` RPC with name
4. **Database lookup**: API checks if server with this name already exists
5. **Reuse or create**:
   - **If found**: Returns existing `server_id`, updates status to "online"
   - **If not found**: Creates new server record, returns new `server_id`
6. **Normal operation**: Manager uses assigned `server_id` for all operations

### Key Principle

**Same name = Same server = Same configurations preserved**

The server name is the primary key for re-identification. As long as the host manager uses the same name across restarts, it will reconnect to the same server record in the database.

## Server Naming Strategy

### Option 1: Explicit SERVER_NAME (Recommended for Production)

Set `SERVER_NAME` explicitly to ensure consistent identity:

```bash
SERVER_NAME=gameserver-01
ENVIRONMENT=production
```

**Result**: Always uses `gameserver-01`, same server record every restart.

### Option 2: Environment-Based Naming (Recommended for Multiple Managers)

Use `ENVIRONMENT` to distinguish multiple managers on the same host:

```bash
# Dev manager
ENVIRONMENT=dev
# Generates: hostname-dev (e.g., myserver-dev)

# Prod manager (same host)
ENVIRONMENT=production
# Generates: hostname-production (e.g., myserver-production)
```

**Result**: Each environment gets its own stable server record.

### Option 3: Auto-Generated Name (Not Recommended)

If neither `SERVER_NAME` nor `ENVIRONMENT` is set:

```bash
# No SERVER_NAME, no ENVIRONMENT
```

**Result**: Generates `hostname-{random-uuid}` - **creates new server every restart!** ⚠️

This breaks configuration associations and should be avoided.

## Multiple Managers on Same Host

Perfect for your dev/prod on same host scenario:

```bash
# /etc/systemd/system/manmanv2-dev.service
[Service]
Environment="ENVIRONMENT=dev"
Environment="REGISTRATION_KEY=secret"
Environment="RABBITMQ_URL=amqp://user:pass@localhost:5672/manmanv2"
# Uses port range 27000-27999 for dev

# /etc/systemd/system/manmanv2-prod.service
[Service]
Environment="ENVIRONMENT=production"
Environment="REGISTRATION_KEY=secret"
Environment="RABBITMQ_URL=amqp://user:pass@localhost:5672/manmanv2"
# Uses port range 28000-28999 for prod
```

Results in two server records:
- `myserver-dev` (server_id=1) - all dev game configs associated here
- `myserver-production` (server_id=2) - all prod game configs associated here

## Configuration Associations

Server configurations (ServerGameConfig) are linked by `server_id`:

```sql
-- ServerGameConfig table
CREATE TABLE server_game_configs (
    sgc_id BIGSERIAL PRIMARY KEY,
    server_id BIGINT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
    game_config_id BIGINT NOT NULL,
    port_bindings JSONB,
    parameters JSONB,
    ...
);
```

**When server is preserved across restarts:**
✅ All ServerGameConfigs remain associated
✅ Port allocations preserved
✅ Parameter overrides maintained
✅ Session history intact

**When new server is created (wrong name):**
❌ Configurations orphaned (still reference old server_id)
❌ Port conflicts possible
❌ Must reconfigure everything

## Deployment Examples

### Single Manager Per Host

```bash
# Simple stable naming
ENVIRONMENT=production
API_ADDRESS=api.example.com:50051
REGISTRATION_KEY=secret
```

Generates: `hostname-production` (stable across restarts)

### Dev and Prod on Same Host

```bash
# Dev instance (.env.dev)
ENVIRONMENT=dev
API_ADDRESS=localhost:50051
REGISTRATION_KEY=secret
RABBITMQ_URL=amqp://user:pass@localhost:5672/manmanv2-dev

# Prod instance (.env.prod)
ENVIRONMENT=production
API_ADDRESS=localhost:50051
REGISTRATION_KEY=secret
RABBITMQ_URL=amqp://user:pass@localhost:5672/manmanv2-prod
```

Both can run simultaneously, each with separate server record.

### Kubernetes DaemonSet with Node Names

```yaml
env:
- name: SERVER_NAME
  valueFrom:
    fieldRef:
      fieldPath: spec.nodeName  # Uses k8s node name
- name: ENVIRONMENT
  value: "production"
```

Each node gets stable name like `node-01-production`.

### Explicit Naming for Control

```bash
# Maximum control - specify exact name
SERVER_NAME=gameserver-nyc-01
ENVIRONMENT=production
```

## Troubleshooting

### Issue: "Server configurations missing after restart"

**Cause**: Manager registered with different name (new server_id)

**Solution**:
1. Check server list: `SELECT server_id, name, environment FROM servers;`
2. Find old server record with your configs
3. Note the name it used
4. Set `SERVER_NAME` to match that name
5. Restart manager

**Migration**:
```sql
-- Check for orphaned configs
SELECT sgc_id, server_id, game_config_id
FROM server_game_configs
WHERE server_id = 1;  -- Old server

-- If needed, reassociate to new server
UPDATE server_game_configs
SET server_id = 2  -- New server
WHERE server_id = 1;

-- Then delete old server
DELETE FROM servers WHERE server_id = 1;
```

### Issue: "Two managers conflict on same host"

**Cause**: Both using same name (e.g., just hostname)

**Solution**: Set different `ENVIRONMENT` values:
```bash
# Manager 1
ENVIRONMENT=dev

# Manager 2
ENVIRONMENT=prod
```

Also ensure different port ranges in ServerGameConfig configurations.

### Issue: "Name keeps changing"

**Cause**: Not setting `SERVER_NAME` or `ENVIRONMENT`

**Solution**: Set at least `ENVIRONMENT`:
```bash
ENVIRONMENT=production
```

## Database Queries

### List all servers with their configs:
```sql
SELECT
    s.server_id,
    s.name,
    s.environment,
    s.status,
    COUNT(sgc.sgc_id) as config_count
FROM servers s
LEFT JOIN server_game_configs sgc ON s.server_id = sgc.server_id
GROUP BY s.server_id
ORDER BY s.server_id;
```

### Find servers that haven't checked in:
```sql
SELECT server_id, name, environment, last_seen
FROM servers
WHERE status = 'online'
  AND last_seen < NOW() - INTERVAL '5 minutes'
ORDER BY last_seen;
```

### Check if a name is already taken:
```sql
SELECT server_id, name, environment, status
FROM servers
WHERE name = 'myserver-dev';
```

## Best Practices

1. **Always set ENVIRONMENT**: Especially if running multiple managers
2. **Document your naming scheme**: Keep track of which servers have which names
3. **Use DNS-friendly names**: lowercase, hyphens, no special characters
4. **Avoid changing names**: Once set, keep it consistent
5. **Monitor stale servers**: Clean up old offline servers periodically

## Security Note

Server names are not secrets. They're used for organizational purposes only. The `REGISTRATION_KEY` provides the actual security barrier for registration.
