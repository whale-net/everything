-- Reverse of 002_spec_entities.up.sql. Drop order is the reverse of
-- creation order; none of these tables carries a DB-enforced FK to another
-- (see the up migration's LB2 parentage note), so there is no cascade
-- ordering constraint beyond readability.
DROP TABLE non_goal;
DROP TABLE persona;
DROP TABLE load_bearing_decision;
DROP TABLE requirement;
DROP TABLE feature;
DROP TABLE feature_set;
DROP TABLE product;
