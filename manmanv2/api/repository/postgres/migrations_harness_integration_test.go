//go:build integration

// Shared harness for the repository integration tests that run against the
// real migration history. Sibling files in this package predate
// //manmanv2/migrate/schema and hand-write a const DDL string scoped to the
// columns their repository touches; these tests instead replay every shipped
// migration, so drift between a repository's SQL and the actual schema -- a
// column a migration adds that a SELECT never reads, a CHECK a test DDL
// omits -- fails here rather than in production.
//
// Included in the srcs of every go_test in this package that uses it, since
// each integration target compiles only its own files.
package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // sql.Open("pgx", ...) for the migrate Runner

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/manmanv2/migrate/schema"
	manman "github.com/whale-net/everything/manmanv2/models"
)

// newMigratedPool returns a pool on a throwaway Postgres whose schema is the
// full shipped migration history. Costs a container-backed replay of all
// migrations, so prefer t.Run subtests over many top-level Test funcs when the
// schema each needs is the same.
func newMigratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("open sql db for migrations: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir).Up(); err != nil {
		t.Fatalf("apply real migrations: %v", err)
	}

	return db.Pool
}

// jsonStrings encodes a []string the way handlers.stringArrayToJSONB does, so
// round-trip assertions use the same on-the-wire shape production writes.
func jsonStrings(values ...string) manman.JSONB {
	items := make([]interface{}, len(values))
	for i, v := range values {
		items[i] = v
	}
	return manman.JSONB{"items": items}
}

// strPtr is a local string-pointer helper; the production one is unexported in
// the handlers package.
func strPtr(s string) *string { return &s }
