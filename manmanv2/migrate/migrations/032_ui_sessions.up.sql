-- UI session storage: cookie holds only an opaque session_id; tokens live here.
-- refresh_token is AES-256-GCM encrypted by the application (SECRET_KEY).
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
