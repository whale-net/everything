-- Host drain state (#2360, manmanv2 M6, C29 groundwork).
-- Inert in this task: no cordon enforcement, no eviction. Just the
-- persisted state and its transitions via DrainServer/UndrainServer.
--
-- Deliberately not SCD2 (see AGENTS.md § SCD2): servers is a mutable
-- dimension and M6 has no history requirement for drain state.

ALTER TABLE servers
    ADD COLUMN drain_state TEXT NOT NULL DEFAULT 'schedulable',
    ADD COLUMN drain_requested_at TIMESTAMPTZ NULL;

ALTER TABLE servers
    ADD CONSTRAINT servers_drain_state_check
        CHECK (drain_state IN ('schedulable', 'draining', 'drained'));
