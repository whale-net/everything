package migrate

import (
	"context"
	"database/sql"
	"embed"
	"flag"
	"fmt"
	"log"
	"os"
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

// DefaultConfig returns a config with defaults from environment variables
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

// RunCLI is a convenience function for running migration CLI
// migrations: embedded filesystem with migration files
// migrateDir: subdirectory within migrations (e.g., "migrations")
func RunCLI(migrations embed.FS, migrateDir string) {
	var (
		down           = flag.Bool("down", false, "Rollback all migrations")
		steps          = flag.Int("steps", 0, "Run N migrations (positive=up, negative=down)")
		version        = flag.Bool("version", false, "Print current migration version")
		force          = flag.Int("force", -1, "Force set migration version (for recovery)")
		forceDangerous = flag.Bool("force-dangerous", false, "Skip history validation when forcing (dangerous)")
		history        = flag.Bool("history", false, "Show migration history")
		historyLimit   = flag.Int("history-limit", 20, "Number of history entries to show")
		tracked        = flag.Bool("tracked", true, "Use history tracking for migrations (default: true)")
	)
	flag.Parse()

	cfg := DefaultConfig()
	db, err := connect(context.Background(), cfg)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

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

	// Default: run up
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

	v, dirty, err := runner.Version()
	if err != nil {
		log.Fatalf("Failed to get final version: %v", err)
	}
	log.Printf("Migration completed successfully. Version: %d (dirty: %v)", v, dirty)
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
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host,
		cfg.Port,
		cfg.User,
		cfg.Password,
		cfg.Database,
		cfg.SSLMode,
	)

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

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
