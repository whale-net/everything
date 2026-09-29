-- 025_revision_event_agent_kind: widen revision_event's two kind CHECKs to
-- admit 'agent', matching krill_session (migration 018). Strictly permissive.
ALTER TABLE revision_event DROP CONSTRAINT revision_event_acting_kind_check;
ALTER TABLE revision_event ADD CONSTRAINT revision_event_acting_kind_check
    CHECK (acting_kind IN ('human', 'service', 'agent'));

ALTER TABLE revision_event DROP CONSTRAINT revision_event_on_behalf_of_kind_check;
ALTER TABLE revision_event ADD CONSTRAINT revision_event_on_behalf_of_kind_check
    CHECK (on_behalf_of_kind IN ('human', 'service', 'agent'));
