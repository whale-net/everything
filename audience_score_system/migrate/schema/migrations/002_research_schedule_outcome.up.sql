-- LB3 record chain for M1: idea -> verdict version -> schedule draft ->
-- committed entry -> published video -> metrics, kept intact by foreign
-- key the whole way. No table below stores a copy of verdict text or idea
-- title outside idea/viability_verdict -- every downstream row reaches an
-- idea only by following a FK (schedule_entry.verdict_id ->
-- viability_verdict.idea_id, video_schedule_match.schedule_entry_id ->
-- schedule_entry, ...). See issue #1569.
--
-- Also lands mcp_idempotency (NFR2/LB4, every MCP write-back tool's replay
-- guard) and the synced_video/video_metrics read models the Temporal sync
-- writes into (FR14/FR21).

-- -- idea -----------------------------------------------------------------
-- The stable identity LB3 requires. Everything downstream references an
-- idea or a specific viability_verdict version, never free text.

CREATE TABLE idea (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id            UUID        NOT NULL REFERENCES channel(id),
    title                 TEXT        NOT NULL,
    created_by_person_id  UUID        NOT NULL REFERENCES person(id),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Supports "ideas for this Channel" reads (IdeaStore.ListByChannel).
CREATE INDEX idea_channel_id ON idea(channel_id);

-- -- research_note ----------------------------------------------------------
-- A note can predate an idea (idea_id nullable, FR9). source_url NULL means
-- uncited (FR10) -- distinct from an empty string, never conflated.

CREATE TABLE research_note (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id            UUID        NOT NULL REFERENCES channel(id),
    idea_id               UUID        REFERENCES idea(id),
    text                  TEXT        NOT NULL,
    source_url            TEXT,
    author_person_id      UUID        NOT NULL REFERENCES person(id),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    idempotency_key       TEXT
);

CREATE INDEX research_note_channel_id ON research_note(channel_id);
CREATE INDEX research_note_idea_id ON research_note(idea_id);

-- -- viability_verdict --------------------------------------------------------
-- Append-only version log (FR12) -- NEVER UPDATEd, per AGENTS.md "SCD2"
-- explicitly excluding append-only event logs from the SCD2
-- valid_from/valid_to pattern. VerdictStore.Append allocates
-- version = max+1 in one tx; the UNIQUE(idea_id, version) constraint below
-- is the backstop against a racing double-append reusing a version number.
-- v_current_verdict (below) derives "the current verdict" via
-- DISTINCT ON (idea_id) ... ORDER BY version DESC rather than this table
-- ever being mutated in place.

CREATE TABLE viability_verdict (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    idea_id               UUID        NOT NULL REFERENCES idea(id),
    version               INT         NOT NULL,
    verdict               TEXT        NOT NULL CHECK (verdict IN ('viable', 'not-viable', 'needs-more-research')),
    reasoning             TEXT        NOT NULL,
    author_person_id      UUID        NOT NULL REFERENCES person(id),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    idempotency_key       TEXT,
    UNIQUE (idea_id, version)
);

-- -- verdict_citation ----------------------------------------------------------
-- FR11's cited-note list: which research_note rows a viability_verdict
-- version relied on.

CREATE TABLE verdict_citation (
    verdict_id        UUID NOT NULL REFERENCES viability_verdict(id),
    research_note_id  UUID NOT NULL REFERENCES research_note(id),
    PRIMARY KEY (verdict_id, research_note_id)
);

-- -- pacing_policy -----------------------------------------------------------
-- Natural key = Channel (channel_id UNIQUE) so PacingStore.Upsert converges
-- on repeated calls with identical values, satisfying NFR2 by construction
-- rather than needing a separate idempotency_key.

CREATE TABLE pacing_policy (
    id                       UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id               UUID        NOT NULL UNIQUE REFERENCES channel(id),
    target_uploads_per_week  NUMERIC     NOT NULL,
    preferred_days           TEXT[]      NOT NULL DEFAULT '{}',
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by_person_id     UUID        NOT NULL REFERENCES person(id)
);

-- -- schedule_entry ------------------------------------------------------------
-- The draft-and-committed record, one row per proposed slot. verdict_id is
-- the FK to the specific viability_verdict *version* that judged the idea
-- viable -- LB3's load-bearing link, not nullable: ScheduleStore.SaveDraft
-- must reject any verdict that is not 'viable' (FR16).

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

-- Supports "schedule for this Channel" reads (ScheduleStore.ListByChannel)
-- and v_prediction_vs_outcome's join on committed entries.
CREATE INDEX schedule_entry_channel_id ON schedule_entry(channel_id);
CREATE INDEX schedule_entry_idea_id ON schedule_entry(idea_id);
CREATE INDEX schedule_entry_verdict_id ON schedule_entry(verdict_id);

-- -- synced_video --------------------------------------------------------------
-- FR14/FR21 read model of what YouTube actually says. UNIQUE(channel_id,
-- youtube_video_id) is the natural key SyncStore.UpsertVideos upserts on so
-- a re-sync updates rather than duplicates.

CREATE TABLE synced_video (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id           UUID        NOT NULL REFERENCES channel(id),
    youtube_video_id     TEXT        NOT NULL,
    title                TEXT,
    privacy_status       TEXT        NOT NULL CHECK (privacy_status IN ('public', 'private', 'unlisted')),
    publish_at           TIMESTAMPTZ,
    published_at         TIMESTAMPTZ,
    is_scheduled_draft   BOOLEAN     NOT NULL DEFAULT FALSE,
    last_synced_at       TIMESTAMPTZ NOT NULL,
    UNIQUE (channel_id, youtube_video_id)
);

-- -- video_metrics --------------------------------------------------------------
-- M1 stores views + retention + CTR/impressions only (C16's deeper
-- Analytics is out of scope). UNIQUE(synced_video_id, measured_at) is the
-- upsert natural key for SyncStore.UpsertMetrics; adding columns later must
-- not require reshaping this table.

CREATE TABLE video_metrics (
    id                               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    synced_video_id                  UUID        NOT NULL REFERENCES synced_video(id),
    views                            BIGINT,
    average_view_duration_seconds    NUMERIC,
    average_view_percentage          NUMERIC,
    impressions                      BIGINT,
    impression_ctr                   NUMERIC,
    measured_at                      TIMESTAMPTZ NOT NULL,
    UNIQUE (synced_video_id, measured_at)
);

-- Supports "latest metrics for this video" lookups (v_prediction_vs_outcome
-- below, MatchStore reads) without a table scan.
CREATE INDEX video_metrics_synced_video_id_measured_at
    ON video_metrics(synced_video_id, measured_at DESC);

-- -- video_schedule_match --------------------------------------------------------
-- FR22/FR23's outcome link between what actually published and what was
-- scheduled. schedule_entry_id is nullable -- a synced video can arrive
-- with no matching schedule_entry (state starts 'pending' or 'auto' with
-- low confidence) pending human resolution (MatchStore.Resolve).

CREATE TABLE video_schedule_match (
    id                       UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    synced_video_id          UUID        NOT NULL REFERENCES synced_video(id),
    schedule_entry_id        UUID        REFERENCES schedule_entry(id),
    confidence               NUMERIC     NOT NULL,
    state                    TEXT        NOT NULL CHECK (state IN ('auto', 'pending', 'confirmed', 'rejected')),
    resolved_by_person_id    UUID        REFERENCES person(id),
    resolved_at              TIMESTAMPTZ,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- At most one live (non-rejected) match per synced_video_id -- a video
-- cannot simultaneously carry two active outcome links. Re-matching after a
-- rejection is a new row, not a resurrection of the old one.
CREATE UNIQUE INDEX video_schedule_match_synced_video_id_live
    ON video_schedule_match(synced_video_id) WHERE state != 'rejected';

CREATE INDEX video_schedule_match_schedule_entry_id ON video_schedule_match(schedule_entry_id);

-- -- mcp_idempotency -----------------------------------------------------------
-- NFR2/LB4: the ONLY replay-safety mechanism every MCP write-back tool
-- uses. No in-memory caches anywhere in the codebase substitute for this
-- table (see the Idempotency interface's Do method).

CREATE TABLE mcp_idempotency (
    tool_name             TEXT        NOT NULL,
    person_id             UUID        NOT NULL REFERENCES person(id),
    idempotency_key       TEXT        NOT NULL,
    request_fingerprint   TEXT        NOT NULL,
    result_ref            UUID,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tool_name, person_id, idempotency_key)
);

-- -- v_current_verdict ---------------------------------------------------------
-- Pre-joined "latest viability_verdict per idea" read, per AGENTS.md "SCD2"
-- Views convention -- derived with DISTINCT ON rather than viability_verdict
-- itself ever being UPDATEd (it is an append-only log, not SCD2 -- see the
-- comment on that table above).

CREATE VIEW v_current_verdict AS
SELECT DISTINCT ON (idea_id)
    id,
    idea_id,
    version,
    verdict,
    reasoning,
    author_person_id,
    created_at,
    idempotency_key
FROM viability_verdict
ORDER BY idea_id, version DESC;

-- -- v_prediction_vs_outcome -----------------------------------------------------
-- FR24's comparison read and M3's C14 aggregate surface: idea x
-- v_current_verdict x schedule_entry (committed only) x
-- video_schedule_match (state in auto/confirmed, i.e. a live, non-pending,
-- non-rejected match) x synced_video x the latest video_metrics row per
-- video. Ideas with no committed schedule_entry, or no resolved match, or
-- no recorded metrics do not appear -- this view carries both the verdict
-- and the outcome, never one without the other.

CREATE VIEW v_prediction_vs_outcome AS
SELECT
    i.id                             AS idea_id,
    i.channel_id                     AS channel_id,
    i.title                          AS idea_title,
    cv.id                            AS verdict_id,
    cv.version                       AS verdict_version,
    cv.verdict                       AS verdict,
    cv.reasoning                     AS verdict_reasoning,
    se.id                            AS schedule_entry_id,
    se.proposed_publish_at           AS proposed_publish_at,
    se.approved_at                   AS approved_at,
    vsm.id                           AS match_id,
    vsm.state                        AS match_state,
    vsm.confidence                   AS match_confidence,
    sv.id                            AS synced_video_id,
    sv.youtube_video_id              AS youtube_video_id,
    sv.title                         AS video_title,
    sv.published_at                  AS published_at,
    vm.views                         AS views,
    vm.average_view_duration_seconds AS average_view_duration_seconds,
    vm.average_view_percentage       AS average_view_percentage,
    vm.impressions                   AS impressions,
    vm.impression_ctr                AS impression_ctr,
    vm.measured_at                   AS metrics_measured_at
FROM idea i
JOIN v_current_verdict cv
    ON cv.idea_id = i.id
JOIN schedule_entry se
    ON se.idea_id = i.id AND se.verdict_id = cv.id AND se.state = 'committed'
JOIN video_schedule_match vsm
    ON vsm.schedule_entry_id = se.id AND vsm.state IN ('auto', 'confirmed')
JOIN synced_video sv
    ON sv.id = vsm.synced_video_id
JOIN LATERAL (
    SELECT m.views, m.average_view_duration_seconds, m.average_view_percentage,
           m.impressions, m.impression_ctr, m.measured_at
    FROM video_metrics m
    WHERE m.synced_video_id = sv.id
    ORDER BY m.measured_at DESC
    LIMIT 1
) vm ON TRUE;
