# Log Processor Service

Real-time log streaming service for ManManV2. Consumes logs from RabbitMQ and provides gRPC streaming API for clients.

## Architecture

- **Consumer Manager**: Creates on-demand RabbitMQ consumers for each session
- **gRPC Server**: Streams logs to clients via `StreamSessionLogs` RPC
- **Fan-out**: Supports multiple concurrent clients streaming the same session's logs

## Configuration

Environment variables:

- `RABBITMQ_URL` - RabbitMQ connection string (default: `amqp://guest:guest@localhost:5672/`)
- `GRPC_PORT` - gRPC server port (default: `50053`)
- `LOG_BUFFER_TTL` - Queue TTL in seconds (default: `180` = 3 minutes)
- `LOG_BUFFER_MAX_MESSAGES` - Max queue size (default: `500`)
- `DEBUG_LOG_OUTPUT` - Enable stdout logging (default: `false`)

## Queue Configuration

Each session gets a dedicated queue:

- **Name**: `logs.session.{sessionID}`
- **Routing Key**: `logs.session.{sessionID}`
- **Exchange**: `manman` (topic exchange)
- **Durable**: `false` (ephemeral)
- **Auto-delete**: `true` (deleted when last consumer disconnects)
- **TTL**: 3 minutes (configurable)
- **Max Length**: 500 messages (configurable)
- **Overflow**: `drop-head` (oldest messages dropped)

## Usage

### Starting the service

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

## Operational Notes

- Consumers are created on-demand when the first client subscribes
- Consumers are cleaned up when the last client disconnects
- Late subscribers within the TTL window receive buffered messages
- RabbitMQ queue is automatically deleted after TTL expires
