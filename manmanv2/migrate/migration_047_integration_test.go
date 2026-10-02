//go:build integration

// Round-trip and constraint coverage for migration 047 (MCP server state).
//
//	bazel test //manmanv2/migrate:migration_047_integration_test --test_output=all
package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/manmanv2/migrate/schema"
)

func tableExists047(ctx context.Context, t *testing.T, db *dbtest.Postgres, name string) bool {
	t.Helper()
	var ok bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, name).Scan(&ok); err != nil {
		t.Fatalf("check table %s: %v", name, err)
	}
	return ok
}

func TestMigration047_RoundTripAndConstraints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	tables := []string{"mcp_idempotency_record", "mcp_confirmation_token", "mcp_gamer_start_allowlist", "mcp_gamer_action_allowlist"}
	for _, tb := range tables {
		if !tableExists047(ctx, t, db, tb) {
			t.Fatalf("table %s missing after Up", tb)
		}
	}

	// Idempotency key uniqueness.
	ins := `INSERT INTO mcp_idempotency_record (caller_issuer, caller_subject, tool_name, idempotency_key, args_hash, result) VALUES ('i','s','t','k','h','{}')`
	if _, err := db.Pool.Exec(ctx, ins); err != nil {
		t.Fatalf("insert idempotency: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, ins); err == nil {
		t.Fatal("expected duplicate idempotency key to fail")
	}

	// Conditional single-use consume, and expiry.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO mcp_confirmation_token (token_id, caller_issuer, caller_subject, tool_name, args_hash, entity_fingerprint) VALUES ('tok','i','s','t','h','f')`); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	consume := `UPDATE mcp_confirmation_token SET consumed_at = NOW() WHERE token_id = $1 AND consumed_at IS NULL AND expires_at > NOW()`
	tag, err := db.Pool.Exec(ctx, consume, "tok")
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("first consume: rows=%d err=%v", tag.RowsAffected(), err)
	}
	if tag, _ = db.Pool.Exec(ctx, consume, "tok"); tag.RowsAffected() != 0 {
		t.Fatal("second consume must affect 0 rows")
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO mcp_confirmation_token (token_id, caller_issuer, caller_subject, tool_name, args_hash, entity_fingerprint, issued_at, expires_at) VALUES ('old','i','s','t','h','f', NOW() - INTERVAL '10 minutes', NOW() - INTERVAL '5 minutes')`); err != nil {
		t.Fatalf("insert expired token: %v", err)
	}
	if tag, _ = db.Pool.Exec(ctx, consume, "old"); tag.RowsAffected() != 0 {
		t.Fatal("expired token must not be consumable")
	}

	// Allowlists: one current row per key; re-grant after revoke works.
	for _, c := range []struct{ ins, revoke string }{
		{`INSERT INTO mcp_gamer_start_allowlist (deployment_id, granted_by) VALUES (1,'a')`,
			`UPDATE mcp_gamer_start_allowlist SET valid_to = NOW() WHERE deployment_id = 1 AND valid_to IS NULL`},
		{`INSERT INTO mcp_gamer_action_allowlist (deployment_id, action_name, granted_by) VALUES (1,'say','a')`,
			`UPDATE mcp_gamer_action_allowlist SET valid_to = NOW() WHERE deployment_id = 1 AND action_name = 'say' AND valid_to IS NULL`},
	} {
		if _, err := db.Pool.Exec(ctx, c.ins); err != nil {
			t.Fatalf("grant: %v", err)
		}
		if _, err := db.Pool.Exec(ctx, c.ins); err == nil {
			t.Fatalf("expected duplicate current grant to fail: %s", c.ins)
		}
		if _, err := db.Pool.Exec(ctx, c.revoke); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		if _, err := db.Pool.Exec(ctx, c.ins); err != nil {
			t.Fatalf("re-grant after revoke: %v", err)
		}
	}

	// Down removes all four; up re-applies.
	if err := runner.Migrate(46); err != nil {
		t.Fatalf("Migrate(46): %v", err)
	}
	for _, tb := range tables {
		if tableExists047(ctx, t, db, tb) {
			t.Fatalf("table %s still present after down", tb)
		}
	}
	if err := runner.Migrate(47); err != nil {
		t.Fatalf("Migrate(47): %v", err)
	}
}
