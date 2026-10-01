-- amend_note: an amended note is a NEW task_note row pointing at the row it
-- supersedes. Notes stay append-only (not SCD2); the old row is retained.
ALTER TABLE task_note
    ADD COLUMN supersedes_note_id UUID REFERENCES task_note(id);

-- A note can be superseded at most once (linear amendment chain).
CREATE UNIQUE INDEX task_note_supersedes_idx
    ON task_note(supersedes_note_id) WHERE supersedes_note_id IS NOT NULL;
