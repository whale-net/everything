# LeafLab — Environment Variables

> Read this when configuring, deploying, or debugging the LeafLab domain as a whole. Per-component variables (auth, session, listen addresses) live in each component's own `ENV.md` — see `api/ENV.md`, `migrate/ENV.md`, `ui/ENV.md`.

## Postgres MCP (Claude Code plugin)

`.mcp.json` at the plugin root (`leaflab/plugin/data/.mcp.json`, symlinked to
`.agents/plugins/leaflab-data` — see `.claude-plugin/marketplace.json`) wires
up three read-restricted (`--access-mode=restricted`) crystaldba
`postgres-mcp` servers via `uvx`, one per environment, following
`tools/app_registry`'s and `audience_score_system/plugin/data`'s identical
plugin pattern:

| Server | Connection |
|---|---|
| `leaflab-pg-tilt` | Hardcoded to the local default (`postgres://postgres:password@localhost:5432/leaflab`) — not a secret |
| `leaflab-pg-dev` | `LEAFLAB_DEV_DATABASE_URI` (shell env var, not set by default) |
| `leaflab-pg-prod` | `LEAFLAB_PROD_DATABASE_URI` (shell env var, not set by default) |

This replaces the old ad-hoc `postgres-leaflab` entry that used to live
directly in the repo-root `.mcp.json` (tilt only, no dev/prod split).
