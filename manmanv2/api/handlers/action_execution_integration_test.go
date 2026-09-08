//go:build integration

// FR8 live-at-execution guarantee (task #2093, plan #2080): ExecuteAction
// must fetch the action definition -- and its input fields -- fresh from
// the repository AT EXECUTION TIME, so action adds/edits/removes are
// immediately effective on running sessions with no session restart. This
// file protects that guarantee with tests rather than adding behavior
// (FR8 was verified true in code; a future regression to cached
// definitions fails these tests).
//
// Same precedent as the other integration targets: build-tagged
// `integration`, self-contained DDL, real Postgres via //libs/go/dbtest --
// plus a real RabbitMQ container (testcontainers core) because
// ExecuteAction publishes the rendered command and logs the execution
// either way; the rendered_command row is what the freshness assertions
// read.
//
// Run it explicitly (requires working Docker):
//
//	bazel test //manmanv2/api/handlers:action_execution_integration_test --test_output=all
package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

// executionSchema is self-contained DDL covering exactly the columns the
// session/game-config/sgc/action repositories touch, in the post-037
// action shape (deleted_at; no cascade on action_executions.action_id).
const executionSchema = `
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
		args_template TEXT,
		env_template JSONB DEFAULT '{}',
		entrypoint TEXT,
		command TEXT,
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
		port_bindings JSONB DEFAULT '{}',
		status VARCHAR(50) NOT NULL DEFAULT 'inactive',
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE sessions (
		session_id BIGSERIAL PRIMARY KEY,
		sgc_id BIGINT NOT NULL REFERENCES server_game_configs(sgc_id) ON DELETE CASCADE,
		started_at TIMESTAMPTZ,
		ended_at TIMESTAMPTZ,
		exit_code INTEGER,
		status VARCHAR(50) NOT NULL DEFAULT 'pending',
		created_at TIMESTAMPTZ DEFAULT NOW(),
		updated_at TIMESTAMPTZ DEFAULT NOW()
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

	CREATE TABLE action_executions (
		execution_id BIGSERIAL PRIMARY KEY,
		-- CASCADE: this file must work on the pre-037 stack (hard DELETE)
		-- and the post-037 stack (soft delete) alike; FR10's no-cascade
		-- retention property is asserted by 2092's own integration target,
		-- not here.
		action_id BIGINT NOT NULL REFERENCES action_definitions(action_id) ON DELETE CASCADE,
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

// startRabbitMQ starts a throwaway RabbitMQ container and returns an
// amqp:// URL for it (guest/guest). No broker module is vendored, so this
// uses the testcontainers core API directly.
func startRabbitMQ(ctx context.Context, t *testing.T) string {
	t.Helper()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "rabbitmq:3-alpine",
			ExposedPorts: []string{"5672/tcp"},
			WaitingFor:   wait.ForListeningPort("5672/tcp"),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start rabbitmq container: %v", err)
	}
	t.Cleanup(func() {
		_ = ctr.Terminate(context.Background())
	})
	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("rabbitmq host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "5672/tcp")
	if err != nil {
		t.Fatalf("rabbitmq port: %v", err)
	}
	return fmt.Sprintf("amqp://guest:guest@%s:%s/", host, port.Port())
}

// newExecutionHarness builds a fully-real ActionHandler stack: Postgres
// with the execution schema, real repositories, and a real CommandPublisher
// over a throwaway RabbitMQ (publishes succeed; replies time out, which the
// handler logs as a failed execution -- WITH the rendered command, which is
// exactly what these tests assert on).
func newExecutionHarness(t *testing.T) (*ActionHandler, *pgxpool.Pool, int64, int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	if _, err := db.Pool.Exec(ctx, executionSchema); err != nil {
		t.Fatalf("apply executionSchema: %v", err)
	}

	rmqURL := startRabbitMQ(ctx, t)
	conn, err := rmq.NewConnectionFromURL(rmqURL)
	if err != nil {
		t.Fatalf("rmq connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	pub, err := NewCommandPublisher(conn)
	if err != nil {
		t.Fatalf("NewCommandPublisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	handler := NewActionHandler(
		postgres.NewActionRepository(db.Pool),
		postgres.NewSessionRepository(db.Pool),
		postgres.NewServerGameConfigRepository(db.Pool),
		postgres.NewGameConfigRepository(db.Pool),
		pub,
	)

	// Seed: game -> game_config -> server -> sgc -> running session.
	var gameID, configID, serverID, sgcID, sessionID int64
	seed := func(q string, dest any, args ...any) {
		t.Helper()
		if err := db.Pool.QueryRow(ctx, q, args...).Scan(dest); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	seed(`INSERT INTO games (name) VALUES ('exec-game') RETURNING game_id`, &gameID)
	seed(`INSERT INTO game_configs (game_id, name, image) VALUES ($1, 'exec-config', 'image') RETURNING config_id`, &configID, gameID)
	seed(`INSERT INTO servers (name) VALUES ('exec-server') RETURNING server_id`, &serverID)
	seed(`INSERT INTO server_game_configs (server_id, game_config_id, status) VALUES ($1, $2, 'running') RETURNING sgc_id`, &sgcID, serverID, configID)
	seed(`INSERT INTO sessions (sgc_id, status, started_at) VALUES ($1, 'running', NOW()) RETURNING session_id`, &sessionID, sgcID)

	return handler, db.Pool, gameID, sessionID
}

// createGameAction inserts a game-level action and returns its id.
func createGameAction(t *testing.T, pool *pgxpool.Pool, gameID int64, name, template string) int64 {
	t.Helper()
	var actionID int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO action_definitions (definition_level, entity_id, name, label, command_template)
		 VALUES ('game', $1, $2, $2, $3) RETURNING action_id`,
		gameID, name, template,
	).Scan(&actionID); err != nil {
		t.Fatalf("create action %s: %v", name, err)
	}
	return actionID
}

// lastRenderedCommand returns the most recent execution row's rendered
// command for the action (any status -- the publish timeout path logs the
// render too, which is exactly the freshness surface under test).
func lastRenderedCommand(t *testing.T, pool *pgxpool.Pool, actionID int64) string {
	t.Helper()
	var cmd string
	if err := pool.QueryRow(context.Background(),
		`SELECT rendered_command FROM action_executions WHERE action_id = $1 ORDER BY execution_id DESC LIMIT 1`,
		actionID,
	).Scan(&cmd); err != nil {
		if err == sql.ErrNoRows {
			t.Fatalf("no execution row logged for action %d", actionID)
		}
		t.Fatalf("read execution: %v", err)
	}
	return cmd
}

// execute runs ExecuteAction and returns its response.
func execute(t *testing.T, h *ActionHandler, sessionID, actionID int64) *pb.ExecuteActionResponse {
	t.Helper()
	resp, err := h.ExecuteAction(context.Background(), &pb.ExecuteActionRequest{
		SessionId: sessionID,
		ActionId:  actionID,
	})
	if err != nil {
		t.Fatalf("ExecuteAction: %v", err)
	}
	return resp
}

// TestExecuteAction_EditImmediatelyEffective proves an edit to the
// definition is visible to the very next execution with no restart: the
// execution renders the CURRENT command template, not a cached copy. The
// red mutation (cache the Get result in the handler) makes this fail.
func TestExecuteAction_EditImmediatelyEffective(t *testing.T) {
	h, pool, gameID, sessionID := newExecutionHarness(t)
	actionID := createGameAction(t, pool, gameID, "say_version", "say v1")

	resp := execute(t, h, sessionID, actionID)
	if got := lastRenderedCommand(t, pool, actionID); got != "say v1" {
		t.Fatalf("first execution rendered %q, want say v1 (resp: %+v)", got, resp)
	}

	// Edit the definition (as the UI/API would) and execute again -- no
	// restart, no cache warm-up beyond the previous execution.
	if _, err := pool.Exec(context.Background(),
		`UPDATE action_definitions SET command_template = 'say v2' WHERE action_id = $1`, actionID,
	); err != nil {
		t.Fatalf("edit definition: %v", err)
	}

	execute(t, h, sessionID, actionID)
	if got := lastRenderedCommand(t, pool, actionID); got != "say v2" {
		t.Fatalf("FR8 regression: second execution rendered %q, want the EDITED say v2 -- definition reads are stale/cached", got)
	}
}

// TestExecuteAction_AddImmediatelyEffective proves an action created after
// the handler (and any prior execution) exists is immediately executable.
func TestExecuteAction_AddImmediatelyEffective(t *testing.T) {
	h, pool, gameID, sessionID := newExecutionHarness(t)

	// Warm up with a different action first, so any per-handler
	// definition cache would be exercised before the ADD.
	warm := createGameAction(t, pool, gameID, "warmup", "warmup")
	execute(t, h, sessionID, warm)

	added := createGameAction(t, pool, gameID, "newly_added", "hello new")
	execute(t, h, sessionID, added)
	if got := lastRenderedCommand(t, pool, added); got != "hello new" {
		t.Fatalf("FR8 regression: newly added action not immediately executable (rendered %q)", got)
	}
}

// TestExecuteAction_RemoveImmediatelyEffective proves a removed (soft-
// deleted) action is immediately non-executable: ExecuteAction must fail
// with NotFound, not execute a cached copy of the removed definition.
func TestExecuteAction_RemoveImmediatelyEffective(t *testing.T) {
	h, pool, gameID, sessionID := newExecutionHarness(t)
	actionID := createGameAction(t, pool, gameID, "doomed", "say goodbye")
	execute(t, h, sessionID, actionID)

	if err := postgres.NewActionRepository(pool).Delete(context.Background(), actionID); err != nil {
		t.Fatalf("delete action: %v", err)
	}

	_, err := h.ExecuteAction(context.Background(), &pb.ExecuteActionRequest{
		SessionId: sessionID,
		ActionId:  actionID,
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("FR8 regression: executing a removed action returned %v, want NotFound", err)
	}
}
