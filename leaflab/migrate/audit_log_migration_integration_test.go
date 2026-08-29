//go:build integration

// Real-Postgres integration coverage for migration 016_audit_log's
// append-only enforcement (FR8, NFR6.2, NFR6.3) -- the half of this task's
// Testing section that can only be proven against a real migration file,
// not a hermetic test schema: the BEFORE UPDATE OR DELETE trigger genuinely
// blocks UPDATE/DELETE (not just the REVOKE, which layers on top of it),
// and audit_log carries no valid_to column. Requires a real TimescaleDB
// image (not plain postgres), same as
// //leaflab/migrate:ownership_migration_integration_test, since migration
// 001 creates a hypertable. See //libs/go/dbtest's README for how to run
// tests like this one.
package main_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newPreAuditLogDB migrates up through 015 (the last migration before
// 016_audit_log) and returns the runner (still positioned at 015) plus a
// pool connected as the database's owning role -- the same role identity
// migration 016's REVOKE targets (CURRENT_USER at migration time), per
// 016_audit_log.up.sql's doc comment that DB_USER and the migration role
// are the same identity in this deployment.
func newPreAuditLogDB(t *testing.T) (*migrate.Runner, *dbtest.Postgres) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: timescaleImage})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Migrate(15); err != nil {
		t.Fatalf("migrate to version 15 (pre-audit-log): %v", err)
	}

	return runner, db
}

// TestAuditLogMigration_AppendOnlyTriggerBlocksUpdateAndDelete is the
// load-bearing test named in this task's Testing section: after migration
// 016 applies, both UPDATE and DELETE against audit_log fail when executed
// as the application role (here, the database's owning role -- the same
// identity REVOKE UPDATE, DELETE ON audit_log FROM CURRENT_USER targeted at
// migration time).
func TestAuditLogMigration_AppendOnlyTriggerBlocksUpdateAndDelete(t *testing.T) {
	runner, db := newPreAuditLogDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 016: %v", err)
	}

	var auditID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO audit_log (actor_subject, actor_kind, action, entity_kind)
		VALUES ($1, $2, $3, $4) RETURNING audit_id
	`, "test-actor", "human", "TestAction", "test-entity").Scan(&auditID); err != nil {
		t.Fatalf("test setup: insert audit_log row: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `UPDATE audit_log SET action = 'tampered' WHERE audit_id = $1`, auditID); err == nil {
		t.Error("UPDATE audit_log succeeded, want it refused (NFR6.2/NFR6.3 append-only)")
	}

	if _, err := db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE audit_id = $1`, auditID); err == nil {
		t.Error("DELETE FROM audit_log succeeded, want it refused (NFR6.2/NFR6.3 append-only)")
	}

	// The row must still be exactly as inserted -- neither statement above
	// may have partially applied.
	var action string
	if err := db.Pool.QueryRow(ctx, `SELECT action FROM audit_log WHERE audit_id = $1`, auditID).Scan(&action); err != nil {
		t.Fatalf("read back audit_log row after refused UPDATE/DELETE: %v", err)
	}
	if action != "TestAction" {
		t.Errorf("action = %q after a refused UPDATE, want unchanged %q", action, "TestAction")
	}
}

// TestAuditLogMigration_TriggerBlocksIndependentlyOfRevoke isolates the
// trigger layer from the REVOKE layer: 016_audit_log.up.sql's doc comment
// claims two *independent* layers ("verified empirically... ownership does
// not grant an irrevocable bypass"), so this proves the trigger alone still
// blocks UPDATE/DELETE even when the connecting role's privileges are
// restored via a GRANT ... TO PUBLIC (which the owning role can still issue
// despite its own DML privileges having been revoked -- GRANT/ownership
// administration is not itself gated by the DML REVOKE). If only the
// REVOKE were doing the work, this GRANT would make the UPDATE/DELETE
// below succeed; it must not.
func TestAuditLogMigration_TriggerBlocksIndependentlyOfRevoke(t *testing.T) {
	runner, db := newPreAuditLogDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 016: %v", err)
	}

	var auditID int64
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO audit_log (actor_subject, actor_kind, action, entity_kind)
		VALUES ($1, $2, $3, $4) RETURNING audit_id
	`, "test-actor", "human", "TestAction", "test-entity").Scan(&auditID); err != nil {
		t.Fatalf("test setup: insert audit_log row: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `GRANT UPDATE, DELETE ON audit_log TO PUBLIC`); err != nil {
		t.Fatalf("test setup: restore UPDATE/DELETE privilege via PUBLIC grant: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `UPDATE audit_log SET action = 'tampered' WHERE audit_id = $1`, auditID); err == nil {
		t.Error("UPDATE audit_log succeeded after privileges were restored via PUBLIC grant, want the trigger to still refuse it")
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE audit_id = $1`, auditID); err == nil {
		t.Error("DELETE FROM audit_log succeeded after privileges were restored via PUBLIC grant, want the trigger to still refuse it")
	}
}

// TestAuditLogMigration_NoValidToColumn is NFR6.3's negative assertion:
// audit_log must not be given SCD2 shape -- no valid_to column, ever. This
// is the real assertion the ownership migration test's NFR6.3 comment
// (leaflab/migrate/ownership_migration_integration_test.go) anticipated for
// "FR8 audit rows" once this table existed.
func TestAuditLogMigration_NoValidToColumn(t *testing.T) {
	runner, db := newPreAuditLogDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 016: %v", err)
	}

	var exists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'audit_log' AND column_name = 'valid_to')
	`).Scan(&exists); err != nil {
		t.Fatalf("query information_schema.columns: %v", err)
	}
	if exists {
		t.Error("audit_log has a valid_to column, want none (NFR6.3: append-only, not SCD2 shape)")
	}
}

// TestAuditLogMigration_DownReversesCleanly and
// TestAuditLogMigration_UpDownUpIsIdempotentSafe mirror the Validation
// discipline 015_ownership's migration tests already apply to this
// migration set: up and down both clean, and re-running up after down is
// safe against a fresh database.
func TestAuditLogMigration_DownReversesCleanly(t *testing.T) {
	runner, db := newPreAuditLogDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 016: %v", err)
	}
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("reverse migration 016: %v", err)
	}

	var exists bool
	if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'audit_log')`).Scan(&exists); err != nil {
		t.Fatalf("check audit_log exists after down: %v", err)
	}
	if exists {
		t.Error("audit_log still exists after reversing migration 016")
	}
}

func TestAuditLogMigration_UpDownUpIsIdempotentSafe(t *testing.T) {
	runner, db := newPreAuditLogDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("first apply of migration 016: %v", err)
	}
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("reverse migration 016: %v", err)
	}
	if err := runner.Steps(1); err != nil {
		t.Fatalf("second apply of migration 016: %v", err)
	}

	var exists bool
	if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'audit_log')`).Scan(&exists); err != nil {
		t.Fatalf("check audit_log exists after up-down-up: %v", err)
	}
	if !exists {
		t.Error("audit_log does not exist after up-down-up, want it re-created by the second apply")
	}
}
