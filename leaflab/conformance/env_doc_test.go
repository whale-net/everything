package conformance

import (
	"regexp"
	"strings"
	"testing"
)

// TestENVDoc_APICoversEveryVariable verifies that every environment variable
// read by leaflab/api/main.go is documented in leaflab/api/ENV.md.
// Source of record: getEnv() calls in api/main.go.
func TestENVDoc_APICoversEveryVariable(t *testing.T) {
	mainGo := mustReadFile(t, "api/main.go")
	envMD := mustReadFile(t, "api/ENV.md")

	// Extract every getEnv("VAR", ...) call in api/main.go
	varRe := regexp.MustCompile(`getEnv\("([A-Z0-9_]+)"`)
	matches := varRe.FindAllStringSubmatch(mainGo, -1)
	if len(matches) < 5 {
		t.Fatalf("only found %d getEnv(...) calls in api/main.go -- check the data dependency in BUILD.bazel", len(matches))
	}

	seen := map[string]bool{}
	for _, m := range matches {
		v := m[1]
		if seen[v] {
			continue
		}
		seen[v] = true
		if !strings.Contains(envMD, "`"+v+"`") {
			t.Errorf("api/main.go reads %q via getEnv but api/ENV.md does not document it", v)
		}
	}
}

// TestENVDoc_UICoversEveryVariable verifies that every environment variable
// read by leaflab/ui/main.go is documented in leaflab/ui/ENV.md.
// Source of record: getEnv() calls in ui/main.go.
func TestENVDoc_UICoversEveryVariable(t *testing.T) {
	mainGo := mustReadFile(t, "ui/main.go")
	envMD := mustReadFile(t, "ui/ENV.md")

	// Extract every getEnv("VAR", ...) call in ui/main.go
	varRe := regexp.MustCompile(`getEnv\("([A-Z0-9_]+)"`)
	matches := varRe.FindAllStringSubmatch(mainGo, -1)
	if len(matches) < 5 {
		t.Fatalf("only found %d getEnv(...) calls in ui/main.go -- check the data dependency in BUILD.bazel", len(matches))
	}

	seen := map[string]bool{}
	for _, m := range matches {
		v := m[1]
		if seen[v] {
			continue
		}
		seen[v] = true
		if !strings.Contains(envMD, "`"+v+"`") {
			t.Errorf("ui/main.go reads %q via getEnv but ui/ENV.md does not document it", v)
		}
	}
}
