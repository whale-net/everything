-- Rollback App Registry Initial Schema

DROP TABLE IF EXISTS domain_adoption;
DROP TABLE IF EXISTS idempotency_key;
DROP TABLE IF EXISTS artifact_link;
DROP TABLE IF EXISTS artifact;
DROP TABLE IF EXISTS build;
DROP TABLE IF EXISTS chart_app;
DROP TABLE IF EXISTS chart;
DROP TABLE IF EXISTS app;
