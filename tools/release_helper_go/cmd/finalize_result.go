package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// writeFinalizeResultFile writes res (finalize-app's/finalize-chart's
// ReleaseResult) to outputDir as <domain>-<name>.json -- the bookkeeping
// sidecar file worker/release/finalize.go's readFinalizeResult reads back
// after this command runs as a subprocess, to recover res.EffectiveVersion
// (see finalize_app.go/finalize_chart.go's --output-dir flag docs). Shared
// by both commands (previously two near-identical ~15-line blocks) so the
// naming convention and error wrapping can't drift between them.
//
// By the time this is called, the actual release/publish work (registry
// retag or chart package+ChartMuseum upload, plus BeginPublish ->
// RecordArtifact/FailPublish's App Registry bookkeeping) has already fully
// committed via ExecuteFinalizeApp/ExecuteFinalizeChart -- this file write
// is a secondary, best-effort convenience for the Temporal caller, not part
// of that transaction. Callers must treat a non-nil error here as a warning
// to surface, NOT as cause to fail the command/exit non-zero: doing so
// would make a disk-full/permission error on this sidecar write
// indistinguishable, from finalize.go's perspective, from the actual
// publish having failed -- routing a real release straight to
// ReleaseRunTargetStateFailed with no fallback check (see this PR's
// Finding 1).
func writeFinalizeResultFile(outputDir, domain, name string, res *ReleaseResult) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("create --output-dir %s: %w", outputDir, err)
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal finalize result: %w", err)
	}
	outPath := filepath.Join(outputDir, fmt.Sprintf("%s-%s.json", domain, name))
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("write finalize result %s: %w", outPath, err)
	}
	return nil
}
