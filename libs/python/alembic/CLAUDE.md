# libs/python/alembic — Agent Instructions

## Data docs maintenance

Every domain embedding this library (`manman`, `friendly_computing_machine`,
and others — see `README.md`) keeps a `<domain>/DATA.md`: a table-by-table,
view-by-view reference (ER diagram, SCD2 convention notes, example queries)
derived from that domain's migrations. `leaflab/DATA.md` is the canonical
example (a different migration tool, but the target `DATA.md` structure is
tool-agnostic).

`<domain>/DATA.md` is **not** kept in sync automatically. After a migration
(or a batch of related migrations) lands in a domain's `migrations/versions/`
directory — e.g. `manman/src/migrations/versions/`,
`friendly_computing_machine/src/migrations/versions/` — run the `/data-docs
<domain>` skill before considering the schema change done, so `DATA.md` (and
its entry in the domain's `TOC.md`) reflects the actual current schema.

Don't run it after every individual revision file mid-feature — batch it:
run `/data-docs <domain>` once the schema change is complete, as part of
wrapping up the PR that lands it.

See `AGENTS.md` "SCD2 (Slowly Changing Dimensions Type 2)" for the
`valid_from`/`valid_to` convention `DATA.md` must describe accurately.
