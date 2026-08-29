# htmxsse - Server-Sent Events for HTMX with Live Updates

A Go library providing server-sent events (SSE) over RabbitMQ for HTMX-based applications. Delivers full-state fragments that swap DOM elements on reconnect to maintain scroll position and text selection, with automatic heartbeat-based keepalives for unchanged state.

## Features

- **Live Updates via RabbitMQ**: Subscribe to topics and stream events via SSE with automatic broker attachment and retry.
- **Full-State Fragments**: Per-connection, per-topic fragment rendering for consistent, reproducible updates.
- **Reconnect Baseline Suppression**: Clients carry a baseline of the last-seen state; the server suppresses duplicate swaps, emitting keepalives for unchanged content instead. Preserves scroll position and text selection on reconnect.
- **Multi-Topic Support**: A single connection can subscribe to multiple topics; all per-topic guarantees hold independently.
- **Heartbeat-Based Keepalives**: Configurable heartbeat interval with automatic keepalive emission when fragments are unchanged.
- **Configurable Retry and Timeout**: Advertise client retry interval via SSE `retry:` field; set stream lifetime limits.
- **Broker-Free Testing**: Inject a fake Transport and clock for testing without a live broker.
- **Slow-Subscriber Drop Policy**: Handles backpressure by dropping oldest unread events when a subscriber's buffer fills; no wire signal or connection state change.

## Installation

```starlark
# In your BUILD.bazel
go_library(
    name = "myapp",
    deps = [
        "//libs/go/htmxsse",
        "//libs/go/rmq",
    ],
)
```

## Quick Start

### Basic Setup

```go
package main

import (
    "context"
    "net/http"
    
    "github.com/whale-net/everything/libs/go/htmxsse"
    "github.com/whale-net/everything/libs/go/rmq"
)

func main() {
    // Connect to RabbitMQ
    conn, err := rmq.Dial("amqp://guest:guest@localhost:5672/")
    if err != nil {
        log.Fatal(err)
    }
    defer conn.Close()
    
    // Create the Hub with a default attach function
    config := htmxsse.DefaultConfig()
    exchangeName := "my-exchange"  // Configured by caller
    attachFunc := htmxsse.DefaultAttachFunc(exchangeName, conn)
    hub := htmxsse.NewHub(attachFunc, config)
    defer hub.Close()
    
    // Define a fragment function to render live content
    fragment := func(r *http.Request, topic string) ([]byte, error) {
        // Render current state for this topic
        // Called once per connection on connect, and per heartbeat/event
        return []byte(fmt.Sprintf(`{"topic": "%s", "state": "current"}`, topic)), nil
    }
    
    // Setup routes
    mux := http.NewServeMux()
    mux.HandleFunc("/events", htmxsse.Handler(hub, []string{"updates"}, fragment))
    
    http.ListenAndServe(":8000", mux)
}
```

### With Multiple Topics

```go
// Subscribe a single connection to multiple topics
topics := []string{"promotion-updates", "inventory-changes", "alerts"}

fragment := func(r *http.Request, topic string) ([]byte, error) {
    switch topic {
    case "promotion-updates":
        return renderPromotionState(r)
    case "inventory-changes":
        return renderInventoryState(r)
    case "alerts":
        return renderAlerts(r)
    }
    return nil, errors.New("unknown topic")
}

mux.HandleFunc("/events", htmxsse.Handler(hub, topics, fragment))
```

### HTMX Integration

```html
<div hx-ext="sse" sse-connect="/events" sse-swap="updates,promotion-updates">
    <!-- Content here will be updated by SSE events -->
</div>

<script>
// On reconnect, the browser automatically sends Last-Event-ID header
// The server uses this to suppress duplicate swaps for unchanged state
</script>
```

## Configuration

### Config Struct

```go
type Config struct {
    // ExchangeName is the RabbitMQ exchange name for the Hub.
    // Required: caller must configure this to match the exchange
    // declared in the attach function (see DefaultAttachFunc).
    // Example: "app-registry.htmxsse"
    ExchangeName string
    
    // HeartbeatInterval is the interval between heartbeats.
    // Default: 30 seconds
    // Heartbeats check if the fragment has changed; if unchanged,
    // a keepalive is emitted (no swap). If changed, a swap is emitted.
    HeartbeatInterval time.Duration
    
    // MaxStreamLifetime is the maximum lifetime of a single SSE stream.
    // Default: 1 hour
    // After this duration, the stream is closed, and the client
    // reconnects (with Last-Event-ID).
    MaxStreamLifetime time.Duration
    
    // SubscriberBufferDepth is the depth of the per-subscriber event queue.
    // Default: 100
    // When the queue is full, the oldest unread event is dropped
    // (drop-oldest policy; no wire signal to the client).
    SubscriberBufferDepth int
    
    // AdvertisedRetryInterval is the interval advertised to clients
    // via the SSE retry: field.
    // Default: 5 seconds
    // Validated: must be < 2 * HeartbeatInterval
    // An advertised retry >= 2 * heartbeat allows the client-side
    // debounce to swallow the not-live indicator, hiding disconnection.
    AdvertisedRetryInterval time.Duration
}
```
```

### DefaultConfig

```go
config := htmxsse.DefaultConfig()
// HeartbeatInterval:       30 * time.Second
// MaxStreamLifetime:       1 * time.Hour
// SubscriberBufferDepth:   100
// AdvertisedRetryInterval: 5 * time.Second
```

### Validation

The Hub validates the configuration:

```go
if err := config.Validate(); err != nil {
    log.Fatal(err)  // e.g., "advertisedRetryInterval must be less than 2*heartbeatInterval"
}

hub := htmxsse.NewHub(attachFunc, config)
```

## Environment Variables

The Hub takes its configuration from the adopting process. Environment variables are the adopter's responsibility.

**Example from `tools/app_registry/ui`** (the in-scope adopter):

| Variable | Default | Description |
|----------|---------|-------------|
| `SSE_HEARTBEAT_INTERVAL` | `30s` | Interval between heartbeats; parsed via `time.ParseDuration`. |
| `SSE_MAX_STREAM_LIFETIME` | `1h` | Maximum stream lifetime; parsed via `time.ParseDuration`. |
| `SSE_SUBSCRIBER_BUFFER_DEPTH` | `100` | Per-subscriber buffer depth. |
| `SSE_ADVERTISED_RETRY_INTERVAL` | `5s` | Retry interval advertised to clients; parsed via `time.ParseDuration`. Must be < 2 * heartbeat. |
| `RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | RabbitMQ connection string. |

The adopter constructs a `Config` by reading these variables, validates it, and passes it to `NewHub`.

See `tools/app_registry/ENV.md` for the full set of environment variables the in-scope adopter manages.

## Message Broker Requirements

### Exchange Declaration

The Hub binds to a caller-configured RabbitMQ exchange during transport attachment. The exchange name is configured via Config.ExchangeName and passed to the attach function. The exchange signature itself is **byte-identical across every process** (FR0.7):

```go
// Declared by DefaultAttachFunc:
ch.ExchangeDeclare(
    exchangeName,      // Caller-configured (e.g., "app-registry.htmxsse")
    "topic",            // kind (must be "topic" for routing-key-based topic routing)
    true,               // durable: yes (survives broker restart)
    false,              // autoDelete: no (must be explicitly deleted)
    false,              // internal: no (clients can publish to it)
    false,              // noWait: no (wait for confirmation)
    nil,                // args: none
)
```

A mismatch in these arguments (typically durable=false when a restart erases messages) causes a 406 `PRECONDITION_FAILED` error and closes the channel.

### Topic Derivation and Routing Keys

Topics are derived from RabbitMQ routing keys. A publisher sends messages to the configured exchange (via Config.ExchangeName) with a routing key; the Hub binds with `#` (match all) and extracts the routing key as the topic:

```go
// Handler subscribes to:
topics := []string{"promotion-updates", "inventory-changes"}

// Publisher sends to:
    // exchange: <configured via Config.ExchangeName>
// routing_key: "promotion-updates" → arrives at Handler as topic "promotion-updates"
// routing_key: "inventory-changes" → arrives at Handler as topic "inventory-changes"
```

## API Reference

### Hub

#### `func NewHub(attachFunc AttachFunc, config Config) *Hub`

Creates a new Hub with the given attach function and configuration. Attachment is lazy; the transport connects on the first subscription.

#### `func NewHubWithClock(attachFunc AttachFunc, config Config, clock Clock) *Hub`

Creates a new Hub with a custom clock (primarily for testing).

#### `func (h *Hub) Subscribe(topic string) (<-chan Event, func())`

Subscribes to a topic and returns a receive-only event channel and an unsubscribe function. Calling unsubscribe closes the channel and removes the subscription. The Hub automatically attaches to the transport on the first subscription.

#### `func (h *Hub) Close() error`

Closes the Hub, cancels the transport context, and cleans up all subscriptions.

### Handler

#### `func Handler(hub *Hub, topics []string, fragment Fragment) http.HandlerFunc`

Creates an HTTP handler that upgrades a request to SSE and streams events for the given topics.

**Topics**: One or more topic names. Order is sorted internally for consistent baseline encoding.

**Fragment function signature**:
```go
type Fragment func(*http.Request, string) ([]byte, error)
```

**Fragment contract (FR3)**:
- Invoked once per connection on connect (for each topic).
- Invoked per event received on any topic (once per event, once per connection).
- Invoked per heartbeat interval (once per heartbeat, once per connection, once per topic).
- **Errors are transient**: any error causes no bytes to be written for that event/heartbeat and does not close the stream.
- **Credential freshness over a long stream is the adopter's obligation** (FR27). The fragment function has access to the original request; it is responsible for re-authenticating or refreshing credentials as needed. The library does not refresh credentials; reconnect is the client's responsibility.
- Every invocation receives the same request object; the caller (adopter) must not mutate the request or assume per-topic isolation of request state.

**Example fragment with credential checking**:
```go
fragment := func(r *http.Request, topic string) ([]byte, error) {
    user := getUser(r.Context())  // From your auth layer
    if user == nil {
        return nil, errors.New("user not authenticated")  // Transient; stream stays open
    }
    
    // Render state for this user and topic
    state, err := fetchState(r.Context(), user.ID, topic)
    if err != nil {
        return nil, err  // Transient; stream stays open
    }
    
    return renderFragment(state), nil
}
```

### Response Guarantees (Handler)

#### **FR2: Context Cancellation**

The handler returns when the request context is done. This is the **only** way an adopter ends a stream early.

**Pattern for wrapping and capturing cancel**:
```go
type sse struct {
    hub      *htmxsse.Hub
    fragment htmxsse.Fragment
}

func (s *sse) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // Wrap the request context to capture cancel for early termination
    ctx, cancel := context.WithCancel(r.Context())
    defer cancel()
    r = r.WithContext(ctx)
    
    // Your custom logic can call cancel() if needed
    handler := htmxsse.Handler(s.hub, []string{"updates"}, s.fragment)
    handler(w, r)
}
```

#### **FR2: Response-Commit Ordering**

Headers are flushed before the first fragment invocation. A fragment error on connect is a failure on an **established stream** (HTTP 200, but no fragment data). The stream stays open for subsequent events and heartbeats.

#### **FR5: Reconnect-Baseline Suppression**

The client's last-seen baseline is carried in the `Last-Event-ID` request header. The adopter must **not** modify or clear this header. The server compares incoming fragments to the baseline and emits:
- **Swap** (with full baseline ID): fragment has changed or client has no baseline for this topic.
- **Keepalive** (no ID, no swap): fragment is unchanged from baseline.

**The multi-topic rule**: Suppression is per-topic. The carried baseline is a set of all topics (example: `topic-a:hash1|topic-b:hash2`). If the handler cannot parse the header, or a topic is missing from it, that topic **swaps** (fails safe toward fresh data).

**Critical for adopters**: `sse-connect` (the handler URL) **must sit outside the swapped region**. If the handler endpoint itself is inside a div that gets swapped, a reconnect causes the event listener to be re-attached before the new connection receives `Last-Event-ID`.

#### **FR5: Per-Frame Baseline Set**

Every emitted frame's `id:` field carries the full baseline set, not only the topic that changed. This ensures reconnect can suppress swaps for all topics simultaneously.

**Encoding** (planner note 16):
- Format: pipe-separated `topic:hash` pairs, e.g. `topic-a:abc123|topic-b:def456`
- Each hash is the hex-encoded SHA256 hash of the rendered fragment.
- Topics are in sorted order.
- **ASCII-safe**: contains only alphanumeric characters and `|` and `:`.
- **Length-bounded**: grows as `(topic count) * (hash size)`. Hash size is constant (64 hex chars for SHA256). Proxy/ingress header-buffer limits may truncate; truncation causes safe-fail swap for the omitted topic.

**Example**: After rendering a 3-topic connection:
```
topic-a fragment: `<div>state-1</div>` → hash: abc...
topic-b fragment: `<div>state-2</div>` → hash: def...
topic-c fragment: `<div>state-3</div>` → hash: ghi...

Emitted ID: topic-a:abc...|topic-b:def...|topic-c:ghi...
```

On reconnect with this ID, client sends `Last-Event-ID: topic-a:abc...|topic-b:def...|topic-c:ghi...`. The server parses it and, if all three hashes match the current fragments, emits three keepalives (no swaps).

#### **NFR11: No-Swap Guarantee (Byte-Equality)**

The guarantee silently evaporates if your fragment renders:
- **Relative timestamps** (e.g., `"updated_at": "5 minutes ago"`)
- **Nonces or UUIDs** (e.g., `"request_id": "abc...123"`)
- **Re-ordered maps** (if your language re-orders JSON keys on every render)

Every heartbeat will produce a different hash, causing every heartbeat to become a swap, destroying scroll position and text selection.

**Safe rendering**: Use absolute timestamps (e.g., Unix timestamp or RFC3339). The in-scope adopter renders absolute values (`:122`, `:152`, `:193` in `promotion_details.templ`), which is why Phase 1 is safe. The second adopter is the one who will hit it.

#### **NFR7: Degradation Semantics**

If the broker is down, the exchange is missing, or the transport cannot attach:
- The host still starts and serves (handler responds with HTTP 200).
- Live updates are simply absent (no events flow).
- The page converges on the heartbeat: every heartbeat interval, the fragment is re-rendered and emitted (as a keepalive or swap).
- On broker recovery, the transport reattaches automatically (exponential backoff, max 30s delay).

### Types

#### `Event`

```go
type Event struct {
    Topic      string // Topic this event was delivered on
    RoutingKey string // Original RabbitMQ routing key
    Body       []byte // Event payload
}
```

#### `AttachFunc`

```go
type AttachFunc func(context.Context) (Transport, error)
```

A function that creates and returns a Transport (typically a RabbitMQ consumer). Called once; the Hub retries on transport failure.

`DefaultAttachFunc(exchangeName string, conn *rmq.Connection)` creates an attach function that declares the specified exchange and returns an ephemeral consumer. The exchangeName must match the value configured in Config.ExchangeName.

#### `Clock`

```go
type Clock interface {
    Now() time.Time
    NewTicker(d time.Duration) Ticker
    Sleep(ctx context.Context, d time.Duration) error
}
```

Abstraction over time for testing. Use `NewHubWithClock` to inject a fake clock.

## Usage Patterns

### Per-Connection Fragment Rendering

Each connection renders fragments independently. This allows per-request state (e.g., user ID, authentication context) to influence the output:

```go
fragment := func(r *http.Request, topic string) ([]byte, error) {
    user := getUser(r.Context())
    if user == nil {
        return nil, errors.New("unauthenticated")
    }
    
    // Render user-specific state
    data, err := fetchUserData(r.Context(), user.ID, topic)
    if err != nil {
        return nil, err  // Transient; try again on next heartbeat
    }
    
    return renderState(data), nil
}
```

### Multi-Topic Coordination

A single connection can subscribe to related topics and emit coordinated updates:

```go
fragment := func(r *http.Request, topic string) ([]byte, error) {
    // Each topic gets its own fragment, but they share request context
    switch topic {
    case "status":
        return renderStatus(r), nil
    case "details":
        return renderDetails(r), nil
    default:
        return nil, fmt.Errorf("unknown topic: %s", topic)
    }
}

handler := htmxsse.Handler(hub, []string{"status", "details"}, fragment)
mux.HandleFunc("/events", handler)
```

### Custom Retry Configuration

```go
config := htmxsse.DefaultConfig()
config.HeartbeatInterval = 20 * time.Second
config.AdvertisedRetryInterval = 10 * time.Second  // Must be < 2 * 20s = 40s

if err := config.Validate(); err != nil {
    log.Fatal(err)
}

hub := htmxsse.NewHub(attachFunc, config)
```

## Testing

### Broker-Free Testing with Fake Transport

Test your fragment function and handler logic without a live broker:

```go
import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"
    
    "github.com/whale-net/everything/libs/go/htmxsse"
)

func TestHandler_FragmentError(t *testing.T) {
    // Create a fake transport (does nothing)
    fakeTransport := &fakeTransport{}
    
    // Create an attach function that returns the fake transport
    attachFunc := func(ctx context.Context) (htmxsse.Transport, error) {
        return fakeTransport, nil
    }
    
    config := htmxsse.DefaultConfig()
    hub := htmxsse.NewHub(attachFunc, config)
    defer hub.Close()
    
    // Fragment function that always errors
    fragment := func(r *http.Request, topic string) ([]byte, error) {
        return nil, errors.New("fragment error")
    }
    
    // Create handler and request
    handler := htmxsse.Handler(hub, []string{"test-topic"}, fragment)
    req := httptest.NewRequest("GET", "/events", nil)
    ctx, cancel := context.WithCancel(req.Context())
    req = req.WithContext(ctx)
    
    w := httptest.NewRecorder()
    
    // Run handler in goroutine
    done := make(chan struct{})
    go func() {
        handler(w, req)
        close(done)
    }()
    
    // Cancel context to close stream
    time.Sleep(50 * time.Millisecond)
    cancel()
    <-done
    
    // Verify response is still HTTP 200 (no error response on transient fragment error)
    if w.Code != http.StatusOK {
        t.Errorf("expected 200, got %d", w.Code)
    }
}
```

### Using httptest.ResponseRecorder

`httptest.ResponseRecorder` captures SSE output for testing:

```go
func TestReconnectBaseline(t *testing.T) {
    fakeTransport := &fakeTransport{}
    hub := htmxsse.NewHub(
        func(ctx context.Context) (htmxsse.Transport, error) {
            return fakeTransport, nil
        },
        htmxsse.DefaultConfig(),
    )
    defer hub.Close()
    
    fragment := func(r *http.Request, topic string) ([]byte, error) {
        return []byte(`{"state": "current"}`), nil
    }
    
    handler := htmxsse.Handler(hub, []string{"test"}, fragment)
    
    // First request
    req1 := httptest.NewRequest("GET", "/events", nil)
    ctx1, cancel1 := context.WithCancel(req1.Context())
    req1 = req1.WithContext(ctx1)
    
    w1 := httptest.NewRecorder()
    go handler(w1, req1)
    
    time.Sleep(50 * time.Millisecond)
    cancel1()
    
    // Extract ID from first response
    body1 := w1.Body.String()
    // Parse "id: ..." line
    
    // Second request with Last-Event-ID
    req2 := httptest.NewRequest("GET", "/events", nil)
    req2.Header.Set("Last-Event-ID", extractedID)
    ctx2, cancel2 := context.WithCancel(req2.Context())
    req2 = req2.WithContext(ctx2)
    
    w2 := httptest.NewRecorder()
    go handler(w2, req2)
    
    time.Sleep(50 * time.Millisecond)
    cancel2()
    
    // Verify keepalive emitted (not swap)
    body2 := w2.Body.String()
    if !strings.Contains(body2, "event: test-keepalive") {
        t.Errorf("expected keepalive, got: %s", body2)
    }
}
```

### Injecting a Custom Clock

For testing heartbeat and lifetime logic:

```go
type fakeClock struct {
    now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) NewTicker(d time.Duration) htmxsse.Ticker {
    return &fakeTicker{interval: d}
}
func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
    c.now = c.now.Add(d)
    return nil
}

func TestHeartbeatInterval(t *testing.T) {
    clock := &fakeClock{now: time.Now()}
    hub := htmxsse.NewHubWithClock(attachFunc, config, clock)
    
    // Advance time and verify heartbeat behavior
}
```

## Phase 1 Exit: Frozen Decisions

This README documents the three frozen behavioral decisions made in Phase 1:

### Decision 1: FR1a — Slow-Subscriber Policy

**Policy**: Drop-oldest.

When a subscriber's per-topic buffer is full, the next incoming event drains the oldest buffered event and sends the new one. This is internal state; no wire signal is emitted to the client. The client receives a continuous stream with no errors, but some events may be missed if the subscriber falls too far behind. The adopter is responsible for ensuring fragment rendering is fast enough; if not, consider increasing `SubscriberBufferDepth`.

### Decision 2: FR5 — Per-Topic Baseline Set Encoding

**Encoding**: Pipe-separated `topic:hash` pairs, sorted by topic name.

Format: `topic-a:abc123|topic-b:def456|...`

Properties:
- **ASCII-safe**: Contains only alphanumeric characters, `|`, and `:`.
- **Bounded in length**: Length grows as `(topic count) * 64` (SHA256 hash size in hex). Subject to proxy/ingress header-buffer limits when carried in `Last-Event-ID`.
- **Full baseline per frame**: Every emitted frame's `id:` field carries the entire baseline set, enabling the client to suppress swaps for all topics simultaneously on reconnect. Topics are in sorted order for consistency.

### Decision 3: FR23/FR26/NFR16 — Advertised Client Retry Interval

**Advertised via**: SSE `retry:` field, in milliseconds.

**Validation clause (planner note 19)**: `AdvertisedRetryInterval < 2 * HeartbeatInterval`.

An advertised retry at or above `2 × heartbeat` allows the client-side debounce to swallow the not-live indicator, hiding disconnection. The startup validation in `Config.Validate()` enforces this: if configuration violates it, `NewHub` will panic on validation failure.

**Example**:
- Heartbeat: 30 seconds
- Max advertised retry: < 60 seconds
- Default advertised retry: 5 seconds (safe)

This ensures the client sends a reconnection attempt before the debounce window closes, keeping the connection live.
