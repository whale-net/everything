-- Migration 033 down: reverse the boundary capture schema -- drop
-- boundary_partial before boundary_capture since it references
-- boundary_capture(capture_id).

DROP TABLE IF EXISTS boundary_partial;
DROP TABLE IF EXISTS boundary_capture;
