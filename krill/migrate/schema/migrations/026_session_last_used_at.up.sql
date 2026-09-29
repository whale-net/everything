-- Idle-expiry marker: touched on mint and on each session-gated write.
-- Mutable on purpose (not SCD2; sessions are not versioned). Pre-existing
-- rows stay NULL, which the gate treats as expired.
ALTER TABLE krill_session ADD COLUMN last_used_at TIMESTAMPTZ NULL;
