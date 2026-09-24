# Omnigent agent bundles

Omnigent-native agent config bundles for this repo — uploadable directly via
`sys_session_create(config_path=...)`. Distinct from
[`tools/project-manager/omnigent-agents/`](../../project-manager/omnigent-agents/README.md),
which holds Omnigent ports of the `project-manager` plugin's Claude Code
personas; this directory is for agents that exist only as Omnigent bundles,
with no corresponding Claude Code plugin persona.

Each subdirectory is a standalone single-agent bundle (`config.yaml`).

## Agents

| Agent | Harness | Model | Purpose |
|-------|---------|-------|---------|
| `local-pi/` | `pi-native` | `locallm/bonsai2` | General-purpose dev agent wired to krill's prod work-axis MCP surface (`krill-mcp-prod`, `/mcp/spec`) |

## Bundle shape

A bundle is a directory holding one `config.yaml` (see `local-pi/config.yaml`
for a worked example, or the ground-truth `claude-native-ui` bundle pulled
via `sys_agent_download` during authoring):

```yaml
spec_version: 1
name: <agent name>
description: >-
  <one paragraph -- when to use this agent>
executor:
  harness: <claude-sdk | claude-native | pi-native | codex-native | ...>
  model: <provider/model-id>       # omit for harnesses with a pinned model
mcp_servers:
  - name: <server name>
    serverUrl: <https url>          # see "Known gaps" below
prompt: |
  <system prompt>
os_env:
  type: caller_process
  cwd: .
  sandbox:
    type: none
```

`omnigent run --harness --help` lists the valid `harness` values; run
`omnigent host --help` / `omnigent config list` locally to see which
harnesses have credentials configured on this machine.

## Building and testing a new agent locally

1. Write `tools/omnigent/agents/<name>/config.yaml` following the shape
   above.
2. Test it against a local server, without touching the shared prod
   server:
   ```sh
   omnigent run tools/omnigent/agents/<name>          # AGENT may be a directory or a YAML file
   omnigent run tools/omnigent/agents/<name> --server local
   ```
3. Iterate — `omnigent run` re-reads the YAML each launch, so no
   upload/re-upload step is needed while testing locally.
4. From inside a running Omnigent session (e.g. this one), you can instead
   upload straight into the current server with the `sys_session_create`
   MCP tool:
   ```
   sys_session_create(config_path="tools/omnigent/agents/<name>")
   ```
   then verify with `sys_agent_get` (confirms `mcp_servers`/`harness`
   resolved as written) and `sys_list_models` (confirms the `model` string
   resolves for that harness).

## Deploying to the shared server

The shared server for this repo's agents is `https://omnigent.whalenet.dev`
(`~/.omnigent/config.yaml`'s configured default — check with `omnigent
config list`).

- **Ad hoc / ephemeral:** `omnigent run tools/omnigent/agents/<name>
  --server https://omnigent.whalenet.dev` uploads the local YAML as a
  one-off agent and spawns a local runner tunneling to the server, so any
  `os_env: caller_process` terminals/MCPs still execute on your machine.
- **Persistent registration** (so the agent shows up in `sys_agent_list`
  for others to launch by `agent_id`): either upload it via
  `sys_session_create(config_path=...)` from a live session against that
  server (as in "Building and testing" above), or have whoever operates the
  server add `--agent tools/omnigent/agents/<name>` to its `omnigent
  server` startup command (`omnigent server --help`) — pre-registers the
  bundle at boot, replacing any existing agent of the same name.
- **If the bundle needs to run terminals/MCPs on a specific machine**
  (anything with `os_env: caller_process`, like every bundle in this repo),
  that machine must first be registered as a host against the target
  server: `omnigent host https://omnigent.whalenet.dev`, or `omnigent host
  enable` for a persistent per-user system service (`omnigent host
  --help`).

## Reference docs

This repo vendors no separate Omnigent documentation — the CLI's own
`--help` text is the authoritative, version-matched reference for whatever
`omnigent` build is installed here (`omnigent --version`):

- `omnigent --help` — top-level command/harness list
- `omnigent run --help` — local vs. `--server` launch topologies (its own
  docstring cites "RUNNER.md §6 Flow 1" for the local-runner/remote-server
  architecture — that doc isn't vendored into this repo or shipped with the
  installed package, so it can't be linked from here; ask in the Omnigent
  support channel if you need it)
- `omnigent host --help` — registering/serving a machine as a host
- `omnigent server --help` — running/deploying the server itself
- `omnigent config --help` / `omnigent config list` — defaults and
  configured credentials by harness
- `omnigent doctor --help`, `omnigent diagnose --help` — environment/health
  checks for bug reports

## Known gaps

- **`spec_version: 1` is required** — confirmed live: `omnigent run` rejects
  a bundle with `Error: config.yaml missing required field: spec_version`
  otherwise. Every bundle here must set it.
- **MCP-server wiring syntax is best-effort, not confirmed.** Each bundle's
  `mcp_servers:` key is modeled on `tools/project-manager/mcp_config.json`'s
  server-entry shape (`name` + `serverUrl`). `sys_agent_get` confirms the
  server recognizes a top-level `mcp_servers` field on an agent, but no
  populated example was available to inspect while authoring these bundles
  — every live agent checked (`claude-native-ui`, `polly`) had an empty
  list. Confirm the field/key names resolve as expected after uploading via
  `sys_session_create(config_path=...)`, by checking `sys_agent_get` on the
  resulting session, before relying on it.
- **Model string format** (`provider/model-id`, e.g. `locallm/bonsai2`) is
  confirmed from `sys_session_create`'s own `model` parameter description
  and mirrors `whagent_net/config/agents.yaml`'s `model:` convention, but
  `locallm` as a provider id isn't otherwise referenced in this repo —
  confirm it resolves via `sys_list_models` after upload.
