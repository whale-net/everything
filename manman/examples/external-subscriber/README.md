# External Event Subscriber Example

This example demonstrates how to build an external consumer that subscribes to ManManV2 events published to the `external` exchange.

## Use Cases

- **Slack Notifications**: Send alerts when hosts go offline or sessions crash
- **Monitoring Dashboards**: Update Grafana/Prometheus metrics in real-time
- **Logging & Analytics**: Aggregate events for analysis
- **Alerting Systems**: Trigger PagerDuty/OpsGenie alerts
- **Audit Logging**: Record all lifecycle events for compliance

## Architecture

```
┌─────────────────┐
│ manmanv2-       │──► Publishes to "external" exchange
│ processor       │     • manman.host.online/offline/stale
└─────────────────┘     • manman.session.running/stopped/crashed

         │
         │ RabbitMQ (external exchange)
         │
         ▼
┌─────────────────┐
│ External        │──► Subscribes to "manman.#"
│ Subscriber      │     Processes events, sends notifications
└─────────────────┘
```

## Events Published

### Host Events

**Routing Key:** `manman.host.<status>`

```json
{
  "server_id": 42,
  "status": "online" | "offline" | "stale"
}
```

**Use Cases:**
- `manman.host.online`: Send "Host X is back online" notification
- `manman.host.offline`: Trigger alert if critical host
- `manman.host.stale`: Escalate to on-call engineer

### Session Events

**Routing Key:** `manman.session.<status>`

```json
{
  "session_id": 123,
  "sgc_id": 456,
  "status": "running" | "stopped" | "crashed",
  "exit_code": 0 | 1 | null
}
```

**Use Cases:**
- `manman.session.running`: Log session start time
- `manman.session.stopped`: Update metrics, clean up resources
- `manman.session.crashed`: Send alert with exit code

## Configuration

Environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | RabbitMQ connection URL |
| `QUEUE_NAME` | `external-subscriber-events` | Unique queue name for this subscriber |
| `EXTERNAL_EXCHANGE` | `external` | External exchange name |

## Running Locally

### With Docker Compose

```bash
# Start RabbitMQ (if not already running)
docker run -d --name rabbitmq -p 5672:5672 rabbitmq:3-management

# Run subscriber
bazel run //manman/examples/external-subscriber
```

### With Environment Variables

```bash
export RABBITMQ_URL="amqp://user:pass@localhost:5672/dev"
export QUEUE_NAME="my-subscriber"
bazel run //manman/examples/external-subscriber
```

## Building for Production

### Build Binary

```bash
bazel build //manman/examples/external-subscriber
```

### Build Container Image

```bash
bazel build //manman/examples/external-subscriber:image
```

### Deploy to Kubernetes

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manman-slack-notifier
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: subscriber
        image: your-registry/external-subscriber:latest
        env:
        - name: RABBITMQ_URL
          value: "amqp://user:pass@rabbitmq:5672/prod"
        - name: QUEUE_NAME
          value: "slack-notifier"
        - name: SLACK_WEBHOOK_URL
          valueFrom:
            secretKeyRef:
              name: slack-credentials
              key: webhook-url
```

## Extending the Example

### Adding Slack Notifications

```go
import "github.com/slack-go/slack"

func sendSlackNotification(message string) {
    webhookURL := os.Getenv("SLACK_WEBHOOK_URL")
    msg := &slack.WebhookMessage{
        Text: message,
    }
    slack.PostWebhook(webhookURL, msg)
}
```

### Adding Prometheus Metrics

```go
import "github.com/prometheus/client_golang/prometheus"

var (
    hostStatusGauge = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "manman_host_status",
            Help: "Host status (1=online, 0=offline)",
        },
        []string{"server_id"},
    )
)

func updatePrometheusMetric(serverID int64, status string) {
    value := 0.0
    if status == "online" {
        value = 1.0
    }
    hostStatusGauge.WithLabelValues(fmt.Sprintf("%d", serverID)).Set(value)
}
```

### Adding Database Logging

```go
type EventLog struct {
    Timestamp  time.Time
    RoutingKey string
    ServerID   *int64
    SessionID  *int64
    Status     string
}

func logEventToDB(event EventLog) {
    db.Create(&event)
}
```

## Queue Naming Strategy

Each subscriber instance should have a **unique queue name** to ensure:
- Each subscriber receives all events (fanout pattern)
- Multiple subscribers can run independently
- Queue persists across subscriber restarts

**Examples:**
- `slack-notifier` - For Slack integration
- `prometheus-exporter` - For metrics collection
- `audit-logger` - For compliance logging
- `pagerduty-alerts` - For on-call alerts

## Routing Key Patterns

Subscribe to specific event types:

```go
// All manman events
routingKeys := []string{"manman.#"}

// Only host events
routingKeys := []string{"manman.host.#"}

// Only crashed sessions
routingKeys := []string{"manman.session.crashed"}

// Multiple patterns
routingKeys := []string{
    "manman.host.offline",
    "manman.host.stale",
    "manman.session.crashed",
}
```

## Error Handling

The example implements proper error handling:

**Permanent Errors (no retry):**
- Malformed JSON messages
- Unknown event types

**Transient Errors (retry):**
- Network failures to external services
- Temporary API unavailability

**Strategy:**
- Log errors but don't requeue malformed messages
- Use exponential backoff for external API calls
- Implement circuit breakers for failing services

## Monitoring

Add health checks to the subscriber:

```go
http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    w.Write([]byte("ok"))
})

go http.ListenAndServe(":8080", nil)
```

## Testing

Create a test event publisher:

```go
func publishTestEvent() {
    conn, _ := rmq.NewConnectionFromURL("amqp://localhost:5672/")
    publisher, _ := rmq.NewPublisher(conn)

    event := hostrmq.HostStatusUpdate{
        ServerID: 1,
        Status:   "online",
    }

    publisher.Publish(context.Background(), "external", "manman.host.online", event)
}
```

## Best Practices

1. **Idempotency**: Handle duplicate events gracefully
2. **Timeouts**: Set timeouts for external API calls
3. **Circuit Breakers**: Prevent cascading failures
4. **Structured Logging**: Use JSON logging for easy parsing
5. **Metrics**: Expose Prometheus metrics for monitoring
6. **Graceful Shutdown**: Finish processing in-flight messages
7. **Dead Letter Queue**: Handle permanently failed messages

## Next Steps

- Add Slack webhook integration
- Implement Prometheus metrics exporter
- Create PagerDuty alerting logic
- Add database logging for audit trail
- Implement email notifications
- Create webhook forwarder for custom integrations
