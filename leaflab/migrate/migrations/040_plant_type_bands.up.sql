-- Migration 035: plant-type expected-value bands (Phase 5 scaffold, FR58).
--
-- 035 is the next free migration number: the highest present anywhere on
-- disk across every .pm-worktrees/* checkout at scaffold time was 034
-- (034_plant_type_ownership, #1378's lineage) -- 035 was unclaimed anywhere.
-- Stating this in the PR since sibling v2 branches on this plan have
-- collided on migration numbers before (034_plant_type_ownership's own doc
-- comment records #1439/#1452's precedent) -- if this collides with another
-- sibling branch by the time this merges, that is an integration-time
-- renumbering, not a defect in this migration itself.
--
-- Adds plant_type_band: a per-plant-type, per-measurement-type set of
-- qualitative bands (e.g. low/ideal/high), stored and readable so a current
-- value can be rendered alongside a qualitative label (FR58). Deliberately
-- NOT SCD2 -- a band is current configuration, not a fact with history, and
-- no V1 requirement needs "what were this type's bands as of time T." A
-- band is replaced wholesale by SetPlantTypeBands (Implementation phase),
-- never versioned in place.
--
-- Bands hang off plant_type (FR55) rather than plant, so a household-owned
-- type and a global type carry bands identically, and a later per-plant
-- override (explicitly out of scope for V1 -- see this task's issue text)
-- would be a strictly additive table, never a migration of this one.

CREATE TABLE plant_type_band (
  plant_type_band_id BIGSERIAL PRIMARY KEY,
  plant_type_id  BIGINT NOT NULL REFERENCES plant_type(plant_type_id),
  sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
  band_label TEXT NOT NULL,          -- e.g. low / ideal / high
  min_value DOUBLE PRECISION NULL,   -- NULL = unbounded below
  max_value DOUBLE PRECISION NULL,   -- NULL = unbounded above
  sort_order INT NOT NULL,
  UNIQUE (plant_type_id, sensor_type_id, band_label)
);

-- Lookup index for the read path: resolving all bands for one
-- (plant_type_id, sensor_type_id) pair, ordered for band-walk resolution
-- (Implementation phase) -- mirrors the rest of this schema's convention of
-- indexing the read path's actual WHERE/ORDER BY shape rather than relying
-- on the primary key alone.
CREATE INDEX idx_plant_type_band_lookup
  ON plant_type_band(plant_type_id, sensor_type_id, sort_order);
