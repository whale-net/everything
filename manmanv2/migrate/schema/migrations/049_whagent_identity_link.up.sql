-- whagent-net identity linking: maps a whagent-net operator's (iss, sub) to
-- the manmanv2 Keycloak user they confirmed they are, so the MCP can act as
-- that user for a whagent-net agent. Current-state mapping (one link per
-- whagent identity, no history), not SCD2.
CREATE TABLE whagent_identity_link (
    iss        TEXT        NOT NULL,
    sub        TEXT        NOT NULL,
    user_sub   TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (iss, sub)
);
CREATE INDEX whagent_identity_link_user_sub ON whagent_identity_link(user_sub);

-- Single-use record of consumed link assertions (replay protection); rows
-- are reaped once expires_at has passed.
CREATE TABLE whagent_link_assertion (
    jti        TEXT        PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX whagent_link_assertion_expires_at ON whagent_link_assertion(expires_at);
