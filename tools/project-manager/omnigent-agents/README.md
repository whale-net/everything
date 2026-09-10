# Omnigent-ported project-manager agent bundles

This directory holds Omnigent-flavored copies of the `project-manager` plugin's
persona specs (`tools/project-manager/agents/*.md`), ported for manual upload
into Omnigent for testing. They are **not** used by the Claude Code plugin
itself — the plugin still reads `agents/*.md` directly.

Each subdirectory is a standalone single-agent bundle (`config.yaml`) uploadable
via `sys_session_create(config_path=...)`. None of these personas dispatch each
other directly — the plugin's skills orchestrate that externally — so no bundle
has a `tools.agents:` sub-agent list.

## Known gaps vs. the source specs

- **No confirmed Omnigent MCP-server wiring syntax.** `mcp__tilt-mcp__*`
  (system-validator's live Tilt inspection) and `mcp__agentsync-mcp__*`
  (architect's and producer's live cross-persona Discussion sync) were not
  ported. Each affected `config.yaml` notes this at the top of its `prompt:`.
- **No WebSearch equivalent** — omitted from producer's bundle.
- **No TaskCreate/TaskUpdate/TaskList equivalent** — omitted from
  project-manager's bundle; it's instructed to track todos in prose instead.

## Status

These are testing artifacts. `tools/project-manager/agents/*.md` remains the
source of truth; this directory is expected to drift from it over time and is
not a maintained doc.
