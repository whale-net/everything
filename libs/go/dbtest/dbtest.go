// Package dbtest starts a real, throwaway Postgres instance (via
// testcontainers-go) for tests that need to exercise actual SQL — unique
// constraints, partial indexes, triggers — none of which an in-memory fake
// can verify.
//
// A single Postgres container is shared across every test in a test binary
// (process); each call to NewPostgres provisions its own database and login
// role inside that container instead of starting a new container. This
// amortizes the ~2s container-start cost across the whole test binary while
// still giving each test a fully isolated schema: a test can only see and
// modify its own database, connected as a role that owns nothing else.
//
// This package requires a working Docker daemon and network access to pull
// the postgres image. It is meant to be called only from tests whose Bazel
// target is tagged manual/no-cache/integration/no-sandbox, mirroring
// //tools/scripts:test_cross_compilation. See the package README for the
// exact bazel invocation and required environment variables.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
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
	// Image overrides the postgres image reference. Defaults to
	// DefaultImage. All tests in a process that request the same Image
	// share one container; requesting a different Image starts an
	// additional shared container for that image.
	Image string

	// Schema is raw DDL applied once, immediately after the pool connects
	// and before it is handed back to the caller. Keep it self-contained
	// (no dependency on migrations from other packages) so the test stays
	// hermetic. It runs against the test's own database, as the test's own
	// role, so it can create anything that role needs to own.
	Schema string
}

// Postgres is an isolated database and role inside a shared Postgres test
// container, plus a ready connection pool.
type Postgres struct {
	Pool *pgxpool.Pool

	// ConnString is the connection string used to build Pool, scoped to
	// this test's own database and role. Useful for tests that want to
	// open additional independent connections (e.g. to exercise
	// concurrent-insert races against a unique index).
	ConnString string
}

// sharedContainer is one Postgres container reused across every test in the
// process that requests a given image.
type sharedContainer struct {
	once      sync.Once
	container *postgres.PostgresContainer
	adminPool *pgxpool.Pool
	err       error
}

var (
	containersMu sync.Mutex
	containers   = map[string]*sharedContainer{}
)

// getSharedContainer returns the (lazily started) container for image,
// starting it at most once per process regardless of how many tests ask
// for it concurrently.
func getSharedContainer(ctx context.Context, image string) *sharedContainer {
	containersMu.Lock()
	sc, ok := containers[image]
	if !ok {
		sc = &sharedContainer{}
		containers[image] = sc
	}
	containersMu.Unlock()

	sc.once.Do(func() {
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
			if ctr != nil {
				_ = testcontainers.TerminateContainer(ctr)
			}
			sc.err = fmt.Errorf("dbtest: start postgres container: %w", err)
			return
		}

		connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			_ = testcontainers.TerminateContainer(ctr)
			sc.err = fmt.Errorf("dbtest: build connection string: %w", err)
			return
		}

		pool, err := pgxpool.New(ctx, connStr)
		if err != nil {
			_ = testcontainers.TerminateContainer(ctr)
			sc.err = fmt.Errorf("dbtest: create admin pool: %w", err)
			return
		}
		if err := pool.Ping(ctx); err != nil {
			pool.Close()
			_ = testcontainers.TerminateContainer(ctr)
			sc.err = fmt.Errorf("dbtest: ping: %w", err)
			return
		}

		sc.container = ctr
		sc.adminPool = pool
	})

	return sc
}

// identRe matches the characters NewPostgres keeps from t.Name() when
// deriving a database/role identifier; anything else is dropped so the
// result is always a safe, unquoted Postgres identifier.
var identRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// randomSuffix returns 8 hex characters of crypto-random suffix, used to
// keep identifiers unique across parallel subtests that share a sanitized
// name (e.g. "TestFoo/case_1" and "TestFoo/case#1" both sanitize the same).
func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("dbtest: read random suffix: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// NewPostgres provisions an isolated database and login role for t inside a
// Postgres container shared across the whole test binary (started at most
// once per process, per Options.Image), applies Options.Schema, and returns
// a ready pool connected as that role to that database. On any failure it
// fails the test immediately (via t.Fatalf).
//
// t.Cleanup is registered automatically to drop the database and role;
// callers do not need to call any Close method themselves. The shared
// container itself is never terminated by NewPostgres — it is reused by
// every test in the process and left for Docker/testcontainers' Ryuk reaper
// to clean up after the process exits.
func NewPostgres(ctx context.Context, t testing.TB, opts Options) *Postgres {
	t.Helper()

	image := opts.Image
	if image == "" {
		image = DefaultImage
	}

	sc := getSharedContainer(ctx, image)
	if sc.err != nil {
		t.Fatalf("dbtest: %v", sc.err)
		return nil // unreachable, satisfies compiler
	}

	ident := strings.ToLower(strings.Trim(identRe.ReplaceAllString(t.Name(), "_"), "_"))
	if len(ident) > 40 {
		ident = ident[:40]
	}
	suffix := randomSuffix()
	dbName := fmt.Sprintf("dbtest_%s_%s", ident, suffix)
	roleName := fmt.Sprintf("dbtest_role_%s_%s", ident, suffix)
	// Random per-test password; only ever used over a loopback connection
	// to a throwaway container, so a short hex string is sufficient.
	rolePassword := randomSuffix() + randomSuffix()

	admin := sc.adminPool

	if _, err := admin.Exec(ctx, fmt.Sprintf(
		`CREATE ROLE %s LOGIN PASSWORD '%s'`, roleName, rolePassword)); err != nil {
		t.Fatalf("dbtest: create role %s: %v", roleName, err)
		return nil
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(
		`CREATE DATABASE %s OWNER %s`, dbName, roleName)); err != nil {
		t.Fatalf("dbtest: create database %s: %v", dbName, err)
		return nil
	}

	t.Cleanup(func() {
		dropCtx := context.Background()
		// Force-disconnect any lingering sessions (e.g. a leaked
		// connection from the test) before DROP DATABASE, which errors
		// if the database is still in use.
		_, _ = admin.Exec(dropCtx, fmt.Sprintf(
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
			 WHERE datname = '%s' AND pid <> pg_backend_pid()`, dbName))
		_, _ = admin.Exec(dropCtx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, dbName))
		_, _ = admin.Exec(dropCtx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, roleName))
	})

	baseConnStr, err := sc.container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dbtest: build connection string: %v", err)
		return nil
	}
	connStr := replaceConnStringAuthAndDB(baseConnStr, roleName, rolePassword, dbName)

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("dbtest: create pool: %v", err)
		return nil
	}
	t.Cleanup(pool.Close)

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

	return &Postgres{Pool: pool, ConnString: connStr}
}

// replaceConnStringAuthAndDB rewrites a postgres:// connection string's
// userinfo and path (database name) to the given role, password, and
// database, keeping the host/port/query string from base.
func replaceConnStringAuthAndDB(base, user, password, dbName string) string {
	// base looks like: postgres://user:pass@host:port/dbname?query
	afterScheme, ok := strings.CutPrefix(base, "postgres://")
	if !ok {
		panic(fmt.Sprintf("dbtest: unexpected connection string scheme: %s", base))
	}
	_, hostAndRest, ok := strings.Cut(afterScheme, "@")
	if !ok {
		panic(fmt.Sprintf("dbtest: connection string missing userinfo: %s", base))
	}
	hostPort, dbAndQuery, ok := strings.Cut(hostAndRest, "/")
	if !ok {
		panic(fmt.Sprintf("dbtest: connection string missing database: %s", base))
	}
	_, query, hasQuery := strings.Cut(dbAndQuery, "?")

	result := fmt.Sprintf("postgres://%s:%s@%s/%s", user, password, hostPort, dbName)
	if hasQuery {
		result += "?" + query
	}
	return result
}
