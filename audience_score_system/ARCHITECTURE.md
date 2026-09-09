# Audience Score System — Architecture

Design record for `//audience_score_system`. Read [`README.md`](README.md)
first for what the system is and how to run it locally, and
[`PRODUCT.md`](PRODUCT.md) for the vision and load-bearing decisions this
architecture implements.

This doc is split into one file per topic under
[`architecture/`](architecture/) — every file there is real, current
design. Jump straight to the file you need rather than reading serially.
Filenames are numbered in the order a first-time reader would want them,
but the number is not load-bearing — grep the table below, or
`architecture/` itself, for the topic you need. Each file's `# ` title is
the exact heading text the section had before this split, so a citation
like `ARCHITECTURE.md "NFR3 interface allocation"` in a code comment stays
discoverable by grepping the directory.

| File | Read it for |
|---|---|
| Language: Go throughout (below, this file) | Why ASS is Go end to end, and the tradeoff accepted for M1 |
| [`architecture/02-mcp-server.md`](architecture/02-mcp-server.md) | The `mcp` binary: SDK choice, YouTube client wiring, caller authentication (mcpauth/OAuth2, plus the parallel whagent-net path, issue #2116/FR12), Channel-scoping + idempotency middleware, statelessness (LB4), observability |
| [`architecture/03-component-map.md`](architecture/03-component-map.md) | The four binaries (`migrate`, `web`, `mcp`, `worker`) and Postgres, one table |
| [`architecture/04-oauth-grants.md`](architecture/04-oauth-grants.md) | C1 sign-in vs. C2 Channel-connect, token storage, needs-reauth lifecycle, schedule creation at connect time |
| [`architecture/05-nfr3-interface-allocation.md`](architecture/05-nfr3-interface-allocation.md) | What's web UI vs. MCP-only, and every amendment as capabilities shipped — start here for "is X reachable from MCP" |
| [`architecture/06-temporal-schedule-upsert-helper.md`](architecture/06-temporal-schedule-upsert-helper.md) | `//libs/go/temporal`'s `UpsertSchedule`, and the interval-pinning bug it fixed |
| [`architecture/07-data-model.md`](architecture/07-data-model.md) | Migrations, table by table, in the order they landed |

## Language: Go throughout

ASS is **Go throughout** — `web`, `mcp`, and `worker` all reuse
`//libs/go/temporal`, `//libs/go/db`, and `//libs/go/migrate`, the same as
`migrate` (this scaffold). This is a deliberate override of the pattern
`product/01-current-state.md` surveyed: every existing MCP server in this
repo (`serial-mcp`, `agentsync-mcp`, `tilt-mcp`) is Python + FastMCP. ASS's
MCP server is the first Go MCP server in this repo — accepted as a known
gap for M1 (no in-repo Go MCP framework precedent to follow), not a
blocker. The tradeoff: one shared language/toolchain across all four
binaries and reuse of the hardened Go Postgres/Temporal libraries, at the
cost of writing the MCP protocol layer without a same-language precedent.
