package schema

import "testing"

// TestMigrationsEmbedResolves asserts the embedded migrations FS resolves
// without error and Dir matches the subdirectory golang-migrate.RunCLI
// expects, even before any real *.sql migration files exist. See schema.go
// for why the embed directive uses "all:migrations" rather than a "*.sql"
// glob while the directory is empty.
func TestMigrationsEmbedResolves(t *testing.T) {
	entries, err := Migrations.ReadDir(Dir)
	if err != nil {
		t.Fatalf("Migrations.ReadDir(%q) failed: %v", Dir, err)
	}
	_ = entries

	if Dir != "migrations" {
		t.Errorf("Dir = %q, want %q", Dir, "migrations")
	}
}
