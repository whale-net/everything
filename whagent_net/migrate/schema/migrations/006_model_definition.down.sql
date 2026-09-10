-- Reverses 006_model_definition.up.sql. Restoring agent_definition.model's
-- NOT NULL fails if any row was seeded with model_definition_id set and
-- model NULL -- expected: an operator rolling back this migration must
-- first re-seed those rows with a direct model, same as any other
-- down migration that narrows a column back after data used the wider
-- shape.
ALTER TABLE agent_definition
    DROP CONSTRAINT agent_definition_model_xor_model_definition,
    DROP COLUMN model_definition_id,
    ALTER COLUMN model SET NOT NULL;

DROP TABLE IF EXISTS model_definition;
