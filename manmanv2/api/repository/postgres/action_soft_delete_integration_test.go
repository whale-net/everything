//go:build integration

// Real-Postgres coverage for FR9 + FR10 (task #2092, plan #2080):
// ActionRepository.Delete is a SOFT delete that retains action_executions
// history, soft-deleted definitions vanish from listings/lookup (and so
// cannot be executed), and re-creating an action at the same
// (definition_level, entity_id, name) restores the soft-deleted row --
// identically via the repository Create (UI/API writer) and via the seed
// scripts' raw ON CONFLICT upsert. Same precedent as
// pending_restart_integration_test.go / action_integration_test.go:
// build-tagged `integration`, self-contained DDL, run explicitly with
// Docker.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:action_soft_delete_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	manman "github.com/whale-net/everything/manmanv2/models"
)

// softDeleteSchema is self-contained DDL for the action tables in their
// POST-037 shape (deleted_at on definitions; plain, non-cascading FK from
// action_executions) plus the minimal parent chain executions need.
const softDeleteSchema = `
	CREATE TABLE games (
		game_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE,
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE game_configs (
		config_id BIGSERIAL PRIMARY KEY,
		game_id BIGINT NOT NULL REFERENCES games(game_id) ON DELETE CASCADE,
		name VARCHAR(255) NOT NULL,
		image VARCHAR(500) NOT NULL,
		env_template JSONB DEFAULT '{}',
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE servers (
		server_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE,
		status VARCHAR(50) NOT NULL DEFAULT 'offline',
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE server_game_configs (
		sgc_id BIGSERIAL PRIMARY KEY,
		server_id BIGINT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
		game_config_id BIGINT NOT NULL REFERENCES game_configs(config_id) ON DELETE CASCADE,
		status VARCHAR(50) NOT NULL DEFAULT 'inactive',
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE sessions (
		session_id BIGSERIAL PRIMARY KEY,
		sgc_id BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
		status VARCHAR(50) NOT NULL DEFAULT 'pending',
		created_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE action_definitions (
		action_id BIGSERIAL PRIMARY KEY,
		definition_level VARCHAR(50) NOT NULL,
		entity_id BIGINT NOT NULL,
		name VARCHAR(100) NOT NULL,
		label VARCHAR(200) NOT NULL,
		description TEXT,
		command_template TEXT NOT NULL,
		display_order INT NOT NULL DEFAULT 0,
		group_name VARCHAR(100),
		button_style VARCHAR(50) DEFAULT 'primary',
		icon VARCHAR(100),
		requires_confirmation BOOLEAN DEFAULT false,
		confirmation_message TEXT,
		enabled BOOLEAN DEFAULT true,
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW(),
		deleted_at TIMESTAMPTZ,
		UNIQUE (definition_level, entity_id, name),
		CHECK (name ~ '^[a-z0-9_]+$'),
		CHECK (definition_level IN ('game', 'game_config', 'server_game_config')),
		CHECK (button_style IN ('primary', 'secondary', 'success', 'danger', 'warning', 'info', 'light', 'dark'))
	);

	CREATE TABLE action_input_fields (
		field_id BIGSERIAL PRIMARY KEY,
		action_id BIGINT NOT NULL REFERENCES action_definitions(action_id) ON DELETE CASCADE,
		name VARCHAR(100) NOT NULL,
		label VARCHAR(200) NOT NULL,
		field_type VARCHAR(50) NOT NULL,
		required BOOLEAN DEFAULT false,
		placeholder TEXT,
		help_text TEXT,
		default_value TEXT,
		display_order INT NOT NULL DEFAULT 0,
		pattern VARCHAR(500),
		min_value NUMERIC,
		max_value NUMERIC,
		min_length INT,
		max_length INT,
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW(),
		UNIQUE(action_id, name),
		CHECK (name ~ '^[a-z0-9_]+$')
	);

	CREATE TABLE action_input_options (
		option_id BIGSERIAL PRIMARY KEY,
		field_id BIGINT NOT NULL REFERENCES action_input_fields(field_id) ON DELETE CASCADE,
		value VARCHAR(255) NOT NULL,
		label VARCHAR(200) NOT NULL,
		is_default BOOLEAN DEFAULT false,
		display_order INT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW(),
		UNIQUE(field_id, value)
	);

	-- FR10 (migration 037): NO ON DELETE CASCADE on action_id.
	CREATE TABLE action_executions (
		execution_id BIGSERIAL PRIMARY KEY,
		action_id BIGINT NOT NULL REFERENCES action_definitions(action_id),
		session_id BIGINT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
		triggered_by VARCHAR(200),
		input_values JSONB,
		rendered_command TEXT NOT NULL,
		status VARCHAR(50) NOT NULL DEFAULT 'success',
		error_message TEXT,
		executed_at TIMESTAMPTZ DEFAULT NOW(),
		CHECK (status IN ('success', 'failed', 'validation_error'))
	);
`

func newSoftDeleteRepo(t *testing.T) (*ActionRepository, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	if _, err := db.Pool.Exec(ctx, softDeleteSchema); err != nil {
		t.Fatalf("apply softDeleteSchema: %v", err)
	}
	return &ActionRepository{db: db.Pool}, db.Pool
}

// seedActionWithExecution inserts a definition (with one field and one
// option) plus one execution row and returns the definition id, session id,
// and execution id.
func seedActionWithExecution(t *testing.T, pool *pgxpool.Pool, level string, entityID int64, name string) (int64, int64, int64) {
	t.Helper()
	ctx := context.Background()

	var gameID, configID, serverID, sgcID, sessionID int64
	mustQueryRow(t, pool, `INSERT INTO games (name) VALUES ('sd-game') RETURNING game_id`, []any{&gameID})
	mustQueryRow(t, pool, `INSERT INTO game_configs (game_id, name, image) VALUES ($1, 'sd-config', 'img') RETURNING config_id`, []any{&configID}, gameID)
	mustQueryRow(t, pool, `INSERT INTO servers (name) VALUES ('sd-server') RETURNING server_id`, []any{&serverID})
	mustQueryRow(t, pool, `INSERT INTO server_game_configs (server_id, game_config_id) VALUES ($1, $2) RETURNING sgc_id`, []any{&sgcID}, serverID, configID)
	mustQueryRow(t, pool, `INSERT INTO sessions (sgc_id, status) VALUES ($1, 'running') RETURNING session_id`, []any{&sessionID}, sgcID)

	var actionID int64
	mustQueryRow(t, pool, `
		INSERT INTO action_definitions (definition_level, entity_id, name, label, command_template)
		VALUES ($1, $2, $3, 'Seed Action', 'say hi') RETURNING action_id`,
		[]any{&actionID}, level, entityID, name)

	var fieldID int64
	mustQueryRow(t, pool, `
		INSERT INTO action_input_fields (action_id, name, label, field_type)
		VALUES ($1, 'reason', 'Reason', 'text') RETURNING field_id`,
		[]any{&fieldID}, actionID)
	if _, err := pool.Exec(ctx, `INSERT INTO action_input_options (field_id, value, label) VALUES ($1, 'a', 'Option A')`, fieldID); err != nil {
		t.Fatalf("seed option: %v", err)
	}

	var executionID int64
	mustQueryRow(t, pool, `
		INSERT INTO action_executions (action_id, session_id, triggered_by, rendered_command, status)
		VALUES ($1, $2, 'tester', 'say hi', 'success') RETURNING execution_id`,
		[]any{&executionID}, actionID, sessionID)

	return actionID, sessionID, executionID
}

func mustQueryRow(t *testing.T, pool *pgxpool.Pool, query string, dest []any, args ...any) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), query, args...).Scan(dest...); err != nil {
		t.Fatalf("seed query %q: %v", query, err)
	}
}

// TestActionDelete_SoftDeletesRetainingExecutions is the FR9/FR10 red case
// turned green: delete a definition that has executions -> the definition
// disappears from listings and lookup (so it cannot be executed) while the
// execution rows remain retained and queryable. Under the legacy hard
// DELETE this test fails on the retained-history assertion (and, post-037,
// the hard DELETE itself is rejected by the plain FK).
func TestActionDelete_SoftDeletesRetainingExecutions(t *testing.T) {
	repo, pool := newSoftDeleteRepo(t)
	actionID, _, executionID := seedActionWithExecution(t, pool, "game", 1, "save_game")

	if err := repo.Delete(context.Background(), actionID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Gone from every listing...
	byLevel, err := repo.ListByLevel(context.Background(), "game", 1)
	if err != nil {
		t.Fatalf("ListByLevel: %v", err)
	}
	if len(byLevel) != 0 {
		t.Fatalf("soft-deleted definition still listed: %d rows", len(byLevel))
	}
	byGame, err := repo.ListByGame(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListByGame: %v", err)
	}
	if len(byGame) != 0 {
		t.Fatalf("soft-deleted definition still in game actions: %d rows", len(byGame))
	}

	// ...and not executable (lookup treats it as absent).
	if _, _, err := repo.Get(context.Background(), actionID); err == nil {
		t.Fatalf("Get on soft-deleted definition must fail, got nil error")
	}

	// Execution history retained and queryable.
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM action_executions WHERE execution_id = $1`, executionID,
	).Scan(&count); err != nil {
		t.Fatalf("count executions: %v", err)
	}
	if count != 1 {
		t.Fatalf("execution row was removed by definition deletion (FR10 violation)")
	}

	// The row itself still exists, marked deleted.
	var deletedAt *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT deleted_at FROM action_definitions WHERE action_id = $1`, actionID,
	).Scan(&deletedAt); err != nil {
		t.Fatalf("fetch soft-deleted row: %v", err)
	}
	if deletedAt == nil {
		t.Fatalf("expected deleted_at to be set after Delete")
	}
}

// TestActionCreate_RestoresSoftDeletedRow proves the FR10 re-create
// semantics for the API/UI writer: creating an action at the same
// (definition_level, entity_id, name) as a soft-deleted one restores that
// row (same action_id), live, listed, and executable.
func TestActionCreate_RestoresSoftDeletedRow(t *testing.T) {
	repo, pool := newSoftDeleteRepo(t)
	actionID, _, _ := seedActionWithExecution(t, pool, "game", 1, "save_game")

	if err := repo.Delete(context.Background(), actionID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Re-create via the repository (the UI/API writer path).
	desc := "recreated"
	created, err := repo.Create(context.Background(), &manman.ActionDefinition{
		DefinitionLevel: "game",
		EntityID:        1,
		Name:            "save_game",
		Label:           "Save Game",
		Description:     &desc,
		CommandTemplate: "save",
		ButtonStyle:     "primary",
		Enabled:         true,
	}, nil, nil)
	if err != nil {
		t.Fatalf("Create after soft-delete: %v", err)
	}
	if created != actionID {
		t.Fatalf("re-create returned action_id %d, want the soft-deleted row's %d restored", created, actionID)
	}

	// Live again: listed and lookup-able (=> executable).
	byLevel, err := repo.ListByLevel(context.Background(), "game", 1)
	if err != nil {
		t.Fatalf("ListByLevel: %v", err)
	}
	if len(byLevel) != 1 || byLevel[0].ActionID != actionID {
		t.Fatalf("re-created definition not listed correctly: %+v", byLevel)
	}
	if got, _, err := repo.Get(context.Background(), actionID); err != nil || got == nil {
		t.Fatalf("Get on restored definition failed: %v", err)
	}

	// Its deleted_at is cleared.
	var deletedAt *time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT deleted_at FROM action_definitions WHERE action_id = $1`, actionID,
	).Scan(&deletedAt); err != nil {
		t.Fatalf("fetch restored row: %v", err)
	}
	if deletedAt != nil {
		t.Fatalf("restored row still has deleted_at set: %v", deletedAt)
	}

	// The re-created definition is fully specified like a UI re-create:
	// repository Create owns the field set (wipe + re-insert), so the
	// re-created action carries the caller-provided fields/options and is
	// immediately listed and executable.
	fields := []*manman.ActionInputField{{
		ActionID:     actionID,
		Name:         "reason",
		Label:        "Reason",
		FieldType:    "text",
		DisplayOrder: 0,
	}}
	if err := repo.Update(context.Background(), &manman.ActionDefinition{
		ActionID:        actionID,
		DefinitionLevel: "game",
		EntityID:        1,
		Name:            "save_game",
		Label:           "Save Game",
		CommandTemplate: "save",
		ButtonStyle:     "primary",
		Enabled:         true,
	}, fields, []*manman.ActionInputOption{{FieldID: 0, Value: "a", Label: "Option A"}}); err != nil {
		t.Fatalf("Update after re-create: %v", err)
	}
	got, gotFields, err := repo.Get(context.Background(), actionID)
	if err != nil {
		t.Fatalf("Get fields: %v", err)
	}
	if got == nil || len(gotFields) != 1 || len(gotFields[0].Options) != 1 {
		t.Fatalf("re-created definition not fully restored with caller-provided fields: %+v", gotFields)
	}
}

// TestActionSeedUpsertPath_RestoresSoftDeletedRow proves the seed-script
// writer gets the identical FR10 outcome: the raw
// ON CONFLICT (definition_level, entity_id, name) DO UPDATE SET
// deleted_at = NULL upsert used by seed_actions.sh revives a soft-deleted
// definition without touching a live row's other columns.
func TestActionSeedUpsertPath_RestoresSoftDeletedRow(t *testing.T) {
	repo, pool := newSoftDeleteRepo(t)
	actionID, _, _ := seedActionWithExecution(t, pool, "game", 1, "save_game")

	if err := repo.Delete(context.Background(), actionID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Seed-path upsert (same shape as manmanv2/scripts/seed_actions.sh).
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO action_definitions (definition_level, entity_id, name, label, description, command_template)
		VALUES ('game', 1, 'save_game', 'Save Game (seed)', NULL, 'save')
		ON CONFLICT (definition_level, entity_id, name) DO UPDATE SET
			deleted_at = NULL`,
	); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	// Same row restored (not a duplicate), live and listed.
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM action_definitions WHERE definition_level = 'game' AND entity_id = 1 AND name = 'save_game'`,
	).Scan(&count); err != nil {
		t.Fatalf("count definitions: %v", err)
	}
	if count != 1 {
		t.Fatalf("seed upsert created a duplicate row (count = %d), want the soft-deleted row revived in place", count)
	}

	byLevel, err := repo.ListByLevel(context.Background(), "game", 1)
	if err != nil {
		t.Fatalf("ListByLevel: %v", err)
	}
	if len(byLevel) != 1 || byLevel[0].ActionID != actionID {
		t.Fatalf("seed-restored definition not listed correctly: %+v", byLevel)
	}
}
