# pi_console — Environment Variables

Both binaries read config from environment variables only; there are no
config files.

## `bridge` (runs on each host with `pi` installed)

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8787` | Port the bridge HTTP/SSE API listens on. |
| `PI_CONSOLE_PI_BIN` | `pi` | Path to the `pi` binary to spawn. |
| `PI_CONSOLE_PI_ARGS` | *(empty)* | Extra space-separated args appended to every `pi --mode rpc --no-session` invocation (e.g. `--session-dir /var/lib/pi_console`). |
| `PI_CONSOLE_BRIDGE_TOKEN` | *(empty)* | Shared bearer token required on every request except `/healthz`. **Strongly recommended** — see the Security section in [README.md](README.md); the bridge grants shell access to `pi`'s tools if left unset, and logs a warning when it is. |

## `ui` (the single front end you open in a browser)

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | Port the UI HTTP server listens on. |
| `PI_CONSOLE_HOSTS` | *(required)* | Comma-separated `name=baseURL` pairs, e.g. `PI_CONSOLE_HOSTS="devbox=http://devbox:8787,laptop=http://localhost:8787"`. The UI refuses to start with no hosts configured. |
| `PI_CONSOLE_BRIDGE_TOKEN` | *(empty)* | Bearer token sent to every configured bridge. Must match that bridge's own `PI_CONSOLE_BRIDGE_TOKEN`; all bridges currently share one token. |
