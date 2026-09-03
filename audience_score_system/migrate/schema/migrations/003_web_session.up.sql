-- `web`'s signed-in session store (C1, FR1/FR2). See issue #1570's
-- Implementation section: the browser cookie carries only an opaque
-- session_id (HttpOnly, Secure, SameSite=Lax); everything else -- the
-- resolved person, and any Google refresh token -- lives here server-side.
-- refresh_token is AES-256-GCM encrypted at rest by
-- web/auth.SessionManager before it ever reaches this table (see
-- web/auth/session.go) -- this column is never plaintext.
--
-- Deliberately not SCD2 (AGENTS.md "SCD2" applies to entity history, not
-- ephemeral session rows) -- a session is simply deleted on logout/
-- expiry, mirroring libs/go/htmxauth's ui_sessions table.

CREATE TABLE web_session (
    session_id     TEXT        PRIMARY KEY,
    person_id      UUID        NOT NULL REFERENCES person(id),
    refresh_token  TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at     TIMESTAMPTZ NOT NULL
);

-- Supports "resolve this session_id if not expired" (every RequireSignedIn
-- call) and periodic cleanup of expired rows.
CREATE INDEX web_session_expires_at ON web_session(expires_at);

-- Supports "find this Person's sessions" (e.g. a future sign-out-everywhere
-- feature) without a table scan.
CREATE INDEX web_session_person_id ON web_session(person_id);
