-- Down-migrating drops the whole append-only shipment history along with
-- the table -- there is no "unship" behavior to preserve beyond that:
-- `milestone_ref` and `entity_milestone` are left completely untouched,
-- same posture as every other migration in this package on its own
-- Down().
DROP INDEX IF EXISTS delivery_shipment_entity_milestone_idx;
DROP INDEX IF EXISTS delivery_shipment_milestone_idx;
DROP TABLE IF EXISTS delivery_shipment;
