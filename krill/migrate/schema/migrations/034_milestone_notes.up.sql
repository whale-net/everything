-- Free-form markdown design rationale for a milestone. Nullable: NULL means
-- unset. Rides the SCD2 row, so each revision keeps the notes it carried.
ALTER TABLE milestone_ref ADD COLUMN notes TEXT NULL;
