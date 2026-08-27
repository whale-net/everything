-- Migration 019 down: reverse the attribute_region_plants scaffold.

DROP FUNCTION IF EXISTS attribute_region_plants(BIGINT, TIMESTAMPTZ);
