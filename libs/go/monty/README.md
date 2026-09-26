# monty

Go client for the [Monty](https://github.com/pydantic/monty) sandbox — Pydantic's
Python 3.14 interpreter, which runs untrusted Python in a worker process that a
stack overflow or an allocator abort cannot take down with it.

Two transports, one API:

- **local** — a `monty subprocess` child, pooled and reused.
- **remote** — a `monty-server` ("Full Monty") WebSocket, one worker per connection.
  See [`remote/`](remote/).

They speak the same protocol and differ only in framing, so everything above
the connection is shared: a snippet written against a local pool runs unchanged
against a server.

## Usage

```go
pool, err := monty.NewPool(monty.Config{
    Binary: "/usr/local/bin/monty",
    Limits: monty.Limits{
        MaxMemoryBytes:  64 << 20,
        MaxFeedDuration: 10 * time.Second,
    },
})
if err != nil {
    return err
}
defer pool.Close()

ctx := context.Background()
session, err := pool.Checkout(ctx)
if err != nil {
    return err
}
defer session.Close()

res, err := session.Run(ctx, "sum(range(10))", monty.RunOptions{})
if err != nil {
    return err
}
fmt.Println(res.Value) // 45
```

A `Session` is a REPL: globals persist across runs, and a Python exception
ends the turn without ending the session.

```go
session.Run(ctx, "import statistics", monty.RunOptions{}) // -> None
res, _ := session.Run(ctx, "statistics.mean([1, 2, 3])", monty.RunOptions{})
```

### Remote workers

```go
pool, err := monty.NewPool(monty.Config{
    NewConn: remote.Dialer("wss://monty.internal/"),
})
```

The server has no authentication of its own and speaks plain `ws://` by
default — terminate TLS and authenticate at an ingress, and pass whatever that
ingress needs as a handshake header:

```go
remote.Dialer(url, remote.WithHeader("Authorization", "Bearer "+token))
```

Remote workers are single-use: the server hands out one per connection and
never reuses one, so the pool will not try to.

### Host functions

A name the sandbox cannot resolve suspends the turn, and the host answers it.
Returning `monty.ErrNotFound` tells Monty the call has no handler here, which
makes it raise the call's own default — `NameError` for an unknown name,
`PermissionError` naming the path for a filesystem call.

```go
res, err := session.Run(ctx, "triple(5)", monty.RunOptions{
    Host: func(_ context.Context, call *monty.Call) (any, error) {
        if call.Name != "triple" {
            return nil, monty.ErrNotFound
        }
        return call.Args[0].(int64) * 3, nil
    },
})
```

### Values

Python values cross the wire as one flat arena per message. They map onto Go
types as:

| Python | Go |
|---|---|
| `None` | `nil` |
| `bool` | `bool` |
| `int` | `int64`, or `*big.Int` when wider than 64 bits |
| `float` | `float64` |
| `str` | `string` |
| `bytes` | `[]byte` |
| `list` | `[]any` |
| `tuple` | `monty.Tuple` |
| `set` / `frozenset` | `monty.Set` / `monty.FrozenSet` |
| `dict` | `*monty.Dict` — insertion-ordered, arbitrary hashable keys |

Anything outside that set (`datetime`, host class instances, the `repr`
placeholder) decodes to `*monty.UnsupportedValueError` rather than to a lossy
approximation, so a caller can tell "the sandbox returned something I don't
model" from "the sandbox returned `None`".

Inputs go the other way through the same mapping:

```go
session.Run(ctx, "sorted(words)", monty.RunOptions{
    Inputs: map[string]any{"words": []any{"pear", "apple"}},
})
```

## Errors

| Sentinel | Meaning |
|---|---|
| `ErrCrashed` | the worker died. The turn may or may not have run — **not** safe to retry |
| `ErrShutdown` | a server declined the request while draining. It did **not** run — safe to resend |
| `ErrProtocol` | the peer violated the wire contract; the worker is discarded |
| `ErrVersion` | the peer does not serve `monty.ProtocolVersion` |
| `ErrNotFound` | return this from a `HostFunc` to get Monty's default for the call |
| `ErrSuspensionLimit` | a `Run` exceeded its host-call budget |

A Python exception is a `*monty.PythonError` — use `monty.IsPythonError(err)`.
`monty.IsTransient(err)` reports whether a fresh attempt is worth making; it is
true only for `ErrShutdown`.

## Security

- The worker binary decides what your sandbox is. Pass `Config.Binary`
  explicitly rather than letting `$PATH` choose it.
- Set `MaxMemoryBytes` and `MaxFeedDuration`. Unset limits are the worker's
  defaults, not a safe default.
- A remote peer need not be a Monty sandbox at all — it may be plain CPython
  with no limits and full host access, relying on deployment isolation. None of
  the guarantees transfer across that boundary; they become properties of
  whatever is running on the other end.
- `Dump`/`Load` state is not authenticated. Only restore bytes whose provenance
  and integrity you established yourself.

## Protocol

The wire schema is `protos/monty.proto`, vendored verbatim from the Monty
repository (MIT) at `crates/monty-proto/proto/monty/v1/monty.proto`. Monty
publishes it specifically so a parent can be written in any language.

`monty.ProtocolVersion` tracks Monty's `PROTOCOL_VERSION`. Monty does **not**
negotiate: a worker serving a different range answers `Configure` with a
`FatalError` naming it. Re-vendor the `.proto` when upgrading the worker, and
keep its header comment — it carries the protocol rules this client depends on.

## Bazel

```
bazel build //libs/go/monty/...
bazel test  //libs/go/monty/...                       # unit tests, no worker needed
bazel test  //libs/go/monty:monty_integration_test --test_output=all
```

The integration test needs a real worker binary:

```
uv tool install pydantic-monty-runtime     # or: pip install pydantic-monty-runtime
export MONTY_BIN=$(command -v monty)
```

It covers both transports: the local one against a spawned `monty subprocess`,
the remote one through an in-test WebSocket relay that bridges to the same
worker the way `monty-server` does — so the remote path is testable without a
commercial server.

## Not implemented yet

Host class instances and method routing, filesystem mounts, snapshots
(`Dump`/`Load`), and session persistence across a server drain.
