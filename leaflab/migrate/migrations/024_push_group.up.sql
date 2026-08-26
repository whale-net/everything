-- Multi-board push groups (FR48)
-- Each multi-board push creates a push_group to track the collective ack state
-- and provide a stable group identifier for future status queries.

CREATE TABLE push_group (
  push_group_id TEXT PRIMARY KEY,
  household_id BIGINT NOT NULL,
  scope INT NOT NULL,  -- ConfigScope enum (1=COMPLETE, 2=EDIT)
  reason TEXT NOT NULL,  -- Required justification (FR48)
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  
  CONSTRAINT fk_push_group_household FOREIGN KEY (household_id) REFERENCES household(household_id)
);

CREATE INDEX idx_push_group_household ON push_group(household_id);
CREATE INDEX idx_push_group_created ON push_group(created_at DESC);

-- Per-board ack state within a push group
-- Tracks whether each board has acked, rejected, or remained silent
CREATE TABLE push_group_board_ack (
  push_group_id TEXT NOT NULL,
  board_id BIGINT NOT NULL,
  ack_state INT NOT NULL,  -- BoardAckState enum (1=ACKED, 2=REJECTED, 3=SILENT)
  device_id TEXT NOT NULL,
  version_accepted BIGINT,  -- Version assigned to this board (null if not successful)
  first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  acked_at TIMESTAMPTZ,  -- When board acked (if state=ACKED)
  rejected_at TIMESTAMPTZ,  -- When board rejected (if state=REJECTED)
  
  PRIMARY KEY (push_group_id, board_id),
  CONSTRAINT fk_push_group_board_ack_group FOREIGN KEY (push_group_id) REFERENCES push_group(push_group_id),
  CONSTRAINT fk_push_group_board_ack_board FOREIGN KEY (board_id) REFERENCES board(board_id)
);

CREATE INDEX idx_push_group_board_ack_device ON push_group_board_ack(device_id);
CREATE INDEX idx_push_group_board_ack_first_seen ON push_group_board_ack(first_seen_at DESC);
