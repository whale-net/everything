package conformance

import (
	"regexp"
	"strings"
	"testing"
)

// TestTiltfile_UIUsesSameDatabaseURLAsRegistry is the local-stack half of
// NFR-3 (FR-48, FR-49): the Tiltfile must set the UI's database URL from
// the exact same value used to build the registry's PG_DATABASE_URL, so
// local dev cannot drift into looking like two databases. It checks this
// structurally, not just textually -- both blocks must reference the same
// bound Starlark variable, and that variable must be assigned exactly
// once.
func TestTiltfile_UIUsesSameDatabaseURLAsRegistry(t *testing.T) {
	tf := mustReadFile(t, "Tiltfile")

	// pg_database_url must be computed exactly once. If a second,
	// independently-computed pg_database_url ever appears (e.g. someone
	// adds a per-component override), this test must catch it even though
	// the variable name would still match below.
	assigns := regexp.MustCompile(`(?m)^pg_database_url\s*=`).FindAllString(tf, -1)
	if len(assigns) != 1 {
		t.Fatalf("expected exactly 1 top-level `pg_database_url = ...` assignment in the Tiltfile, found %d",
			len(assigns))
	}

	// Every k8s_yaml(blob("""...""").format(...)) block that declares a
	// PG_DATABASE_URL container env var must pass pg_database_url as the
	// format() keyword feeding it -- i.e. the exact same bound variable,
	// not a copy or a re-derived value.
	blockRe := regexp.MustCompile(`(?s)k8s_yaml\(blob\(""".*?"""\.format\(.*?\)\)\)`)
	blocks := blockRe.FindAllString(tf, -1)
	if len(blocks) < 2 {
		t.Fatalf("expected at least 2 k8s_yaml(blob(...).format(...)) blocks in the Tiltfile, found %d -- "+
			"check the data dependency in BUILD.bazel", len(blocks))
	}

	var uiBlockChecked, otherBlockChecked bool
	for _, b := range blocks {
		if !strings.Contains(b, "name: PG_DATABASE_URL") {
			continue // this component doesn't set PG_DATABASE_URL at all
		}
		if !strings.Contains(b, `value: "{pg_database_url}"`) {
			t.Errorf("a Tiltfile block sets PG_DATABASE_URL but not from the {pg_database_url} placeholder:\n%s", b)
			continue
		}
		if !strings.Contains(b, "pg_database_url=pg_database_url") {
			t.Errorf("a Tiltfile block's PG_DATABASE_URL env var is not fed from the same "+
				"pg_database_url binding via .format(pg_database_url=pg_database_url, ...):\n%s", b)
			continue
		}
		if strings.Contains(b, "name: app-registry-ui") || strings.Contains(b, "app-registry-ui\n") {
			uiBlockChecked = true
		} else {
			otherBlockChecked = true
		}
	}

	if !uiBlockChecked {
		t.Error("did not find a Tiltfile block for app-registry-ui that sets PG_DATABASE_URL from " +
			"pg_database_url -- the UI must source its database URL from the same value as the rest " +
			"of App Registry (see ENV.md 'Local Development (Tilt)')")
	}
	if !otherBlockChecked {
		t.Error("did not find any other App Registry component's Tiltfile block sourcing " +
			"PG_DATABASE_URL from pg_database_url -- check the regex/data dependency, this should " +
			"match migration/api/worker too")
	}
}
