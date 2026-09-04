package migrate

import (
	"context"
	"database/sql"
	"embed"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Config holds database connection configuration
type Config struct {
	Host            string
	Port            int
	User            string
	Password        string
	Database        string
	SSLMode         string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// DefaultConfig returns a config with defaults from environment variables.
// Used only when PG_DATABASE_URL is not set.
func DefaultConfig() *Config {
	return &Config{
		Host:            getEnv("DB_HOST", "localhost"),
		Port:            getEnvInt("DB_PORT", 5432),
		User:            getEnv("DB_USER", "postgres"),
		Password:        getEnv("DB_PASSWORD", ""),
		Database:        getEnv("DB_NAME", "postgres"),
		SSLMode:         getEnv("DB_SSL_MODE", "disable"),
		MaxOpenConns:    25,
		MaxIdleConns:    5,
		ConnMaxLifetime: 5 * time.Minute,
	}
}

// RunCLI is a convenience function for running migration CLI.
// migrations: embedded filesystem with migration files
// migrateDir: subdirectory within migrations (e.g., "migrations")
// opts: optional Option values — e.g. WithSeeder(fn) to seed reference data after up.
// Callers that pass no opts retain identical behaviour.
func RunCLI(migrations embed.FS, migrateDir string, opts ...Option) {
	var (
		down           = flag.Bool("down", false, "Rollback all migrations")
		steps          = flag.Int("steps", 0, "Run N migrations (positive=up, negative=down)")
		version        = flag.Bool("version", false, "Print current migration version")
		force          = flag.Int("force", -1, "Force set migration version (for recovery)")
		forceDangerous = flag.Bool("force-dangerous", false, "Skip history validation when forcing (dangerous)")
		history        = flag.Bool("history", false, "Show migration history")
		historyLimit   = flag.Int("history-limit", 20, "Number of history entries to show")
		tracked        = flag.Bool("tracked", true, "Use history tracking for migrations (default: true)")
		autoDown       = flag.Bool("auto-down", false, "Allow automatically migrating down when the DB is ahead of this binary's migrations (e.g. after a rollback). Same effect as MIGRATE_AUTO_DOWN=true. Without one of these, a detected rollback fails loudly instead of running destructive down migrations unattended.")
		bypassVersion  = flag.Int("bypass-version", -1, "Operator-approved ceiling: if the DB is ahead of this binary's latest migration but at or below this version, leave the schema as-is (no migration run) instead of failing or auto-migrating down. Same effect as MIGRATE_BYPASS_VERSION. For additive-only migrations (e.g. a new column an older binary just ignores) where the extra state is safe to keep.")
	)
	flag.Parse()

	cfg := DefaultConfig()
	db, err := connect(context.Background(), cfg)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	o := applyOptions(opts)
	runner := NewRunner(db, migrations, migrateDir)

	// Handle history flag
	if *history {
		if err := runner.tracker.EnsureHistoryTable(); err != nil {
			log.Fatalf("Failed to ensure history table: %v", err)
		}
		entries, err := runner.tracker.GetHistory(*historyLimit)
		if err != nil {
			log.Fatalf("Failed to get history: %v", err)
		}
		printHistory(entries)
		return
	}

	// Handle version flag
	if *version {
		v, dirty, err := runner.Version()
		if err != nil {
			log.Fatalf("Failed to get version: %v", err)
		}
		fmt.Printf("Version: %d (dirty: %v)\n", v, dirty)
		return
	}

	// Handle force flag
	if *force >= 0 {
		log.Printf("Forcing version to %d...", *force)
		if err := runner.ForceWithValidation(*force, *forceDangerous); err != nil {
			log.Fatalf("Failed to force version: %v", err)
		}
		log.Println("Version forced successfully")
		return
	}

	// Handle steps flag
	if *steps != 0 {
		direction := "up"
		if *steps < 0 {
			direction = "down"
		}
		log.Printf("Running %d migration(s) %s...", abs(*steps), direction)
		if err := runner.Steps(*steps); err != nil {
			log.Fatalf("Failed to run steps: %v", err)
		}
		log.Println("Migration completed successfully")
		return
	}

	// Handle down flag
	if *down {
		log.Println("Rolling back all migrations...")
		if err := runner.Down(); err != nil {
			log.Fatalf("Failed to rollback: %v", err)
		}
		log.Println("Rollback completed successfully")
		return
	}

	// Bring every WithSource-registered library migration fully up to date,
	// each independently tracked (see ApplySource's doc) -- before touching
	// this binary's own migrations at all. Only on the default reconcile
	// path: -history/-version/-force/-steps/-down (all returned above) act
	// on this binary's own migrations only and never run a Source.
	for _, src := range o.sources {
		log.Printf("Applying %s's bundled migrations...", src.Name)
		if err := ApplySource(db, src); err != nil {
			log.Fatalf("Failed to apply %s's migrations: %v", src.Name, err)
		}
	}

	// bypassCeiling: an operator-approved ceiling on how far AHEAD of this
	// binary's latest migration the DB is allowed to be without failing or
	// auto-migrating down (see the rollback-detection block below). Same
	// effect as MIGRATE_BYPASS_VERSION. This never runs a migration itself
	// -- it only widens what counts as an acceptable "ahead" state, for
	// cases like an additive-only migration (a new column an older binary
	// just ignores) where rolling the schema back would drop data an
	// operator wants to keep.
	bypassCeiling := *bypassVersion
	if bypassCeiling < 0 {
		bypassCeiling = getEnvInt("MIGRATE_BYPASS_VERSION", -1)
	}

	// Default: reconcile the schema to this binary's latest known migration
	// version. Normally that means running up, but if the DB is already
	// ahead of this image's latest migration (e.g. an older image re-runs
	// this job after a rollback), Up() would silently no-op and leave the
	// newer schema in place. Detect that case explicitly rather than
	// silently doing nothing -- but don't auto-run destructive down
	// migrations unattended (wrong image, a stray version bump, a racing
	// job) without -auto-down/MIGRATE_AUTO_DOWN; fail loudly instead so a
	// human decides. MIGRATE_AUTO_DOWN exists because this binary is
	// normally deployed as a Helm Job whose args are fixed per chart --
	// env vars (Helm values' per-app `env` map) are how a team opts in.
	allowAutoDown := *autoDown || getEnvBool("MIGRATE_AUTO_DOWN", false)

	targetVersion, err := runner.LatestVersion()
	if err != nil {
		log.Fatalf("Failed to determine latest migration version: %v", err)
	}

	currentVersion, dirty, err := runner.Version()
	if err != nil {
		log.Fatalf("Failed to get current migration version: %v", err)
	}
	if dirty {
		log.Fatalf("Database is in dirty state (version %d). Use -force to recover", currentVersion)
	}

	ranUp := false
	if currentVersion > targetVersion {
		log.Printf("Detected rollback: DB is at version %d, this image's latest migration is %d.", currentVersion, targetVersion)
		switch {
		case bypassCeiling >= 0 && currentVersion <= uint(bypassCeiling):
			log.Printf("DB version %d is within the operator-approved bypass ceiling %d -- leaving schema as-is, no migration run.", currentVersion, bypassCeiling)
		case allowAutoDown:
			log.Println("Migrating down...")
			if err := runner.Migrate(targetVersion); err != nil {
				log.Fatalf("Failed to roll back migrations: %v", err)
			}
			log.Println("Rollback completed successfully")
		default:
			log.Fatalf("Refusing to auto-migrate down (destructive) without -auto-down or MIGRATE_AUTO_DOWN=true. Set one of those to roll back automatically, raise -bypass-version/MIGRATE_BYPASS_VERSION to at least %d if this ahead-state is expected and safe to leave as-is, or use -steps/-down for an explicit, manual rollback.", currentVersion)
		}
	} else {
		log.Println("Running migrations...")
		var migrationErr error
		if *tracked {
			migrationErr = runner.UpWithTracking()
		} else {
			migrationErr = runner.Up()
		}
		if migrationErr != nil {
			log.Fatalf("Failed to run migrations: %v", migrationErr)
		}
		ranUp = true
	}

	v, dirty, err := runner.Version()
	if err != nil {
		log.Fatalf("Failed to get final version: %v", err)
	}
	log.Printf("Migration completed successfully. Version: %d (dirty: %v)", v, dirty)

	// Run seeders (up only — skipped on rollback, and already returned above for down/steps/etc.)
	if ranUp {
		for i, seeder := range o.seeders {
			log.Printf("Running seeder %d/%d...", i+1, len(o.seeders))
			if err := seeder(context.Background(), db); err != nil {
				log.Fatalf("Seeder %d failed: %v", i+1, err)
			}
		}
		if len(o.seeders) > 0 {
			log.Printf("All seeders completed successfully")
		}
	}
}

// printHistory prints migration history in a formatted table
func printHistory(entries []HistoryEntry) {
	if len(entries) == 0 {
		fmt.Println("No migration history found")
		return
	}

	fmt.Println("\nMigration History:")
	fmt.Println("─────────────────────────────────────────────────────────────────────────────")
	fmt.Printf("%-10s %-8s %-10s %-10s %-12s %-10s %s\n",
		"ID", "Version", "Direction", "Status", "Duration", "Started", "Error")
	fmt.Println("─────────────────────────────────────────────────────────────────────────────")

	for _, entry := range entries {
		durationStr := "-"
		if entry.DurationMs != nil {
			durationStr = fmt.Sprintf("%dms", *entry.DurationMs)
		}

		errorStr := ""
		if entry.ErrorMessage != nil && *entry.ErrorMessage != "" {
			errorStr = truncate(*entry.ErrorMessage, 40)
		}

		fmt.Printf("%-10d %-8d %-10s %-10s %-12s %-10s %s\n",
			entry.HistoryID,
			entry.Version,
			entry.Direction,
			entry.Status,
			durationStr,
			entry.StartedAt.Format("15:04:05"),
			errorStr,
		)
	}
	fmt.Println("─────────────────────────────────────────────────────────────────────────────")
}

// truncate truncates a string to maxLen characters with ellipsis
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func connect(ctx context.Context, cfg *Config) (*sql.DB, error) {
	dsn := os.Getenv("PG_DATABASE_URL")
	if dsn == "" {
		dsn = fmt.Sprintf(
			"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			cfg.Host,
			cfg.Port,
			cfg.User,
			cfg.Password,
			cfg.Database,
			cfg.SSLMode,
		)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		var result int
		if _, err := fmt.Sscanf(value, "%d", &result); err == nil {
			return result
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if result, err := strconv.ParseBool(value); err == nil {
			return result
		}
	}
	return defaultValue
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
