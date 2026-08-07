// Package testpg provides a disposable postgres container for integration tests.
//
// It starts a postgres:16 container via the Docker API, waits for readiness,
// optionally runs golang-migrate migrations, and tears everything down on Close.
//
// Usage:
//
//	func TestSomething(t *testing.T) {
//	    pg := testpg.Start(t) // starts container, waits for ready
//	    defer pg.Close()
//
//	    pool, err := pgxpool.New(context.Background(), pg.ConnString())
//	    // ... use pool ...
//	}
//
// With migrations:
//
//	//go:embed migrations/*.sql
//	var migrations embed.FS
//
//	func TestWithMigrations(t *testing.T) {
//	    pg := testpg.Start(t, testpg.WithMigrations(migrations, "migrations"))
//	    defer pg.Close()
//	    // database is migrated and ready
//	}
package testpg

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log"
	"net"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // register "pgx" database/sql driver

	libmigrate "github.com/whale-net/everything/libs/go/migrate"
)

const (
	defaultImage    = "postgres:16"
	defaultUser     = "test"
	defaultPassword = "test"
	defaultDB       = "testdb"
	readyTimeout    = 30 * time.Second
	readyInterval   = 250 * time.Millisecond
)

// Option configures a Container.
type Option func(*options)

type options struct {
	image      string
	user       string
	password   string
	database   string
	migrations *migrationOpt
}

type migrationOpt struct {
	fs  embed.FS
	dir string
}

// WithImage overrides the default postgres Docker image.
func WithImage(img string) Option {
	return func(o *options) { o.image = img }
}

// WithCredentials sets the postgres user, password, and database name.
func WithCredentials(user, password, database string) Option {
	return func(o *options) {
		o.user = user
		o.password = password
		o.database = database
	}
}

// WithMigrations runs golang-migrate migrations after the container is ready.
// fs is an embedded filesystem containing migration files, dir is the subdirectory
// within the FS (e.g. "migrations").
func WithMigrations(fs embed.FS, dir string) Option {
	return func(o *options) {
		o.migrations = &migrationOpt{fs: fs, dir: dir}
	}
}

// Container represents a running postgres test container.
type Container struct {
	connString  string
	containerID string
	cli         *client.Client
	pool        *pgxpool.Pool
}

// ConnString returns the postgres connection string for the running container.
func (c *Container) ConnString() string {
	return c.connString
}

// Pool returns a pgxpool.Pool connected to the test database.
// The pool is created during Start and closed during Close.
func (c *Container) Pool() *pgxpool.Pool {
	return c.pool
}

// Close stops and removes the container and closes the connection pool.
func (c *Container) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if c.pool != nil {
		c.pool.Close()
	}

	timeout := 5
	_ = c.cli.ContainerStop(ctx, c.containerID, container.StopOptions{Timeout: &timeout})
	_ = c.cli.ContainerRemove(ctx, c.containerID, container.RemoveOptions{Force: true})
	_ = c.cli.Close()
}

// Start creates and starts a postgres container, waits for it to accept connections,
// and optionally runs migrations. It calls t.Fatal on any error.
func Start(t *testing.T, opts ...Option) *Container {
	t.Helper()

	o := &options{
		image:    defaultImage,
		user:     defaultUser,
		password: defaultPassword,
		database: defaultDB,
	}
	for _, fn := range opts {
		fn(o)
	}

	ctx := context.Background()

	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		t.Fatalf("testpg: docker client: %v", err)
	}

	// Pull image (no-op if already present)
	pullReader, err := cli.ImagePull(ctx, o.image, image.PullOptions{})
	if err != nil {
		cli.Close()
		t.Fatalf("testpg: pull image %s: %v", o.image, err)
	}
	// Drain the reader to complete the pull
	buf := make([]byte, 4096)
	for {
		_, readErr := pullReader.Read(buf)
		if readErr != nil {
			break
		}
	}
	pullReader.Close()

	// Find a free host port
	hostPort, err := freePort()
	if err != nil {
		cli.Close()
		t.Fatalf("testpg: find free port: %v", err)
	}

	pgPort, _ := nat.NewPort("tcp", "5432")

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: o.image,
			Env: []string{
				fmt.Sprintf("POSTGRES_USER=%s", o.user),
				fmt.Sprintf("POSTGRES_PASSWORD=%s", o.password),
				fmt.Sprintf("POSTGRES_DB=%s", o.database),
			},
			ExposedPorts: nat.PortSet{pgPort: struct{}{}},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				pgPort: []nat.PortBinding{{
					HostIP:   "127.0.0.1",
					HostPort: fmt.Sprintf("%d", hostPort),
				}},
			},
			AutoRemove: false,
		},
		nil, nil,
		fmt.Sprintf("testpg-%s-%d", t.Name(), time.Now().UnixNano()),
	)
	if err != nil {
		cli.Close()
		t.Fatalf("testpg: create container: %v", err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		cli.Close()
		t.Fatalf("testpg: start container: %v", err)
	}

	connStr := fmt.Sprintf(
		"postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable",
		o.user, o.password, hostPort, o.database,
	)

	// Wait for postgres to accept connections
	if err := waitReady(ctx, connStr); err != nil {
		timeout := 5
		_ = cli.ContainerStop(ctx, resp.ID, container.StopOptions{Timeout: &timeout})
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		cli.Close()
		t.Fatalf("testpg: postgres not ready after %v: %v", readyTimeout, err)
	}

	// Create a pool for the caller
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		timeout := 5
		_ = cli.ContainerStop(ctx, resp.ID, container.StopOptions{Timeout: &timeout})
		_ = cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		cli.Close()
		t.Fatalf("testpg: create pool: %v", err)
	}

	c := &Container{
		connString:  connStr,
		containerID: resp.ID,
		cli:         cli,
		pool:        pool,
	}

	// Run migrations if configured
	if o.migrations != nil {
		if err := runMigrations(connStr, o.migrations); err != nil {
			c.Close()
			t.Fatalf("testpg: migrations: %v", err)
		}
	}

	log.Printf("testpg: postgres ready at %s (container %s)", connStr, resp.ID[:12])
	return c
}

// waitReady polls postgres until it accepts a connection or the timeout expires.
func waitReady(ctx context.Context, connStr string) error {
	deadline := time.Now().Add(readyTimeout)
	var lastErr error

	for time.Now().Before(deadline) {
		pool, err := pgxpool.New(ctx, connStr)
		if err != nil {
			lastErr = err
			time.Sleep(readyInterval)
			continue
		}

		err = pool.Ping(ctx)
		pool.Close()
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(readyInterval)
	}

	return fmt.Errorf("timeout waiting for postgres: %w", lastErr)
}

// runMigrations uses libs/go/migrate to apply migrations.
func runMigrations(connStr string, m *migrationOpt) error {
	// Use pgx driver to match the rest of the codebase
	db, err := sql.Open("pgx", connStr)
	if err != nil {
		return fmt.Errorf("open db for migrations: %w", err)
	}
	defer db.Close()

	runner := libmigrate.NewRunner(db, m.fs, m.dir)
	return runner.Up()
}

// freePort asks the OS for an available TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}
