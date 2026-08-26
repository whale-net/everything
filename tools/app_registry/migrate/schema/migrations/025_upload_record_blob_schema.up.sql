-- App Registry — Upload records and blob schema (FR-7, FR-12, FR-52, FR-25)
--
-- This migration implements the core data model for artifact uploads, blob
-- deduplication, and object key storage. See ARCHITECTURE.md for design details.
--
-- FR-7 (core half): Recording the authorization intent to publish artifacts.
-- FR-52: Single-row lifecycle state for uploads (NOT SCD2).
-- FR-12: Blob deduplication keyed on three-tuple (digest, encoding, content_type).
-- FR-25: Storing actual object keys per version and variant.
-- NFR-11: Attribution fields (principal, workflow run, timestamp).
--
-- == Upload Records (FR-7, FR-52, NFR-11) ==
--
-- One row per issued authorization. Deliberately NOT SCD2 because an upload
-- is an entity converging on a terminal state (allocated -> uploading -> confirmed
-- -> completed), not a slowly-changing attribute of something else. Lifecycle
-- state is a field on a single row, not a history-tracking mechanism.
-- Append-only transition history (if needed) would be a separate log table,
-- not valid_from/valid_to columns here. This is an explicit departure from
-- AGENTS.md's SCD2 convention, justified by the asymmetry: promotion state
-- (SCD2) is "what artifact is current in this environment at time T", an
-- attribute of the environment; upload lifecycle is "this authorization
-- converging to terminal completion", an entity lifecycle. Different semantics,
-- different modeling.
--
CREATE TABLE upload_record (
    upload_id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    object_key             TEXT NOT NULL,
    artifact_kind          TEXT NOT NULL CHECK (artifact_kind IN ('image', 'chart')),

    -- artifact_identity: denormalized owner identification for flexibility.
    -- Typically either app_id or chart_id, but stored as TEXT to allow
    -- identifying artifacts before they're registered in the artifact table
    -- (early authorization before full artifact record exists).
    artifact_identity      TEXT NOT NULL,

    -- Which artifact/version this upload was requested for. Can be either:
    -- - artifact_id (UUID) if uploaded after artifact is already recorded
    -- - version string if uploading a new/future version not yet in artifact table
    version_reference      TEXT NOT NULL,

    -- Requesting principal: user/service/workflow that initiated the upload.
    requesting_principal   TEXT NOT NULL,

    -- When the authorization was issued.
    issued_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Lifecycle state: single field per FR-52, not SCD2.
    -- Possible states: 'allocated' (authorized, not started), 'uploading'
    -- (in progress), 'confirmed' (upload complete, bytes verified), 'failed'
    -- (terminal failure, no retry without manual intervention).
    state                  TEXT NOT NULL DEFAULT 'allocated'
        CHECK (state IN ('allocated', 'uploading', 'confirmed', 'failed')),

    -- When state last changed.
    state_changed_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- NFR-11: Attribution (principal, workflow run, timestamp).
    -- Distinct from requesting_principal: the authorizing principal may differ
    -- from the workflow running the actual upload.
    attribution_principal  TEXT NOT NULL,
    workflow_run_id        TEXT NOT NULL,
    attribution_timestamp  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX upload_record_state_idx ON upload_record (state, state_changed_at)
    WHERE state IN ('allocated', 'uploading');

-- Index to distinguish completed from unreported (never confirmed).
CREATE INDEX upload_record_completion_idx ON upload_record (state)
    WHERE state IN ('confirmed', 'failed');

-- == Blob Records (FR-12, FR-61, FR-46) ==
--
-- Stored blobs, identified by the three-tuple (uncompressed_content_digest,
-- stored_encoding, content_type) per FR-61. One row per distinct stored blob.
-- The three-tuple key ensures two blobs differing only in encoding or only in
-- content_type are distinct rows, as required by FR-61(c).
--
-- Confirmation is distinct from mere existence (FR-46): only a confirmed blob
-- is ever a dedupe target. Unconfirmed and failed-verification blobs are
-- representable and queryable, but not selectable as dedupe hits.
--
-- Deliberately NOT SCD2: a blob is an immutable, content-addressed entity.
-- Its existence is append-only; it never "becomes" something else. The
-- confirmation_state is not a slowly-changing attribute of an environment or
-- version; it is a property of the blob itself converging toward terminal
-- confirmation. This follows the same asymmetry reasoning as upload_record.
--
CREATE TABLE blob_record (
    blob_id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Three-tuple key: (uncompressed_content_digest, stored_encoding, content_type)
    -- Uniqueness is enforced by the unique index below.
    uncompressed_content_digest TEXT NOT NULL,
    stored_encoding            TEXT NOT NULL,
    content_type               TEXT NOT NULL,

    -- Confirmation state: 'unconfirmed' (exists but not yet verified),
    -- 'confirmed' (bytes verified, safe for dedup), 'failed_verification'
    -- (terminal failure, do not use for dedup).
    confirmation_state         TEXT NOT NULL DEFAULT 'unconfirmed'
        CHECK (confirmation_state IN ('unconfirmed', 'confirmed', 'failed_verification')),

    -- When this blob was first recorded.
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- When confirmation_state last changed.
    confirmation_changed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- UNIQUE constraint on the three-tuple key.
    UNIQUE (uncompressed_content_digest, stored_encoding, content_type)
);

-- Index to find confirmed blobs for dedup lookups.
CREATE INDEX blob_record_confirmed_idx ON blob_record (confirmation_state)
    WHERE confirmation_state = 'confirmed';

-- == Blob ↔ Version Relationship (FR-12) ==
--
-- Many-to-one: one stored blob may be referenced by multiple published versions
-- across different minor series, different majors, and different owners/kinds.
-- Within a single (owner, kind, major, minor) series the existing uniqueness rule
-- (artifact_version_idx) bounds it to one version per digest.
--
-- A published version's blob reference is immutable: never updated after publish.
-- Changing which bytes a version resolves to requires a new version.
--
-- This table represents the M:1 relationship; no write path ever issues an UPDATE
-- against a published version's blob reference (FR-52 falsification test).
--
CREATE TABLE blob_version (
    blob_id       UUID NOT NULL REFERENCES blob_record (blob_id),
    artifact_id   UUID NOT NULL REFERENCES artifact (artifact_id),
    PRIMARY KEY (blob_id, artifact_id)
);

-- Index for reverse queries: "which versions reference this blob?"
CREATE INDEX blob_version_artifact_id_idx ON blob_version (artifact_id);

-- == Stored Object Keys (FR-25) ==
--
-- The registry stores the actual object key of every object it publishes,
-- per version and per declared variant. This table provides the storage.
-- The resolution task reads from here, and FR-40's recovery route can discover
-- a key from the database with the API server down.
--
-- Deliberately NOT SCD2: the stored object key for a (version, variant) pair
-- is assigned once at publish time and never changes. It's a static property
-- of the published artifact, not a changing attribute.
--
CREATE TABLE stored_object_key (
    stored_object_key_id  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    artifact_id           UUID NOT NULL REFERENCES artifact (artifact_id),

    -- Variant key: identifies which declared variant this key is for.
    -- Example: "amd64", "arm64", "linux/amd64", etc.
    variant_key           TEXT NOT NULL,

    -- The actual object key in storage (GHCR path, OCI reference, etc.).
    object_key            TEXT NOT NULL,

    -- When this key was recorded.
    recorded_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (artifact_id, variant_key)
);

CREATE INDEX stored_object_key_artifact_id_idx ON stored_object_key (artifact_id);
