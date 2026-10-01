-- 032_milestone_ships_alongside: work that ships with a milestone but is
-- not a capability. Plain append-only row table mirroring milestone_deferral's
-- original shape (010): no SCD2 pair, no un-ship verb.
CREATE TABLE milestone_ships_alongside (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    milestone_id                 UUID        NOT NULL,
    body                         TEXT        NOT NULL,
    position                     INT         NOT NULL DEFAULT 0,
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX milestone_ships_alongside_milestone_idx ON milestone_ships_alongside(milestone_id);
CREATE INDEX milestone_ships_alongside_scope_idx ON milestone_ships_alongside(scope_id);
