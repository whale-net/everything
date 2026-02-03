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
		SSLMode:         getEnv("DB_SSLMODE", "disable"),
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
		down    = flag.Bool("down", false, "Rollback all migrations")
		steps   = flag.Int("steps", 0, "Run N migrations (positive=up, negative=down)")
		version = flag.Bool("version", false, "Print current migration version")
		force   = flag.Int("force", -1, "Force set migration version (for recovery)")
	)
	flag.Parse()

	cfg := DefaultConfig()
	db, err := connect(context.Background(), cfg)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	runner := NewRunner(db, migrations, migrateDir)

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
		if err := runner.Force(*force); err != nil {
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
	if err := runner.Up(); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	v, dirty, err := runner.Version()
	if err != nil {
		log.Fatalf("Failed to get final version: %v", err)
	}
	log.Printf("Migration completed successfully. Version: %d (dirty: %v)", v, dirty)
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
