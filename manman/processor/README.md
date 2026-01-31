# ManManV2 Event Processor Service

The ManManV2 Event Processor Service (`manmanv2-processor`) is a RabbitMQ consumer that processes events from host managers and updates the database to maintain data consistency.

## Architecture

**Pattern:** Single-queue consumer with wildcard routing key bindings
**Queue:** `processor-events` (durable, exclusive to processor)
**Internal Exchange:** `manman` (consumes from)
**External Exchange:** `external` (publishes to)

### Message Flow

```
┌─────────────────┐
│  manman-host    │──► Publishes to "manman" exchange
└─────────────────┘     • status.host.online
                        • status.session.started
                        • health.heartbeat

┌─────────────────┐
│ manman-processor│──► Consumes from "manman" exchange
└─────────────────┘    Publishes to "external" exchange
                        • manman.host.online
                        • manman.session.started
                        • manman.host.stale

┌─────────────────┐
│   slackbot      │──► Consumes from "external" exchange
└─────────────────┘     Subscribes: "manman.#"
```

### Routing Keys Consumed

- `status.host.#` - Host online/offline events
- `status.session.#` - Session state transitions
- `health.#` - Host health heartbeats (every 30s)

### External Events Published

- `manman.host.online` - Host came online
- `manman.host.offline` - Host went offline
- `manman.host.stale` - Host detected as stale (no heartbeat)
- `manman.session.running` - Session started
- `manman.session.stopped` - Session stopped gracefully
- `manman.session.crashed` - Session crashed

## Configuration

Environment variables:

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `RABBITMQ_URL` | - | Yes | RabbitMQ connection URL (includes vhost) |
| `DB_HOST` | `localhost` | No | PostgreSQL host |
| `DB_PORT` | `5432` | No | PostgreSQL port |
| `DB_USER` | `postgres` | No | PostgreSQL user |
| `DB_PASSWORD` | - | Yes | PostgreSQL password |
| `DB_NAME` | `manman` | No | PostgreSQL database name |
| `DB_SSL_MODE` | `disable` | No | PostgreSQL SSL mode |
| `QUEUE_NAME` | `processor-events` | No | RabbitMQ queue name |
| `LOG_LEVEL` | `info` | No | Log level (debug, info, warn, error) |
| `HEALTH_CHECK_PORT` | `8080` | No | HTTP health check server port |
| `STALE_HOST_THRESHOLD_SECONDS` | `10` | No | Seconds before marking host as stale |
| `EXTERNAL_EXCHANGE` | `external` | No | External exchange name |

## Components

### Handlers

- **HostStatusHandler** (`handlers/host_status.go`) - Processes host status updates
- **SessionStatusHandler** (`handlers/session_status.go`) - Processes session state transitions with validation
- **HealthHandler** (`handlers/health.go`) - Processes heartbeats and detects stale hosts

### Consumer

- **ProcessorConsumer** (`consumer/consumer.go`) - RabbitMQ consumer wrapper with routing

### Repository Extensions

New methods added to support event processing:

**ServerRepository:**
- `UpdateStatusAndLastSeen(serverID, status, lastSeen)` - Atomic status + timestamp update
- `UpdateLastSeen(serverID, lastSeen)` - Heartbeat timestamp update
- `ListStaleServers(thresholdSeconds)` - Find hosts missing heartbeats
- `MarkServersOffline(serverIDs)` - Batch mark hosts as offline

**SessionRepository:**
- `UpdateStatus(sessionID, status)` - Simple status update
- `UpdateSessionStart(sessionID, startedAt)` - Mark session as running
- `UpdateSessionEnd(sessionID, status, endedAt, exitCode)` - Mark session as stopped/crashed

## Features

### Session State Machine Validation

The processor enforces valid state transitions to prevent data corruption:

```
pending → starting → running → stopping → stopped
   ↓          ↓         ↓          ↓
crashed   crashed   crashed   crashed
```

Invalid transitions are rejected with permanent error (no retry).

### Stale Host Detection

Background task runs every 60 seconds to detect hosts that haven't sent heartbeats:
- Queries servers where `last_seen < NOW() - threshold`
- Marks them as offline
- Publishes `manman.host.stale` events to external exchange

Default threshold: **10 seconds** (configurable)

### Error Handling

**Permanent Errors (NACK without requeue):**
- Malformed JSON messages
- Entity not found (server/session)
- Invalid state transitions

**Transient Errors (NACK with requeue):**
- Database connection failures
- Query timeouts
- External publish failures (doesn't fail internal processing)

### Health Checks

- `GET /healthz` - Liveness probe (returns 200 if process running)
- `GET /readyz` - Readiness probe (returns 200 if DB connected)

### Graceful Shutdown

1. Receive SIGTERM/SIGINT
2. Cancel context (stops consumer)
3. Wait for in-flight messages (30s timeout)
4. Close connections
5. Exit

## Building

```bash
# Build binary
bazel build //manman/processor:manmanv2-processor

# Build container image
bazel build //manman/processor:manmanv2-processor_image

# Run locally (requires RabbitMQ and PostgreSQL)
bazel run //manman/processor:manmanv2-processor
```

## Deployment

The processor is included in the ManMan Helm chart as a worker deployment:

```yaml
replicas: 1  # Singleton service
app_type: worker
```

Kubernetes manifests are auto-generated by the release system.

## Logging

Structured JSON logging with fields:
- `timestamp` - ISO8601 timestamp
- `level` - Log level (debug, info, warn, error)
- `routing_key` - Message routing key
- `server_id` / `session_id` - Entity IDs
- `error` - Error message (if applicable)

Example:
```json
{
  "timestamp": "2025-01-31T12:34:56Z",
  "level": "info",
  "msg": "host status updated successfully",
  "server_id": 42,
  "status": "online"
}
```

## Database Connection Pool

- Max connections: 5
- Min connections: 2
- Idle timeout: 5 minutes
- Connect timeout: 30 seconds

## Message Processing

- **QoS:** Prefetch count = 1 (sequential processing)
- **Per-message timeout:** 5 seconds (from publisher)
- **Acknowledgment:** Manual ACK/NACK based on error type
