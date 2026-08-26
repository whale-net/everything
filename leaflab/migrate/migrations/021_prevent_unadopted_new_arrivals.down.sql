-- Down migration 021: Remove Unadopted arrival prevention

DROP TRIGGER IF EXISTS trigger_warn_unadopted_assignment ON board_ownership;
DROP FUNCTION IF EXISTS warn_unadopted_assignment();
DROP FUNCTION IF EXISTS prevent_new_arrivals_to_unadopted();
