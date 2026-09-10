// Package seed is whagent-net's agent-definition seeder (issue #2121,
// LB5/NFR6): a libs/go/migrate.Seeder that upserts whagent_net/config's
// checked-in agents.yaml into the `agent_definition` and `model_definition`
// tables on every `migrate` run (whagent_net/migrate/main.go's
// migrate.WithSeeder(seed.Seeder())) -- config-driven seeding, but
// `agent_definition` stays a real, versioned table, never replaced by a
// config-lookup shortcut (ARCHITECTURE.md "Domain-owned MCP servers and
// the tool contract").
//
// # Versioning rule
//
// Re-running the seeder is idempotent, and a changed definition mints a
// NEW version row rather than editing one already pinned to a session
// (whagent_net/session/agentdef.go's Upsert doc comment: "Upsert
// inserts (AgentID, Version) or... replaces every column" -- that method
// is for writing one already-decided (agent_id, version) row, not for
// deciding whether this config entry IS a new version). For each
// config.AgentDefinitionConfig, seedOne:
//
//  1. reads the latest existing agent_definition row for its agent_id
//     (mirroring agentdef.go's GetLatest query, but over *sql.DB, not
//     *pgxpool.Pool -- libs/go/migrate.Seeder's signature is
//     func(ctx, *sql.DB) error, the same raw database/sql handle
//     firmware/sensor/catalog.Seeder() already models for this exact
//     WithSeeder shape);
//  2. compares model/model_definition_id/tool_set/max_turns/max_cost_usd/
//     required_role against that row field-by-field;
//  3. no existing row, or every field identical -> no-op (re-running is
//     idempotent -- an identical config produces zero new rows, not a
//     new version every invocation);
//  4. any field differs -> INSERT a new row at version = latest + 1 (or
//     version 1 if no row exists yet), leaving the prior version's row
//     untouched -- a session already pinned to it (session_agent, SCD2)
//     keeps seeing exactly what it was assigned.
//
// model_definition rows are seeded first (agent_definition may FK
// reference them) and are NOT versioned the same way -- seedModelDefinitions
// is a plain upsert-by-name, matching session.ModelDefinitionStore.Upsert's
// documented "prefer a new name over an in-place edit" discipline (see
// session/modeldef.go).
//
// # Config validation
//
// The model-catalogue half of validation (a model the configured
// OpenRouter provider does not serve) lives here, not in
// whagent_net/config (see that package's doc comment): Seeder checks
// every model_definitions entry's and every direct-model agent's model
// against llm.Catalog.Supports *before* writing any row, so a bad model
// fails the whole seeder run loudly rather than writing a half-seeded
// table -- the check-all-then-write-all shape below is what makes that
// guarantee hold even for a config with more than one entry.
// whagent_net/config.Load already enforces the pure shape rules (missing
// agent_id, an agent naming neither/both of model and model_definition, a
// model_definition reference with no matching entry, empty tool_set)
// before Seeder ever runs.
package seed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/whagent_net/config"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
)

// catalogTTL is the model-catalogue cache TTL Seeder's llm.Catalog uses --
// matches api/main.go's default. Irrelevant in practice (Seeder runs once
// per `migrate` invocation and the process exits, so the cache is never
// reused across a TTL window), kept only so NewCatalog has a sane value.
const catalogTTL = 5 * time.Minute

// Seeder returns a libs/go/migrate.Seeder-compatible function that
// upserts whagent_net/config.Load's model and agent definitions into
// `model_definition`/`agent_definition`, per this package's doc comment.
//
// Compatible with libs/go/migrate.WithSeeder -- pass the result directly:
//
//	migrate.RunCLI(schema.Migrations, schema.Dir, migrate.WithSeeder(seed.Seeder()))
func Seeder() func(ctx context.Context, db *sql.DB) error {
	return func(ctx context.Context, db *sql.DB) error {
		modelDefs, agents, err := config.Load()
		if err != nil {
			return err
		}

		// Reads the same OPENROUTER_API_KEY/OPENROUTER_BASE_URL env vars
		// api/main.go and worker/main.go construct their own llm.Client
		// from (ENV.md) -- Seeder has no dependency-injection seam of its
		// own (libs/go/migrate.Seeder's signature takes only ctx and db),
		// so it reads its one extra dependency (the model catalogue)
		// straight from the environment, the same way every other
		// binary's main.go does.
		catalog := llm.NewCatalog(
			llm.NewClient(os.Getenv("OPENROUTER_API_KEY"), getEnv("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1")),
			catalogTTL,
		)

		return SeedAgents(ctx, db, modelDefs, agents, catalog)
	}
}

// SeedAgents is Seeder's implementation, factored out over explicit
// model/agent definition lists and catalog rather than reading
// config.Load() and the environment itself -- the seam
// whagent_net/migrate/seed's Testing phase needs to exercise the
// version-diff/idempotency rule and the model-catalogue validation rule
// directly (a config with an unserved model, or a version-diff/re-run
// scenario), without needing a real OpenRouter endpoint or a rebuild of
// the embedded agents.yaml for every case. Seeder (above) is a thin
// wrapper: config.Load() plus a real llm.Catalog constructed from the
// environment.
func SeedAgents(ctx context.Context, db *sql.DB, modelDefs []config.ModelDefinitionConfig, agents []config.AgentDefinitionConfig, catalog *llm.Catalog) error {
	// Pass 1: validate every model this config will actually seed against
	// the provider catalogue before writing anything (this file's doc
	// comment, "Config validation") -- a bad model anywhere must never
	// leave a good entry half-seeded.
	for _, md := range modelDefs {
		supported, err := catalog.Supports(ctx, md.Model)
		if err != nil {
			return fmt.Errorf("seed: model_definition %q: check model %q against provider catalogue: %w", md.Name, md.Model, err)
		}
		if !supported {
			return fmt.Errorf("seed: model_definition %q: model %q is not served by the configured provider", md.Name, md.Model)
		}
	}
	for _, a := range agents {
		if a.Model == "" {
			// Referencing a model_definition instead -- that entry's own
			// model was already checked in the loop above (config.Validate
			// already guarantees the reference resolves to a real entry).
			continue
		}
		supported, err := catalog.Supports(ctx, a.Model)
		if err != nil {
			return fmt.Errorf("seed: agent %q: check model %q against provider catalogue: %w", a.AgentID, a.Model, err)
		}
		if !supported {
			return fmt.Errorf("seed: agent %q: model %q is not served by the configured provider", a.AgentID, a.Model)
		}
	}

	// Pass 2: seed model_definitions -- agent_definition rows below may FK
	// reference them by id.
	nameToID, err := seedModelDefinitions(ctx, db, modelDefs)
	if err != nil {
		return fmt.Errorf("seed: model_definitions: %w", err)
	}

	// Pass 3: diff-and-insert each agent_definition entry (this file's doc
	// comment, "Versioning rule").
	for _, a := range agents {
		if err := seedOne(ctx, db, a, nameToID); err != nil {
			return fmt.Errorf("seed: agent %q: %w", a.AgentID, err)
		}
	}
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// toToolSet converts agents.yaml's tool_set entries into
// session.ToolServerRef -- the exact Go shape
// session.AgentDefinitionStore.Upsert marshals into the `tool_set` JSONB
// column (agentdef.go), so a config entry and the row it produces compare
// byte-for-byte identical whenever nothing has changed (this package's
// idempotency guarantee depends on both writers -- this seeder and the
// hand-authored Upsert path -- agreeing on exactly this shape).
func toToolSet(refs []config.ToolServerRefConfig) []session.ToolServerRef {
	out := make([]session.ToolServerRef, len(refs))
	for i, r := range refs {
		out[i] = session.ToolServerRef{ServerURL: r.ServerURL, AllowedTools: r.AllowedTools}
	}
	return out
}

// toProviderPreferences converts a model_definitions entry's `provider`
// block into session.ProviderPreferences -- the exact Go shape
// session.ModelDefinitionStore.Upsert marshals into the `provider` JSONB
// column (modeldef.go), mirroring toToolSet's byte-for-byte agreement
// reasoning above.
func toProviderPreferences(p config.ProviderPreferencesConfig) session.ProviderPreferences {
	return session.ProviderPreferences{
		Only:              p.Only,
		Ignore:            p.Ignore,
		Order:             p.Order,
		Quantizations:     p.Quantizations,
		Sort:              p.Sort,
		AllowFallbacks:    p.AllowFallbacks,
		RequireParameters: p.RequireParameters,
		DataCollection:    p.DataCollection,
	}
}

// seedModelDefinitions upserts every model_definitions entry by name
// (session.ModelDefinitionStore's documented non-versioned behavior --
// modeldef.go) and returns the name -> id mapping seedOne needs to
// populate agent_definition.model_definition_id.
func seedModelDefinitions(ctx context.Context, db *sql.DB, modelDefs []config.ModelDefinitionConfig) (map[string]uuid.UUID, error) {
	nameToID := make(map[string]uuid.UUID, len(modelDefs))
	for _, md := range modelDefs {
		providerJSON, err := json.Marshal(toProviderPreferences(md.Provider))
		if err != nil {
			return nil, fmt.Errorf("model_definition %q: marshal provider: %w", md.Name, err)
		}

		var id uuid.UUID
		if err := db.QueryRowContext(ctx, `
			INSERT INTO model_definition (name, model, provider)
			VALUES ($1, $2, $3)
			ON CONFLICT (name) DO UPDATE SET
				model = EXCLUDED.model,
				provider = EXCLUDED.provider
			RETURNING id
		`, md.Name, md.Model, providerJSON).Scan(&id); err != nil {
			return nil, fmt.Errorf("model_definition %q: upsert: %w", md.Name, err)
		}
		nameToID[md.Name] = id
	}
	return nameToID, nil
}

// requiredRoleColumn converts agents.yaml's plain (possibly empty) string
// into agent_definition.required_role's nullable shape
// (session.AgentDefinition.RequiredRole): an empty string in config means
// "no role required", stored as SQL NULL, never the empty string -- so a
// definition with no required_role compares equal to itself across seeder
// runs instead of drifting between "" and NULL.
func requiredRoleColumn(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// equalRequiredRole compares two nullable required_role values.
func equalRequiredRole(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// equalStringPtr compares two nullable string values (agent_definition.model).
func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// equalUUIDPtr compares two nullable UUID values (agent_definition.model_definition_id).
func equalUUIDPtr(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// latestRow is the subset of an existing agent_definition row seedOne
// diffs a config entry against.
type latestRow struct {
	Version           int
	Domain            string
	Model             *string
	ModelDefinitionID *uuid.UUID
	ToolSet           []session.ToolServerRef
	MaxTurns          int
	MaxCostUSD        float64
	RequiredRole      *string
}

// seedOne applies this package's versioning rule (see the package doc
// comment) for one config.AgentDefinitionConfig entry: read the latest
// existing row (if any), no-op if every field matches, otherwise insert a
// new row at latest.Version + 1 (or 1 if none exists) -- never an
// in-place update of an existing version (agent_definition has no UPDATE
// path here at all, unlike session.AgentDefinitionStore.Upsert, which
// exists for a different caller writing one already-decided
// (agent_id, version) pair -- see this package's doc comment). nameToID
// resolves a.ModelDefinition (if set) to the model_definition row
// seedModelDefinitions already wrote.
func seedOne(ctx context.Context, db *sql.DB, a config.AgentDefinitionConfig, nameToID map[string]uuid.UUID) error {
	existing, found, err := latestAgentDefinition(ctx, db, a.AgentID)
	if err != nil {
		return fmt.Errorf("read latest version: %w", err)
	}

	toolSet := toToolSet(a.ToolSet)
	role := requiredRoleColumn(a.RequiredRole)

	// Exactly one of model / model_definition_id -- config.Validate
	// already enforces this and that a.ModelDefinition (if set) resolves
	// to a real model_definitions entry, so nameToID[a.ModelDefinition] is
	// always present here.
	var model *string
	var modelDefID *uuid.UUID
	if a.ModelDefinition != "" {
		id := nameToID[a.ModelDefinition]
		modelDefID = &id
	} else {
		m := a.Model
		model = &m
	}

	nextVersion := 1
	if found {
		nextVersion = existing.Version + 1
		if existing.Domain == a.Domain &&
			equalStringPtr(existing.Model, model) &&
			equalUUIDPtr(existing.ModelDefinitionID, modelDefID) &&
			reflect.DeepEqual(existing.ToolSet, toolSet) &&
			existing.MaxTurns == a.MaxTurns &&
			existing.MaxCostUSD == a.MaxCostUSD &&
			equalRequiredRole(existing.RequiredRole, role) {
			// Identical to the latest seeded version -- re-running the
			// seeder is idempotent: no new row.
			return nil
		}
	}

	toolSetJSON, err := json.Marshal(toolSet)
	if err != nil {
		return fmt.Errorf("marshal tool_set: %w", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_definition (agent_id, domain, version, model, model_definition_id, tool_set, max_turns, max_cost_usd, required_role)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, a.AgentID, a.Domain, nextVersion, model, modelDefID, toolSetJSON, a.MaxTurns, a.MaxCostUSD, role); err != nil {
		return fmt.Errorf("insert version %d: %w", nextVersion, err)
	}
	return nil
}

// latestAgentDefinition reads the highest-Version agent_definition row for
// agentID -- mirrors whagent_net/session/agentdef.go's GetLatest query,
// over *sql.DB (this package's Seeder signature) rather than
// *pgxpool.Pool (session.AgentDefinitionStore's), since
// libs/go/migrate.Seeder's type is func(ctx, *sql.DB) error, the same raw
// database/sql handle every other domain's WithSeeder-registered seeder
// in this repo receives (e.g. firmware/sensor/catalog.Seeder).
func latestAgentDefinition(ctx context.Context, db *sql.DB, agentID string) (latestRow, bool, error) {
	var row latestRow
	var toolSetJSON []byte
	err := db.QueryRowContext(ctx, `
		SELECT version, domain, model, model_definition_id, tool_set, max_turns, max_cost_usd, required_role
		FROM agent_definition
		WHERE agent_id = $1
		ORDER BY version DESC
		LIMIT 1
	`, agentID).Scan(&row.Version, &row.Domain, &row.Model, &row.ModelDefinitionID, &toolSetJSON, &row.MaxTurns, &row.MaxCostUSD, &row.RequiredRole)
	if err != nil {
		if err == sql.ErrNoRows { //nolint:errorlint // database/sql documents this exact sentinel, never wrapped
			return latestRow{}, false, nil
		}
		return latestRow{}, false, err
	}
	if err := json.Unmarshal(toolSetJSON, &row.ToolSet); err != nil {
		return latestRow{}, false, fmt.Errorf("unmarshal tool_set: %w", err)
	}
	return row, true, nil
}
