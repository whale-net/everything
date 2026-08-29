package main

// Doc-coverage conformance check for NFR17 (Phase-3 attribution
// documentation, #1367's Testing section): "every table and continuous
// aggregate created by Phase 3 migrations appears in leaflab/DATA.md (or
// its split directory) and is classified SCD2 / not-SCD2."
//
// leaflab/conformance/ (the shared conformance suite #1367 names) does not
// exist in this worktree's ancestry yet -- it is created by a sibling task
// (#1366) not merged into this branch. This is the local, same-package
// placeholder the situation calls for, mirroring
// leaflab/ui/nfr18_conformance_test.go's precedent.
//
// Deliberately derives each table's expected SCD2/not-SCD2 classification
// from the migration's own CREATE TABLE column list (does it declare both
// valid_from and valid_to?) rather than hardcoding "table X should be
// SCD2" -- so the test stays correct if a future migration edit changes a
// table's shape, and a genuinely new table only needs a name added to
// phase3Entities below, not a hand-maintained classification.

import (
	"embed"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

//go:embed migrations/017_plant_region_history.up.sql migrations/022_tiers.up.sql migrations/033_boundary_capture.up.sql
var docCoverageMigrations embed.FS

// phase3Entities are every table/continuous-aggregate this task's scope
// names explicitly ("the ER diagram (with plant_region_history,
// boundary_capture, boundary_partial, the tier continuous aggregates...)"
// and "the SCD2 table list (add plant_region_history)"). Table and
// continuous-aggregate name extraction below is parsed from the migration
// files rather than hardcoded; this list only says *which* migration file
// each entity's definition lives in, so the parser knows where to look.
var phase3Entities = []struct {
	name    string
	migFile string
}{
	{"plant_region_history", "migrations/017_plant_region_history.up.sql"},
	{"sensor_reading_5m", "migrations/022_tiers.up.sql"},
	{"sensor_reading_1h", "migrations/022_tiers.up.sql"},
	{"boundary_capture", "migrations/033_boundary_capture.up.sql"},
	{"boundary_partial", "migrations/033_boundary_capture.up.sql"},
}

var createTableRe = regexp.MustCompile(`(?s)CREATE TABLE (\w+) \((.*?)\n\);`)
var createMatViewRe = regexp.MustCompile(`CREATE MATERIALIZED VIEW (\w+)`)

// classify reports whether name is a base table declaring the SCD2
// valid_from/valid_to pair, or a continuous aggregate (never SCD2 -- the
// convention doesn't apply to a derived, recomputed relation).
func classify(t *testing.T, migSQL, name string) (isSCD2, isContinuousAggregate bool) {
	t.Helper()
	for _, m := range createTableRe.FindAllStringSubmatch(migSQL, -1) {
		if m[1] != name {
			continue
		}
		cols := m[2]
		return strings.Contains(cols, "valid_from") && strings.Contains(cols, "valid_to"), false
	}
	for _, m := range createMatViewRe.FindAllStringSubmatch(migSQL, -1) {
		if m[1] == name {
			return false, true
		}
	}
	t.Fatalf("could not find CREATE TABLE or CREATE MATERIALIZED VIEW %s in migration SQL -- phase3Entities/regex out of sync with the migration file", name)
	return false, false
}

// findLeaflabDataMD locates leaflab/DATA.md by walking up from the test's
// working directory, mirroring leaflab/ui/nfr18_conformance_test.go's
// leaflabUIDir marker-file pattern.
func findLeaflabDataMD(t *testing.T) string {
	t.Helper()
	for _, c := range []string{".", "..", "../..", "../../..", "../../../..", "../../../../.."} {
		p := filepath.Join(c, "leaflab", "DATA.md")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	t.Fatal("could not locate leaflab/DATA.md -- check the data dependency in BUILD.bazel")
	return ""
}

// normalizeProse strips Markdown blockquote ">" line-prefixes and collapses
// all whitespace to single spaces, so a classification phrase like "not
// SCD2" is found by substring search even when DATA.md soft-wraps it
// across a line break (or a blockquote-continuation line) in the raw
// source.
func normalizeProse(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, ">")
		lines[i] = strings.TrimSpace(l)
	}
	return strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
}

// window returns up to n bytes of content starting at name's first
// backtick-quoted occurrence, so a classification-token search can be
// scoped near the name instead of matching anywhere in a ~600-line doc.
func window(content, name string, n int) (string, bool) {
	marker := "`" + name + "`"
	idx := strings.Index(content, marker)
	if idx < 0 {
		return "", false
	}
	end := idx + n
	if end > len(content) {
		end = len(content)
	}
	return content[idx:end], true
}

// TestPhase3Entities_DocumentedAndClassifiedInDataMD proves every
// Phase-3-migration table/continuous-aggregate has both a DATA.md mention
// and a classification consistent with its own migration-declared shape --
// this fails the moment a table is added to one of these migration files
// without a corresponding phase3Entities entry AND doc update.
func TestPhase3Entities_DocumentedAndClassifiedInDataMD(t *testing.T) {
	dataPath := findLeaflabDataMD(t)
	dataBytes, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("read %s: %v", dataPath, err)
	}
	content := string(dataBytes)

	if !strings.Contains(content, "SCD2 tables in this schema") {
		t.Fatalf("%s: missing the SCD2 table list -- guard is vacuous, check the data dependency", dataPath)
	}

	migCache := map[string]string{}
	for _, e := range phase3Entities {
		if _, ok := migCache[e.migFile]; !ok {
			b, err := docCoverageMigrations.ReadFile(e.migFile)
			if err != nil {
				t.Fatalf("read embedded %s: %v", e.migFile, err)
			}
			migCache[e.migFile] = string(b)
		}
	}

	for _, e := range phase3Entities {
		isSCD2, isCA := classify(t, migCache[e.migFile], e.name)

		marker := "`" + e.name + "`"
		if !strings.Contains(content, marker) {
			t.Errorf("%s: %s (from %s) is not mentioned in DATA.md at all", dataPath, marker, e.migFile)
			continue
		}

		w, _ := window(content, e.name, 600)
		lowerW := strings.ToLower(normalizeProse(w))

		switch {
		case isSCD2:
			// Expect a markdown table row inside the SCD2 table list, i.e.
			// the marker appears at the start of a "| `name` |" row
			// somewhere in the document.
			rowRe := regexp.MustCompile(`(?m)^\|\s*` + regexp.QuoteMeta(marker))
			if !rowRe.MatchString(content) {
				t.Errorf("%s: %s is an SCD2 table (declares valid_from/valid_to in %s) but has no SCD2-table-list row in DATA.md", dataPath, marker, e.migFile)
			}
		case isCA:
			if !strings.Contains(lowerW, "continuous aggregate") {
				t.Errorf("%s: %s is a continuous aggregate (%s) but is not documented as one near its first mention in DATA.md", dataPath, marker, e.migFile)
			}
		default:
			if !strings.Contains(lowerW, "not scd2") {
				t.Errorf("%s: %s is not SCD2 (no valid_from/valid_to in %s) but has no explicit \"not SCD2\" classification near its first mention in DATA.md", dataPath, marker, e.migFile)
			}
		}
	}
}
