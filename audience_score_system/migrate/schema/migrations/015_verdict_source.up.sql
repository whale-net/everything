-- viability_verdict gains a `source` column (M4.1 FR5 / NFR4, issue #1898):
-- an authorship marker distinguishing a verdict written by an agent via
-- MCP's save_viability_verdict from one written by a human via the web
-- save-verdict form (#1901), on the record itself.
--
-- `DEFAULT 'agent'` **is** NFR4's deterministic backfill: every
-- pre-existing row lands on `source = 'agent'` in the same ALTER TABLE
-- statement that adds the column (Postgres rewrites the existing rows in
-- place for a non-volatile DEFAULT), so no row is ever null or ambiguous
-- after this migration runs. The DEFAULT stays in place afterwards too --
-- not just for backfill -- so a bare INSERT from an unmigrated code path
-- remains valid and honest rather than failing; store.VerdictStore.Append
-- always sets the value explicitly regardless (store/verdict.go), so the
-- DEFAULT is a safety net, not the write path.
--
-- The CHECK set is deliberately closed here, unlike outcome_bar.metric_name
-- in migration 014 (whose open set was the point -- FR1 wanted a new bar
-- metric addable with no migration). `agent`/`human` enumerates the only
-- two surfaces that can author a verdict; that's a closed, known set,
-- matching viability_verdict.verdict's own CHECK precedent in migration
-- 002 rather than outcome_bar's open one.

ALTER TABLE viability_verdict
    ADD COLUMN source TEXT NOT NULL DEFAULT 'agent'
        CHECK (source IN ('agent', 'human'));
