-- Reverse migration 015: drop viability_verdict.source. Structural
-- reversibility only (FR45's best-effort reversibility policy) -- the
-- per-row authorship values are gone once dropped, same convention as
-- migrations 002/010/011/013's down migrations.

ALTER TABLE viability_verdict DROP COLUMN source;
