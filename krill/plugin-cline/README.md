# krill plugin-cline

Cline CLI port of the `krill-design` and `krill-work` Claude Code plugins in
`krill/plugin/`. The Claude plugins remain the source of truth and are
untouched; this tree is a parallel, best-effort conversion produced by
`convert.py` in this directory (re-run it against `krill/plugin/` when that
tree changes, rather than hand-editing here). Targets the Cline CLI's native
discovery paths (verified against the CLI binary's own path resolution):
agents in `<workspace>/.cline/agents/`, workflows in
`<workspace>/.clinerules/workflows/`, and MCP servers in
`~/.cline/data/settings/cline_mcp_settings.json`.

## Mapping

| Claude plugin | Cline CLI equivalent |
|---|---|
| `design/agents/*.md`, `work/agents/*.md`, `shared/agents/help.md` | one agent file per persona in `design/agents/krill-<persona>.md` / `work/agents/krill-<persona>.md` (YAML frontmatter `name`/`description`, prompt body = roleDefinition; the persona's MCP servers named in prose) |
| `design/skills/*/SKILL.md`, `work/skills/*/SKILL.md`, `shared/skills/{status,help}/SKILL.md` | `workflows/<skill>.md` under each plugin (shared skills copied into both) |
| `.mcp.json` | `mcp.json` — Cline MCP settings shape (`streamableHttp`, same URLs/headers, `alwaysAllow: []`), ready to merge into `cline_mcp_settings.json` |
| `shared/CONVENTIONS.md` | `shared/CONVENTIONS.md` (copied, references rewritten) |
| `@shared/snippets/...` includes | inlined as a "Task lifecycle blocker (from shared/snippets)" section |

Note: the earlier `.roomodes` output of this converter is gone — the Cline
CLI does not read `.roomodes` (that is a VS Code extension file); agent
markdown files are the CLI's native format.

## Install

1. Copy or symlink each persona into your workspace's agent dir:
   `krill/plugin-cline/{design,work}/agents/krill-*.md` →
   `<workspace>/.cline/agents/` (or `~/.cline/data/settings/agents/` to
   install globally). The `krill-help` agent exists in both plugins; one
   copy is enough.
2. Merge the servers from `design/mcp.json` / `work/mcp.json` into Cline's
   MCP settings at `~/.cline/data/settings/cline_mcp_settings.json` (the
   three `krill-mcp-tilt`/`-dev`/`-prod` spec servers are shared between the
   two files — dedupe when merging).
3. Workflows are plain markdown — copy or symlink them into
   `<workspace>/.clinerules/workflows/` (or `.cline/workflows/`) and point
   Cline at `workflows/<name>.md` when an agent tells you to run one.

Be aware Cline has no plugin marketplace or skill auto-discovery; these
conventions are best-effort.

## Known limitations

- No per-agent tool allowlists (the Claude plugins' `groups` have no CLI
  equivalent) — every agent can call every configured MCP server; each
  persona's servers are noted in prose in its prompt body.
- No `@`-includes — shared snippets are inlined verbatim at conversion time.
- No skill auto-discovery — workflows must be invoked explicitly.
