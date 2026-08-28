# pi_console

A simple HTMX web UI for driving [pi.dev](https://pi.dev) coding agents in
[RPC mode](https://pi.dev/docs/latest/rpc) across multiple hosts.

pi's RPC mode is a JSONL protocol over a single process's stdin/stdout — one
`pi --mode rpc` subprocess is one conversation, with no network surface of
its own. `pi_console` adds that network surface with two small Go binaries:

- **`bridge`** runs on each host that has the `pi` CLI installed. It spawns
  `pi --mode rpc` subprocesses on demand and exposes them over HTTP + SSE.
- **`ui`** is the single HTMX front end you actually open in a browser. It's
  configured with a list of hosts, and proxies chat sessions to whichever
  host's bridge you pick — so one UI can drive agents running on any number
  of machines.

```
 browser  <--HTMX/SSE-->  ui  <--HTTP/SSE-->  bridge (host A)  <--stdio-->  pi --mode rpc
                           |
                           +----HTTP/SSE-->  bridge (host B)  <--stdio-->  pi --mode rpc
```

## Running it

On each host that should be reachable, start the bridge (requires `pi` on
`PATH`, or point `PI_CONSOLE_PI_BIN` at it):

```bash
bazel run //tools/pi_console/bridge -- # or the built binary directly
PI_CONSOLE_BRIDGE_TOKEN=changeme PORT=8787 bazel run //tools/pi_console/bridge
```

Then start the UI, pointing it at every bridge:

```bash
PI_CONSOLE_HOSTS="devbox=http://devbox:8787,laptop=http://localhost:8787" \
PI_CONSOLE_BRIDGE_TOKEN=changeme \
bazel run //tools/pi_console/ui
```

Open `http://localhost:8080`, pick a host, and chat. Each host gets its own
`pi` subprocess (its own conversation); the UI holds no agent state itself,
so a session survives a UI restart as long as the bridge process is still
running (reload the browser at `/chat?host=<name>&id=<session-id>` to
reattach — the bridge replays recent history to newly (re)connected
subscribers).

See [ENV.md](ENV.md) for the full environment variable reference.

## Security

`pi`'s tools (notably `bash`) give it the ability to run arbitrary shell
commands as whatever user runs the bridge. The bridge process is therefore
equivalent to a remote-code-execution endpoint, gated only by the shared
bearer token in `PI_CONSOLE_BRIDGE_TOKEN` (pi's own RPC protocol has no
authentication of its own — see the "Key Design Points" in
[the pi RPC docs](https://pi.dev/docs/latest/rpc)). Treat that token like a
credential, and only run bridges on networks you trust (a VPN or SSH tunnel,
not the open internet). Always set `PI_CONSOLE_BRIDGE_TOKEN` — the bridge
logs a warning and runs unauthenticated if it's left unset.

## Known limitations (MVP scope)

- One `pi` process per session; sessions aren't restarted if a bridge or its
  `pi` subprocess dies.
- Each bridge session keeps only its last ~5000 raw RPC lines in memory for
  reconnect replay — very long-running sessions will lose early history from
  new browser tabs/reloads.
- The UI renders assistant text streaming, tool call start/end, and errors;
  it doesn't yet surface thinking traces, session forking, model switching,
  or the other richer RPC commands (`fork`, `compact`, `set_model`, etc.).
- No multi-user auth on the UI itself — anyone who can reach it can drive
  any configured host's agent.
