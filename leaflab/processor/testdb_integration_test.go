//go:build integration

// Shared real-Postgres setup for this package's integration tests -- see
// convergence_integration_test.go's doc comment and //libs/go/dbtest's
// README for how to run them.
package main

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/migrate/schema"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// leaflabTimescaleImage is the same TimescaleDB-flavoured image the local
// dev Tiltfile uses. dbtest's default postgres:16-alpine image doesn't ship
// the TimescaleDB extension, and migration 001_initial_schema.up.sql runs
// `CREATE EXTENSION timescaledb` and `create_hypertable(...)`, so every
// leaflab integration test needs this image instead of the default.
const leaflabTimescaleImage = "timescale/timescaledb:latest-pg16"

// newLeafLabTestPool provisions a real, throwaway Postgres (via dbtest) and
// applies every real leaflab migration from leaflab/migrate/schema -- the
// exact same embed.FS //leaflab/migrate applies in production -- so these
// tests exercise the actual schema rather than a hand-maintained copy of it.
func newLeafLabTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: leaflabTimescaleImage})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("open database/sql handle for migrations: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	if err := runner.Up(); err != nil {
		t.Fatalf("apply real leaflab migrations: %v", err)
	}

	return db.Pool
}
