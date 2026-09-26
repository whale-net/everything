# `libs/go/monty` — Go client for the Monty sandbox (design notes)

Research write-up for a Go binding to [Monty](https://github.com/pydantic/monty), Pydantic's
Python 3.14 sandbox. Two transports are wanted: a local worker subprocess, and a remote
`monty-server` ("Full Monty") over WebSocket.

Status: **research only** — no code written. When this becomes a real library it should be
replaced by a `README.md` + `ARCHITECTURE.md` pair, and get a row in [`libs/TOC.md`](../../TOC.md).

## 1. Why a Go binding is needed

Monty ships host bindings for Python (`pydantic-monty`), TypeScript (`@pydantic/monty`) and
Rust (`monty-pool`). There is no Go binding. The wire protocol is deliberately public and
language-neutral:

> "The protocol is protobuf (rather than Monty's internal CBOR dump format) so a parent or
> child can be implemented in any language." — `crates/monty-proto/README.md`

The schema is one checked-in file, MIT licensed:
`crates/monty-proto/proto/monty/v1/monty.proto` (1059 lines, 62 messages, 4 enums).

## 2. Protocol facts a Go client must implement

| Fact | Value |
|------|-------|
| Wire format | protobuf (`package monty.v1`) |
| stdio framing | 4-byte **little-endian** length prefix + body |
| WebSocket framing | **one binary WS message per protobuf body, no length prefix** |
| Protocol version | `PROTOCOL_VERSION = 5`, `MIN_SUPPORTED = 5` (single-version window) |
| Worker argv | exactly `<binary> subprocess` — no flags, empty env, stdin/stdout piped, stderr inherited |
| Binary lookup | explicit path → `$MONTY_BIN` → bundled package → `PATH` |
| Max frame | 256 MiB (`MAX_FRAME_LEN`); 1 GiB cumulative decode budget per frame |
| Auth | **none** — plain `ws://`, TLS terminated at an ingress |

`scripts/websocket_relay.py` (MIT, in the Monty repo) is the reference bridge and the single
best artifact for a Go implementer: it shows that the *only* difference between the two
transports is the length prefix. It is also the local test harness (see §6).

### Session state machine

Strict alternation, no multiplexing: the parent writes exactly one `ParentRequest`, the child
replies with zero or more streamed `Print` events followed by exactly one turn-ender. A Go
client can therefore use a single blocking read loop, but must be **cancel-safe** — keep the
partial-frame buffer in the connection (or run a reader goroutine, as the Rust WS transport
does) so a cancelled `context` doesn't lose protocol state.

`ParentRequest`: `configure`, `install_dependencies`, `feed`, `resume_call`,
`resume_name_lookup`, `resume_futures`, `dump`, `load`, `reset`, `shutdown`, `abort_feed`.

`ChildEvent` turn-enders: `complete`, `error`, `typing_error`, `fatal_error`, `ok`,
`dump_result`, `shutdown_dump`. Suspensions: `function_call`, `os_call`, `name_lookup`,
`resolve_futures`.

Handshake: none at spawn. The first request on a fresh worker is `Configure` (which carries
`protocol_version`); the only accepted reply is `Ok`. A mismatched version comes back as
`FatalError` naming the child's supported range, then exit code 4. `EOF` at a frame boundary
means the child crashed — there is no `FatalError` in that case.

### Worker lifecycle differs by transport

- **Subprocess**: pooled and reused. Session end → `Reset` → `Ok` → worker returns to the
  idle queue. Crash → kill, reap, replace.
- **WebSocket**: single-use. Session end → WebSocket close frame, no `Reset`, never reused.
  Drain is signalled by a `ShutdownDump` turn-ender that "did NOT run the request"; against a
  server with a session store the client redials, `Load`s the named state, and re-sends.

## 3. Package layout

Follows the `libs/go/grpcauth` interface+impl split and the `libs/go/argocd` client shape.

```
libs/go/monty/
  client.go       # Config, Limits, Pool, Checkout, the Configure handshake
  session.go      # the turn state machine: Run, suspensions, print fan-out
  transport.go    # Conn interface + EventStream (the cancel-safe read side)
  local.go        # subprocess transport
  frame.go        # 4-byte LE length-prefix framing
  arena.go        # Arena encode/decode: Go <-> monty.v1.MontyNode
  value.go        # the Python-value mapping (Tuple, Set, Dict, ...)
  oscall.go       # OS-suspension -> Call, plus small wire helpers
  errors.go       # sentinels, PythonError, IsTransient
  remote/         # websocket transport -- its own go_library, so the core
                  # package never depends on a WebSocket library
  protos/         # monty.proto, vendored
```

**Transport seam** — the Rust bindings use `enum WorkerKind` rather than a trait. In Go the
seam ended up being an exported `Conn` plus a `Config.NewConn` hook rather than two
subpackages. That is what lets `remote/` supply a WebSocket transport without the core
package depending on a WebSocket library, and it keeps framing out of the public API: a
caller supplies a whole `Conn`, never a frame.

```go
type transport interface {
    send(*pb.ParentRequest) error
    recv() (*pb.ChildEvent, error)  // nil, nil on clean EOF at a frame boundary
    close() error
    isDead() bool
}
```

Everything above that seam — checkout state machine, suspension counting, feed/turn
deadline backstops, print fan-out, cancellation contract — is transport-independent and
identical for both modes. That is the whole point of the `Transport` field on `Config`.

`Config` as built:

```go
type Config struct {
    NewConn  NewConnFunc   // nil => the built-in subprocess transport
    Binary   string        // subprocess: the monty binary path
    ScriptName string
    Limits   Limits        // memory / feed duration / recursion depth / suspensions
    TypeCheck bool
    PrintFlushInterval time.Duration
    MaxWorkers int
    CheckoutTimeout time.Duration
}
```

## 4. The value codec is the real work

Python values cross as **one flat `Arena` per message**: post-order node list, containers hold
child *indexes*, roots named by index (so an object passed under two names is one node).
Go's `protoc-gen-go` handles the schema fine — the work is a hand-written
`func decodeArena(*pb.Arena) (any, error)` / `encodeArena` pair, because:

- dicts are `NodePairs`, not proto maps (insertion order + arbitrary hashable keys);
- `repr` and `cycle` are **output-only** and must be rejected as inputs;
- every index must be validated before use — the child is untrusted input;
- the mapping is Python-typed, so a `map[string]any` loses int/float/bigint/bytes distinction.
  Decide up front whether `any` is acceptable or whether to define an explicit
  `monty.Value` sum type mirroring `MontyNode`'s 30 arms.

As built, v1 exposes exactly that subset — nil/bool/int64/bigint/float/string/bytes/list/
tuple/set/dict — and returns `*UnsupportedValueError` for every other arm rather than
shipping a half-correct 30-arm codec. The remaining arms (`datetime`, `pathlib.Path`, file
handles, host class instances) are a follow-on.

## 5. Bazel / dependency wiring

Protobuf generation is already solved in this repo — `whagent_net/protos/BUILD.bazel` uses
`go_grpc_library` from `//tools/bazel:grpc.bzl`. Monty needs messages only, no service, so a
plain `go_proto_library` is enough; vendor the `.proto` into
`libs/go/monty/protos/monty.proto` (MIT, with attribution) rather than depending on an
external git repo, so the wire contract is reviewable in-tree.

WebSocket client: `github.com/gorilla/websocket v1.5.3` is **already in the root `go.mod` as an
indirect dep** — promoting it to direct needs no new module. Add it with the rules_go wrapper,
which forces the direct marking that keeps it out of the `bazel mod tidy` strip hazard
(documented in `docs/GO_DEPENDENCIES.md`):

```
bazel run @rules_go//go -- get github.com/gorilla/websocket@v1.5.3
bazel run //:gazelle
bazel mod tidy
# then the vendor-sync skill
```

Deps land in `libs/go/monty/BUILD.bazel` as `@com_github_gorilla_websocket//:websocket`.
Remember: `srcs` is an explicit list, never a glob — new `.go` files need `bazel run //:gazelle`.

## 6. Testing strategy

- **Unit** — codec round-trips against a golden `Arena`, and the state machine against a fake
  `transport` that replays scripted `ChildEvent`s. No external process needed. This is where
  most of the confidence should come from.
- **WebSocket integration** — run `scripts/websocket_relay.py` (vendored, MIT) as a subprocess
  on an ephemeral port; it bridges WS → a real `monty subprocess`, so the *remote* path is
  testable end-to-end **without the commercial server**. Follow the `//libs/go/s3:s3_integration_test`
  shape verbatim: `//go:build integration` on line 1, hand-written `go_test` with
  `gotags = ["integration"]` and `tags = ["external", "integration", "manual", "no-cache",
  "no-sandbox", "requires-network"]`, marked `# keep` so gazelle doesn't delete it. It does
  not depend on `//libs/go/dbtest`, so it must be added to the explicit union in the
  `test-database` job in `.github/workflows/ci.yml`.
- **Subprocess integration** — same target, `MONTY_BIN` pointed at the binary from the
  `pydantic-monty-runtime` wheel (1.0.0, manylinux x86_64/aarch64/musllinux prebuilt).

## 7. Risks / open questions

1. **The commercial server is the only real "monty server".** It is a private container image
   (`key.json` service account), currently `linux/amd64` only, and it has **no authentication**.
   The Go client can be written and fully tested against the OSS relay, but end-to-end
   validation against Full Monty needs Pydantic to grant image access.
2. **Protocol version is a single-version window (5..=5).** There is no negotiation. Any
   Monty upgrade that bumps `PROTOCOL_VERSION` without a compat window breaks every deployed
   client at `Configure` time. The vendored `.proto` plus a CI job that diffs it against
   upstream `main` is the mitigation.
3. **Two workers, one session, no multiplexing.** Don't design an API that invites concurrent
   `Feed` on one session — the protocol cannot express it.
4. **Dumps are unauthenticated.** `Load`/`Dump` bytes must be treated as trusted input with
   verified provenance; the docs are explicit that a checksum alongside untrusted bytes is not
   authentication.
5. **Scope.** v1 should be feed + print + error + host-function round-trip. Mounts, OS calls,
   snapshots and `auto_resume` are each substantial and can follow.

## 8. Suggested phasing

1. **Shipped** — vendored `protos` + generated Go types, the `Conn`/`EventStream` seam, the
   subprocess transport, the WebSocket transport, the turn state machine, the value-codec
   subset, and host-function round-trip.
2. **Next** — the remaining value arms: `datetime`, `pathlib.Path`, file handles.
3. **Next** — filesystem mounts, so `Path.read_text` and friends stop raising
   `PermissionError` and a snippet can actually work with files.
4. **Later** — host class instances (method routing by `object_id`), then snapshots
   (`Dump`/`Load`) and `auto_resume` against a session-storing server.
