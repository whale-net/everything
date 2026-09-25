-- Restores migration 012's seven-value CHECK. Rows already written with
-- status='designed' violate it, so this fails loudly rather than silently
-- dropping or rewriting them -- the correct behaviour for a
-- reversible-schema boundary, and the operator's job to decide what to do
-- with those rows first. Same posture as 018_agent_subject_kind's Down().
ALTER TABLE milestone_status_event DROP CONSTRAINT milestone_status_event_status_check;
ALTER TABLE milestone_status_event ADD CONSTRAINT milestone_status_event_status_check
    CHECK (status IN (
        'not started',
        'in design',
        'planned',
        'in progress',
        'shipped',
        'partially complete',
        'abandoned'
    ));
