# rmq - RabbitMQ Connection, Publishing, and Consumption

A Go library for connecting to RabbitMQ, publishing messages to exchanges, and consuming messages from queues with automatic channel recovery and reconnection.

## Quick Start

### Connection

```go
conn, err := rmq.NewConnectionFromURL("amqp://guest:guest@localhost:5672/")
if err != nil {
    log.Fatal(err)
}
defer conn.Close()
```

### Publishing

#### Default Publisher (manman Exchange)

```go
// Publishes to the hardcoded "manman" exchange
publisher, err := rmq.NewPublisher(conn)
if err != nil {
    log.Fatal(err)
}
defer publisher.Close()

err = publisher.Publish(ctx, "manman", "routing.key", map[string]string{
    "message": "hello",
})
```

#### Custom Exchange Publisher

```go
// Publishes to a custom exchange (must be declared beforehand)
publisher, err := rmq.NewPublisherWithExchange(conn, "my-exchange")
if err != nil {
    log.Fatal(err)
}
defer publisher.Close()

err = publisher.Publish(ctx, "my-exchange", "routing.key", message)
```

#### When to Use Which

- **`NewPublisher(conn)`**: Use when publishing only to the `manman` exchange (the library default). The exchange is automatically declared at publisher creation with standard topic-exchange arguments.
- **`NewPublisherWithExchange(conn, exchange)`**: Use when publishing to a custom exchange (not `manman`). The exchange is declared at construction with the same standard topic-exchange arguments. If your exchange requires different arguments or is broker-provided (like `amq.topic`), see [Exchange Declaration Behavior](#exchange-declaration-behavior).

### Consuming

```go
consumer, err := rmq.NewConsumer(conn, "my-queue")
if err != nil {
    log.Fatal(err)
}

// Bind the queue to an exchange
err = consumer.BindExchange("my-exchange", []string{"routing.key"})
if err != nil {
    log.Fatal(err)
}

// Register a handler for messages
consumer.RegisterHandler("routing.key", func(ctx context.Context, msg rmq.Message) error {
    log.Printf("received: %s", msg.Body)
    return nil
})

// Start consuming (runs in a background goroutine)
err = consumer.Start(ctx)
if err != nil {
    log.Fatal(err)
}
```

## Connection

### API

#### `func NewConnectionFromURL(url string) (*Connection, error)`

Creates a connection to RabbitMQ from an AMQP URL. Returns an error if the connection fails.

#### `func (c *Connection) Channel() (*amqp.Channel, error)`

Opens a channel for the connection.

#### `func (c *Connection) Close() error`

Closes the connection and all associated channels.

## Publisher

### API

#### `func NewPublisher(conn *Connection) (*Publisher, error)`

Creates a publisher for the default `"manman"` topic exchange. The exchange is declared at construction.

#### `func NewPublisherWithExchange(conn *Connection, exchange string) (*Publisher, error)`

Creates a publisher for a custom exchange. The exchange is declared at construction with standard topic-exchange arguments (durable, not auto-delete, not internal).

#### `func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, body interface{}) error`

Publishes a message to the specified exchange. The `body` can be:
- `[]byte`
- `string`
- Any other type (marshaled as JSON)

The message includes:
- `ContentType: "application/json"`
- `DeliveryMode: amqp.Persistent` (all messages are persistent)
- Trace context headers (OpenTelemetry propagation)
- Timestamp

#### `func (p *Publisher) PublishWithExpiry(ctx context.Context, exchange, routingKey string, body interface{}, expiry time.Duration) error`

Like `Publish`, but adds a per-message TTL. Messages older than `expiry` are dropped by the broker.

#### `func (p *Publisher) Close() error`

Closes the publisher and its channel.

### Automatic Channel Recovery

The publisher automatically recovers from channel closures:

1. If a `Publish` call fails with a channel-closed error, the publisher recreates the channel.
2. The channel is redeclared with the same exchange arguments.
3. The publish is retried once on the new channel.
4. If the retry succeeds, `Publish` returns `nil`.
5. If the retry fails, the original error is returned.

This recovery is transparent to the caller.

## Consumer

### API

#### `func NewConsumer(conn *Connection, queueName string) (*Consumer, error)`

Creates a consumer for a **named durable queue** with auto-delete disabled. This is the default.

Use this when you have a single consumer (one replica) for a queue, or when queue durability across broker restarts is important.

#### `func NewConsumerWithOpts(conn *Connection, queueName string, durable, autoDelete bool, messageTTL, maxMessages int) (*Consumer, error)`

Creates a consumer with custom queue options:

- `durable`: Queue survives broker restarts (requires `queueName` to be non-empty).
- `autoDelete`: Queue is deleted when the last consumer disconnects.
- `messageTTL`: Per-message time-to-live in milliseconds (0 = no limit).
- `maxMessages`: Maximum queue depth (0 = no limit).

#### `func (c *Consumer) BindExchange(exchange string, routingKeys []string) error`

Binds the consumer's queue to an exchange with the given routing keys. Can be called multiple times to bind to multiple exchanges or add more routing keys.

#### `func (c *Consumer) RegisterHandler(routingKeyPattern string, handler MessageHandler) error`

Registers a handler function for a routing key pattern:

```go
type MessageHandler func(ctx context.Context, msg Message) error

type Message struct {
    RoutingKey    string
    Body          []byte
    ReplyTo       string
    CorrelationID string
}
```

Errors returned by the handler are logged but do not stop consumption.

#### `func (c *Consumer) Start(ctx context.Context) error`

Starts consuming messages. Returns immediately; consumption runs in a background goroutine. When `ctx` is cancelled, consumption stops.

#### `func (c *Consumer) Close() error`

Closes the consumer and its channel.

### Automatic Reconnection

The consumer automatically reconnects on channel closure:

1. If the channel closes unexpectedly, the consumer logs a warning and attempts to reconnect.
2. For durable queues, the queue is not redeclared (it already exists); only the bindings are reapplied.
3. For non-durable queues, the queue is redeclared and all bindings are reapplied.
4. Reconnection uses exponential backoff with a maximum 30-second delay.
5. Consumption resumes when the new channel is ready.

### Durable vs. Ephemeral Queues

**Durable queues** (`durable=true`, typically `autoDelete=false`):
- Survive broker restarts.
- Good for single-consumer, critical workloads.
- All replicas of a service share the same queue; RabbitMQ round-robins deliveries. If you want fan-out behavior (every replica gets every message), do not use durable queues.

**Ephemeral queues** (`durable=false`, typically `autoDelete=true`):
- Deleted when the last consumer disconnects (if `autoDelete=true`).
- Good for fan-out patterns (e.g., logging, broadcasts to multiple replicas).
- Each replica creates its own ephemeral queue, so every replica receives every message.

### QoS (Quality of Service)

The consumer sets QoS to `Qos(1, 0, false)`, which:
- Prefetches 1 message at a time (serializes delivery to the handler).
- Does not apply a global QoS limit.
- Does not auto-acknowledge messages.

This is appropriate for low-volume workloads. For high-rate topics with multiple consumers, a larger prefetch may reduce latency. This behavior is not changed by this library.

### Dead-Letter Queue (DLQ)

Durable queues without TTL or max-length limits automatically create a dead-letter queue (DLQ) with the name `<queueName>-dlq`. Messages that fail processing (or that reach a limit) are routed to the DLQ. Non-durable queues do not create DLQs.

## Exchange Declaration Behavior

### Understanding Exchange Arguments

Every exchange declared by this library uses these arguments:
- Exchange type: `"topic"` (for routing-key-based topic routing)
- Durable: `true` (survives broker restarts)
- Auto-delete: `false` (explicit deletion required)
- Internal: `false` (clients can publish to it)
- No-wait: `false` (wait for broker confirmation)
- Arguments: `nil` (no additional arguments)

### NewPublisher vs. NewPublisherWithExchange

**`NewPublisher(conn)` declares the `"manman"` exchange:**
```go
ch.ExchangeDeclare("manman", "topic", true, false, false, false, nil)
```

**`NewPublisherWithExchange(conn, exchange)` declares the specified exchange:**
```go
ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil)
```

Both declarations happen at construction time. If the exchange already exists with the same arguments, the declaration succeeds. If it exists with different arguments, the broker returns a 406 `PRECONDITION_FAILED` error and closes the channel.

### Publish-Time Exchange Behavior

The `Publish(ctx, exchange, routingKey, body)` method accepts an exchange argument at publish time:

```go
publisher.Publish(ctx, "my-exchange", "routing.key", message)
```

**This exchange is not declared at publish time.** If:
- The exchange was declared at construction: it is already available, and `Publish` uses it.
- The exchange was not declared (e.g., you are publishing to a broker-provided exchange like `amq.topic` or a pre-declared custom exchange): `PublishWithContext` returns `nil`, but the broker asynchronously closes the channel with a 404 error. The next publish attempt triggers channel recreation, redeclares the original construction exchange, and retries.

**Important: Publishing to an undeclared exchange silently drops the first message.** The broker returns no immediate error; instead, it closes the channel asynchronously.

### Consumer Exchange Behavior

The consumer never declares exchanges. When you call `BindExchange(exchange, routingKeys)`, the consumer only binds its queue to the exchange:

```go
ch.QueueBind(queueName, routingKey, exchange, false, nil)
```

**The exchange must exist before binding.** The exchange can be:
- Declared by your publisher (via `NewPublisher` or `NewPublisherWithExchange`).
- Declared by another service at startup.
- Broker-provided (e.g., `amq.topic`).

If the exchange does not exist, `BindExchange` returns a binding error.

### Correct Pattern for Custom Exchanges

To use a custom exchange (not `manman`):

1. **Choose a publisher constructor:**
   - If only this service publishes: `NewPublisherWithExchange(conn, "my-exchange")`.
   - If multiple services publish: each must use `NewPublisherWithExchange(conn, "my-exchange")` with the same exchange name.

2. **Ensure identical arguments in every process:**
   - Every process that declares the exchange must use identical arguments (`"topic"`, `durable=true`, `autoDelete=false`, `internal=false`, `noWait=false`, `args=nil`).
   - Mismatch is detected as a 406 `PRECONDITION_FAILED` error and closes the channel.

3. **Consumer binding:**
   - Consumers can bind to the exchange via `BindExchange(exchange, routingKeys)`.
   - The exchange must already exist (declared by a publisher or broker).

### Real-World Example: leaflab/api

`leaflab/api` publishes device configuration updates to the `amq.topic` exchange:

```go
const mqttExchange = "amq.topic"

publisher, err := rmq.NewPublisher(rmqConn)  // Declares "manman"
// ...
publisher.Publish(ctx, mqttExchange, "leaflab.device.config", wireData)
```

This works because:
- `NewPublisher` declares `"manman"` at construction (a no-op if it already exists).
- `Publish` is called with `amq.topic`, which is broker-provided and pre-declared.
- The first publish to `amq.topic` triggers a 404 channel close (since it's not declared by the publisher).
- The next publish recreates the channel, redeclares `"manman"`, and retries to `amq.topic` successfully.
- Subsequent publishes to `amq.topic` use the existing channel.

**This pattern works only because the broker tolerates the stray `manman` declaration.** It is not recommended; prefer explicit `NewPublisherWithExchange` for clarity.

## Known Behaviors (Documented, Not Changed)

### Persistent Delivery Mode

All messages published via `Publish` and `PublishWithExpiry` set `DeliveryMode: amqp.Persistent`, regardless of queue durability:

```go
publishing := amqp.Publishing{
    // ...
    DeliveryMode: amqp.Persistent,
    // ...
}
```

This is **semantically incorrect** for idempotent nudges published to ephemeral auto-delete queues (the message survives the queue's deletion). However, it is harmless: the message is simply treated as persistent by the broker, with no practical effect on ephemeral queues.

### Consumer Prefetch Serialization

Consumers set `Qos(1, 0, false)`, which prefetches one message at a time:

```go
ch.Qos(1, 0, false)
```

This serializes delivery to the message handler. For low-volume workloads, this is fine. For high-rate topics with multiple consumers fanning out (e.g., all replicas of a service processing the same topic), a higher prefetch reduces latency. This behavior is intentional and not changed by this library; adopters should tune `Qos` if needed.

## Troubleshooting

### Channel Closure on PRECONDITION_FAILED

**Symptom:** Consumer encounters a 406 `PRECONDITION_FAILED` error.

**Cause:** A queue or exchange was declared with different arguments than its broker definition.

**Solution:**
- For consumer queues: Use `NewConsumerWithOpts` to match the broker's queue definition.
- For exchanges: Ensure all publishers use identical `NewPublisherWithExchange` calls with the same exchange name and arguments.

### First Message Lost to Undeclared Exchange

**Symptom:** A message published to a custom exchange never arrives on the first try.

**Cause:** The exchange was not declared before the first publish.

**Solution:**
- Use `NewPublisherWithExchange` at construction to declare the exchange upfront.
- Ensure all consumers bind to the same exchange name.

### RabbitMQ Connection Lost

**Symptom:** Publisher or consumer stops working after a broker restart.

**Cause:** The connection is closed, but the publisher/consumer is not reconnecting.

**Solution:**
- Publishers automatically reconnect on the next `Publish` call.
- Consumers automatically reconnect with exponential backoff; check logs for reconnection messages.
- For persistent applications, ensure the connection is monitored at the application level.

## TLS Configuration

For TLS setup details, see [TLS_CONFIGURATION.md](TLS_CONFIGURATION.md).
