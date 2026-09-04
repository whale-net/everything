-- Strategy: a per-Idea cadence sitting between viability verdicts and
-- scheduling (issue #1637). A Strategy carries its own recurrence,
-- independent of and finer-grained than the Channel-wide pacing_policy
-- (FR17, migration 002) -- e.g. "short themed clips" weekly while
-- "absurd gaming comedy" is monthly, on the same Channel.
--
-- A Strategy is built directly from one or more viability_verdict rows via
-- strategy_verdict -- not from ideas: idea_id is derivable from
-- viability_verdict.idea_id, and grounding on verdict_id (rather than
-- idea_id) means the exact analysis that justified this Strategy is what's
-- pinned, the LB3 pattern (schedule_entry.verdict_id, migration 002)
-- applied one layer earlier. Cardinality is many-to-many in both
-- directions: a Strategy is usually built from several verdicts (often
-- several Ideas), and the same verdict may ground more than one Strategy.
--
-- There is deliberately no separate "plan" table: a Plan is the computed
-- proposal generate_schedule_plan (mcp/tools/strategy.go) derives from
-- active Strategies + pacing_policy + the existing schedule, read fresh on
-- every call (LB4) -- committing a proposal reuses save_schedule_draft
-- (migration 002/FR16/FR18) rather than a second write path.

CREATE TABLE strategy (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id            UUID        NOT NULL REFERENCES channel(id),
    title                 TEXT        NOT NULL,
    cadence               TEXT        NOT NULL CHECK (cadence IN ('weekly', 'biweekly', 'monthly')),
    preferred_weekday     TEXT,
    active                BOOLEAN     NOT NULL DEFAULT TRUE,
    created_by_person_id  UUID        NOT NULL REFERENCES person(id),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    idempotency_key       TEXT
);

-- Supports "strategies for this Channel" reads (StrategyStore.ListByChannel).
CREATE INDEX strategy_channel_id ON strategy(channel_id);

-- -- strategy_verdict -----------------------------------------------------
-- Which viability_verdict rows a Strategy is built from -- many-to-many:
-- StrategyStore.Save rejects linking a verdict whose verdict column is not
-- 'viable' before writing anything, the same FR16 gate
-- ScheduleStore.SaveDraft already enforces one layer downstream. idea_id
-- is intentionally not a column here -- join through viability_verdict.

CREATE TABLE strategy_verdict (
    strategy_id  UUID NOT NULL REFERENCES strategy(id),
    verdict_id   UUID NOT NULL REFERENCES viability_verdict(id),
    PRIMARY KEY (strategy_id, verdict_id)
);

-- Supports "which Strategies is this verdict part of" lookups and the
-- generate_schedule_plan join from verdict back to its Strategies.
CREATE INDEX strategy_verdict_verdict_id ON strategy_verdict(verdict_id);
