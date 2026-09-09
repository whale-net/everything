-- 002_sessions_list_index: supports ListSessions (FR3/C15, issue #2241).
-- idx_sessions_created_at_id backs both the default (created_at DESC,
-- session_id DESC) ordering and the keyset predicate SessionStore.List
-- applies for pagination -- session_id is the tie-breaker because
-- created_at alone is not unique. The remaining three single-column
-- indexes back the individual agent_id/status/subject_kind filters so a
-- filtered list is never a sequential scan either; Postgres combines them
-- via bitmap index scans as needed alongside the keyset predicate.
CREATE INDEX idx_sessions_created_at_id ON sessions (created_at DESC, session_id DESC);
CREATE INDEX idx_sessions_agent_id ON sessions (agent_id);
CREATE INDEX idx_sessions_status ON sessions (status);
CREATE INDEX idx_sessions_subject_kind ON sessions (subject_kind);
