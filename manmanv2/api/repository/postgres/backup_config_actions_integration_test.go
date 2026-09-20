//go:build integration

// Real-Postgres coverage for FR15 (task #2811, plan #2777):
// BackupConfigRepository.ReorderActions must rewrite display_order to a
// dense 0..n-1 sequence matching the caller's requested order, and must
// reject -- with no partial write -- any actionIDs set that isn't exactly a
// permutation of the config's currently attached actions. Same precedent as
// action_integration_test.go: build-tagged `integration`, self-contained
// DDL, run explicitly with Docker.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:backup_config_actions_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
)

// backupConfigActionsSchema is self-contained DDL for exactly the tables
// ReorderActions/ListActions touch: backup_configs and backup_config_actions,
// plus a minimal action_definitions stand-in to satisfy the FK.
const backupConfigActionsSchema = `
	CREATE TABLE action_definitions (
		action_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(100) NOT NULL,
		command_template TEXT NOT NULL
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

func newBackupConfigActionsTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: backupConfigActionsSchema})
	return db.Pool
}

// seedBackupConfigWithActions creates one backup_config row and n attached
// actions (in id order, display_order 0..n-1) and returns the action ids in
// attachment order.
func seedBackupConfigWithActions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, n int) (backupConfigID int64, actionIDs []int64) {
	t.Helper()
	repo := NewBackupConfigRepository(pool)

	if err := pool.QueryRow(ctx, `
		INSERT INTO backup_configs (volume_id, cadence_minutes, backup_path)
		VALUES (1, 60, 'saves') RETURNING backup_config_id
	`).Scan(&backupConfigID); err != nil {
		t.Fatalf("seed backup_config: %v", err)
	}

	for i := 0; i < n; i++ {
		var actionID int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO action_definitions (name, command_template)
			VALUES ($1, 'noop') RETURNING action_id
		`, actionName(i)).Scan(&actionID); err != nil {
			t.Fatalf("seed action %d: %v", i, err)
		}
		if err := repo.AddAction(ctx, backupConfigID, actionID, i); err != nil {
			t.Fatalf("attach action %d: %v", i, err)
		}
		actionIDs = append(actionIDs, actionID)
	}
	return backupConfigID, actionIDs
}

func actionName(i int) string {
	return "action_" + string(rune('a'+i))
}

// assertActionOrder fails the test unless ListActions returns exactly the
// expected action ids, in the expected order, with dense 0..n-1 display_order.
func assertActionOrder(t *testing.T, ctx context.Context, repo *BackupConfigRepository, backupConfigID int64, want []int64) {
	t.Helper()
	got, err := repo.ListActions(ctx, backupConfigID)
	if err != nil {
		t.Fatalf("ListActions: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d actions, got %d (%v)", len(want), len(got), got)
	}
	for i, a := range got {
		if a.ActionID != want[i] {
			t.Errorf("position %d: expected action_id %d, got %d", i, want[i], a.ActionID)
		}
		if a.DisplayOrder != i {
			t.Errorf("position %d: expected dense display_order %d, got %d", i, i, a.DisplayOrder)
		}
	}
}

// TestReorderActions_RewritesDisplayOrderDensely is the FR15 green case: a
// full permutation of the attached-action set changes execution order and
// ListActions reflects it immediately.
func TestReorderActions_RewritesDisplayOrderDensely(t *testing.T) {
	ctx := context.Background()
	pool := newBackupConfigActionsTestDB(t)
	repo := NewBackupConfigRepository(pool)

	backupConfigID, actionIDs := seedBackupConfigWithActions(t, ctx, pool, 3)
	assertActionOrder(t, ctx, repo, backupConfigID, actionIDs)

	reversed := []int64{actionIDs[2], actionIDs[0], actionIDs[1]}
	if err := repo.ReorderActions(ctx, backupConfigID, reversed); err != nil {
		t.Fatalf("ReorderActions: %v", err)
	}
	assertActionOrder(t, ctx, repo, backupConfigID, reversed)
}

// TestReorderActions_MissingActionRejectedNoPartialWrite is the FR15 red
// case: a reorder request that omits one of the config's currently attached
// actions must be rejected entirely, leaving the stored order untouched --
// not partially applied to the actions it did mention.
func TestReorderActions_MissingActionRejectedNoPartialWrite(t *testing.T) {
	ctx := context.Background()
	pool := newBackupConfigActionsTestDB(t)
	repo := NewBackupConfigRepository(pool)

	backupConfigID, actionIDs := seedBackupConfigWithActions(t, ctx, pool, 3)

	// Omits actionIDs[2]; also would move [0] and [1] earlier -- if the
	// rejection ran after touching rows, those two would show reordered.
	incomplete := []int64{actionIDs[1], actionIDs[0]}
	if err := repo.ReorderActions(ctx, backupConfigID, incomplete); err == nil {
		t.Fatal("expected error for a reorder request omitting an attached action, got nil")
	}
	assertActionOrder(t, ctx, repo, backupConfigID, actionIDs)
}

// TestReorderActions_UnknownActionRejectedNoPartialWrite: an action_id not
// attached to this config at all (never mind a different config) is
// rejected the same way.
func TestReorderActions_UnknownActionRejectedNoPartialWrite(t *testing.T) {
	ctx := context.Background()
	pool := newBackupConfigActionsTestDB(t)
	repo := NewBackupConfigRepository(pool)

	backupConfigID, actionIDs := seedBackupConfigWithActions(t, ctx, pool, 2)

	var strayActionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO action_definitions (name, command_template) VALUES ('stray', 'noop')
		RETURNING action_id
	`).Scan(&strayActionID); err != nil {
		t.Fatalf("seed stray action: %v", err)
	}

	bogus := []int64{actionIDs[0], strayActionID}
	if err := repo.ReorderActions(ctx, backupConfigID, bogus); err == nil {
		t.Fatal("expected error for a reorder request naming an unattached action, got nil")
	}
	assertActionOrder(t, ctx, repo, backupConfigID, actionIDs)
}

// TestReorderActions_DuplicateRejectedNoPartialWrite: a duplicated
// action_id in the request is rejected even though, as a set, it still
// covers every attached action.
func TestReorderActions_DuplicateRejectedNoPartialWrite(t *testing.T) {
	ctx := context.Background()
	pool := newBackupConfigActionsTestDB(t)
	repo := NewBackupConfigRepository(pool)

	backupConfigID, actionIDs := seedBackupConfigWithActions(t, ctx, pool, 2)

	dup := []int64{actionIDs[0], actionIDs[0]}
	if err := repo.ReorderActions(ctx, backupConfigID, dup); err == nil {
		t.Fatal("expected error for a reorder request with a duplicate action_id, got nil")
	}
	assertActionOrder(t, ctx, repo, backupConfigID, actionIDs)
}
