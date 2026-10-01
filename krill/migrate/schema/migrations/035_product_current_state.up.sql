-- Product current-state survey: nullable markdown carried on the product's
-- SCD2 row. NULL means no survey stored; each write is a new revision.
ALTER TABLE product ADD COLUMN current_state TEXT NULL;
