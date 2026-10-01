DELETE FROM task_note WHERE kind = 'cheap-expensive-later';
ALTER TABLE task_note DROP CONSTRAINT IF EXISTS task_note_kind_check;
ALTER TABLE task_note ADD CONSTRAINT task_note_kind_check
    CHECK (kind IN ('scope-note', 'comment'));
