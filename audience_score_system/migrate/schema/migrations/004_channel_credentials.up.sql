-- Channel-connect OAuth grant (C2, issue #1571): per-Channel YouTube
-- OAuth credentials, SCD2 per AGENTS.md "SCD2" -- a reconnect closes the
-- old credential and opens a new one, so token history (who granted
-- which scopes, and when) is auditable. This is a SEPARATE grant from
-- Google sign-in (C1, migration 003's web_session.refresh_token): C1
-- authenticates a Person (openid/email/profile), C2 authorizes this app
-- to call the YouTube Data/Analytics APIs on a specific Channel's behalf
-- (see ../../../ARCHITECTURE.md "OAuth grants").
--
-- access_token_ciphertext/refresh_token_ciphertext are AES-256-GCM
-- ciphertext (see //audience_score_system/tokens), never plaintext --
-- same ASS_TOKEN_ENCRYPTION_KEY-derived key as web/auth.SessionManager
-- (../../../ENV.md).

CREATE TABLE channel_credential (
    id                         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id                 UUID        NOT NULL REFERENCES channel(id),
    granted_by_person_id       UUID        NOT NULL REFERENCES person(id),
    access_token_ciphertext    BYTEA       NOT NULL,
    refresh_token_ciphertext   BYTEA       NOT NULL,
    access_token_expires_at    TIMESTAMPTZ NOT NULL,
    scopes                     TEXT[]      NOT NULL,
    valid_from                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to                   TIMESTAMPTZ
);

-- Exactly one live credential per Channel at a time -- a reconnect must
-- close the prior row (valid_to = NOW()) in the same transaction that
-- opens the new one (tokens.Store.Save), never leave two open rows.
CREATE UNIQUE INDEX ON channel_credential(channel_id) WHERE valid_to IS NULL;

-- Supports "every credential this Person has ever granted" (audit/
-- support reads) without a table scan.
CREATE INDEX channel_credential_granted_by_person_id
    ON channel_credential(granted_by_person_id);
