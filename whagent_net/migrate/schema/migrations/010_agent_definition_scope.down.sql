-- Reverses 010_agent_definition_scope.up.sql.
--
-- Deliberately does NOT backfill a placeholder value the way
-- 007_agent_definition_domain.up.sql did for its one known seed row: this
-- migration is nullable specifically so a scope-less agent definition can
-- exist, and there is no single correct domain value to invent for one --
-- inventing one would silently misassign it to a delegated-grant scope it
-- was never meant to carry. If any row has a NULL scope when this down
-- migration runs, ALTER COLUMN ... SET NOT NULL fails loudly (the correct
-- outcome): an operator rolling back must first decide, per row, what
-- domain that row should carry, and backfill it by hand before retrying.
ALTER TABLE agent_definition
    ALTER COLUMN scope SET NOT NULL;

ALTER TABLE agent_definition
    RENAME COLUMN scope TO domain;
