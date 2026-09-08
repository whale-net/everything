// Package seed is whagent-net's agent-definition seeder (issue #2121,
// LB5/NFR6): a libs/go/migrate.Seeder that upserts whagent_net/config's
// checked-in agents.yaml into the `agent_definition` table on every
// `migrate` run (whagent_net/migrate/main.go's
// migrate.WithSeeder(seed.Seeder())) -- config-driven seeding, but
// `agent_definition` stays a real, versioned table, never replaced by a
// config-lookup shortcut (ARCHITECTURE.md "Domain-owned MCP servers and
// the tool contract").
//
// # Versioning rule (this task's Implementation phase)
//
// Re-running the seeder must be idempotent, and a changed definition must
// mint a NEW version row rather than editing one already pinned to a
// session (whagent_net/session/agentdef.go's Upsert doc comment: "Upsert
// inserts (AgentID, Version) or... replaces every column" -- that method
// is for writing one already-decided (agent_id, version) row, not for
// deciding whether this config entry IS a new version). Seeder's job is
// the missing half: for each config.AgentDefinitionConfig,
//
//  1. read the latest existing agent_definition row for its agent_id
//     (`SELECT ... FROM agent_definition WHERE agent_id = $1 ORDER BY
//     version DESC LIMIT 1`, mirroring agentdef.go's GetLatest query but
//     over *sql.DB, not *pgxpool.Pool -- libs/go/migrate.Seeder's
//     signature is func(ctx, *sql.DB) error, the same raw database/sql
//     handle firmware/sensor/catalog.Seeder() already models for this
//     exact WithSeeder shape);
//  2. compare model/tool_set/max_turns/max_cost_usd/required_role against
//     that row field-by-field;
//  3. no existing row, or every field identical -> no-op (re-running is
//     idempotent -- an identical config produces zero new rows, not a
//     new version every invocation);
//  4. any field differs -> INSERT a new row at version = latest + 1 (or
//     version 1 if no row exists yet), leaving the prior version's row
//     untouched -- a session already pinned to it (session_agent, SCD2)
//     keeps seeing exactly what it was assigned.
//
// This task's Testing section's red/green case is exactly this: seed
// twice unchanged (one row, one version); change one field and seed again
// (two rows for that agent_id, the session pinned to version 1 still
// resolves version 1 via session.AgentDefinitionStore.GetVersion).
//
// # Config validation (this task's Testing section)
//
// The model-catalogue half of validation (a model the configured
// OpenRouter provider does not serve) belongs here, not in
// whagent_net/config (see that package's doc comment) -- Implementation
// phase wires an llm.Catalog.Supports check per entry before any row is
// written, so a bad model fails the whole seeder run loudly rather than
// writing a half-seeded table. whagent_net/config.Load already enforces
// the pure shape rules (missing agent_id/model, empty tool_set) before
// Seeder ever runs.
package seed

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/whale-net/everything/whagent_net/config"
)

// Seeder returns a libs/go/migrate.Seeder-compatible function that
// upserts whagent_net/config.Load's agent definitions into
// `agent_definition`, per this package's doc comment.
//
// Compatible with libs/go/migrate.WithSeeder -- pass the result directly:
//
//	migrate.RunCLI(schema.Migrations, schema.Dir, migrate.WithSeeder(seed.Seeder()))
//
// Not implemented in this Scaffold-phase task (issue #2121) --
// Implementation phase wires this package's doc-commented versioning rule
// through db, the same *sql.DB libs/go/migrate.RunCLI already opens and
// passes to every registered Seeder.
func Seeder() func(ctx context.Context, db *sql.DB) error {
	return func(ctx context.Context, db *sql.DB) error {
		if _, err := config.Load(); err != nil {
			return err
		}
		return fmt.Errorf("seed: agent-definition seeding not implemented (issue #2121 Implementation phase)")
	}
}
