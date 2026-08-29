-- Reverse migration 021: drop the possession-challenge claim schema and the
-- board uptime watermark.

DROP TABLE IF EXISTS board_uptime_watermark;
DROP TABLE IF EXISTS claim_cooldown;
DROP TABLE IF EXISTS claim_challenge_round;
DROP TABLE IF EXISTS claim_challenge;
