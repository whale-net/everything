CREATE TABLE domain_adoption (
    domain      TEXT PRIMARY KEY,
    stage       TEXT NOT NULL DEFAULT 'observe' CHECK (stage IN ('observe', 'promote', 'allocate')),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
