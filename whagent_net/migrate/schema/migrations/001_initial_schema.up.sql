-- 001_initial_schema: every table whagent-net M1 reads or writes (see
-- issue #2109, whagent_net/ARCHITECTURE.md "Three nouns", and
-- whagent_net/PRODUCT.md's load-bearing decisions LB1/LB2/LB4/LB5/LB6/NFR3/
-- NFR6). Column names and nullability here are the contract every later
-- whagent-net task assumes -- do not rename or "improve" them without a new
-- migration.

-- agent_definition (LB5/NFR6): a table even though M1 seeds it from config.
-- tool_set is [{"server_url": "...", "allowed_tools": [...]|null}] -- a
-- null allowed_tools means "whatever the server exposes" (see
-- ARCHITECTURE.md "Domain-owned MCP servers and the tool contract").
CREATE TABLE agent_definition (
    agent_id      TEXT NOT NULL,
    version       INT NOT NULL,
    model         TEXT NOT NULL,
    tool_set      JSONB NOT NULL,
    max_turns     INT NOT NULL DEFAULT 100,
    max_cost_usd  NUMERIC(12, 6) NOT NULL DEFAULT 1.000000,
    required_role TEXT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (agent_id, version)
);

-- sessions (LB2/NFR3): session_id equals the Temporal workflow ID (LB2).
-- subject_* and on_behalf_of_* are deliberately separate NOT NULL column
-- sets -- M1 always writes on_behalf_of_* identical to subject_*, but the
-- columns are never nullable so a future caller acting for someone else
-- needs no schema change. parent_session_id is always NULL in M1 (no C21)
-- but present so a child session is an ordinary row later. cap_kind/
-- error_category/error_detail are populated only for terminal
-- capped/failed sessions (FR3). No CHECK constrains subject_iss/
-- on_behalf_of_iss to Keycloak -- a non-Keycloak issuer must be a row, not
-- a migration.
CREATE TABLE sessions (
    session_id         UUID PRIMARY KEY,
    subject_iss        TEXT NOT NULL,
    subject_sub        TEXT NOT NULL,
    subject_kind       TEXT NOT NULL CHECK (subject_kind IN ('human', 'service')),
    on_behalf_of_iss   TEXT NOT NULL,
    on_behalf_of_sub   TEXT NOT NULL,
    on_behalf_of_kind  TEXT NOT NULL CHECK (on_behalf_of_kind IN ('human', 'service')),
    parent_session_id  UUID NULL REFERENCES sessions (session_id),
    agent_id           TEXT NOT NULL,
    model              TEXT NOT NULL,
    model_override     TEXT NULL,
    status             TEXT NOT NULL CHECK (status IN ('running', 'awaiting_input', 'done', 'stopped', 'failed', 'capped')),
    cap_kind           TEXT NULL CHECK (cap_kind IN ('turns', 'cost')),
    error_category     TEXT NULL CHECK (error_category IN ('retryable', 'non_retryable')),
    error_detail       TEXT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- transcript_event (LB1): append-only, explicitly NOT SCD2. event_id is
-- time-ordered (UUIDv7 or equivalent), globally unique and stable across
-- re-publish. seq is per-session monotonic, allocated by TranscriptStore
-- inside the same transaction as the insert.
CREATE TABLE transcript_event (
    event_id     UUID PRIMARY KEY,
    session_id   UUID NOT NULL REFERENCES sessions (session_id),
    seq          BIGINT NOT NULL,
    turn         INT NOT NULL,
    type         TEXT NOT NULL,
    payload      JSONB NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (session_id, seq)
);

-- Ordered reads by ReadTranscript and the worker's context build.
CREATE INDEX idx_transcript_event_session_seq ON transcript_event (session_id, seq);

-- turn_context (LB1): each turn persists the ordered event-ID list its
-- context was built from, so a debug GetTurnContext (C24) needs no schema
-- change.
CREATE TABLE turn_context (
    session_id UUID NOT NULL,
    turn       INT NOT NULL,
    event_ids  UUID[] NOT NULL,
    PRIMARY KEY (session_id, turn)
);

-- turn_usage (LB6): cost_usd is NOT NULL -- unknown cost is never zero and
-- never null (FR7); cost_estimated flags a token-based estimate when the
-- provider omits cost. generation_id is the provider's returned generation
-- id -- M1 has no reader, it is still recorded.
CREATE TABLE turn_usage (
    session_id        UUID NOT NULL,
    turn              INT NOT NULL,
    model             TEXT NOT NULL,
    prompt_tokens     BIGINT NOT NULL,
    completion_tokens BIGINT NOT NULL,
    cost_usd          NUMERIC(12, 6) NOT NULL,
    cost_estimated    BOOLEAN NOT NULL,
    generation_id     TEXT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (session_id, turn)
);

-- session_agent (LB5/NFR6): SCD2 agent-definition assignment, per
-- AGENTS.md's SCD2 convention -- valid_from/valid_to exactly, no synonyms.
-- The partial unique index enforces at most one open (valid_to IS NULL)
-- assignment per session.
CREATE TABLE session_agent (
    session_id    UUID NOT NULL,
    agent_id      TEXT NOT NULL,
    agent_version INT NOT NULL,
    valid_from    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to      TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX idx_session_agent_current ON session_agent (session_id) WHERE valid_to IS NULL;

-- tool_call_idempotency (LB4): whagent-net's own ledger of key -> outcome
-- for tool calls dispatched by the worker; the domain server keeps its own
-- separately.
CREATE TABLE tool_call_idempotency (
    idempotency_key TEXT PRIMARY KEY,
    session_id      UUID NOT NULL,
    turn            INT NOT NULL,
    call_index      INT NOT NULL,
    tool            TEXT NOT NULL,
    server_url      TEXT NOT NULL,
    outcome         JSONB NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (session_id, turn, call_index)
);
