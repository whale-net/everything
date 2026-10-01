-- Adds 'cheap-expensive-later' to task_note.kind: a Feature's
-- "Stays cheap / expensive later" statement, rendered in the capability map.
ALTER TABLE task_note DROP CONSTRAINT IF EXISTS task_note_kind_check;
ALTER TABLE task_note ADD CONSTRAINT task_note_kind_check
    CHECK (kind IN ('scope-note', 'comment', 'cheap-expensive-later'));
