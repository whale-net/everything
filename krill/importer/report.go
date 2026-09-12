package importer

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ReportEntry is one line of the import report (FR16): a source
// identifier -- a capability, decision, milestone id, or a persona/non-goal
// name (neither of which carries a source-document number) -- and the
// entity id it became.
type ReportEntry struct {
	// Kind is one of "capability", "decision", "persona", "non_goal", or
	// "milestone".
	Kind string
	// SourceID is how the source document names this entity: "C3", "LB2",
	// "M1", or (for a persona/non-goal, which the source names only by
	// their prose heading, not a number) their Name.
	SourceID string
	Name     string
	EntityID uuid.UUID
}

// Report is FR16's entity-id report: every capability, decision, persona,
// non-goal, and milestone the importer found in the source, alongside the
// entity id it became. Report is both machine-readable (its exported
// fields marshal directly to JSON) and human-readable (Render).
//
// Coverage is FR11's completeness accounting (issue #2549): populated by
// Import from ComputeCoverage (coverage.go) alongside Entries, so a reader
// of the report can tell not just what became what, but whether anything
// recognizable in the source was left unmapped.
type Report struct {
	ProductID   uuid.UUID
	ProductName string
	Entries     []ReportEntry
	Coverage    []CoverageEntry
}

// UnmappedTotal is the report's own view of coverage.go's UnmappedTotal --
// the number cmd/main.go's --allow-unmapped gate (FR11) checks against
// zero before deciding whether a partial import may proceed.
func (r *Report) UnmappedTotal() int {
	return UnmappedTotal(r.Coverage)
}

func (r *Report) add(kind, sourceID, name string, id uuid.UUID) {
	r.Entries = append(r.Entries, ReportEntry{Kind: kind, SourceID: sourceID, Name: name, EntityID: id})
}

// Render is the report's human-readable rendering: one line per entity,
// grouped in the order the source document presented them, followed by
// FR11's coverage section -- printed loudly (a leading blank line and a
// banner) whenever any file has a non-zero unmapped count, per issue
// #2549's "an Operator/Admin must not be able to mistake a partial import
// for a complete one."
func (r *Report) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Import report: %s (product %s)\n", r.ProductName, r.ProductID)
	for _, e := range r.Entries {
		fmt.Fprintf(&b, "  [%-10s] %-6s %-60s -> %s\n", e.Kind, e.SourceID, truncate(e.Name, 60), e.EntityID)
	}

	total := r.UnmappedTotal()
	fmt.Fprintf(&b, "\nCoverage (%d file(s), %d unmapped item(s)):\n", len(r.Coverage), total)
	for _, c := range r.Coverage {
		fmt.Fprintf(&b, "  %-40s unmapped=%d\n", c.SourceFile, c.UnmappedCount)
		for _, item := range c.UnmappedItems {
			fmt.Fprintf(&b, "    - %s\n", truncate(item, 80))
		}
	}
	if total > 0 {
		fmt.Fprintf(&b, "\n*** WARNING: %d item(s) were recognized but not imported -- see above. Pass --allow-unmapped to proceed anyway. ***\n", total)
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
