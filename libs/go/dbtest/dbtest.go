// Package dbtest starts a real, throwaway Postgres instance (via
// testcontainers-go) for tests that need to exercise actual SQL — unique
// constraints, partial indexes, triggers — none of which an in-memory fake
// can verify.
//
// This package requires a working Docker daemon and network access to pull
// the postgres image. It is meant to be called only from tests whose Bazel
// target is tagged manual/no-cache/integration/no-sandbox, mirroring
// //tools/scripts:test_cross_compilation. See the package README for the
// exact bazel invocation and required environment variables.
package dbtest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// DefaultImage is used when Options.Image is empty.
const DefaultImage = "postgres:16-alpine"

// Options configures NewPostgres.
type Options struct {
	// Image overrides the postgres image reference. Defaults to DefaultImage.
	Image string

	// Schema is raw DDL applied once, immediately after the pool connects
	// and before it is handed back to the caller. Keep it self-contained
	// (no dependency on migrations from other packages) so the test stays
	// hermetic.
	Schema string
}

// Postgres is a running Postgres test container plus a ready connection pool.
type Postgres struct {
	Pool *pgxpool.Pool

	// ConnString is the connection string used to build Pool, useful for
	// tests that want to open additional independent connections (e.g. to
	// exercise concurrent-insert races against a unique index).
	ConnString string

	container *postgres.PostgresContainer
}

// Close closes the pool and terminates the container. Safe to call via
// defer or t.Cleanup.
func (p *Postgres) Close() {
	if p.Pool != nil {
		p.Pool.Close()
	}
	if p.container != nil {
		_ = testcontainers.TerminateContainer(p.container)
	}
}

// NewPostgres starts a Postgres container, applies Options.Schema, and
// returns a ready pool. On any failure it fails the test immediately (via
// t.Fatalf) and cleans up whatever was already started, so callers never
// need to handle an error return.
//
// t.Cleanup is registered automatically; callers do not need to call
// Close() themselves, though it's safe to do so early.
func NewPostgres(ctx context.Context, t testing.TB, opts Options) *Postgres {
	t.Helper()

	image := opts.Image
	if image == "" {
		image = DefaultImage
	}

	ctr, err := postgres.Run(ctx, image,
		postgres.WithDatabase("dbtest"),
		postgres.WithUsername("dbtest"),
		postgres.WithPassword("dbtest"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2),
		),
	)
	if err != nil {
		// ctr may be non-nil even on error (e.g. wait-strategy timeout);
		// best-effort terminate so we don't leak on failure.
		if ctr != nil {
			_ = testcontainers.TerminateContainer(ctr)
		}
		t.Fatalf("dbtest: start postgres container: %v", err)
		return nil // unreachable, satisfies compiler
	}

	result := &Postgres{container: ctr}
	t.Cleanup(result.Close)

	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dbtest: build connection string: %v", err)
		return nil
	}
	result.ConnString = connStr

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("dbtest: create pool: %v", err)
		return nil
	}
	result.Pool = pool

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("dbtest: ping: %v", err)
		return nil
	}

	if opts.Schema != "" {
		if _, err := pool.Exec(ctx, opts.Schema); err != nil {
			t.Fatalf("dbtest: apply schema: %v", err)
			return nil
		}
	}

	return result
}
