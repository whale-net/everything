# Status Service Architecture - Implementation Plan

## Current State
- Status API is currently just a stub with minimal functionality
- All message processing happens in worker services directly
- No centralized status event processing

## Proposed Split: Two Services

### 1. Status API Service (HTTP-only)
**Purpose**: Provide HTTP endpoints for querying status information
**Responsibilities**:
- Health check endpoints
- Worker status queries
- Server instance status
- Historical status data retrieval
- Status dashboards/monitoring endpoints

**Technology**: FastAPI (like existing services)
**Scaling**: Stateless, can scale horizontally based on HTTP traffic

### 2. Status Event Processor Service (Pub/Sub-only)
**Purpose**: Process status-related events and maintain status state
**Responsibilities**:
- Subscribe to worker heartbeats
- Process server lifecycle events (start/stop/crash)
- Update status information in database
- Generate alerts/notifications
- Maintain status metrics and aggregations

**Technology**: Pure message consumer (no HTTP server)
**Scaling**: Can scale based on message volume, typically fewer instances needed

## Benefits of This Split

1. **Single Responsibility**: Each service has one clear purpose
2. **Independent Scaling**: Scale HTTP API and message processing separately
3. **Failure Isolation**: API failures don't affect event processing
4. **Technology Optimization**: Each service can use optimal patterns for its workload
5. **Testing**: Easier to test HTTP logic vs message processing logic separately
6. **Deployment**: Can deploy/update services independently

## Implementation Steps

1. Keep current `start_status_api` command for HTTP-only status API
2. Add new `start_status_processor` command for pub/sub event processing
3. Define clear event schemas for status updates
4. Implement database models for status tracking
5. Create proper separation between read (API) and write (processor) operations

## Message Flow Example
```
Worker Heartbeat → RabbitMQ → Status Processor → Database
                                                     ↓
Status API ← Database ← Status Query Request
```

This follows CQRS (Command Query Responsibility Segregation) pattern naturally.
