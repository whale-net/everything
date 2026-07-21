-- release_registry: drop SCD2 tables (reverse of 001)

DROP TABLE IF EXISTS registry_promotions CASCADE;
DROP TABLE IF EXISTS registry_artifacts CASCADE;
DROP TABLE IF EXISTS registry_commits CASCADE;
DROP TABLE IF EXISTS registry_apps CASCADE;
