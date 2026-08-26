CREATE TABLE ui_sessions (
  session_id TEXT PRIMARY KEY,
  user_info JSONB NOT NULL,
  access_token TEXT NOT NULL,
  refresh_token TEXT NOT NULL,
  token_expires_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_ui_sessions_expires_at ON ui_sessions(expires_at);
