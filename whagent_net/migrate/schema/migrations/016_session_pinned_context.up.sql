-- Adds sessions.pinned_context: caller-supplied system-level text, written
-- once at session creation and never updated. pinned_context_bytes is the
-- UTF-8 byte length so list/get can report size without selecting the text.
-- NULL (every existing row) means no pinned context.
ALTER TABLE sessions
    ADD COLUMN pinned_context TEXT,
    ADD COLUMN pinned_context_bytes INTEGER
        GENERATED ALWAYS AS (octet_length(pinned_context)) STORED;
