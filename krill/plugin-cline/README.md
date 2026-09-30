# krill plugin-cline

Cline port of the `krill-design` and `krill-work` Claude Code plugins in
`krill/plugin/`. The Claude plugins remain the source of truth and are
untouched; this tree is a parallel, best-effort conversion produced by
`convert.py` in this directory (re-run it against `krill/plugin/` when that
tree changes, rather than hand-editing here).

## Mapping

| Claude plugin | Cline equivalent |
|---|---|
| `design/agents/*.md`, `work/agents/*.md`, `shared/agents/help.md` | one custom mode per persona in `design/.roomodes` / `work/.roomodes` (`slug: krill-<persona>`; `roleDefinition` = description + body, with the persona's MCP servers named in prose) |
| `design/skills/*/SKILL.md`, `work/skills/*/SKILL.md`, `shared/skills/{status,help}/SKILL.md` | `workflows/<skill>.md` under each plugin (shared skills copied into both) |
| `.mcp.json` | `mcp.json` — Cline MCP settings (`streamableHttp`, same URLs/headers, `alwaysAllow: []`) |
| `shared/CONVENTIONS.md` | `shared/CONVENTIONS.md` (copied, references rewritten) |
| `@shared/snippets/...` includes | inlined as a "Task lifecycle blocker (from shared/snippets)" section |

## Install

1. Copy the entries from `design/.roomodes` / `work/.roomodes` into your
   workspace `.roomodes` (or use the file as-is if your setup reads it).
2. Import the servers from `design/mcp.json` / `work/mcp.json` into Cline's
   MCP settings.
3. Workflows are plain markdown — point Cline at
   `workflows/<name>.md` (e.g. via `.clinerules/workflows/` or by pasting the
   file into context) when a persona tells you to run a workflow.

Be aware Cline has no plugin marketplace or skill auto-discovery; these
conventions are best-effort.

## Known limitations

- No per-mode MCP tool allowlists — every mode can call every configured MCP
  server; each persona's servers are noted in prose in its `roleDefinition`.
- No `@`-includes — shared snippets are inlined verbatim at conversion time.
- No skill auto-discovery — workflows must be invoked explicitly.
