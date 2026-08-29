// Package conformance holds cross-package NFR17 documentation-conformance
// tests for LeafLab's Phase 2 (ownership) doc set (root plan #1166, task
// #1352). These tests read real checked-in source/doc files as data (never
// a copy or a fixture) so a regression in leaflab/DATA.md, an ENV.md, or
// the schema itself is caught directly, not by proxy. Mirrors the pattern
// in tools/app_registry/conformance (env_doc_test.go, paths_test.go).
//
// Scope note: this package intentionally checks only the Phase 2 slice of
// leaflab/api/ENV.md (the rate-limit buckets, A28 claim constants, and
// admin elevation duration this task's Scope names), not every variable
// leaflab/api/main.go reads. Phase 1's base variables (PORT,
// PG_DATABASE_URL, LEAFLAB_API_AUTH_MODE, ...) are documented by a sibling
// task (#1336) on a branch not yet merged into this one's git ancestry --
// see #1352's Testing-phase comment for the full explanation. A full
// main.go-vs-ENV.md coverage test (mirroring
// tools/app_registry/conformance's env_doc_test.go exactly) is left for
// whichever task lands after both branches are reconciled.
package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whale-net/everything/leaflab/api/claim"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
)

// leaflabDir locates the leaflab/ domain root, using leaflab/BUILD.bazel
// (staged as a data dependency via the "build_file" filegroup, which lives
// in that same BUILD.bazel) as the marker file. Bazel stages data relative
// to the runfiles root; a plain `go test` run from this package's own
// directory finds it by walking up. Mirrors appRegistryDir in
// tools/app_registry/conformance/paths_test.go.
func leaflabDir(t *testing.T) string {
	t.Helper()
	for _, c := range []string{".", "..", "../..", "../../..", "../../../.."} {
		marker := filepath.Join(c, "leaflab", "BUILD.bazel")
		if st, err := os.Stat(marker); err == nil && !st.IsDir() {
			return filepath.Join(c, "leaflab")
		}
	}
	t.Fatal("could not locate leaflab/BUILD.bazel -- check the data dependency in BUILD.bazel")
	return ""
}

// mustReadFile reads a file relative to leaflabDir, failing the test on any
// error.
func mustReadFile(t *testing.T, rel string) string {
	t.Helper()
	dir := leaflabDir(t)
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v (check the data dependency in BUILD.bazel)", rel, err)
	}
	return string(b)
}

// phase2EnvVars is the Phase 2 slice of leaflab/api's environment
// configuration (NFR17 Phase-2 set, task #1352): every rate-limit bucket's
// pair of variables (ratelimit.EnvVarNames over ratelimit.Buckets), every
// A28 claim constant (claim.Env* -- see leaflab/api/claim/config.go), and
// admin elevation's duration (LEAFLAB_ADMIN_ELEVATION_MINUTES, a bare
// literal in leaflab/api/main.go with no exported constant of its own,
// hardcoded here for the same reason). Read from the actual exported
// source of record (ratelimit.EnvVarNames, claim.Env*) rather than
// hand-duplicating the strings, so a rename in either package fails this
// test immediately instead of silently drifting from leaflab/api/ENV.md.
func phase2EnvVars(t *testing.T) []string {
	t.Helper()
	names := []string{"LEAFLAB_ADMIN_ELEVATION_MINUTES"}
	for _, b := range ratelimit.Buckets {
		limitVar, windowVar := ratelimit.EnvVarNames(b)
		names = append(names, limitVar, windowVar)
	}
	names = append(names,
		claim.EnvRoundsRequired,
		claim.EnvRoundBoundSeconds,
		claim.EnvChallengeLifetimeSecs,
		claim.EnvAttemptsPerRound,
		claim.EnvCooldownSeconds,
		claim.EnvRestartThresholdSecs,
		claim.EnvMaxConcurrentOpen,
	)
	if len(names) < 10 {
		t.Fatalf("only found %d Phase 2 env vars -- check ratelimit.Buckets/claim.Env* wiring", len(names))
	}
	return names
}

// TestEnvDoc_APICoversPhase2Variables is the NFR17 Phase-2 doc check for
// leaflab-api: every Phase 2 environment variable (rate-limit buckets, A28
// claim constants, admin elevation duration) must be documented in
// leaflab/api/ENV.md, backtick-quoted, matching this repo's ENV.md
// convention.
func TestEnvDoc_APICoversPhase2Variables(t *testing.T) {
	envMD := mustReadFile(t, "api/ENV.md")
	for _, v := range phase2EnvVars(t) {
		if !strings.Contains(envMD, "`"+v+"`") {
			t.Errorf("leaflab/api/ENV.md does not document %q (Phase 2, NFR17)", v)
		}
	}
}
