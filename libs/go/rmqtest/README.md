# rmqtest

A reusable helper that starts a real RabbitMQ broker (via
[testcontainers-go](https://github.com/testcontainers/testcontainers-go)) and hands back a ready
`*rmq.Connection`, for tests that need to exercise actual AMQP fanout semantics — broadcast
delivery to every bound queue, not a competing-consumer work queue — which an in-process fake
cannot verify.

Mirrors `libs/go/dbtest`: one RabbitMQ container is shared across every test in a test binary
(process), started lazily on first use. Deliberately kept out of `bazel test //...` (Bazel
targets using this package should be tagged `manual`, `integration`, `no-sandbox`,
`requires-network`, matching `libs/go/dbtest`'s targets) so a Docker-less machine stays green.

## Usage

```go
func TestSomethingThatNeedsRealFanout(t *testing.T) {
    ctx := context.Background()
    conn := rmqtest.NewConnection(ctx, t)
    // conn is a *rmq.Connection to a shared broker; use a unique exchange/queue
    // name per test so concurrent tests don't collide.
}
```

`NewConnection` fails the test immediately (`t.Fatalf`) on any setup error. `t.Cleanup` closes
the connection; there is nothing to close manually. The shared container itself is never
terminated by `NewConnection` — it is reused by every test in the process and left for Docker's
Ryuk reaper to clean up after the process exits.

Run a target that uses this package explicitly, e.g.:

```
bazel test //leaflab/broadcast:broadcast_integration_test --test_tag_filters=-manual --test_output=all
```
