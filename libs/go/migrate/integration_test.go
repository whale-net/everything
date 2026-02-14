//go:build integration

package migrate

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/migrations/*.sql
var integrationMigrations embed.FS

// testDB opens a connection to the test postgres instance and creates an
// isolated schema so tests don't interfere with each other.
func testDB(t *testing.T) *sql.DB {
	t.Helper()

	host := envOr("TEST_DB_HOST", "localhost")
	port := envOr("TEST_DB_PORT", "5432")
	user := envOr("TEST_DB_USER", "postgres")
	password := envOr("TEST_DB_PASSWORD", "postgres")
	dbname := envOr("TEST_DB_NAME", "postgres")
	sslmode := envOr("TEST_DB_SSL_MODE", "disable")

	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		host, port, user, password, dbname, sslmode,
	)

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err, "failed to open database")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, db.PingContext(ctx), "failed to ping database")

	// Create an isolated schema for this test to avoid cross-test interference.
	// golang-migrate uses the public schema and the schema_migrations table,
	// so we drop and recreate the schema_migrations table instead.
	cleanupMigrationState(t, db)

	t.Cleanup(func() {
		cleanupMigrationState(t, db)
		db.Close()
	})

	return db
}

// cleanupMigrationState drops migration artifacts so each test starts clean.
func cleanupMigrationState(t *testing.T, db *sql.DB) {
	t.Helper()
	// Drop the tables our migrations create
	_, _ = db.Exec("DROP TABLE IF EXISTS test_table CASCADE")
	// Drop the golang-migrate tracking table
	_, _ = db.Exec("DROP TABLE IF EXISTS schema_migrations CASCADE")
	// Drop the history tracking table
	_, _ = db.Exec("DROP TABLE IF EXISTS migration_history CASCADE")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Runner.Up / Runner.Down
// ---------------------------------------------------------------------------

func TestIntegration_Up(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	err := runner.Up()
	require.NoError(t, err)

	// Verify both migrations applied: test_table exists with email column
	var colCount int
	err = db.QueryRow(`
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'test_table'
	`).Scan(&colCount)
	require.NoError(t, err)
	assert.Equal(t, 3, colCount) // id, name, email

	// Version should be 2
	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(2), version)
	assert.False(t, dirty)
}

func TestIntegration_Up_Idempotent(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	require.NoError(t, runner.Up())

	// Running Up again should succeed (ErrNoChange is swallowed)
	err := runner.Up()
	assert.NoError(t, err)
}

func TestIntegration_Down(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	require.NoError(t, runner.Up())

	err := runner.Down()
	require.NoError(t, err)

	// test_table should no longer exist
	var exists bool
	err = db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_name = 'test_table'
		)
	`).Scan(&exists)
	require.NoError(t, err)
	assert.False(t, exists)
}

// ---------------------------------------------------------------------------
// Runner.Steps
// ---------------------------------------------------------------------------

func TestIntegration_Steps_Up(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	// Step up once — only migration 1 should apply
	err := runner.Steps(1)
	require.NoError(t, err)

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(1), version)
	assert.False(t, dirty)

	// test_table should exist but without email column
	var hasEmail bool
	err = db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'test_table' AND column_name = 'email'
		)
	`).Scan(&hasEmail)
	require.NoError(t, err)
	assert.False(t, hasEmail)
}

func TestIntegration_Steps_Down(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	// Apply all, then step down once
	require.NoError(t, runner.Up())

	err := runner.Steps(-1)
	require.NoError(t, err)

	version, _, err := runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(1), version)

	// email column should be gone
	var hasEmail bool
	err = db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'test_table' AND column_name = 'email'
		)
	`).Scan(&hasEmail)
	require.NoError(t, err)
	assert.False(t, hasEmail)
}

// ---------------------------------------------------------------------------
// Runner.Version
// ---------------------------------------------------------------------------

func TestIntegration_Version_NoMigrations(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	// Before any migration, version should be 0 (ErrNilVersion is handled)
	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(0), version)
	assert.False(t, dirty)
}

func TestIntegration_Version_AfterMigrations(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	require.NoError(t, runner.Steps(1))
	version, _, err := runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(1), version)

	require.NoError(t, runner.Steps(1))
	version, _, err = runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(2), version)
}

// ---------------------------------------------------------------------------
// Runner.Force
// ---------------------------------------------------------------------------

func TestIntegration_Force(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	// Apply migration 1
	require.NoError(t, runner.Steps(1))

	// Force version to 2 without actually running migration 2
	err := runner.Force(2)
	require.NoError(t, err)

	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(2), version)
	assert.False(t, dirty)

	// email column should NOT exist because we forced, not migrated
	var hasEmail bool
	err = db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'test_table' AND column_name = 'email'
		)
	`).Scan(&hasEmail)
	require.NoError(t, err)
	assert.False(t, hasEmail)
}

func TestIntegration_ForceWithValidation_Safe(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	// Apply migration 1
	require.NoError(t, runner.Steps(1))

	// Force to version 1 (safe — no history of failure)
	err := runner.ForceWithValidation(1, false)
	require.NoError(t, err)

	version, _, err := runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(1), version)
}

func TestIntegration_ForceWithValidation_Dangerous(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	require.NoError(t, runner.Steps(1))

	// Record a fake failure in history for version 2
	require.NoError(t, runner.tracker.EnsureHistoryTable())
	historyID, err := runner.tracker.RecordStart(2, "up")
	require.NoError(t, err)
	require.NoError(t, runner.tracker.RecordFailure(historyID, time.Now(), fmt.Errorf("fake error")))

	// Non-dangerous should fail
	err = runner.ForceWithValidation(2, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot force to version 2")

	// Dangerous should succeed
	err = runner.ForceWithValidation(2, true)
	assert.NoError(t, err)

	version, _, err := runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(2), version)
}

// ---------------------------------------------------------------------------
// Runner.UpWithTracking
// ---------------------------------------------------------------------------

func TestIntegration_UpWithTracking(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	err := runner.UpWithTracking()
	require.NoError(t, err)

	// Verify migrations applied
	version, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(2), version)
	assert.False(t, dirty)

	// Verify history was recorded
	entries, err := runner.tracker.GetHistory(10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(entries), 2, "should have at least 2 history entries")

	// All entries should be successful
	for _, entry := range entries {
		assert.Equal(t, "success", entry.Status)
		assert.Equal(t, "up", entry.Direction)
		assert.NotNil(t, entry.DurationMs)
	}
}

func TestIntegration_UpWithTracking_Idempotent(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	require.NoError(t, runner.UpWithTracking())

	// Running again should succeed (no more migrations to apply)
	err := runner.UpWithTracking()
	assert.NoError(t, err)
}

func TestIntegration_UpWithTracking_DirtyState(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	// Apply migration 1
	require.NoError(t, runner.Steps(1))

	// Manually set dirty state in schema_migrations
	_, err := db.Exec("UPDATE schema_migrations SET dirty = true")
	require.NoError(t, err)

	// UpWithTracking should refuse to run on dirty state
	err = runner.UpWithTracking()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dirty state")
}

// ---------------------------------------------------------------------------
// connect function
// ---------------------------------------------------------------------------

func TestIntegration_Connect(t *testing.T) {
	cfg := &Config{
		Host:            envOr("TEST_DB_HOST", "localhost"),
		Port:            5432,
		User:            envOr("TEST_DB_USER", "postgres"),
		Password:        envOr("TEST_DB_PASSWORD", "postgres"),
		Database:        envOr("TEST_DB_NAME", "postgres"),
		SSLMode:         envOr("TEST_DB_SSL_MODE", "disable"),
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxLifetime: 1 * time.Minute,
	}

	if p := envOr("TEST_DB_PORT", ""); p != "" {
		fmt.Sscanf(p, "%d", &cfg.Port)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := connect(ctx, cfg)
	require.NoError(t, err)
	defer db.Close()

	// Verify connection works
	var result int
	err = db.QueryRow("SELECT 1").Scan(&result)
	require.NoError(t, err)
	assert.Equal(t, 1, result)
}

func TestIntegration_Connect_BadHost(t *testing.T) {
	cfg := &Config{
		Host:            "nonexistent-host-that-does-not-exist",
		Port:            5432,
		User:            "postgres",
		Password:        "postgres",
		Database:        "postgres",
		SSLMode:         "disable",
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxLifetime: 1 * time.Minute,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := connect(ctx, cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to ping database")
}

// ---------------------------------------------------------------------------
// createMigrator
// ---------------------------------------------------------------------------

func TestIntegration_CreateMigrator(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	m, err := runner.createMigrator()
	require.NoError(t, err)
	assert.NotNil(t, m)
}

func TestIntegration_CreateMigrator_BadMigrateDir(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "nonexistent/path")

	_, err := runner.createMigrator()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create migration source")
}

// ---------------------------------------------------------------------------
// Full lifecycle: Up -> verify -> Down -> verify
// ---------------------------------------------------------------------------

func TestIntegration_FullLifecycle(t *testing.T) {
	db := testDB(t)
	runner := NewRunner(db, integrationMigrations, "testdata/migrations")

	// 1. Start with no migrations
	v, dirty, err := runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(0), v)
	assert.False(t, dirty)

	// 2. Step up to version 1
	require.NoError(t, runner.Steps(1))
	v, _, err = runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(1), v)

	// 3. Step up to version 2
	require.NoError(t, runner.Steps(1))
	v, _, err = runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(2), v)

	// 4. Step down to version 1
	require.NoError(t, runner.Steps(-1))
	v, _, err = runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(1), v)

	// 5. Step back up to version 2
	require.NoError(t, runner.Steps(1))
	v, _, err = runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(2), v)

	// 6. Down all
	require.NoError(t, runner.Down())

	// After Down, version query returns 0 (ErrNilVersion handled)
	v, _, err = runner.Version()
	require.NoError(t, err)
	assert.Equal(t, uint(0), v)
}
