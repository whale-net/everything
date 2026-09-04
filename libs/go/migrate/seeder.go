package migrate

import (
	"context"
	"database/sql"
	"embed"
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

// WithSource applies an additional migration directory (typically a shared
// library's own `//go:embed migrations/*.sql`, e.g. htmxauth.Migrations)
// alongside the caller's own migrations passed to RunCLI, before RunCLI's
// usual flag handling runs against the caller's own migrations. name must
// be unique among a binary's WithSource calls; see Source and ApplySource's
// doc for why this is tracked independently rather than merged into the
// caller's own version sequence.
func WithSource(name string, fsys embed.FS, dir string) Option {
	return func(o *runOptions) {
		o.sources = append(o.sources, Source{Name: name, FS: fsys, Dir: dir})
	}
}

type runOptions struct {
	seeders []Seeder
	sources []Source
}

func applyOptions(opts []Option) *runOptions {
	o := &runOptions{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}
