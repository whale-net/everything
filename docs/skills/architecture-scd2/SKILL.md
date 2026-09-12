---
name: architecture-scd2
description: Write or review a Slowly Changing Dimension Type 2 (SCD2) history table — the valid_from/valid_to convention, the close-and-open write path, and the current-value/point-in-time query patterns used across this repo (leaflab, app_registry, krill, audience_score_system, whagent_net). Use when adding a migration for a dimension that changes over time and needs a "what was true at time T" read, or when reviewing whether a table should be SCD2 at all.
---

# SCD2 (Slowly Changing Dimensions Type 2)

This is the canonical, harness-neutral source for this repo's SCD2 convention.
It is symlinked into `.claude/skills/architecture-scd2` for Claude Code;
AGENTS.md keeps a short pointer plus the load-bearing decision rules (naming,
the append-only carve-out) inline since many domain docs cite `AGENTS.md §
SCD2` by name — read this skill for the full write/read patterns.

## Column convention

Always `valid_from` / `valid_to` — never synonyms (`assigned_at`/
`unassigned_at`, `start_at`/`end_at`, etc.):

- `valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW()` — when this row became the
  current value
- `valid_to TIMESTAMPTZ` — when it was superseded; `NULL` = still current

## Write path — close and open

```sql
UPDATE <table> SET valid_to = NOW() WHERE <entity_id> = $1 AND valid_to IS NULL;
INSERT INTO <table> (<entity_id>, <data_cols>) VALUES ($1, $2);
```

Both statements run in one transaction.

## Current value

```sql
SELECT * FROM <table> WHERE <entity_id> = $1 AND valid_to IS NULL;
```

Always back this with a partial index:
`CREATE INDEX ON <table>(<entity_id>) WHERE valid_to IS NULL`.

## Value at time T

For joining a fact table to a history table at event time:

```sql
SELECT * FROM <table>
WHERE <entity_id> = $1
  AND valid_from <= $t
  AND (valid_to IS NULL OR valid_to > $t);
-- Example: leaflab's v_sensor_reading_with_plant joins plants active at recorded_at this way.
```

## What is NOT SCD2

Append-only event logs, soft-delete tables, and work queues have different
semantics — do not force them into `valid_from`/`valid_to`. If a table needs
an SCD2-*shaped view* over it, derive one with a window function instead of
adding real `valid_to` writes:

```sql
LEAD(valid_from) OVER (PARTITION BY entity_id ORDER BY version)
```

Worked examples of this carve-out: `leaflab`'s `device_config` (event log)
vs. its `v_board_state_history` view; `app_registry`'s `promotion_sync_event`;
krill's design-session revision log.

## Views

Pre-join SCD2 history tables in `v_` views so downstream consumers
(dashboards, APIs) never replicate the join logic. See `leaflab/` for a
worked example (`v_sensor_reading_with_plant`, `v_board_state_history`).

## Reference

- `leaflab/migrate/schema/migrations/011_scd2_naming.up.sql` +
  `012_views.up.sql` + `013_ownership.up.sql` — the canonical worked example
- `tools/app_registry/migrate/schema/migrations/010_manifest_history.up.sql`
  — a second worked example, including the content-addressed history variant
- `AGENTS.md` § SCD2 — the short, always-loaded version of this convention
- `.claude/skills/data-docs` — documents which tables in a domain are SCD2
  when generating `DATA.md`
