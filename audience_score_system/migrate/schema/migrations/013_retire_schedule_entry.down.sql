-- Reverse migration 013: recreate pacing_policy and schedule_entry (and
-- restore video_schedule_match.schedule_entry_id) with their original
-- migration-002 definitions, in the reverse order they were dropped so FK
-- targets exist before the columns/indexes referencing them.
--
-- Not lossless -- the dropped rows and column values are gone. This is
-- structural reversibility only (the schema shape comes back, the data
-- does not), accepted under FR45's best-effort reversibility policy, same
-- convention as migrations 002/010/011's down migrations.

CREATE TABLE pacing_policy (
    id                       UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id               UUID        NOT NULL UNIQUE REFERENCES channel(id),
    target_uploads_per_week  NUMERIC     NOT NULL,
    preferred_days           TEXT[]      NOT NULL DEFAULT '{}',
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by_person_id     UUID        NOT NULL REFERENCES person(id)
);

CREATE TABLE schedule_entry (
    id                      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id              UUID        NOT NULL REFERENCES channel(id),
    idea_id                 UUID        NOT NULL REFERENCES idea(id),
    verdict_id              UUID        NOT NULL REFERENCES viability_verdict(id),
    proposed_publish_at     TIMESTAMPTZ NOT NULL,
    state                   TEXT        NOT NULL CHECK (state IN ('draft', 'committed')),
    approved_by_person_id   UUID        REFERENCES person(id),
    approved_at             TIMESTAMPTZ,
    created_by_person_id    UUID        NOT NULL REFERENCES person(id),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    idempotency_key         TEXT
);

CREATE INDEX schedule_entry_channel_id ON schedule_entry(channel_id);
CREATE INDEX schedule_entry_idea_id ON schedule_entry(idea_id);
CREATE INDEX schedule_entry_verdict_id ON schedule_entry(verdict_id);

ALTER TABLE video_schedule_match ADD COLUMN schedule_entry_id UUID REFERENCES schedule_entry(id);
CREATE INDEX video_schedule_match_schedule_entry_id ON video_schedule_match(schedule_entry_id);
