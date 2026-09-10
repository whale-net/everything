//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never compiles or runs
// it. See //libs/go/dbtest's README and whagent_net/session/
// store_integration_test.go for the pattern this file follows: spin up a
// throwaway Postgres via dbtest, apply whagent-net's own real embedded
// migrations, then exercise SeedAgents (seed.go) against it -- issue
// #2121's Testing section: "seeding from the config file creates the
// definition rows; re-running is idempotent; a changed definition
// produces a new version row and leaves the prior version intact for
// sessions pinned to it" plus "a definition naming an unserved model ...
// fails the seeder loudly rather than writing a half-row".
//
// The model-catalogue check goes through a real *llm.Catalog, but never a
// real OpenRouter endpoint: llm.NewClient's HTTP transport is pointed at
// an in-process httptest.Server (fakeModelsServer below) serving a fixed
// models list, the same technique whagent_net/llm's own test suite uses
// (client_test.go's stubTransport), reimplemented at this package's
// boundary since SeedAgents takes an already-constructed *llm.Catalog.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //whagent_net/migrate/seed:seed_integration_test --test_output=all
package seed_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/whagent_net/config"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
	"github.com/whale-net/everything/whagent_net/migrate/seed"
)

// newTestDB provisions an isolated, migrated Postgres database (see
// whagent_net/session/store_integration_test.go's newDB, mirrored here).
func newTestDB(t *testing.T) (*sql.DB, *dbtest.Postgres) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	return sqlDB, db
}

// fakeModelsServer stands in for OpenRouter's /models endpoint: it always
// reports served as the complete catalogue of served model IDs, so a test
// controls Catalog.Supports' answer directly instead of depending on a
// real provider's actual catalogue at test-run time.
func fakeModelsServer(t *testing.T, served []string) *httptest.Server {
	t.Helper()
	data := make([]map[string]any, 0, len(served))
	for _, id := range served {
		data = append(data, map[string]any{"id": id, "object": "model", "created": 1700000000, "owned_by": "test"})
	}
	body, err := json.Marshal(map[string]any{"object": "list", "data": data})
	require.NoError(t, err)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

// newTestCatalog builds a real *llm.Catalog whose provider calls never
// leave the process -- see this file's doc comment.
func newTestCatalog(t *testing.T, served []string) *llm.Catalog {
	t.Helper()
	ts := fakeModelsServer(t, served)
	t.Cleanup(ts.Close)
	return llm.NewCatalog(llm.NewClient("test-api-key", ts.URL), time.Minute)
}

// agent builds a minimal, valid config.AgentDefinitionConfig for id/model
// -- callers mutate the returned value's other fields as each test case
// needs.
func agent(id, model string) config.AgentDefinitionConfig {
	return config.AgentDefinitionConfig{
		AgentID: id,
		Domain:  "test-domain",
		Model:   model,
		ToolSet: []config.ToolServerRefConfig{
			{ServerURL: "http://mcp.example.com:8081/", AllowedTools: nil},
		},
		MaxTurns:     100,
		MaxCostUSD:   1.0,
		RequiredRole: "whagent-" + id,
	}
}

// definitionRow is one agent_definition row's diffable columns, for
// assertions below. Model is nullable (migration 006): a row seeded from
// a model_definition reference has Model nil and ModelDefinitionID set,
// the exact inverse of a row seeded with a direct model.
type definitionRow struct {
	Version           int
	Domain            string
	Model             *string
	ModelDefinitionID *uuid.UUID
	MaxTurns          int
	MaxCostUSD        float64
	RequiredRole      *string
}

func readVersions(t *testing.T, ctx context.Context, db *sql.DB, agentID string) []definitionRow {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT version, domain, model, model_definition_id, max_turns, max_cost_usd, required_role
		FROM agent_definition WHERE agent_id = $1 ORDER BY version
	`, agentID)
	require.NoError(t, err)
	defer rows.Close()

	var out []definitionRow
	for rows.Next() {
		var r definitionRow
		require.NoError(t, rows.Scan(&r.Version, &r.Domain, &r.Model, &r.ModelDefinitionID, &r.MaxTurns, &r.MaxCostUSD, &r.RequiredRole))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// modelDefinitionRow is one model_definition row's diffable columns.
type modelDefinitionRow struct {
	ID       uuid.UUID
	Name     string
	Model    string
	Provider json.RawMessage
}

func readModelDefinition(t *testing.T, ctx context.Context, db *sql.DB, name string) *modelDefinitionRow {
	t.Helper()
	var r modelDefinitionRow
	err := db.QueryRowContext(ctx, `
		SELECT id, name, model, provider FROM model_definition WHERE name = $1
	`, name).Scan(&r.ID, &r.Name, &r.Model, &r.Provider)
	if err == sql.ErrNoRows { //nolint:errorlint // database/sql documents this exact sentinel, never wrapped
		return nil
	}
	require.NoError(t, err)
	return &r
}

// TestSeedAgents_CreatesDefinitionRows proves seeding from a config list
// creates one version-1 row per entry.
func TestSeedAgents_CreatesDefinitionRows(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	catalog := newTestCatalog(t, []string{"anthropic/claude-3.5-sonnet", "openai/gpt-4o"})

	agents := []config.AgentDefinitionConfig{
		agent("agent-a", "anthropic/claude-3.5-sonnet"),
		agent("agent-b", "openai/gpt-4o"),
	}
	require.NoError(t, seed.SeedAgents(ctx, db, nil, agents, catalog))

	rowsA := readVersions(t, ctx, db, "agent-a")
	require.Len(t, rowsA, 1)
	assert.Equal(t, 1, rowsA[0].Version)
	require.NotNil(t, rowsA[0].Model)
	assert.Equal(t, "anthropic/claude-3.5-sonnet", *rowsA[0].Model)

	rowsB := readVersions(t, ctx, db, "agent-b")
	require.Len(t, rowsB, 1)
	assert.Equal(t, 1, rowsB[0].Version)
}

// TestSeedAgents_PopulatesDomain proves seeding from config populates
// agent_definition.domain (issue #2424 FR1) -- the sole input
// whagent_net/grantkey.ForDomain may derive a delegated-grant key from.
func TestSeedAgents_PopulatesDomain(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	catalog := newTestCatalog(t, []string{"anthropic/claude-3.5-sonnet"})

	a := agent("agent-a", "anthropic/claude-3.5-sonnet")
	a.Domain = "audience_score_system"
	require.NoError(t, seed.SeedAgents(ctx, db, nil, []config.AgentDefinitionConfig{a}, catalog))

	rows := readVersions(t, ctx, db, "agent-a")
	require.Len(t, rows, 1)
	assert.Equal(t, "audience_score_system", rows[0].Domain)
}

// TestSeedAgents_RealAgentsYAML_MigrationBackfillLeavesNoNullOrEmptyDomain
// seeds the real embedded config/agents.yaml (config.Load, not the
// agent() test helper) against a freshly migrated database and proves
// migration 007's backfill plus this seeding path leave zero
// agent_definition rows with a NULL or empty-string domain.
func TestSeedAgents_RealAgentsYAML_MigrationBackfillLeavesNoNullOrEmptyDomain(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)

	modelDefs, agents, err := config.Load()
	require.NoError(t, err)

	served := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.Model != "" {
			served = append(served, a.Model)
		}
	}
	for _, md := range modelDefs {
		served = append(served, md.Model)
	}
	catalog := newTestCatalog(t, served)

	require.NoError(t, seed.SeedAgents(ctx, db, modelDefs, agents, catalog))

	var badCount int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*) FROM agent_definition WHERE domain IS NULL OR domain = ''
	`).Scan(&badCount))
	assert.Zero(t, badCount, "every agent_definition row (migration backfill + real agents.yaml seeding) must carry a non-empty domain")
}

// TestSeedAgents_ReRunIsIdempotent proves re-running SeedAgents with an
// unchanged config produces zero new rows.
func TestSeedAgents_ReRunIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	catalog := newTestCatalog(t, []string{"anthropic/claude-3.5-sonnet"})

	agents := []config.AgentDefinitionConfig{agent("agent-a", "anthropic/claude-3.5-sonnet")}
	require.NoError(t, seed.SeedAgents(ctx, db, nil, agents, catalog))
	require.NoError(t, seed.SeedAgents(ctx, db, nil, agents, catalog))
	require.NoError(t, seed.SeedAgents(ctx, db, nil, agents, catalog))

	rows := readVersions(t, ctx, db, "agent-a")
	assert.Len(t, rows, 1, "re-running the seeder against an unchanged config must not mint new version rows")
}

// TestSeedAgents_ChangedDefinition_MintsNewVersion_PriorVersionIntact
// proves a changed field (max_turns here) mints a new version row on the
// next seed and leaves the prior version's row byte-for-byte untouched --
// the property a session already pinned to that prior version (SCD2)
// depends on.
func TestSeedAgents_ChangedDefinition_MintsNewVersion_PriorVersionIntact(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	catalog := newTestCatalog(t, []string{"anthropic/claude-3.5-sonnet"})

	v1 := agent("agent-a", "anthropic/claude-3.5-sonnet")
	require.NoError(t, seed.SeedAgents(ctx, db, nil, []config.AgentDefinitionConfig{v1}, catalog))

	v2 := v1
	v2.MaxTurns = 50
	require.NoError(t, seed.SeedAgents(ctx, db, nil, []config.AgentDefinitionConfig{v2}, catalog))

	rows := readVersions(t, ctx, db, "agent-a")
	require.Len(t, rows, 2, "a changed definition must mint a NEW version row, not edit the existing one")
	assert.Equal(t, 1, rows[0].Version)
	assert.Equal(t, 100, rows[0].MaxTurns, "the prior version's row must be left exactly as it was seeded")
	assert.Equal(t, 2, rows[1].Version)
	assert.Equal(t, 50, rows[1].MaxTurns)

	// A third seed with v2 unchanged must not mint a version 3.
	require.NoError(t, seed.SeedAgents(ctx, db, nil, []config.AgentDefinitionConfig{v2}, catalog))
	rows = readVersions(t, ctx, db, "agent-a")
	assert.Len(t, rows, 2, "re-running with the latest version's config unchanged must stay idempotent at that version")
}

// TestSeedAgents_UnservedModel_FailsLoudly_NoHalfRow proves an entry
// naming a model the catalogue does not serve fails the whole run before
// writing any row -- even for an EARLIER, otherwise-good entry in the same
// config list (this file's/seed.go's "check-all-then-write-all" two-pass
// guarantee).
func TestSeedAgents_UnservedModel_FailsLoudly_NoHalfRow(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	catalog := newTestCatalog(t, []string{"anthropic/claude-3.5-sonnet"}) // "openai/not-served" is deliberately absent

	agents := []config.AgentDefinitionConfig{
		agent("agent-good", "anthropic/claude-3.5-sonnet"),
		agent("agent-bad", "openai/not-served"),
	}
	err := seed.SeedAgents(ctx, db, nil, agents, catalog)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent-bad")
	assert.Contains(t, err.Error(), "not served")

	assert.Empty(t, readVersions(t, ctx, db, "agent-good"), "a bad entry anywhere in the config must fail the whole run before ANY row is written, even for an earlier good entry")
	assert.Empty(t, readVersions(t, ctx, db, "agent-bad"))
}

// TestSeedAgents_CatalogueFetchFailure_FailsLoudly proves a provider
// catalogue that cannot be reached at all is surfaced as an error too
// (FR5's "fail closed" discipline, catalog.go), not silently treated as
// "every model is supported".
func TestSeedAgents_CatalogueFetchFailure_FailsLoudly(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	catalog := llm.NewCatalog(llm.NewClient("test-api-key", "http://127.0.0.1:1/unreachable"), time.Minute)

	agents := []config.AgentDefinitionConfig{agent("agent-a", "anthropic/claude-3.5-sonnet")}
	err := seed.SeedAgents(ctx, db, nil, agents, catalog)
	require.Error(t, err)
	assert.Empty(t, readVersions(t, ctx, db, "agent-a"))
}

// TestSeedAgents_ModelDefinition_CreatesRowAndResolvesFK proves an agent
// naming model_definition (not model) gets a model_definition row seeded
// with its provider preferences, and its agent_definition row's
// model_definition_id resolves to that row -- model stays NULL.
func TestSeedAgents_ModelDefinition_CreatesRowAndResolvesFK(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	catalog := newTestCatalog(t, []string{"anthropic/claude-sonnet-4.5"})

	allowFallbacks := false
	modelDefs := []config.ModelDefinitionConfig{{
		Name:  "sonnet-together-only",
		Model: "anthropic/claude-sonnet-4.5",
		Provider: config.ProviderPreferencesConfig{
			Only:           []string{"together"},
			AllowFallbacks: &allowFallbacks,
		},
	}}
	a := agent("agent-a", "")
	a.ModelDefinition = "sonnet-together-only"

	require.NoError(t, seed.SeedAgents(ctx, db, modelDefs, []config.AgentDefinitionConfig{a}, catalog))

	modelDefRow := readModelDefinition(t, ctx, db, "sonnet-together-only")
	require.NotNil(t, modelDefRow)
	assert.Equal(t, "anthropic/claude-sonnet-4.5", modelDefRow.Model)
	assert.JSONEq(t, `{"only":["together"],"allow_fallbacks":false}`, string(modelDefRow.Provider))

	rows := readVersions(t, ctx, db, "agent-a")
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].Model, "an agent seeded via model_definition must leave the model column NULL")
	require.NotNil(t, rows[0].ModelDefinitionID)
	assert.Equal(t, modelDefRow.ID, *rows[0].ModelDefinitionID)
}

// TestSeedAgents_ModelDefinition_ReRunIsIdempotent proves re-running with
// an unchanged model_definitions + agents config produces zero new rows
// in either table -- mirrors TestSeedAgents_ReRunIsIdempotent for the
// model_definition path.
func TestSeedAgents_ModelDefinition_ReRunIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	catalog := newTestCatalog(t, []string{"anthropic/claude-sonnet-4.5"})

	modelDefs := []config.ModelDefinitionConfig{{Name: "sonnet-default", Model: "anthropic/claude-sonnet-4.5"}}
	a := agent("agent-a", "")
	a.ModelDefinition = "sonnet-default"
	agents := []config.AgentDefinitionConfig{a}

	require.NoError(t, seed.SeedAgents(ctx, db, modelDefs, agents, catalog))
	require.NoError(t, seed.SeedAgents(ctx, db, modelDefs, agents, catalog))
	require.NoError(t, seed.SeedAgents(ctx, db, modelDefs, agents, catalog))

	rows := readVersions(t, ctx, db, "agent-a")
	assert.Len(t, rows, 1, "re-running against an unchanged model_definitions + agents config must not mint new agent_definition rows")
}

// TestSeedAgents_ModelDefinitionUnservedModel_FailsLoudly_NoHalfRow proves
// a model_definitions entry naming an unserved model fails the whole run
// before writing any row -- including the model_definition table itself.
func TestSeedAgents_ModelDefinitionUnservedModel_FailsLoudly_NoHalfRow(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	catalog := newTestCatalog(t, []string{"anthropic/claude-3.5-sonnet"}) // "openai/not-served" deliberately absent

	modelDefs := []config.ModelDefinitionConfig{{Name: "bad-def", Model: "openai/not-served"}}
	a := agent("agent-good", "anthropic/claude-3.5-sonnet")

	err := seed.SeedAgents(ctx, db, modelDefs, []config.AgentDefinitionConfig{a}, catalog)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad-def")
	assert.Contains(t, err.Error(), "not served")

	assert.Nil(t, readModelDefinition(t, ctx, db, "bad-def"), "a bad model_definitions entry must fail before ANY row is written")
	assert.Empty(t, readVersions(t, ctx, db, "agent-good"))
}
