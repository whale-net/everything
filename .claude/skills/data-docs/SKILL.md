---
name: data-docs
description: Regenerate or refresh a domain's SQL table/view reference doc (DATA.md) from its migrations, ARCHITECTURE.md, and README. Explains what each table/view is for and how to query it. Run manually and periodically to keep a domain's data documentation in sync with its migrations.
---

# Data Docs — Per-Domain SQL Table Reference

Produces or refreshes `<domain>/DATA.md`: a table-by-table, view-by-view
reference explaining what each piece of a domain's schema is for and how to
query it, derived from that domain's SQL migrations plus its existing docs
(`ARCHITECTURE.md`, `README.md`, `ENV.md`).

This is a **manual, periodic maintenance task** — run it after a batch of
schema-changing migrations lands, not automatically as part of every change.
`leaflab/DATA.md` is the canonical worked example; match its structure.

## Usage

```
/data-docs <domain>          # e.g. /data-docs leaflab
/data-docs <domain>/<subdir> # e.g. /data-docs tools/app_registry
```

If no domain is given, ask which one (check the Domains table in
`AGENTS.md` for valid names).

## Step 1 — Locate inputs

For the target domain, find:

1. **Migrations directory** — this repo uses `golang-migrate/migrate/v4`
   with embedded FS. Convention is `<domain>/migrate/migrations/*.sql` or
   `<domain>/src/migrations/*.sql`, files named `NNN_description.up.sql` /
   `NNN_description.down.sql`, applied in numeric order. Known locations:
   - `leaflab/migrate/migrations/`
   - `manmanv2/migrate/migrations/`
   - `manman/src/migrations/`
   - `friendly_computing_machine/src/migrations/`
   - `tools/app_registry/migrate/schema/migrations/`

   If the domain has no migrations directory, stop — there is nothing to
   document.

2. **Existing docs** — the domain's `TOC.md`, `README.md`, `ARCHITECTURE.md`
   (or split `architecture/` directory), `ENV.md`. Read `TOC.md` first per
   `AGENTS.md`'s Navigation Protocol.

3. **An existing data doc** — grep the domain for `DATA.md`, or check
   whether table/schema documentation already lives inside
   `ARCHITECTURE.md` / a split `architecture/*.md` file (e.g.
   `tools/app_registry/architecture/`). **Do not create a duplicate.** If
   table docs already live elsewhere, update that file in place instead of
   creating a new `DATA.md`.

## Step 2 — Reconstruct the current schema

Read only the `.up.sql` files, in numeric order, and mentally apply them to
build the *current* schema state — the goal is what the schema looks like
today, not a changelog. Ignore `.down.sql` files (rollback-only, not
descriptive of current state) except to confirm a migration is reversible.

For a domain with many migration files, fork a subagent to read them and
return a synthesized schema summary (tables, columns, types, constraints,
indexes, views) rather than pulling all the raw SQL into your own context —
per `AGENTS.md`'s Effective Subagent Usage guidance.

Note for each table:
- Primary key, foreign keys, unique constraints
- Whether it's an SCD2 history table (`valid_from`/`valid_to` columns —
  see `AGENTS.md` "SCD2" section) or an append-only event log
- Partial indexes, especially `WHERE valid_to IS NULL` current-row indexes
- Any `CREATE VIEW` — views are first-class query surfaces, document them
  like tables

## Step 3 — Gather semantic context

Read the domain's `ARCHITECTURE.md`/`architecture/*.md` and `README.md` for
*why* each table exists and what flow writes to it (e.g. a config-push
sequence, an ingestion pipeline). Do not infer purpose from column names
alone when a doc already states it — cite the existing doc's language
rather than re-deriving it differently.

## Step 4 — Write or update `<domain>/DATA.md`

Match `leaflab/DATA.md`'s structure and section order:

1. **Entity Relationships** — one `mermaid erDiagram` block covering every
   table, with PK/FK/UK annotations and relationship cardinalities.
2. **Flow sections** (as needed) — `mermaid flowchart`/`sequenceDiagram`
   blocks for non-obvious write paths (e.g. identity resolution, config
   push, event ingestion). Skip if the domain has no interesting write-path
   nuance.
3. **Any JSONB/structured-column format notes** — document the shape with
   an example, if a column stores structured data.
4. **SCD2 Convention** — a table listing which tables are SCD2 history
   tables and what changes in each, using the exact `valid_from`/`valid_to`
   language from `AGENTS.md`. State plainly which tables are *not* SCD2
   (append-only logs) to prevent a future agent misapplying the pattern.
5. **Analytical Views** (if any `v_` views exist) — a table of view name,
   cardinality, and purpose, plus a "temporal accuracy" note for any view
   that mixes historically-accurate (snapshotted) and current-state joins.
6. **Example queries** — 3-6 realistic `sql` snippets answering questions a
   consumer (dashboard, agent, ad-hoc query) would actually ask. Prefer
   real column/table names over placeholders.

Write directly to `<domain>/DATA.md`, preserving any hand-written sections
that don't map to a migration (editorial framing, cross-references) if the
file already exists — this is a refresh, not a clobber. Diff mentally
against the previous version and only change what the migrations actually
changed.

## Step 5 — Enforce size limits

Apply `AGENTS.md`'s "Size Limits & Splitting" rule: if `DATA.md` exceeds
~800-1000 lines / ~20K tokens, split it. The natural algorithmic boundary
here is **one file per major schema area** (e.g. `DATA/identity.md`,
`DATA/readings.md`, `DATA/views.md`), with `DATA.md` rewritten to a real
index (jump table + ER diagram + SCD2 convention table, since those are
cross-cutting) per the "Over threshold" directory-split pattern. Follow the
full split-mechanics checklist (algorithmic boundary, predictable names,
one canonical entry point, TOC update, grep-verify no dangling references,
re-check each split file's own size) — do not do a line-count split.

## Step 6 — Wire into TOC.md

If `<domain>/DATA.md` is not already listed in `<domain>/TOC.md`, add an
entry describing when to read it (e.g. "ER diagram, SCD2 convention, query
examples — read before writing a new query or migration"). Match the
existing TOC's format (bullet list or `| File | When to read it |` table —
use whichever the domain's TOC already uses).

## Step 7 — Report, don't over-commit

Summarize what changed (new tables documented, stale sections removed,
views added) rather than reprinting the whole file. Do not commit
automatically — this skill is typically run interactively by a human
reviewing the diff; leave staging/committing to the normal git workflow
unless explicitly asked to commit.

## Reference

- `leaflab/DATA.md` — canonical example of the target structure and
  writing style
- `AGENTS.md` "SCD2 (Slowly Changing Dimensions Type 2)" — the
  `valid_from`/`valid_to` convention this doc must describe accurately
- `AGENTS.md` "Size Limits & Splitting" — when and how to split
- `libs/go/migrate/` — the migration runner (`golang-migrate/migrate/v4`,
  embedded FS) shared by all domains listed in Step 1
