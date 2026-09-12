-- model_definition: a named, reusable model id + OpenRouter
-- provider-routing preference bundle (llm.ProviderPreferences) an
-- agent_definition can reference instead of duplicating routing config
-- per agent -- the "this gonna be hell" problem of one env var per
-- OpenRouter provider-routing field (order/allow_fallbacks/
-- require_parameters/data_collection/only/ignore/quantizations/sort).
-- provider stores OpenRouter's own provider-object shape as-is; whagent
-- decodes only the fields session.ProviderPreferences knows about.
--
-- Deliberately NOT SCD2/versioned like agent_definition (AGENTS.md
-- "SCD2" applies to entity state that changes over time and needs
-- as-of queries; this is a plain named lookup row). A model_definition
-- already referenced by a seeded agent_definition version should get a
-- NEW name (and a new agent_definition version) rather than an in-place
-- edit -- see session/modeldef.go's ModelDefinitionStore doc comment.
CREATE TABLE model_definition (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL UNIQUE,
    model      TEXT NOT NULL,
    provider   JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- agent_definition may name a model directly (the existing `model`
-- column) or reference a model_definition row instead -- exactly one of
-- the two. model is now nullable to allow the latter; the CHECK below
-- enforces the XOR at the database layer (config.Validate enforces the
-- identical rule pre-seed, defense in depth for any writer that bypasses
-- the seeder). When model_definition_id is set, it is what the worker
-- resolves the effective model and provider preferences from -- model
-- stays NULL in that row, never a stale duplicate of the referenced
-- model_definition's own model.
ALTER TABLE agent_definition
    ALTER COLUMN model DROP NOT NULL,
    ADD COLUMN model_definition_id UUID NULL REFERENCES model_definition (id),
    ADD CONSTRAINT agent_definition_model_xor_model_definition
        CHECK ((model IS NOT NULL) <> (model_definition_id IS NOT NULL));
