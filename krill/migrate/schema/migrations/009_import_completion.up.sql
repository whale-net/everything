-- 009_import_completion: the one-time, one-way import-completion marker
-- (FR12, NFR3, this task -- issue #2548). After a `whagent_net` import
-- completes, krill records that product's `scope` as import-complete so
-- no krill surface re-reads or re-imports from the source doc set
-- afterward -- see this migration's boundary comment below and
-- krill/importer/importer.go's pre-parse refusal check for the write and
-- read sides of that guarantee.
--
-- ============================================================================
-- Boundary call (LB3): plain, append-only fact -- not SCD2
-- ============================================================================
-- import_completion is a plain, append-only fact row, in the same style
-- as 001_scope.up.sql's own boundary comment: an import completing is not
-- a value that changes over time (NOT SCD2 -- no valid_from/valid_to
-- pair), and it is not the work axis's claimed shape either. There is no
-- update path and no "un-complete" verb -- a re-import would be a new
-- decision requiring a deliberate new operation (e.g. a future explicit
-- re-import primitive), not a flag flip on this row.
--
-- ============================================================================
-- Keyed by (scope_id, product_id), not scope_id alone
-- ============================================================================
-- FR12 says "records that product's `scope` as import-complete", which
-- reads as a scope-level flag. Keying the UNIQUE constraint on
-- (scope_id, product_id) instead is strictly safer under M1's
-- one-Product-per-scope shape and costs nothing today, while a bare
-- scope-level flag would wrongly block importing a *second* product into
-- the same scope later, once M1's one-Product-per-scope assumption no
-- longer holds (see PRODUCT.md's C23/"Later" note on a future decision
-- spanning more than one product).
--
-- product_id is the imported Product's immutable surrogate id -- not a
-- REFERENCES column, same reason as every other parent reference in
-- krill: `product.id` is not table-wide UNIQUE (see
-- 002_spec_entities.up.sql and every subsequent migration's parentage
-- note). krill/store's currentRowExists enforces its existence at write
-- time, same as every other child table.
--
-- source_path is recorded for the audit trail only -- e.g. "whagent_net/"
-- -- so a reader can tell which doc-set root an import came from. NFR3:
-- nothing in krill ever reads this path back to open a file; the import
-- is one-way and this column is never a second writable (or readable)
-- source of truth for the imported content. source_revision is the repo
-- commit SHA the import was taken from, supplied by the caller (krill
-- does not shell out to git), so a reader can tell exactly which revision
-- of the source became krill's rows.
CREATE TABLE import_completion (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id         UUID        NOT NULL REFERENCES scope (id),
    product_id       UUID        NOT NULL, -- imported Product's immutable `id`; see this migration's comment above for why this is not a REFERENCES column
    source_path      TEXT        NOT NULL, -- audit trail only (e.g. "whagent_net/") -- NFR3: nothing ever reads this path back to open a file
    source_revision  TEXT        NOT NULL, -- repo commit SHA the import was taken from, supplied by the caller
    completed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- At most one completion per product per scope -- see this migration's
-- "Keyed by (scope_id, product_id)" note above.
CREATE UNIQUE INDEX import_completion_scope_product_idx ON import_completion(scope_id, product_id);

CREATE INDEX import_completion_scope_idx ON import_completion(scope_id);
