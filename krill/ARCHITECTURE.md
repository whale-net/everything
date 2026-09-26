# krill — Architecture

This document covers what exists after M1's domain scaffolding (issue
#2487), spec entity model (issue #2488), scoped-slice query (issue
#2491), the markdown importer (issue #2492), the MCP spec surface (issue
#2494), and the doc renderer (issue #2495). See
[`PRODUCT.md`](PRODUCT.md) for vision, personas, load-bearing decisions,
and the milestone roadmap that drives what gets built next.

This doc is split into one file per topic under
[`ARCHITECTURE/`](ARCHITECTURE/) — every file there is real, current
design. Jump straight to the file you need rather than reading serially;
filenames are numbered in roughly chronological/milestone order, but the
number is not load-bearing — grep the table below, or `ARCHITECTURE/`
itself, for the topic you need. Each file's own `# ` title is the exact
heading text this section used to carry, so a citation elsewhere in the
repo that names a section (e.g. `ARCHITECTURE.md "The work axis (M4)"`)
stays discoverable by grepping the directory even before it's updated to
the new path.

## Component map (as of this task)

```
                 ┌───────────┐
   Postgres  ◄───│  migrate  │  job: applies schema, seeds `scope` (LB1/NFR2)
   (scope)   ▲    └───────────┘
        │    │
   ┌────┴────┬────────────┬────────────┐
   │   api   │    mcp     │     ui     │
   └─────────┴────────────┴────────────┘
        ▲            ▲            ▲
        │            │            │
        │            │       external-api: Keycloak sign-in plus the
        │            │       operator nav shell behind it; mounts auth's
        │            │       /authorize, /token, /register, and
        │            │       discovery -- the SignInURL mcp's auth front
        │            │       door redirects to
        │            │
        │       external-api: the FR10/NFR1 spec surface -- /mcp/spec,
        │       the same FR5-FR9 slice query as `api`'s /slices/...
        │       routes, over MCP
        │
   external-api: /healthz, /sessions/init, the M1 entity write API (issue
   #2490 — create/attach only), and the FR5-FR9 scoped-slice query surface
   (issue #2491, read-only, also reachable via `api`'s own HTTP routes)
```

`migrate`, `api`, `mcp`, and `ui` each get their own Postgres connection
(`PG_DATABASE_URL`, `//libs/go/db` / `//libs/go/migrate` — see `ENV.md`).
`ui` additionally owns the `ui_sessions` table (migration 007) and, jointly
with `mcp`, the `mcp_credential`/`mcp_oauth_client`/`mcp_auth_code` tables
(migration 006) -- see "krill/ui and the auth front door" below.
`krill/plugin/` now carries `mcp`'s Claude Code plugin entries (`.mcp.json`
/ `mcp_config.json`, issue #2494) rather than being a placeholder, plus a
companion `plugin/data/` "-data" plugin for direct Postgres access to the
same database (see README.md "Claude Code plugin"), and
`//krill:krill_chart` bundles all three binaries.

`krill/store` (issue #2488) is the pgx-based repository over migration
002's spec tables — a library, not a binary, so it does not appear in the
component map above. `api` imports it as of issue #2490
(`krill/api/routes.go`'s `store.New(pool)`), behind the entity create/
attach handlers described in "`init` and the write gate" below.
`krill/slice` (issue #2491) is the query layer built on top of `krill/store`,
and `krill/api/handlers` is the HTTP surface over `krill/slice` — both
also wired into `api` via `krill/api/routes.go` (see "The scoped-slice
query" section below).

## Topics

| File | Read it for |
|---|---|
| [`ARCHITECTURE/01-spec-entity-model.md`](ARCHITECTURE/01-spec-entity-model.md) | The spec entity model (LB2/LB3, issue #2488) — surrogate id vs. revision id, no stored display number, single-parent FK |
| [`ARCHITECTURE/02-capability-map-entries.md`](ARCHITECTURE/02-capability-map-entries.md) | Capability map entries, personas, and non-goals (issue #2488) — why `Cn` maps onto `Feature`, not a fourth table |
| [`ARCHITECTURE/03-scope-table.md`](ARCHITECTURE/03-scope-table.md) | The `scope` table (LB1) — this repo's forge coordinates, and why the indirection |
| [`ARCHITECTURE/04-migration-numbering-m1.md`](ARCHITECTURE/04-migration-numbering-m1.md) | Migration numbering (M1), versions 001-007 |
| [`ARCHITECTURE/05-migration-numbering-m2.md`](ARCHITECTURE/05-migration-numbering-m2.md) | Migration numbering (M2), versions 008-009 |
| [`ARCHITECTURE/06-migration-numbering-m3.md`](ARCHITECTURE/06-migration-numbering-m3.md) | Migration numbering (M3), versions 010-014 |
| [`ARCHITECTURE/07-m3-delivery-axis.md`](ARCHITECTURE/07-m3-delivery-axis.md) | M3's delivery axis, end to end (issue #2690) — the map across all five M3 migrations |
| [`ARCHITECTURE/08-backlog-bucket.md`](ARCHITECTURE/08-backlog-bucket.md) | The backlog bucket vs. the product's `Later` capability bucket (FR5, FR6, issue #2687) |
| [`ARCHITECTURE/09-abandon-verb.md`](ARCHITECTURE/09-abandon-verb.md) | The abandon verb (FR6, issue #2688) — cascade, atomicity, not reversible |
| [`ARCHITECTURE/10-milestone-authoring-schema.md`](ARCHITECTURE/10-milestone-authoring-schema.md) | The milestone authoring schema (FR1, FR2, LB6, issue #2683) |
| [`ARCHITECTURE/11-design-session-vs-krill-session.md`](ARCHITECTURE/11-design-session-vs-krill-session.md) | `design_session` vs `krill_session` (FR1, FR8, #2542) — the most confusable pair in M2 |
| [`ARCHITECTURE/12-design-session-revision-event-http.md`](ARCHITECTURE/12-design-session-revision-event-http.md) | The DesignSession/RevisionEvent HTTP surface (FR1-FR4, FR8, issue #2543) |
| [`ARCHITECTURE/13-derived-open-question-view.md`](ARCHITECTURE/13-derived-open-question-view.md) | The derived open-question view (FR6, FR7, issue #2545) — a window query, never a second table |
| [`ARCHITECTURE/14-mediated-intake.md`](ARCHITECTURE/14-mediated-intake.md) | Mediated intake (FR9, FR10, NFR2, issue #2546) — an Agent proposing entities on a contributor's behalf |
| [`ARCHITECTURE/15-krill-session-two-session-ids.md`](ARCHITECTURE/15-krill-session-two-session-ids.md) | `krill_session` and the two session ids (FR3, #2489) |
| [`ARCHITECTURE/16-init-and-write-gate.md`](ARCHITECTURE/16-init-and-write-gate.md) | `init` and the write gate (FR3, #2489) — `RequireSession`, and which write paths it covers |
| [`ARCHITECTURE/17-scoped-slice-query.md`](ARCHITECTURE/17-scoped-slice-query.md) | The scoped-slice query (LB7, issue #2491) — `slice.Document`'s five granularities |
| [`ARCHITECTURE/18-mcp-spec-surface.md`](ARCHITECTURE/18-mcp-spec-surface.md) | The MCP spec surface (FR10/NFR1, issue #2494) — the two-front-door pattern, `/mcp/spec` |
| [`ARCHITECTURE/19-design-session-mcp-surface.md`](ARCHITECTURE/19-design-session-mcp-surface.md) | The design-session MCP surface (FR1-FR10 over MCP, NFR4, issue #2547) — `/mcp/design`, `RegisterWrite` |
| [`ARCHITECTURE/20-krill-ui-mcpauth-front-door.md`](ARCHITECTURE/20-krill-ui-mcpauth-front-door.md) | krill/ui and the auth front door (the auth-flow gap) |
| [`ARCHITECTURE/21-markdown-importer.md`](ARCHITECTURE/21-markdown-importer.md) | The markdown importer and the delivery-axis association (FR16, FR17, issue #2492), plus one-time import completion (M2's FR12) and completeness accounting / the whagent_net import (M2's FR11) |
| [`ARCHITECTURE/22-amend-as-of-history-reads.md`](ARCHITECTURE/22-amend-as-of-history-reads.md) | Amend and as-of history reads (FR11, FR12, issue #2493) — the SCD2 close-and-open write, as-of slice assembly |
| [`ARCHITECTURE/23-doc-renderer.md`](ARCHITECTURE/23-doc-renderer.md) | The doc renderer (FR13-FR15, NFR3, issue #2495) — citations computed at render time, the generated-doc carve-out |
| [`ARCHITECTURE/24-github-pointer-artifact.md`](ARCHITECTURE/24-github-pointer-artifact.md) | The GitHub pointer artifact (FR20, C9, issue #2496) — one issue per Product |
| [`ARCHITECTURE/25-design-skill-live-milestone-read.md`](ARCHITECTURE/25-design-skill-live-milestone-read.md) | The design skill's live milestone read (FR21, root plan issue #2485) |
| [`ARCHITECTURE/26-work-axis-task-creation.md`](ARCHITECTURE/26-work-axis-task-creation.md) | The work axis: task creation (FR1, NFR1/NFR3/NFR5/NFR6/NFR7, issue #2719) |
| [`ARCHITECTURE/27-task-payload-document.md`](ARCHITECTURE/27-task-payload-document.md) | The task payload document (FR4, FR10, NFR4, issue #2721) — `work.Payload`, `GetMilestoneDeliversSlice` |
| [`ARCHITECTURE/28-work-axis-m4.md`](ARCHITECTURE/28-work-axis-m4.md) | The work axis (M4): task, claim, lease, attempt, note (root plan issue #2717, conformance issue #2728) — the whole-milestone view |
| [`ARCHITECTURE/29-open-items.md`](ARCHITECTURE/29-open-items.md) | Open items — what's still unwired or open as of the most recent milestone |
| [`ARCHITECTURE/30-operator-release-and-manual-escalate.md`](ARCHITECTURE/30-operator-release-and-manual-escalate.md) | Operator `release` and manual `escalate` (FR8, FR9, issue #2872) — force-close, attempt accounting, and the already-escalated design choice |
| [`ARCHITECTURE/31-escalation-console-m5.md`](ARCHITECTURE/31-escalation-console-m5.md) | The escalation/intervention/console axis (M5, root plan issue #2851, conformance issue #2877) — the whole-milestone view: component map addendum, the escalation state machine, the claimability predicate, the `/mcp/ops` mount, the console paging contract, both design-ambiguity resolutions, and the known `ListClaimedTasks` defect |
| [`ARCHITECTURE/32-ui-write-identity.md`](ARCHITECTURE/32-ui-write-identity.md) | The signed-in operator's identity on `ui`'s own write path (LB4) — Keycloak session → real `(iss, sub)` → `writeClient` → `POST /sessions/init` → `X-Krill-Session-Id` |
| [`ARCHITECTURE/33-scd2-amend-all-spec-kinds.md`](ARCHITECTURE/33-scd2-amend-all-spec-kinds.md) | SCD2 amend across every spec-axis kind (FR 8b2e87d1, FR f0f6bc18, FR 39373553, FR b2767a89) — the shared `supersede` body, the reparent/re-kind refusal, sibling-name uniqueness, and migration 020's SCD2 `milestone_ref` |
| [`ARCHITECTURE/34-single-delivery-parent.md`](ARCHITECTURE/34-single-delivery-parent.md) | One delivery parent, batched — why the Now/Next/Later FeatureSet names are cosmetic, the batched `add_delivers`, and the competing-milestone refusal that makes `move_delivery_scope` the re-cut |
