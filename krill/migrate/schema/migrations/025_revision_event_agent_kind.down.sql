-- Restores migration 008's two-value CHECKs. Rows already written with
-- kind='agent' violate them, so this fails loudly rather than rewriting data.
ALTER TABLE revision_event DROP CONSTRAINT revision_event_on_behalf_of_kind_check;
ALTER TABLE revision_event ADD CONSTRAINT revision_event_on_behalf_of_kind_check
    CHECK (on_behalf_of_kind IN ('human', 'service'));

ALTER TABLE revision_event DROP CONSTRAINT revision_event_acting_kind_check;
ALTER TABLE revision_event ADD CONSTRAINT revision_event_acting_kind_check
    CHECK (acting_kind IN ('human', 'service'));
