package main

import (
	"os"
	"strings"
	"testing"
)

// TestFR11_AllowUnmappedGate_ChecksCoverageAndLogsWarningOnAcknowledge is a
// structural check -- mirroring krill/importer/importer_nfr3_test.go's own
// "read this package's own source back" style -- of cmd/main.go's
// --allow-unmapped gate (issue #2549, FR11): without --allow-unmapped, a
// non-zero coverage total must make run() return a non-zero-exit error
// before ever letting the CLI report success; with it, run() must log at
// WARNING (AGENTS.md's logging levels -- an acknowledged partial import
// "had to adjust ... to keep going", not an ERROR) rather than silently
// swallowing the acknowledgment.
//
// Exercising this end to end would mean invoking the built
// //krill/importer/cmd:import binary as a subprocess against a live
// Postgres purely to observe an exit code. The dynamic half of FR11 -- that
// a non-zero unmapped count actually appears in the report for a partial
// doc set -- is proven directly against importer.Import + Report in
// krill/conformance/whagent_net_import_integration_test.go's
// TestFR11_WhagentNetImport_UnmappedHeading_NonZeroExitWithoutAllowUnmapped;
// this test instead pins cmd/main.go's own gate logic the same lightweight
// way importer_nfr3_test.go pins a structural contract that would
// otherwise need a live process to observe.
func TestFR11_AllowUnmappedGate_ChecksCoverageAndLogsWarningOnAcknowledge(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go back (is it declared as `data` on this go_test target?): %v", err)
	}
	body := string(src)

	if !strings.Contains(body, "report.UnmappedTotal()") {
		t.Fatalf("expected run() to gate on report.UnmappedTotal() (FR11) -- found no such call in main.go")
	}
	if !strings.Contains(body, "*allowUnmapped") {
		t.Fatalf("expected run() to check the --allow-unmapped flag before deciding the gate's outcome")
	}
	if !strings.Contains(body, "slog.Warn(") {
		t.Fatalf("expected an acknowledged partial import (--allow-unmapped) to log at WARNING (AGENTS.md's logging levels), not silently succeed")
	}

	// Pin the two outcomes' relative order in the source text: the
	// unmapped-total check, then the !allowUnmapped return (the default,
	// unacknowledged, non-zero-exit path), then the WARNING log (the
	// acknowledged path) -- so a future edit that reorders these into
	// "log WARNING unconditionally, then maybe fail" cannot pass this
	// check by accident.
	totalIdx := strings.Index(body, "report.UnmappedTotal()")
	returnIdx := strings.Index(body, "!*allowUnmapped")
	warnIdx := strings.Index(body, "slog.Warn(")
	if totalIdx == -1 || returnIdx == -1 || warnIdx == -1 || !(totalIdx < returnIdx && returnIdx < warnIdx) {
		t.Fatalf("expected main.go to check UnmappedTotal, then gate on !*allowUnmapped (non-zero exit), then slog.Warn only on the acknowledged path -- got indices total=%d return=%d warn=%d", totalIdx, returnIdx, warnIdx)
	}
}
