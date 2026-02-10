# Log Processor Service

Real-time log streaming service for ManManV2. Consumes logs from RabbitMQ and provides gRPC streaming API for clients.

## Architecture

The log-processor provides two main features:

### 1. Real-Time Streaming
- **Consumer Manager**: Creates on-demand RabbitMQ consumers for each session
- **gRPC Server**: Streams logs to clients via `StreamSessionLogs` RPC
- **Fan-out**: Supports multiple concurrent clients streaming the same session's logs

### 2. Historical Log Archival (Optional)
- **Archiver**: Periodically archives logs to S3 for long-term storage
- **Database**: Stores log references for querying historical logs
- **API Integration**: Retrieves session metadata from ManManV2 API
- **Minute-level granularity**: Logs are archived per minute for efficient retrieval

## Environment Variables

### Required

| Variable | Description | Default | Example |
|----------|-------------|---------|---------|
| `RABBITMQ_URL` | RabbitMQ connection string | `amqp://guest:guest@localhost:5672/` | `amqp://user:pass@rabbitmq:5672/manmanv2-dev` |
| `GRPC_PORT` | gRPC server port | `50053` | `50053` |

### Optional (Streaming)

| Variable | Description | Default | Example |
|----------|-------------|---------|---------|
| `LOG_BUFFER_TTL` | Queue message TTL in seconds | `180` (3 minutes) | `300` |
| `LOG_BUFFER_MAX_MESSAGES` | Maximum messages per queue | `500` | `1000` |
| `DEBUG_LOG_OUTPUT` | Enable debug logging to stdout | `false` | `true` |

### Optional (Archival)

For historical log storage and retrieval:

| Variable | Description | Default | Example |
|----------|-------------|---------|---------|
| `DATABASE_URL` | PostgreSQL connection string | `` (disabled) | `postgres://user:pass@db:5432/manmanv2` |
| `S3_BUCKET` | S3 bucket for log storage | `manman-logs` | `my-logs-bucket` |
| `S3_REGION` | S3 region | `us-east-1` | `us-west-2` |
| `S3_ENDPOINT` | Custom S3 endpoint (MinIO, etc.) | `` (AWS S3) | `http://minio:9000` |
| `S3_ACCESS_KEY` | S3 access key | `` | `minioadmin` |
| `S3_SECRET_KEY` | S3 secret key | `` | `minioadmin` |
| `API_ADDRESS` | ManManV2 API address | `localhost:50051` | `api:50051` |

**Note:** Archival is only enabled when both `DATABASE_URL` and `S3_BUCKET` are configured. If either is missing, the log-processor will only provide real-time streaming.

### Configuration Notes

**LOG_BUFFER_TTL**: Controls how long log messages are retained in the queue. Late subscribers can receive buffered messages if they connect within this window. Increasing this value uses more memory.

**LOG_BUFFER_MAX_MESSAGES**: Sets the maximum number of messages retained per session queue. When exceeded, oldest messages are dropped (drop-head policy). Each message is ~200 bytes on average.

**DEBUG_LOG_OUTPUT**: When enabled, the log-processor will echo all log messages to its own stdout. Useful for debugging but creates high log volume.

## Queue Configuration

Each session gets a dedicated queue with the following settings:

- **Name**: `logs.session.{sessionID}`
- **Routing Key**: `logs.session.{sessionID}`
- **Exchange**: `manman` (topic exchange - must exist)
- **Durable**: `false` (ephemeral, not persisted to disk)
- **Auto-delete**: `true` (deleted when last consumer disconnects)
- **TTL**: Configurable via `LOG_BUFFER_TTL` (default: 3 minutes)
- **Max Length**: Configurable via `LOG_BUFFER_MAX_MESSAGES` (default: 500)
- **Overflow**: `drop-head` (oldest messages dropped when max length reached)

## RabbitMQ Exchange Requirements

The log-processor expects a **topic exchange** named `manman` to exist in RabbitMQ. This exchange should be configured as:

- **Type**: `topic`
- **Durable**: `true` (recommended)
- **Auto-delete**: `false`

**The exchange is created automatically by the host-manager** when it publishes status updates. No manual configuration is needed.

If you need to create it manually:
```bash
rabbitmqadmin declare exchange name=manman type=topic durable=true
```

The log-processor binds queues to this exchange with routing keys matching `logs.session.*`.

## Deployment

### Local Development (Tilt)

The log-processor is automatically deployed when running the manmanv2 Tiltfile:

```bash
cd manman-v2
tilt up
```

Access the gRPC endpoint at `localhost:50053`.

### Kubernetes (Production)

The log-processor is deployed as part of the manmanv2 control plane. Configuration is managed through the release system.

Example deployment:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: log-processor
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: log-processor
        image: ghcr.io/whale-net/manmanv2-log-processor:latest
        env:
        - name: RABBITMQ_URL
          value: "amqp://user:pass@rabbitmq:5672/vhost"
        - name: GRPC_PORT
          value: "50053"
        ports:
        - containerPort: 50053
          name: grpc
```

## Usage

### Starting the service (standalone)

```bash
bazel run //manman/log-processor
```

### Client example (Go)

```go
conn, _ := grpc.Dial("log-processor:50053", grpc.WithInsecure())
client := pb.NewLogProcessorClient(conn)

stream, _ := client.StreamSessionLogs(ctx, &pb.StreamSessionLogsRequest{
    SessionId: 123,
})

for {
    msg, err := stream.Recv()
    if err != nil {
        break
    }
    fmt.Printf("[%s] %s\n", msg.Source, msg.Message)
}
```

### Testing with grpcurl

```bash
# Stream logs for session 123
grpcurl -plaintext -d '{"session_id": 123}' \
  localhost:50053 \
  manman.v1.LogProcessor/StreamSessionLogs
```

## Operational Notes

### Resource Usage

- **Memory**: ~10-20 MB baseline + ~100 KB per active session
- **CPU**: Very low (mostly I/O bound)
- **Network**: ~1-10 KB/s per active stream (depends on log volume)

### Scaling

The log-processor can be scaled horizontally. Multiple instances will each handle different gRPC clients, but RabbitMQ ensures log messages are delivered to all subscribers.

**Recommended:** Run 1-2 replicas for high availability.

### Monitoring

Key metrics to monitor:
- Active gRPC connections
- Active RabbitMQ consumers
- Queue depths (should stay under max length)
- Message publish/consume rates

### Lifecycle

1. **Consumer Creation**: When the first client subscribes to a session
2. **Message Flow**: Host-manager → RabbitMQ → Log-processor → Client
3. **Consumer Cleanup**: When the last client disconnects
4. **Queue Deletion**: Automatically after TTL expires with no consumers

### Troubleshooting

**Logs not streaming:**
- Check that host-manager is running and publishing to RabbitMQ
- Verify RabbitMQ connection (check `RABBITMQ_URL`)
- Ensure session is in "running" status
- Check log-processor logs for errors

**Missing historical logs:**
- Late subscribers only receive messages within the TTL window
- Increase `LOG_BUFFER_TTL` if needed
- Consider enabling persistent storage for long-term log retention

**High memory usage:**
- Reduce `LOG_BUFFER_MAX_MESSAGES`
- Reduce `LOG_BUFFER_TTL`
- Check for stuck consumers that aren't being cleaned up

## Architecture Diagram

```
┌─────────────┐     Logs      ┌──────────┐     gRPC      ┌──────────────┐
│ Host-Manager├──────────────►│ RabbitMQ ├──────────────►│Log-Processor │
└─────────────┘  (fire-forget)└──────────┘   (consume)   └──────┬───────┘
                                                                 │
                                                          ┌──────▼───────┐
                                                          │   UI Server  │
                                                          │  (SSE Bridge)│
                                                          └──────┬───────┘
                                                                 │
                                                          ┌──────▼───────┐
                                                          │   Browser    │
                                                          │ (EventSource)│
                                                          └──────────────┘
```

## Related Documentation

- [Host Manager README](../host/README.md) - Log publishing configuration
- [UI Server Handlers](../../manman-v2/ui/handlers_sessions.go) - SSE endpoint implementation
- [Session Detail UI](../../manman-v2/ui/templates/session_detail.html) - Frontend log viewer
