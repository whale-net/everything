//go:build integration

// Real-Postgres coverage for FR12/FR13/FR15 (task #2811, plan #2777):
// BackupConfigHandler.ListBackupConfigActions must resolve each attached
// Action's name/command_template through the action repository while
// keeping the legacy parallel arrays consistent with the new `items` field,
// and ReorderBackupConfigActions must return NotFound/InvalidArgument for
// an unknown config or a mismatched action_ids set. Real Postgres via
// //libs/go/dbtest, same precedent as action_execution_integration_test.go
// -- no RabbitMQ needed here since none of the exercised handler paths
// publish a command.
//
// Run it explicitly (requires working Docker):
//
//	bazel test //manmanv2/api/handlers:backup_config_actions_integration_test --test_output=all
package handlers

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// backupConfigActionsHandlerSchema is self-contained DDL covering
// action_definitions (+ its empty-by-default input tables) and the
// backup_config/backup_config_actions tables the handler touches.
const backupConfigActionsHandlerSchema = `
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
		UNIQUE (definition_level, entity_id, name)
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
		UNIQUE(action_id, name)
	);

	CREATE TABLE backup_configs (
		backup_config_id BIGSERIAL PRIMARY KEY,
		volume_id        BIGINT  NOT NULL,
		cadence_minutes  INT     NOT NULL CHECK (cadence_minutes > 0),
		backup_path      TEXT    NOT NULL,
		enabled          BOOLEAN NOT NULL DEFAULT true,
		last_backup_at   TIMESTAMP,
		created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		deleted_at       TIMESTAMP
	);

	CREATE TABLE backup_config_actions (
		backup_config_id BIGINT NOT NULL REFERENCES backup_configs(backup_config_id) ON DELETE CASCADE,
		action_id        BIGINT NOT NULL REFERENCES action_definitions(action_id) ON DELETE CASCADE,
		display_order    INT    NOT NULL DEFAULT 0,
		PRIMARY KEY (backup_config_id, action_id)
	);
`

func newBackupConfigActionsHandlerTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: backupConfigActionsHandlerSchema})
	return db.Pool
}

func newBackupConfigActionsHandler(pool *pgxpool.Pool) (*BackupConfigHandler, *postgres.BackupConfigRepository, *postgres.ActionRepository) {
	backupConfigRepo := postgres.NewBackupConfigRepository(pool)
	actionRepo := postgres.NewActionRepository(pool)
	h := NewBackupConfigHandler(backupConfigRepo, nil, nil, nil, nil, nil, actionRepo, nil, nil)
	return h, backupConfigRepo, actionRepo
}

func createHandlerTestBackupConfig(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO backup_configs (volume_id, cadence_minutes, backup_path)
		VALUES (1, 60, 'saves') RETURNING backup_config_id
	`).Scan(&id); err != nil {
		t.Fatalf("seed backup_config: %v", err)
	}
	return id
}

func createHandlerTestAction(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO action_definitions (definition_level, entity_id, name, label, command_template)
		VALUES ('game', 1, $1, $1, 'echo hi') RETURNING action_id
	`, name).Scan(&id); err != nil {
		t.Fatalf("seed action %q: %v", name, err)
	}
	return id
}

// TestListBackupConfigActions_FillsNamesAndKeepsLegacyArraysConsistent is
// the FR12 green case: items[] carries each attached action's name and
// command_template, in the same order as the legacy action_ids/display_orders
// arrays.
func TestListBackupConfigActions_FillsNamesAndKeepsLegacyArraysConsistent(t *testing.T) {
	ctx := context.Background()
	pool := newBackupConfigActionsHandlerTestDB(t)
	h, backupConfigRepo, _ := newBackupConfigActionsHandler(pool)

	backupConfigID := createHandlerTestBackupConfig(t, ctx, pool)
	action1 := createHandlerTestAction(t, ctx, pool, "stop_server")
	action2 := createHandlerTestAction(t, ctx, pool, "flush_saves")

	if err := backupConfigRepo.AddAction(ctx, backupConfigID, action1, 0); err != nil {
		t.Fatalf("attach action1: %v", err)
	}
	if err := backupConfigRepo.AddAction(ctx, backupConfigID, action2, 1); err != nil {
		t.Fatalf("attach action2: %v", err)
	}

	resp, err := h.ListBackupConfigActions(ctx, &pb.ListBackupConfigActionsRequest{BackupConfigId: backupConfigID})
	if err != nil {
		t.Fatalf("ListBackupConfigActions: %v", err)
	}

	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(resp.Items))
	}
	if len(resp.ActionIds) != 2 || len(resp.DisplayOrders) != 2 {
		t.Fatalf("expected legacy arrays of length 2, got %d ids / %d orders", len(resp.ActionIds), len(resp.DisplayOrders))
	}
	for i, item := range resp.Items {
		if item.ActionId != resp.ActionIds[i] {
			t.Errorf("position %d: items[].action_id=%d does not match legacy action_ids[]=%d", i, item.ActionId, resp.ActionIds[i])
		}
		if item.DisplayOrder != resp.DisplayOrders[i] {
			t.Errorf("position %d: items[].display_order=%d does not match legacy display_orders[]=%d", i, item.DisplayOrder, resp.DisplayOrders[i])
		}
	}
	if resp.Items[0].Name != "stop_server" || resp.Items[0].CommandTemplate != "echo hi" {
		t.Errorf("expected first item to carry action1's name/template, got %+v", resp.Items[0])
	}
	if resp.Items[1].Name != "flush_saves" {
		t.Errorf("expected second item to carry action2's name, got %+v", resp.Items[1])
	}
}

// TestListBackupConfigActions_SoftDeletedActionAppearsWithEmptyName: an
// attached action whose definition has since been soft-deleted still
// appears in the list -- with an empty name/template -- rather than being
// dropped.
func TestListBackupConfigActions_SoftDeletedActionAppearsWithEmptyName(t *testing.T) {
	ctx := context.Background()
	pool := newBackupConfigActionsHandlerTestDB(t)
	h, backupConfigRepo, _ := newBackupConfigActionsHandler(pool)

	backupConfigID := createHandlerTestBackupConfig(t, ctx, pool)
	actionID := createHandlerTestAction(t, ctx, pool, "stop_server")
	if err := backupConfigRepo.AddAction(ctx, backupConfigID, actionID, 0); err != nil {
		t.Fatalf("attach action: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE action_definitions SET deleted_at = NOW() WHERE action_id = $1`, actionID); err != nil {
		t.Fatalf("soft-delete action: %v", err)
	}

	resp, err := h.ListBackupConfigActions(ctx, &pb.ListBackupConfigActionsRequest{BackupConfigId: backupConfigID})
	if err != nil {
		t.Fatalf("ListBackupConfigActions: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("expected soft-deleted action's attachment to still appear, got %d items", len(resp.Items))
	}
	if resp.Items[0].ActionId != actionID {
		t.Errorf("expected item to reference action_id %d, got %d", actionID, resp.Items[0].ActionId)
	}
	if resp.Items[0].Name != "" || resp.Items[0].CommandTemplate != "" {
		t.Errorf("expected empty name/template for a soft-deleted action, got %+v", resp.Items[0])
	}
}

// TestAddBackupConfigAction_AtChosenPositionReordersList is FR13's
// "add at a chosen position" case. AddBackupConfigAction sets exactly the
// display_order it's given -- it does not shift other rows out of the way
// -- so a caller inserting into an occupied slot first frees it with its
// own explicit-order AddBackupConfigAction call on the displaced action
// (matching backup_config.go's handler comment: "a caller placing an
// action mid-list should use the position it currently sees"). The
// resulting list reflects the dense order.
func TestAddBackupConfigAction_AtChosenPositionReordersList(t *testing.T) {
	ctx := context.Background()
	pool := newBackupConfigActionsHandlerTestDB(t)
	h, backupConfigRepo, _ := newBackupConfigActionsHandler(pool)

	backupConfigID := createHandlerTestBackupConfig(t, ctx, pool)
	first := createHandlerTestAction(t, ctx, pool, "first")
	second := createHandlerTestAction(t, ctx, pool, "second")
	middle := createHandlerTestAction(t, ctx, pool, "middle")

	if err := backupConfigRepo.AddAction(ctx, backupConfigID, first, 0); err != nil {
		t.Fatalf("attach first: %v", err)
	}
	if err := backupConfigRepo.AddAction(ctx, backupConfigID, second, 1); err != nil {
		t.Fatalf("attach second: %v", err)
	}

	// Free position 1 by bumping the current occupant to 2, then attach
	// the new action at the now-free position 1.
	if _, err := h.AddBackupConfigAction(ctx, &pb.AddBackupConfigActionRequest{
		BackupConfigId: backupConfigID,
		ActionId:       second,
		DisplayOrder:   2,
	}); err != nil {
		t.Fatalf("AddBackupConfigAction bump second to position 2: %v", err)
	}
	if _, err := h.AddBackupConfigAction(ctx, &pb.AddBackupConfigActionRequest{
		BackupConfigId: backupConfigID,
		ActionId:       middle,
		DisplayOrder:   1,
	}); err != nil {
		t.Fatalf("AddBackupConfigAction at position 1: %v", err)
	}

	resp, err := h.ListBackupConfigActions(ctx, &pb.ListBackupConfigActionsRequest{BackupConfigId: backupConfigID})
	if err != nil {
		t.Fatalf("ListBackupConfigActions: %v", err)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("expected 3 items after add-at-position, got %d", len(resp.Items))
	}
	got := []int64{resp.Items[0].ActionId, resp.Items[1].ActionId, resp.Items[2].ActionId}
	want := []int64{first, middle, second}
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("expected order %v after inserting %q at position 1, got %v", want, "middle", got)
	}
	gotOrders := []int32{resp.DisplayOrders[0], resp.DisplayOrders[1], resp.DisplayOrders[2]}
	if gotOrders[0] != 0 || gotOrders[1] != 1 || gotOrders[2] != 2 {
		t.Errorf("expected dense display_orders [0 1 2], got %v", gotOrders)
	}
}

// TestReorderBackupConfigActions_NotFoundForUnknownConfig
func TestReorderBackupConfigActions_NotFoundForUnknownConfig(t *testing.T) {
	ctx := context.Background()
	pool := newBackupConfigActionsHandlerTestDB(t)
	h, _, _ := newBackupConfigActionsHandler(pool)

	_, err := h.ReorderBackupConfigActions(ctx, &pb.ReorderBackupConfigActionsRequest{
		BackupConfigId: 999999,
		ActionIds:      []int64{1, 2},
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound for an unknown backup_config_id, got %v", err)
	}
}

// TestReorderBackupConfigActions_InvalidArgumentForMismatchedSet
func TestReorderBackupConfigActions_InvalidArgumentForMismatchedSet(t *testing.T) {
	ctx := context.Background()
	pool := newBackupConfigActionsHandlerTestDB(t)
	h, backupConfigRepo, _ := newBackupConfigActionsHandler(pool)

	backupConfigID := createHandlerTestBackupConfig(t, ctx, pool)
	a1 := createHandlerTestAction(t, ctx, pool, "a1")
	a2 := createHandlerTestAction(t, ctx, pool, "a2")
	if err := backupConfigRepo.AddAction(ctx, backupConfigID, a1, 0); err != nil {
		t.Fatalf("attach a1: %v", err)
	}
	if err := backupConfigRepo.AddAction(ctx, backupConfigID, a2, 1); err != nil {
		t.Fatalf("attach a2: %v", err)
	}

	// Omits a2 -- a reorder must never silently drop an attachment.
	_, err := h.ReorderBackupConfigActions(ctx, &pb.ReorderBackupConfigActionsRequest{
		BackupConfigId: backupConfigID,
		ActionIds:      []int64{a1},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for a reorder omitting an attached action, got %v", err)
	}

	// Duplicate entry -- also rejected even though, as a set, it still
	// covers every attached action id (it doesn't cover a2 at all here).
	_, err = h.ReorderBackupConfigActions(ctx, &pb.ReorderBackupConfigActionsRequest{
		BackupConfigId: backupConfigID,
		ActionIds:      []int64{a1, a1},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for a reorder with a duplicate action_id, got %v", err)
	}
}

// TestReorderBackupConfigActions_ChangesOrder is the FR15 green case at the
// handler layer: a valid permutation is accepted and a subsequent list call
// reflects the new order.
func TestReorderBackupConfigActions_ChangesOrder(t *testing.T) {
	ctx := context.Background()
	pool := newBackupConfigActionsHandlerTestDB(t)
	h, backupConfigRepo, _ := newBackupConfigActionsHandler(pool)

	backupConfigID := createHandlerTestBackupConfig(t, ctx, pool)
	a1 := createHandlerTestAction(t, ctx, pool, "a1")
	a2 := createHandlerTestAction(t, ctx, pool, "a2")
	if err := backupConfigRepo.AddAction(ctx, backupConfigID, a1, 0); err != nil {
		t.Fatalf("attach a1: %v", err)
	}
	if err := backupConfigRepo.AddAction(ctx, backupConfigID, a2, 1); err != nil {
		t.Fatalf("attach a2: %v", err)
	}

	if _, err := h.ReorderBackupConfigActions(ctx, &pb.ReorderBackupConfigActionsRequest{
		BackupConfigId: backupConfigID,
		ActionIds:      []int64{a2, a1},
	}); err != nil {
		t.Fatalf("ReorderBackupConfigActions: %v", err)
	}

	resp, err := h.ListBackupConfigActions(ctx, &pb.ListBackupConfigActionsRequest{BackupConfigId: backupConfigID})
	if err != nil {
		t.Fatalf("ListBackupConfigActions: %v", err)
	}
	if len(resp.ActionIds) != 2 || resp.ActionIds[0] != a2 || resp.ActionIds[1] != a1 {
		t.Fatalf("expected reordered [a2, a1], got %v", resp.ActionIds)
	}
}
