package importer

import "fmt"

// CoverageEntry is FR11's completeness accounting for one source file in an
// imported doc set: how many headings or list items Parse recognized as
// syntax it understands (a heading, a `-` bullet, a fenced block entry) but
// did not map to any entity it returns -- as opposed to prose, structural
// headings the layout doesn't require mapping (e.g. a `##` divider with no
// bullets under it), or a line Parse's own grammar was never meant to
// recognize at all. UnmappedItems carries the literal text of each such
// line so a report reader can tell at a glance what was skipped, not just
// how many.
//
// This is scaffolding for FR11 item 2 (issue #2549): ComputeCoverage below
// is a stub. The actual "recognized syntax with no entity" classification
// -- e.g. a `- ...` bullet under a heading Parse doesn't scan for entities,
// or a capability/decision/milestone line whose id token Parse's regexes
// fail to match for a formatting reason -- is Implementation-phase work.
type CoverageEntry struct {
	SourceFile    string
	UnmappedCount int
	UnmappedItems []string
}

// ComputeCoverage re-scans rootPath's doc set (the same files Parse just
// read) and reports, per file, whatever recognized-but-unmapped syntax it
// finds. It never touches krill/store, mirroring Parse's own "never
// touches a database" contract -- coverage accounting is a property of the
// parse, not the write.
//
// TODO(#2549 Implementation phase): walk each source file's lines the same
// way parse.go's section scanners do and flag anything that looks like an
// entity-shaped line (a bullet under Personas/Non-goals, a `Cn —`/`LBn —`
// line, a `### M<n>` heading) that the corresponding Parsed* slice does not
// account for. Returns no entries (i.e. reports zero unmapped items) until
// that lands -- callers must not treat an empty result as a completeness
// proof yet.
func ComputeCoverage(rootPath string, parsed *ParsedProduct) ([]CoverageEntry, error) {
	if rootPath == "" {
		return nil, fmt.Errorf("krill/importer: ComputeCoverage: rootPath must not be empty")
	}
	if parsed == nil {
		return nil, fmt.Errorf("krill/importer: ComputeCoverage: parsed must not be nil")
	}
	return nil, nil
}

// UnmappedTotal sums UnmappedCount across every CoverageEntry -- the single
// number cmd/main.go's --allow-unmapped gate (FR11) checks against zero.
func UnmappedTotal(coverage []CoverageEntry) int {
	total := 0
	for _, c := range coverage {
		total += c.UnmappedCount
	}
	return total
}
