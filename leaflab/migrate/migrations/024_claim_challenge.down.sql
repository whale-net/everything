-- Migration 024 down: Claim challenge (FR76)

DROP TABLE IF EXISTS claim_cooldown;
DROP TABLE IF EXISTS claim_challenge_round;
DROP TABLE IF EXISTS claim_challenge;
