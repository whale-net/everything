-- Reverse migration 001: drop tables in FK-safe order (children before
-- parents). No data-preservation attempted, matching this repo's other
-- down migrations' convention (e.g. tools/app_registry/migrate).

DROP TABLE IF EXISTS channel_invite;
DROP TABLE IF EXISTS channel_person;
DROP TABLE IF EXISTS channel;
DROP TABLE IF EXISTS person;
