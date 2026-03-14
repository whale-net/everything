// Package db provides a standard PostgreSQL connection pool.
// All services should use this instead of calling pgxpool.New directly.
//
// The pool URL is read from PG_DATABASE_URL by default.
// Pass a non-empty url to override (useful when a service connects to two databases).
package db

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool creates a pgxpool from the given URL, or from PG_DATABASE_URL when url is empty.
// The pool is health-checked before returning.
func NewPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	if url == "" {
		url = os.Getenv("PG_DATABASE_URL")
	}
	if url == "" {
		return nil, fmt.Errorf("no database URL: set PG_DATABASE_URL or pass a URL explicitly")
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return pool, nil
}
