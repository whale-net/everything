package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ParsedProduct is the in-memory result of parsing one product's
// `PRODUCT.md` + `product/*.md` doc set (tools/project-manager/
// CONVENTIONS.md § Layout: Vision, Personas, Load-bearing decisions,
// Non-goals inline in PRODUCT.md; Current state, Capability map, Roadmap
// split out). Parse never touches a database -- it is safe to run against a
// doc set this milestone does not import (see Testing's whagent_net
// parse-fixture use, importer_integration_test.go).
//
// "Current state" (product/01-current-state.md) carries no entity of its
// own in krill's model (issue #2488's ARCHITECTURE.md section) and is
// therefore not parsed here.
type ParsedProduct struct {
	Name       string
	Vision     string
	Personas   []ParsedPersona
	Decisions  []ParsedDecision
	NonGoals   []ParsedNonGoal
	Buckets    []ParsedBucket
	Milestones []ParsedMilestone
}

// ParsedPersona is one `- **Name** — description` bullet under `##
// Personas`.
type ParsedPersona struct {
	Name        string
	Description string
}

// ParsedDecision is one `LB<n> — ...` entry under `## Load-bearing
// decisions`, regardless of whether the source wraps it in its own fenced
// block (krill's PRODUCT.md) or bundles every entry into one fence
// (whagent_net's) -- Parse does not require a fence at all.
type ParsedDecision struct {
	ID   string // "LB1".."LBn"
	Name string // the entry's title line, e.g. "LB1 — Scope is a surrogate key, and it is on every row from M1"
	Body string // the full block text, including the title line
}

// NonGoalKind mirrors store.NonGoalKind without importing krill/store here
// -- write.go does that translation, keeping this file store-independent
// per Parse's "never touches a database" contract.
type NonGoalKind string

const (
	NonGoalPermanent NonGoalKind = "permanent"
	NonGoalDeferred  NonGoalKind = "deferred"
)

// ParsedNonGoal is one `- **Name.** description` bullet under `##
// Non-goals`, bucketed Permanent vs. deferred-not-foreclosed when the
// source marks that split (krill's own PRODUCT.md); when it does not
// (whagent_net's flat list), every bullet defaults to Permanent -- a
// defensible reading of an unmarked list, not a guess this file hides: see
// parseNonGoals's comment.
type ParsedNonGoal struct {
	Kind NonGoalKind
	Name string
	Body string
}

// ParsedBucket is one `## Now` / `## Next` / `## Later` heading in
// `product/02-capability-map.md` and the capabilities listed under it, in
// file order.
type ParsedBucket struct {
	Name         string
	Capabilities []ParsedCapability
}

// ParsedCapability is one `Cn — description` line, bold-and-dashed
// (krill's own map) or bare (whagent_net's) -- both shapes match.
type ParsedCapability struct {
	ID          string // "C1".."Cn"
	Description string
}

// ParsedMilestone is one `### M<n> — <outcome sentence>` heading in
// `product/03-roadmap.md`, plus the capability ids its own `Delivers:`
// line names and the decision ids its own `Must not foreclose:` line names
// -- never a milestone referenced from a *different* section's prose (see
// parseRoadmap's comment on why only the line directly under a milestone's
// own heading counts).
type ParsedMilestone struct {
	Number           int
	ID               string // "M1".."Mn"
	Title            string
	Delivers         []string // capability ids, e.g. ["C1", "C2"]
	MustNotForeclose []string // decision ids, e.g. ["LB1", "LB4"]
}

var (
	headingRe       = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)
	personaRe       = regexp.MustCompile(`^-\s+\*\*(.+?)\*\*\s*[—–-]\s*(.+)$`)
	nonGoalRe       = regexp.MustCompile(`^-\s+\*\*([^*]+?)\.\*\*\s*(.*)$`)
	capabilityRe    = regexp.MustCompile(`^-?\s*\*{0,2}C(\d+)\*{0,2}\s*[—–-]\s*(.+)$`)
	decisionTitleRe = regexp.MustCompile(`^LB(\d+)\s*[—–-]\s*(.+)$`)
	milestoneHdRe   = regexp.MustCompile(`^#{2,3}\s*M(\d+)\s*[—–-]\s*(.+)$`)
	deliversRe      = regexp.MustCompile(`^Delivers:\s*(.*)$`)
	mustNotRe       = regexp.MustCompile(`^Must not foreclose:\s*(.*)$`)
	bareMilestoneRe = regexp.MustCompile(`\bM(\d+)\b`)
	capTokenRe      = regexp.MustCompile(`\bC\d+\b`)
	lbTokenRe       = regexp.MustCompile(`\bLB\d+\b`)
)

// Parse reads rootPath's `PRODUCT.md` and `product/{01-current-state,
// 02-capability-map,03-roadmap}.md` (the layout tools/project-manager/
// CONVENTIONS.md § Layout defines) and returns the entities found. It
// returns an error naming any bare `M<n>` reference in the roadmap that the
// roadmap's own headings never define (FR17's "fail loudly" clause) -- it
// never partially writes anything, since Parse alone never writes at all.
func Parse(rootPath string) (*ParsedProduct, error) {
	productBody, err := readFile(filepath.Join(rootPath, "PRODUCT.md"))
	if err != nil {
		return nil, err
	}
	capabilityBody, err := readFile(filepath.Join(rootPath, "product", "02-capability-map.md"))
	if err != nil {
		return nil, err
	}
	roadmapBody, err := readFile(filepath.Join(rootPath, "product", "03-roadmap.md"))
	if err != nil {
		return nil, err
	}

	p := &ParsedProduct{
		Name:      parseName(productBody),
		Vision:    strings.TrimSpace(sectionBody(productBody, "Vision")),
		Personas:  parsePersonas(sectionBody(productBody, "Personas")),
		Decisions: parseDecisions(sectionBody(productBody, "Load-bearing decisions")),
		NonGoals:  parseNonGoals(sectionBody(productBody, "Non-goals")),
		Buckets:   parseCapabilityMap(capabilityBody),
	}

	milestones, err := parseRoadmap(roadmapBody)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(rootPath, "product", "03-roadmap.md"), err)
	}
	p.Milestones = milestones

	return p, nil
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(b), nil
}

// parseName extracts the product name from PRODUCT.md's title line, e.g.
// "# krill — Product brief" -> "krill". Falls back to the raw title text if
// the "— Product brief" suffix is not present.
func parseName(content string) string {
	for _, line := range strings.Split(content, "\n") {
		m := headingRe.FindStringSubmatch(line)
		if m == nil || len(m[1]) != 1 {
			continue
		}
		title := m[2]
		if idx := strings.Index(title, "—"); idx != -1 {
			return strings.TrimSpace(title[:idx])
		}
		if idx := strings.Index(title, " -- "); idx != -1 {
			return strings.TrimSpace(title[:idx])
		}
		return title
	}
	return ""
}

// sectionBody returns the text between a `## <title>` heading (exact,
// case-sensitive match on title) and the next `##`-or-shallower heading, or
// EOF. Deeper headings (`### ...`) inside the section -- e.g. krill's "Two
// intake candidates" and "`Later` capabilities" asides under Load-bearing
// decisions -- stay part of the returned body; callers that scan for
// specific line shapes (decisionTitleRe, nonGoalRe) are unaffected by their
// presence.
func sectionBody(content, title string) string {
	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		m := headingRe.FindStringSubmatch(line)
		if m != nil && len(m[1]) == 2 && m[2] == title {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return ""
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		m := headingRe.FindStringSubmatch(lines[i])
		if m != nil && len(m[1]) <= 2 {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

func parsePersonas(body string) []ParsedPersona {
	var personas []ParsedPersona
	for _, line := range strings.Split(body, "\n") {
		m := personaRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		personas = append(personas, ParsedPersona{Name: m[1], Description: strings.TrimSpace(m[2])})
	}
	return personas
}

// parseDecisions scans body for `LB<n> — ...` title lines and collects
// every following line up to the next title line (or a `###`-or-shallower
// heading, i.e. the end of the fenced/unfenced block of entries) as that
// entry's Body. Fence marker lines (a line that is exactly "```", ignoring
// surrounding whitespace) are dropped -- present or absent, they carry no
// content of their own.
func parseDecisions(body string) []ParsedDecision {
	var decisions []ParsedDecision
	var current *ParsedDecision
	var buf []string

	flush := func() {
		if current != nil {
			current.Body = strings.TrimSpace(strings.Join(buf, "\n"))
			decisions = append(decisions, *current)
		}
		current = nil
		buf = nil
	}

	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		if trimmed == "```" || strings.HasPrefix(trimmed, "```") {
			continue
		}
		if hm := headingRe.FindStringSubmatch(trimmed); hm != nil {
			// A heading (e.g. "### Two intake candidates...") ends the
			// current entry and is not itself entry content.
			flush()
			continue
		}
		if m := decisionTitleRe.FindStringSubmatch(trimmed); m != nil {
			flush()
			current = &ParsedDecision{ID: "LB" + m[1], Name: trimmed}
			buf = []string{trimmed}
			continue
		}
		if current != nil {
			buf = append(buf, line)
		}
	}
	flush()
	return decisions
}

// parseNonGoals splits body on krill's "**Permanent:**" / "**Explicitly
// *not* non-goals" dividers when present; when they are not (whagent_net's
// flat bullet list), every bullet is bucketed Permanent. This default is a
// deliberate, stated reading -- an unmarked list is not evidence the
// product intends every entry as "deferred, not foreclosed", and the
// schema's CHECK constraint (migration 002) requires one of the two kinds.
func parseNonGoals(body string) []ParsedNonGoal {
	var nonGoals []ParsedNonGoal
	kind := NonGoalPermanent
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(trimmed, "**Permanent:**"):
			kind = NonGoalPermanent
			continue
		case strings.HasPrefix(trimmed, "**Explicitly"):
			kind = NonGoalDeferred
			continue
		}
		m := nonGoalRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		nonGoals = append(nonGoals, ParsedNonGoal{Kind: kind, Name: m[1], Body: strings.TrimSpace(m[2])})
	}
	return nonGoals
}

// parseCapabilityMap scans content for `## Now` / `### Now` (and Next /
// Later) headings and the `Cn — description` lines under each, in file
// order. Heading level (h2 in krill's map, h3 in whagent_net's) is not
// significant -- only the bucket name is.
func parseCapabilityMap(content string) []ParsedBucket {
	var buckets []ParsedBucket
	var current *ParsedBucket

	for _, raw := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(raw)
		if hm := headingRe.FindStringSubmatch(trimmed); hm != nil {
			level, name := len(hm[1]), hm[2]
			if level == 2 || level == 3 {
				switch name {
				case "Now", "Next", "Later":
					buckets = append(buckets, ParsedBucket{Name: name})
					current = &buckets[len(buckets)-1]
					continue
				}
			}
			// Any other heading (e.g. the vocabulary-collision callout) does
			// not end the current bucket -- only a recognized bucket heading
			// starts a new one.
			continue
		}
		if current == nil {
			continue
		}
		if m := capabilityRe.FindStringSubmatch(trimmed); m != nil {
			current.Capabilities = append(current.Capabilities, ParsedCapability{
				ID:          "C" + m[1],
				Description: strings.TrimSpace(m[2]),
			})
		}
	}
	return buckets
}

// parseRoadmap scans content for `### M<n> — <title>` headings and, for
// each, the `Delivers:` / `Must not foreclose:` line directly under that
// heading (before the next milestone heading). Only the token list on that
// line itself is taken -- text after the first em/en-dash on the same
// physical line (krill's trailing "— all seven, each for its own
// reason:..." explanations, continued on indented lines below) is prose,
// not more list items, and is never scanned for tokens; scanning it would
// pick up the cross-references those explanations make to *other*
// milestones' or entities' ids.
//
// After the per-milestone pass, parseRoadmap scans the whole document for
// every bare `M<n>` token (heading, prose, anywhere) and fails loudly,
// naming the milestone, if any such token has no corresponding `###
// M<n>` heading anywhere in the document -- FR17's "the source names a
// milestone its own roadmap section never defines" clause.
func parseRoadmap(content string) ([]ParsedMilestone, error) {
	var milestones []ParsedMilestone
	var current *ParsedMilestone
	defined := map[string]bool{}

	for _, raw := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(raw)
		if hm := milestoneHdRe.FindStringSubmatch(trimmed); hm != nil {
			num, _ := strconv.Atoi(hm[1])
			id := "M" + hm[1]
			milestones = append(milestones, ParsedMilestone{Number: num, ID: id, Title: strings.TrimSpace(hm[2])})
			current = &milestones[len(milestones)-1]
			defined[id] = true
			continue
		}
		if current == nil {
			continue
		}
		if m := deliversRe.FindStringSubmatch(trimmed); m != nil {
			current.Delivers = capTokenRe.FindAllString(firstClause(m[1]), -1)
			continue
		}
		if m := mustNotRe.FindStringSubmatch(trimmed); m != nil {
			current.MustNotForeclose = lbTokenRe.FindAllString(firstClause(m[1]), -1)
			continue
		}
	}

	// Fail loudly on any bare M<n> the roadmap never defines with its own
	// heading -- collect a stable, sorted set so the error is
	// deterministic rather than depending on map iteration order.
	referenced := map[string]bool{}
	for _, m := range bareMilestoneRe.FindAllString(content, -1) {
		referenced[m] = true
	}
	var undefined []string
	for id := range referenced {
		if !defined[id] {
			undefined = append(undefined, id)
		}
	}
	if len(undefined) > 0 {
		sort.Strings(undefined)
		return nil, fmt.Errorf("references undefined milestone(s): %s", strings.Join(undefined, ", "))
	}

	return milestones, nil
}

// firstClause returns s up to (not including) its first em-dash or
// double-hyphen, i.e. the token-list portion of a `Delivers:`/`Must not
// foreclose:` line before any trailing prose explanation.
func firstClause(s string) string {
	if idx := strings.Index(s, "—"); idx != -1 {
		return s[:idx]
	}
	if idx := strings.Index(s, " -- "); idx != -1 {
		return s[:idx]
	}
	return s
}
