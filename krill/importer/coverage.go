package importer

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

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
// FR11 item 2 (issue #2549): ComputeCoverage below implements the
// "recognized syntax with no entity" classification -- e.g. a `- ...`
// bullet under a heading Parse scans for entities but whose shape doesn't
// match, or a capability/decision/milestone line whose id token Parse's
// regexes fail to match for a formatting reason.
type CoverageEntry struct {
	SourceFile    string
	UnmappedCount int
	UnmappedItems []string
}

// Loose id-token regexes used only by coverage accounting, never by Parse
// itself. Each is deliberately more permissive than parse.go's own strict
// regex for the same shape (personaRe, nonGoalRe, decisionTitleRe,
// capabilityRe, milestoneHdRe): it only needs to recognize "this line is
// trying to be an entity of this kind," not parse it correctly. A line
// that matches the loose regex but not the strict one -- or matches the
// strict one but whose extracted id is absent from the corresponding
// Parsed* slice, which would mean Parse silently dropped something it
// otherwise appeared to recognize -- is what "recognized but unmapped"
// means.
var (
	looseDecisionRe   = regexp.MustCompile(`^LB(\d+)\b`)
	looseCapabilityRe = regexp.MustCompile(`^-?\s*\*{0,2}C(\d+)\*{0,2}\b`)
	looseMilestoneRe  = regexp.MustCompile(`^#{2,3}\s*M(\d+)\b`)
)

// ComputeCoverage re-scans rootPath's doc set (the same three files Parse
// just read: PRODUCT.md, product/02-capability-map.md,
// product/03-roadmap.md) and reports, per file, whatever recognized-but-
// unmapped syntax it finds. It never touches krill/store, mirroring
// Parse's own "never touches a database" contract -- coverage accounting
// is a property of the parse, not the write.
//
// product/01-current-state.md is deliberately not scanned: parse.go's own
// ParsedProduct doc comment states "current state" carries no entity of
// its own in krill's model, so Parse never reads that file either --
// there is nothing for coverage to hold it accountable to.
//
// The classification per file:
//   - PRODUCT.md: every `- ...` bullet under `## Personas` not matching
//     personaRe, every `- ...` bullet under `## Non-goals` not matching
//     nonGoalRe, and every line under `## Load-bearing decisions` that
//     looks like an `LB<n>` title (looseDecisionRe) whose id is not in
//     parsed.Decisions.
//   - product/02-capability-map.md: every line anywhere in the file that
//     looks like a `C<n>` capability line (looseCapabilityRe) whose id is
//     not in any of parsed.Buckets -- this also catches a capability-
//     shaped line sitting under a heading parseCapabilityMap does not
//     recognize as a bucket (Now/Next/Later), since such a line never
//     makes it into parsed.Buckets even when it is otherwise
//     well-formed.
//   - product/03-roadmap.md: every `#{2,3} M<n>` heading
//     (looseMilestoneRe) whose id is not in parsed.Milestones.
//
// A line the grammar was never meant to recognize at all (ordinary prose,
// a structural heading with no bullets under it, a cross-reference to an
// id elsewhere in running text) never matches a loose regex in the first
// place, so it is never counted -- this mirrors CoverageEntry's own doc
// comment on what "recognized syntax" excludes.
func ComputeCoverage(rootPath string, parsed *ParsedProduct) ([]CoverageEntry, error) {
	if rootPath == "" {
		return nil, fmt.Errorf("krill/importer: ComputeCoverage: rootPath must not be empty")
	}
	if parsed == nil {
		return nil, fmt.Errorf("krill/importer: ComputeCoverage: parsed must not be nil")
	}

	productPath := filepath.Join(rootPath, "PRODUCT.md")
	capabilityPath := filepath.Join(rootPath, "product", "02-capability-map.md")
	roadmapPath := filepath.Join(rootPath, "product", "03-roadmap.md")

	productBody, err := readFile(productPath)
	if err != nil {
		return nil, err
	}
	capabilityBody, err := readFile(capabilityPath)
	if err != nil {
		return nil, err
	}
	roadmapBody, err := readFile(roadmapPath)
	if err != nil {
		return nil, err
	}

	knownPersonas := map[string]bool{}
	for _, p := range parsed.Personas {
		knownPersonas[p.Name] = true
	}
	knownNonGoals := map[string]bool{}
	for _, ng := range parsed.NonGoals {
		knownNonGoals[ng.Name] = true
	}
	knownDecisions := map[string]bool{}
	for _, d := range parsed.Decisions {
		knownDecisions[d.ID] = true
	}
	knownCapabilities := map[string]bool{}
	for _, bucket := range parsed.Buckets {
		for _, c := range bucket.Capabilities {
			knownCapabilities[c.ID] = true
		}
	}
	knownMilestones := map[string]bool{}
	for _, m := range parsed.Milestones {
		knownMilestones[m.ID] = true
	}

	var productUnmapped []string
	productUnmapped = append(productUnmapped, unmappedBullets(sectionBody(productBody, "Personas"), personaRe, 1, knownPersonas)...)
	productUnmapped = append(productUnmapped, unmappedBullets(sectionBody(productBody, "Non-goals"), nonGoalRe, 1, knownNonGoals)...)
	productUnmapped = append(productUnmapped, unmappedTokens(sectionBody(productBody, "Load-bearing decisions"), looseDecisionRe, "LB", knownDecisions)...)

	capabilityUnmapped := unmappedTokens(capabilityBody, looseCapabilityRe, "C", knownCapabilities)
	milestoneUnmapped := unmappedTokens(roadmapBody, looseMilestoneRe, "M", knownMilestones)

	return []CoverageEntry{
		{SourceFile: "PRODUCT.md", UnmappedCount: len(productUnmapped), UnmappedItems: productUnmapped},
		{SourceFile: "product/02-capability-map.md", UnmappedCount: len(capabilityUnmapped), UnmappedItems: capabilityUnmapped},
		{SourceFile: "product/03-roadmap.md", UnmappedCount: len(milestoneUnmapped), UnmappedItems: milestoneUnmapped},
	}, nil
}

// unmappedBullets scans body (a section's text, from parse.go's own
// sectionBody) for `- ...` bullet lines and reports, verbatim, any bullet
// that either does not match strict at all, or matches it but whose
// captured name (group[nameGroup]) is absent from known -- the latter
// would mean Parse recognized the line's shape yet the resulting entity
// never made it into the Parsed* slice, which coverage must still catch
// rather than assume can't happen.
func unmappedBullets(body string, strict *regexp.Regexp, nameGroup int, known map[string]bool) []string {
	var out []string
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(raw)
		if !strings.HasPrefix(trimmed, "-") {
			continue
		}
		if m := strict.FindStringSubmatch(trimmed); m != nil && known[m[nameGroup]] {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// unmappedTokens scans body for lines matching loose (a permissive
// "this line names an id of this kind" regex whose only capture group is
// the id's numeric suffix) and reports, verbatim, any line whose
// idPrefix+digits id is not present in known.
func unmappedTokens(body string, loose *regexp.Regexp, idPrefix string, known map[string]bool) []string {
	var out []string
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(raw)
		m := loose.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		if known[idPrefix+m[1]] {
			continue
		}
		out = append(out, trimmed)
	}
	return out
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
