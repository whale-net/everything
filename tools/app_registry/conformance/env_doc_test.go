package conformance

import (
	"regexp"
	"strings"
	"testing"
)

// TestENVDoc_UISectionCoversEveryVariable is the FR-49 doc check: every
// environment variable ui/main.go's LoadConfig actually reads must be
// documented in tools/app_registry/ENV.md's "UI" section -- and there must
// be exactly one ENV.md in the domain (AGENTS.md: "one ENV.md per domain
// per AGENTS.md; the UI does not get its own file").
func TestENVDoc_UISectionCoversEveryVariable(t *testing.T) {
	mainGo := mustReadFile(t, "ui/main.go")
	envMD := mustReadFile(t, "ENV.md")

	// Extract every getEnv("VAR", ...) call in main.go -- the source of
	// record for what the UI actually reads (see ENV.md's own framing:
	// "ui/main.go's LoadConfig, the source of record for defaults below").
	varRe := regexp.MustCompile(`getEnv\("([A-Z0-9_]+)"`)
	matches := varRe.FindAllStringSubmatch(mainGo, -1)
	if len(matches) < 5 {
		t.Fatalf("only found %d getEnv(...) calls in ui/main.go -- check the data dependency in BUILD.bazel", len(matches))
	}

	// Isolate the "## UI (`app-registry-ui`)" section so a variable
	// mentioned only in passing elsewhere in ENV.md doesn't count.
	secIdx := strings.Index(envMD, "## UI (`app-registry-ui`)")
	if secIdx == -1 {
		t.Fatal("tools/app_registry/ENV.md has no '## UI (`app-registry-ui`)' section")
	}
	section := envMD[secIdx:]
	if next := regexp.MustCompile(`(?m)^## `).FindStringIndex(section[1:]); next != nil {
		section = section[:next[0]+1]
	}

	seen := map[string]bool{}
	for _, m := range matches {
		v := m[1]
		if seen[v] {
			continue
		}
		seen[v] = true
		if !strings.Contains(section, "`"+v+"`") {
			t.Errorf("ui/main.go reads %q via getEnv but ENV.md's UI section does not document it", v)
		}
	}

	// The documented rules this issue requires: same database, same
	// schema, sessions-only, no cookie fallback.
	required := []string{
		"same database",
		"same schema",
		"session store",
		"never falls back to cookie",
	}
	lower := strings.ToLower(section)
	for _, phrase := range required {
		if !strings.Contains(lower, phrase) {
			t.Errorf("ENV.md's UI section does not mention %q -- FR-49 requires the same-database, "+
				"same-schema, sessions-only, and no-cookie-fallback rules to be stated there", phrase)
		}
	}
}

// TestENVDoc_NoSecondENVMDInUIPackage guards AGENTS.md's "one ENV.md per
// domain" rule directly against the filesystem, via the same conformance
// filegroup the NFR-3 test uses -- not just against what this doc says
// about itself.
func TestENVDoc_NoSecondENVMDInUIPackage(t *testing.T) {
	dir := appRegistryDir(t)
	entries := mustReadDir(t, dir+"/ui")
	for _, name := range entries {
		if strings.EqualFold(name, "ENV.md") {
			t.Errorf("found %s/ui/%s -- the UI must not have its own ENV.md; "+
				"its variables belong in tools/app_registry/ENV.md's UI section", dir, name)
		}
	}
}
