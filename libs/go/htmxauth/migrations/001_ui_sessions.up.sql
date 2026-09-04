-- UI session storage: cookie holds only an opaque session_id; tokens live here.
-- refresh_token is AES-256-GCM encrypted by the application (SECRET_KEY).
--
-- Owned by libs/go/htmxauth (see migrations.go) and applied via
-- migrate.WithSource, independently tracked in its own
-- "schema_migrations_htmxauth" table (see libs/go/migrate's ApplySource) --
-- not merged into an adopting domain's own migration sequence or table. Do
-- not copy this file into a domain's own migrations/ directory — a library
-- schema change then only needs to land here, not in every adopting domain.

CREATE TABLE IF NOT EXISTS ui_sessions (
    session_id       TEXT        PRIMARY KEY,
    user_info        JSONB       NOT NULL DEFAULT '{}',
    access_token     TEXT        NOT NULL,
    refresh_token    TEXT        NOT NULL,
    token_expires_at TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at       TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_ui_sessions_expires_at ON ui_sessions(expires_at);
