package schema

import "testing"

// TestMigrationsEmbedResolves asserts the embedded migrations FS resolves
// without error and Dir matches the subdirectory golang-migrate.RunCLI
// expects. See audience_score_system/migrate/schema/schema_test.go for the
// precedent this mirrors.
func TestMigrationsEmbedResolves(t *testing.T) {
	entries, err := Migrations.ReadDir(Dir)
	if err != nil {
		t.Fatalf("Migrations.ReadDir(%q) failed: %v", Dir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("Migrations.ReadDir(%q) returned no entries -- expected at least 001_initial_schema.up/down.sql", Dir)
	}

	if Dir != "migrations" {
		t.Errorf("Dir = %q, want %q", Dir, "migrations")
	}
}
