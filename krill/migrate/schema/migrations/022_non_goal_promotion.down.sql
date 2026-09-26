-- Reverse of 022_non_goal_promotion. The register goes first: the `outcome`
-- column is what makes a tombstone readable as a RETIRE, so an older
-- schema that kept the column but lost the table would leave a
-- `non_goal_promotion` read pointing at nothing.
DROP TABLE non_goal_promotion;

ALTER TABLE void_event DROP COLUMN outcome;
