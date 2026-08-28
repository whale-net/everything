// Package conformance holds cross-package structural conformance checks
// for the leaflab API that a doc comment alone can't enforce (mirroring
// tools/app_registry/conformance and leaflab/ui's local
// nfr18_conformance_test.go placeholder).
//
// FR49 (#1375): the board's reported manifest inventory
// (leaflab/api/GetReportedInventory, backed by migration 035's
// board_manifest_report/board_manifest_report_entry tables) is a report,
// never a source -- it is used only to compute drift
// (leaflab/api/GetConfigDrift) against the stored desired state, and must
// never feed a config materialisation path. leaflab/api/config's own doc
// comment already states this invariant in prose ("never the reported
// manifest, FR49"); the guard this package adds asserts it structurally,
// by grepping that package's own checked-in source for any import of or
// reference to the manifest-report tables/types, so a future change that
// quietly wires the manifest in as a fallback source fails a test instead
// of only violating a comment. See #1375's Implementation phase for the
// guard itself.
package conformance
