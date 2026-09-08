-- Reverse migration 020: drop person_oidc_identity outright. Purely
-- additive table with no dependent view/column of its own (person is
-- untouched), so a plain DROP TABLE is the exact inverse of the up
-- migration.
DROP TABLE IF EXISTS person_oidc_identity;
