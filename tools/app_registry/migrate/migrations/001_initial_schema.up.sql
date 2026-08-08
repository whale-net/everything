-- App Registry Initial Schema
--
-- Covers the AR-1 subset of ARCHITECTURE.md's "Tables" section: app identity,
-- builds, artifacts, and the per-domain adoption gate. Environments,
-- promotions, promotion_event, writeback_outbox, and v_current_promotion are
-- deliberately NOT here — they land in 002 (AR-3) and 003 (AR-4). See
-- migrate/README.md "Planned migrations".

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- App: one release_app manifest. Never hard-deleted so promotion/artifact
-- history stays queryable for apps that no longer exist.
CREATE TABLE app (
    app_id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    domain            TEXT NOT NULL,
    name              TEXT NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    language          TEXT NOT NULL DEFAULT '',
    app_type          TEXT NOT NULL DEFAULT '',
    deploy_unit       TEXT NOT NULL DEFAULT 'chart' CHECK (deploy_unit IN ('chart', 'image', 'none')),
    bazel_label       TEXT NOT NULL DEFAULT '',
    image_repository  TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'missing', 'archived')),
    first_seen_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (domain, name)
);

CREATE INDEX app_status_idx ON app (status);
CREATE INDEX app_domain_idx ON app (domain);

-- Chart: a Helm chart composing one or more apps. Usually the real
-- promotion target — see ARCHITECTURE.md "Promotability".
CREATE TABLE chart (
    chart_id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    domain            TEXT NOT NULL,
    name              TEXT NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    chart_repository  TEXT NOT NULL DEFAULT '',
    deploy_unit       TEXT NOT NULL DEFAULT 'chart' CHECK (deploy_unit IN ('chart', 'image', 'none')),
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'missing', 'archived')),
    first_seen_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (domain, name)
);

CREATE INDEX chart_status_idx ON chart (status);
CREATE INDEX chart_domain_idx ON chart (domain);

-- ChartApp: which apps a chart composes, per its manifest. Reconciled
-- wholesale alongside app/chart on every ReconcileApps call.
CREATE TABLE chart_app (
    chart_id  UUID NOT NULL REFERENCES chart (chart_id) ON DELETE CASCADE,
    app_id    UUID NOT NULL REFERENCES app (app_id) ON DELETE CASCADE,
    PRIMARY KEY (chart_id, app_id)
);

CREATE INDEX chart_app_app_id_idx ON chart_app (app_id);

-- Build: one CI run. Every artifact hangs off a build so any deployed digest
-- traces back to a commit and a workflow run.
CREATE TABLE build (
    build_id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    git_sha           TEXT NOT NULL,
    git_ref           TEXT NOT NULL DEFAULT '',
    workflow_run_id   TEXT NOT NULL,
    workflow_attempt  INTEGER NOT NULL DEFAULT 1,
    actor             TEXT NOT NULL DEFAULT '',
    started_at        TIMESTAMPTZ,
    recorded_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (workflow_run_id, workflow_attempt)
);

CREATE INDEX build_git_sha_idx ON build (git_sha);

-- Artifact: a published, digest-addressable image or chart. digest is the
-- real identity; version is a convenience label that may move. owner_id is a
-- generated denormalization of app_id/chart_id so a single unique index can
-- express the (owner, kind, version) collision guard AR-5 depends on.
CREATE TABLE artifact (
    artifact_id    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind           TEXT NOT NULL CHECK (kind IN ('image', 'chart')),
    app_id         UUID REFERENCES app (app_id),
    chart_id       UUID REFERENCES chart (chart_id),
    owner_id       UUID GENERATED ALWAYS AS (COALESCE(app_id, chart_id)) STORED NOT NULL,
    repository     TEXT NOT NULL,
    version        TEXT NOT NULL,
    digest         TEXT NOT NULL,
    build_id       UUID NOT NULL REFERENCES build (build_id),
    published_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT artifact_owner_matches_kind CHECK (
        (kind = 'image' AND app_id IS NOT NULL AND chart_id IS NULL) OR
        (kind = 'chart' AND chart_id IS NOT NULL AND app_id IS NULL)
    )
);

-- Load-bearing, not optimizations — see migrate/README.md "Required indexes".
CREATE UNIQUE INDEX artifact_digest_idx ON artifact (digest);
CREATE UNIQUE INDEX artifact_version_idx ON artifact (owner_id, kind, version);
CREATE INDEX artifact_build_id_idx ON artifact (build_id);

-- ArtifactLink: chart artifact -> pinned image artifact, resolved to digests
-- at publish time by tools/helm. Makes "which image digest is running"
-- answerable without rendering a chart.
CREATE TABLE artifact_link (
    chart_artifact_id  UUID NOT NULL REFERENCES artifact (artifact_id) ON DELETE CASCADE,
    image_artifact_id  UUID NOT NULL REFERENCES artifact (artifact_id) ON DELETE CASCADE,
    app_id             UUID NOT NULL REFERENCES app (app_id),
    repository         TEXT NOT NULL,
    version            TEXT NOT NULL,
    digest             TEXT NOT NULL,
    PRIMARY KEY (chart_artifact_id, image_artifact_id)
);

CREATE INDEX artifact_link_image_artifact_id_idx ON artifact_link (image_artifact_id);

-- IdempotencyKey: key -> prior serialized response, for safe CI/client
-- retries on write RPCs. See ARCHITECTURE.md "Idempotency".
CREATE TABLE idempotency_key (
    idempotency_key  TEXT PRIMARY KEY,
    method           TEXT NOT NULL DEFAULT '',
    response_bytes   BYTEA NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- DomainAdoption: one row per domain, gating the per-domain cutover
-- described in ARCHITECTURE.md "Resolved questions" #3. Ships now even
-- though only AR-5 enforces `allocate`.
CREATE TABLE domain_adoption (
    domain      TEXT PRIMARY KEY,
    stage       TEXT NOT NULL DEFAULT 'observe' CHECK (stage IN ('observe', 'promote', 'allocate')),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
