package migrate

import (
	"context"
	"database/sql"
)

// Seeder is an idempotent function that seeds reference data after migrations.
// It receives the same *sql.DB used by the runner and must be safe to call
// multiple times (INSERT ON CONFLICT DO NOTHING / DO UPDATE style).
type Seeder func(ctx context.Context, db *sql.DB) error

// Option configures the migration runner / CLI.
type Option func(*runOptions)

// WithSeeder registers a Seeder to run after a successful up migration.
// Multiple seeders may be registered; they run in registration order.
// Seeders are only invoked on up operations, not down/version/history.
func WithSeeder(s Seeder) Option {
	return func(o *runOptions) {
		o.seeders = append(o.seeders, s)
	}
}

type runOptions struct {
	seeders []Seeder
}

func applyOptions(opts []Option) *runOptions {
	o := &runOptions{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}
