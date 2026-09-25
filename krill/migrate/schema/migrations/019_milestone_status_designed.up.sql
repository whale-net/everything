-- 019_milestone_status_designed: widen `milestone_status_event.status`'s
-- CHECK to admit an eighth value, 'designed', sitting between 'in design'
-- and 'planned' (issue #2963, FR 5652b8b9).
--
-- Migration 012 created the column with FR8's then-seven-value set and
-- wrote in its own comment that "a future write path cannot silently
-- persist an eighth value" -- that eighth value is this one. The approved
-- design splits what used to be a single 'in design' -> 'planned' step
-- into two distinct milestones-of-the-process: a container that has been
-- designed (its scope and FR budget decided) is no longer the same thing
-- as one still being designed, and only the former is allowed to commit to
-- a plan. 'designed' is that intermediate rung, and the Go-side enum
-- (store.MilestoneStatusDesigned) plus the request gate
-- (api/handlers.ValidMilestoneStatuses) widen in the same change, so the
-- database, the Go constant, and the MCP/HTTP validator all agree.
--
-- Widening a CHECK is strictly permissive: every value 012 already
-- accepted is still accepted, so this cannot invalidate existing rows. It
-- is the same idiom -- and the same posture -- as 018_agent_subject_kind.
--
-- What this migration does NOT do: it does not encode the *edge table*
-- that governs which status may follow which. That is a write-path rule
-- (api/handlers' transition validator, which set_milestone_status runs
-- before it reaches this column), not a column-shape rule -- the same
-- split every other krill schema/Go boundary makes, and the reason a
-- DB CHECK here would have to grow a second, ever-widening exception
-- table.
--
-- The constraint keeps the name 012's inline CHECK was auto-assigned,
-- milestone_status_event_status_check, so there is exactly ever one
-- CHECK on this column and Drop/Add is a replace rather than a second,
-- competing one.
ALTER TABLE milestone_status_event DROP CONSTRAINT milestone_status_event_status_check;
ALTER TABLE milestone_status_event ADD CONSTRAINT milestone_status_event_status_check
    CHECK (status IN (
        'not started',
        'in design',
        'designed',
        'planned',
        'in progress',
        'shipped',
        'partially complete',
        'abandoned'
    ));
