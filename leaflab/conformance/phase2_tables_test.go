package conformance

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// phase2Migrations lists every migration file, by base name, that root plan
// #1166 Phase 2 (ownership) added or is documented to add. Each is parsed
// for CREATE TABLE statements and every table found must appear in
// leaflab/DATA.md's ER diagram or SCD2/not-SCD2 classification lists
// (NFR17 Phase-2 set, task #1352).
//
// 032_support_reference.up.sql (task #1346) is listed here even though it
// has not yet landed in this branch's own git ancestry -- see #1352's
// Implementation-phase note, which documented support_reference in
// DATA.md/ARCHITECTURE.md/ENV.md by reading it off task #1346's (closed)
// branch directly, ahead of the git merge. readMigrationIfPresent below
// skips a migration file that does not exist yet rather than failing, so
// this list stays accurate as the plan's stacked branches merge forward
// without ever going stale or needing to be re-edited.
var phase2Migrations = []string{
	"015_ownership.up.sql",
	"016_audit_log.up.sql",
	"018_household_grant.up.sql",
	"021_claim_challenge.up.sql",
	"022_departure_record.up.sql",
	"023_board_release_token.up.sql",
	"029_admin_elevation.up.sql",
	"032_support_reference.up.sql",
}

var createTableRe = regexp.MustCompile(`(?i)CREATE TABLE(?:\s+IF NOT EXISTS)?\s+(\w+)`)

// readMigrationIfPresent reads a migration file relative to
// leaflab/migrate/migrations, returning ("", false) if the file does not
// exist yet (a sibling stacked task not yet merged -- see phase2Migrations'
// doc comment) rather than failing the test.
func readMigrationIfPresent(t *testing.T, dir, name string) (string, bool) {
	t.Helper()
	path := filepath.Join(dir, "migrate", "migrations", name)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b), true
}

// tableDocumented reports whether table appears in leaflab/DATA.md either
// as an ER-diagram entity ("tablename {", the mermaid entity block opener)
// or backtick-quoted anywhere else (the SCD2 / not-SCD2 classification
// tables' convention).
func tableDocumented(dataMD, table string) bool {
	if strings.Contains(dataMD, table+" {") {
		return true
	}
	if strings.Contains(dataMD, "`"+table+"`") {
		return true
	}
	return false
}

// TestDataDoc_Phase2TablesDocumented is the NFR17 Phase-2 doc check for
// leaflab/DATA.md: every table a Phase 2 ownership migration creates must
// be documented -- either drawn in the ER diagram or explicitly classified
// SCD2 / not-SCD2 (NFR6.3).
func TestDataDoc_Phase2TablesDocumented(t *testing.T) {
	dir := leaflabDir(t)
	dataMD := mustReadFile(t, "DATA.md")

	sawAnyMigration := false
	for _, mig := range phase2Migrations {
		sql, ok := readMigrationIfPresent(t, dir, mig)
		if !ok {
			continue
		}
		sawAnyMigration = true

		matches := createTableRe.FindAllStringSubmatch(sql, -1)
		if len(matches) == 0 {
			t.Errorf("migration %s matched no CREATE TABLE statement -- check createTableRe against its SQL", mig)
			continue
		}
		for _, m := range matches {
			table := m[1]
			if !tableDocumented(dataMD, table) {
				t.Errorf("migration %s creates table %q but leaflab/DATA.md does not list it in the ER diagram or the SCD2/not-SCD2 tables", mig, table)
			}
		}
	}
	if !sawAnyMigration {
		t.Fatal("none of the known Phase 2 migration files were found under leaflab/migrate/migrations -- check the data dependency in BUILD.bazel or the phase2Migrations list")
	}
}
