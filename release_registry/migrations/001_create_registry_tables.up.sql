-- release_registry: version registry SCD2 tables and audit trail

-- ============================================================
-- registry_apps: domain-app declarations (SCD2)
-- ============================================================

CREATE TABLE IF NOT EXISTS registry_apps (
    id            SERIAL PRIMARY KEY,
    app_key       VARCHAR(255) NOT NULL UNIQUE,
    domain        VARCHAR(100) NOT NULL,
    name          VARCHAR(100) NOT NULL,
    registry      TEXT,
    organization  TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_registry_apps_key ON registry_apps(app_key);

-- ============================================================
-- registry_commits: append-only audit trail (no SCD2)
-- ============================================================

CREATE TABLE IF NOT EXISTS registry_commits (
    id        SERIAL PRIMARY KEY,
    repo      VARCHAR(500) NOT NULL,
    sha       VARCHAR(64) NOT NULL,
    ref       VARCHAR(500),
    timestamp TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_registry_commits_sha ON registry_commits(sha);

-- ============================================================
-- registry_artifacts: version-to-commit link (SCD2)
-- ============================================================

CREATE TABLE IF NOT EXISTS registry_artifacts (
    id           SERIAL PRIMARY KEY,
    app_key      VARCHAR(255) NOT NULL REFERENCES registry_apps(app_key),
    kind         VARCHAR(16) NOT NULL CHECK (kind IN ('IMAGE', 'HELM_CHART')),
    version      VARCHAR(50) NOT NULL,
    commit_sha   VARCHAR(64) NOT NULL REFERENCES registry_commits(sha),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_registry_artifacts_app_key ON registry_artifacts(app_key);
CREATE INDEX IF NOT EXISTS idx_registry_artifacts_version ON registry_artifacts(version);

-- ============================================================
-- registry_promotions: SCD2 per AGENTS.md convention
--   valid_from / valid_to, partial index on current rows
--   NULL = still current, superseded = has a value
-- ============================================================

CREATE TABLE IF NOT EXISTS registry_promotions (
    id           SERIAL PRIMARY KEY,
    app_key      VARCHAR(255) NOT NULL REFERENCES registry_apps(app_key),
    env          VARCHAR(50) NOT NULL,
    kind         VARCHAR(16) NOT NULL CHECK (kind IN ('IMAGE', 'HELM_CHART')),
    version      VARCHAR(50) NOT NULL,
    commit_sha   VARCHAR(64) REFERENCES registry_commits(sha),
    valid_from   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_registry_promotions_app_key ON registry_promotions(app_key) WHERE valid_to IS NULL;
