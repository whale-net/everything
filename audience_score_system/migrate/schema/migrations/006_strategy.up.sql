-- Strategy: a per-Idea cadence sitting between viability verdicts and
-- scheduling (issue #1637). A Strategy carries its own recurrence,
-- independent of and finer-grained than the Channel-wide pacing_policy
-- (FR17, migration 002) -- e.g. "short themed clips" weekly while
-- "absurd gaming comedy" is monthly, on the same Channel.
--
-- A Strategy is built from one or more viable-verdict Ideas via
-- strategy_idea, each row pinned to the exact viability_verdict version
-- that judged that Idea viable at link time -- the LB3 pattern
-- (schedule_entry.verdict_id, migration 002) applied one layer earlier:
-- never just idea_id.
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

-- -- strategy_idea -------------------------------------------------------
-- Which viable-verdict Ideas a Strategy is built from. verdict_id is NOT
-- NULL and not nullable-by-design: StrategyStore.Save rejects linking an
-- Idea whose current verdict is not 'viable' before writing anything, the
-- same FR16 gate ScheduleStore.SaveDraft already enforces one layer
-- downstream.

CREATE TABLE strategy_idea (
    strategy_id  UUID NOT NULL REFERENCES strategy(id),
    idea_id      UUID NOT NULL REFERENCES idea(id),
    verdict_id   UUID NOT NULL REFERENCES viability_verdict(id),
    PRIMARY KEY (strategy_id, idea_id)
);

-- Supports "which Strategies is this Idea part of" lookups and the
-- generate_schedule_plan join from idea back to its Strategy.
CREATE INDEX strategy_idea_idea_id ON strategy_idea(idea_id);
