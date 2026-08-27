-- Migration 016 down: reverse the append-only audit log.

DROP TABLE IF EXISTS audit_log;
