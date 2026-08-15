//go:build integration

// Real-Postgres integration coverage for environmentRepo (environment.go):
// Upsert/Get/List/Archive and the real UNIQUE (key) constraint. See
// postgres_integration_helpers_test.go's doc comment for why this package
// builds these files under the "integration" tag, and TESTING.md for how
// to run them.
package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// --- 5. migration 002 / environment table (AR-3b) --------------------------

// TestMigration002SeedsDevStageProd proves migration 002 applies cleanly on
// top of 001 (newTestRegistry already ran both) and that its seed data
// landed: dev/stage/prod, ordered by rank ascending, none archived. This is
// the one seed assertion that can only be proven against a real migration
// run -- server/repository/fake has no migrations to apply.
func TestMigration002SeedsDevStageProd(t *testing.T) {
	_, pool := newTestRegistry(t)

	rows, err := pool.Query(context.Background(), `SELECT key, rank, archived FROM environment ORDER BY rank ASC`)
	if err != nil {
		t.Fatalf("query environment: %v", err)
	}
	defer rows.Close()

	type seeded struct {
		key      string
		rank     int32
		archived bool
	}
	var got []seeded
	for rows.Next() {
		var s seeded
		if err := rows.Scan(&s.key, &s.rank, &s.archived); err != nil {
			t.Fatalf("scan environment row: %v", err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate environment rows: %v", err)
	}

	want := []seeded{{"dev", 0, false}, {"stage", 10, false}, {"prod", 20, false}}
	if len(got) != len(want) {
		t.Fatalf("expected migration 002 to seed exactly %v, got %v", want, got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("expected seeded environment %+v at rank position %d, got %+v", w, i, got[i])
		}
	}
}

// TestEnvironment_KeyUniqueConstraint proves the real UNIQUE (key)
// constraint on the environment table -- not application logic -- rejects a
// second row for a key that already exists. The repository layer's Upsert
// never reaches this path on its own (it looks up by key before deciding
// whether to insert or update), so this issues the raw INSERT directly to
// exercise the constraint itself.
func TestEnvironment_KeyUniqueConstraint(t *testing.T) {
	_, pool := newTestRegistry(t)

	// "dev" already exists from migration 002's seed data.
	_, err := pool.Exec(context.Background(), `
		INSERT INTO environment (key, display_name, rank) VALUES ('dev', 'Duplicate Dev', 99)`)
	if err == nil {
		t.Fatalf("expected a duplicate key insert to be rejected by the UNIQUE (key) constraint, got nil error")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError, got: %v (%T)", err, err)
	}
	if pgErr.Code != sqlStateUniqueViolation {
		t.Fatalf("expected SQLSTATE %s (unique_violation), got %s: %v", sqlStateUniqueViolation, pgErr.Code, err)
	}

	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM environment WHERE key = 'dev'`).Scan(&count); err != nil {
		t.Fatalf("count dev rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 'dev' row after the rejected duplicate insert, found %d", count)
	}
}

// TestEnvironmentRepo_UpsertCreateThenUpdate exercises the repository layer
// (not raw SQL) end to end against real Postgres: a fresh key creates a row,
// a repeated key updates every field but Key and Archived.
func TestEnvironmentRepo_UpsertCreateThenUpdate(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	created, wasCreated, err := reg.Environments().Upsert(ctx, repository.Environment{
		Key: "canary", DisplayName: "Canary", Rank: 5, GitopsPath: "environments/canary",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !wasCreated || created.EnvironmentID == "" {
		t.Fatalf("expected a newly created environment, got %+v (created=%v)", created, wasCreated)
	}

	updated, wasCreated, err := reg.Environments().Upsert(ctx, repository.Environment{
		Key: "canary", DisplayName: "Canary (renamed)", Rank: 6, RequiresApproval: true,
		AllowedPrincipals: []string{"alice@example.com", "bob@example.com"},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if wasCreated {
		t.Fatalf("expected created=false on the second upsert of the same key")
	}
	if updated.EnvironmentID != created.EnvironmentID {
		t.Fatalf("expected the same environment_id across upserts, got %s vs %s", updated.EnvironmentID, created.EnvironmentID)
	}
	if updated.DisplayName != "Canary (renamed)" || updated.Rank != 6 || !updated.RequiresApproval {
		t.Fatalf("expected fields updated in place, got %+v", updated)
	}
	if len(updated.AllowedPrincipals) != 2 {
		t.Fatalf("expected allowed_principals persisted as a real TEXT[] round-trip, got %+v", updated.AllowedPrincipals)
	}
}
