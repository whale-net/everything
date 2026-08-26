-- Multi-board push groups (FR48)
-- Each multi-board push creates a push_group identifying who made the push and why;
-- device_config rows created by that push reference it, and the group's ack state
-- is derived from those rows' membership entries.

CREATE TABLE push_group (
  push_group_id TEXT PRIMARY KEY,
  actor_subject VARCHAR(255) NOT NULL,
  reason TEXT NOT NULL,  -- Required justification (FR48.2)
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_push_group_created ON push_group(created_at DESC);

-- Associate device_config rows with the push group that created them.
ALTER TABLE device_config ADD COLUMN push_group_id TEXT REFERENCES push_group(push_group_id);
CREATE INDEX idx_device_config_push_group ON device_config(push_group_id);

-- Per-board-config ack state within a push group (FR48.1).
-- ack_state mirrors pb.BoardAckState: 0=unspecified/silent, 1=acked, 2=rejected.
CREATE TABLE push_group_membership (
  membership_id BIGSERIAL PRIMARY KEY,
  push_group_id TEXT NOT NULL REFERENCES push_group(push_group_id) ON DELETE CASCADE,
  device_config_id BIGINT NOT NULL REFERENCES device_config(config_id) ON DELETE CASCADE,
  ack_state INT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (push_group_id, device_config_id)
);

CREATE INDEX idx_push_group_membership_group ON push_group_membership(push_group_id);
