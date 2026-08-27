-- UI session storage for leaflab-ui (FR13): cookie holds only an opaque
-- session_id; tokens live here. refresh_token is AES-256-GCM encrypted by
-- the application (SECRET_KEY).
--
-- Owner of this schema shape: libs/go/htmxauth (db_session.go). If a
-- future library change alters the table definition, coordinated
-- migrations are required in every adopting domain (see
-- tools/app_registry/migrate/schema/migrations/012_ui_sessions.up.sql and
-- manmanv2/migrate/migrations/032_ui_sessions.up.sql for the same shape in
-- the other two adopting domains) before that library bump can land.

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
