# ManManV2 Processor - Implementation Verification

## Files Created

### Core Service
- ✅ `manman/processor/main.go` - Entry point, startup orchestration, health checks
- ✅ `manman/processor/config.go` - Configuration struct and environment loading
- ✅ `manman/processor/BUILD.bazel` - Main build file with release_app configuration

### Handlers Package
- ✅ `manman/processor/handlers/handler.go` - Handler interface and registry with wildcard routing
- ✅ `manman/processor/handlers/publisher.go` - Publisher interface for external exchange
- ✅ `manman/processor/handlers/host_status.go` - Host status message handler
- ✅ `manman/processor/handlers/session_status.go` - Session status handler with state validation
- ✅ `manman/processor/handlers/health.go` - Health heartbeat handler with stale detection
- ✅ `manman/processor/handlers/errors.go` - Permanent error type definition
- ✅ `manman/processor/handlers/BUILD.bazel` - Handlers package build file

### Consumer Package
- ✅ `manman/processor/consumer/consumer.go` - RabbitMQ consumer wrapper
- ✅ `manman/processor/consumer/BUILD.bazel` - Consumer package build file

### Documentation
- ✅ `manman/processor/README.md` - Service documentation

## Files Modified

### Repository Interfaces
- ✅ `manman/api/repository/repository.go` - Added ServerRepository and SessionRepository methods

### PostgreSQL Implementation
- ✅ `manman/api/repository/postgres/server.go` - Implemented new server methods:
  - `UpdateStatusAndLastSeen()` - Atomic status + timestamp update
  - `UpdateLastSeen()` - Heartbeat timestamp update
  - `ListStaleServers()` - Find stale hosts
  - `MarkServersOffline()` - Batch mark hosts offline

- ✅ `manman/api/repository/postgres/session.go` - Implemented new session methods:
  - `UpdateStatus()` - Simple status update
  - `UpdateSessionStart()` - Mark session as running
  - `UpdateSessionEnd()` - Mark session as stopped/crashed

### Helm Chart
- ✅ `manman/BUILD.bazel` - Added processor to MANMAN_APPS list

## Build Verification

```bash
# ✅ Binary builds successfully
bazel build //manman/processor:manmanv2-processor

# ✅ Container image builds successfully
bazel build //manman/processor:manmanv2-processor_image

# Binary location
bazel-bin/manman/processor/manmanv2-processor_/manmanv2-processor
```

## Configuration Test

Required environment variables:
```bash
export RABBITMQ_URL="amqp://guest:guest@localhost:5672/dev"
export DB_PASSWORD="your_password"

# Optional (with defaults)
export DB_HOST="localhost"
export DB_PORT="5432"
export DB_USER="postgres"
export DB_NAME="manman"
export STALE_HOST_THRESHOLD_SECONDS="10"
export EXTERNAL_EXCHANGE="external"
```

## Key Implementation Features

### 1. Event Processing Pipeline
- ✅ Consumes from `manman` exchange (internal)
- ✅ Publishes to `external` exchange (cross-domain)
- ✅ Wildcard routing key matching (`status.host.#`, `status.session.#`, `health.#`)
- ✅ Sequential message processing (QoS=1)

### 2. Session State Machine
Valid transitions enforced:
```
pending → starting → running → stopping → stopped
   ↓          ↓         ↓          ↓
crashed   crashed   crashed   crashed
```

### 3. Stale Host Detection
- ✅ Background checker runs every 60 seconds
- ✅ Configurable threshold (default: 10 seconds)
- ✅ Publishes `manman.host.stale` events to external exchange
- ✅ Batch updates for performance

### 4. Error Handling Strategy
**Permanent Errors (NACK without requeue):**
- Malformed JSON
- Entity not found
- Invalid state transitions

**Transient Errors (NACK with requeue):**
- Database failures
- Connection timeouts

### 5. External Event Publishing
Events published to `external` exchange:
- `manman.host.online` - Host status change
- `manman.host.offline` - Host status change
- `manman.host.stale` - Stale host detected
- `manman.session.running` - Session started
- `manman.session.stopped` - Session ended gracefully
- `manman.session.crashed` - Session crashed

### 6. Observability
- ✅ Structured JSON logging with slog
- ✅ Health check endpoints (`/healthz`, `/readyz`)
- ✅ Session statistics logging from health messages
- ✅ Detailed error context in logs

### 7. Graceful Shutdown
- ✅ Signal handling (SIGTERM, SIGINT)
- ✅ Context cancellation propagation
- ✅ 30-second timeout for in-flight messages
- ✅ Resource cleanup (DB pool, RMQ connections)

### 8. Database Connection Pool
- ✅ Max connections: 5
- ✅ Min connections: 2
- ✅ Idle timeout: 5 minutes
- ✅ Connect timeout: 30 seconds
- ✅ Connection health verification on startup

## Integration Points

### With Host Manager
- Receives messages on `manman` exchange
- Routing keys: `status.host.*`, `status.session.*`, `health.*`
- Message types: `HostStatusUpdate`, `SessionStatusUpdate`, `HealthUpdate`

### With External Consumers (e.g., Slackbot)
- Publishes to `external` exchange
- Routing keys: `manman.host.*`, `manman.session.*`
- Domain-prefixed for multi-tenant support

### With Database
- Updates `servers` table (status, last_seen)
- Updates `sessions` table (status, started_at, ended_at, exit_code)
- Uses RETURNING clause for update verification

## Next Steps for Testing

### Unit Tests (TODO)
```bash
# Test handlers
bazel test //manman/processor/handlers:handlers_test

# Test consumer
bazel test //manman/processor/consumer:consumer_test

# Test repository methods
bazel test //manman/api/repository/postgres:postgres_test
```

### Integration Tests (TODO)
1. Start test RabbitMQ and PostgreSQL
2. Publish test messages
3. Verify database updates
4. Test error cases
5. Verify ACK/NACK behavior

### Manual Testing
1. Deploy to dev environment
2. Start host manager
3. Trigger session lifecycle
4. Verify processor logs
5. Query database for state consistency
6. Test stale host detection (stop host manager, wait 10s)
7. Test graceful shutdown (SIGTERM)

## Success Criteria

- ✅ Processor builds successfully
- ✅ All required handlers implemented
- ✅ Database repository methods implemented
- ✅ External exchange publishing implemented
- ✅ Stale host detection implemented
- ✅ Session state validation implemented
- ✅ Error handling with ACK/NACK logic
- ✅ Health checks implemented
- ✅ Graceful shutdown implemented
- ✅ Structured logging implemented
- ✅ Configuration loading implemented

## Changes from Original Plan

1. **Stale host threshold**: Changed from 120s to 10s for tighter feedback loop
2. **External exchange**: Initially called "shared", renamed to "external" per user preference
3. **Publisher implementation**: Added exchange declaration in NewRMQPublisher
4. **Repository methods**: Added `ListStaleServers` and `MarkServersOffline` for batch operations

## Environment Isolation

- Uses RabbitMQ vhosts for environment isolation (`/dev`, `/prod`)
- External exchange is per-vhost (not global)
- Ensures dev messages don't cross into prod
