// Package render implements FR13-FR15 (issue #2495): the one-way
// projection of a krill Product's current spec back out to the committed
// doc set -- `<domain>/PRODUCT.md` plus `<domain>/product/*.md`, the exact
// layout krill/render/README.md describes. It is the
// mirror image of krill/importer: importer only ever parses a committed
// doc into entities, this package only ever renders entities into a doc.
//
// FR15/LB5 -- one-way, structurally enforced: Render's only dependency on
// krill's data is the Source interface below, and every method on it is a
// read (a List or a Get). There is no Create, no Update, no method capable
// of writing to krill anywhere in this package's own dependency graph --
// nothing here holds a *store.Store, only the narrow read surface Source
// declares. StoreSource (store_source.go) is the adapter a caller wires up
// against a real *store.Store, but Render itself never sees that concrete
// type, only the interface. See render_integration_test.go's read-only-role
// test for the empirical proof: Render succeeds against a Postgres
// connection whose role has had every write privilege revoked.
//
// FR14 -- every numbered citation (`Cn`, `LBn`) this package renders is
// read directly off each Feature's/LoadBearingDecision's own stored
// DisplayNumber (migration 017, issue #2969) -- assigned once at creation
// (product-wide auto-increment, or an imported document's own token), never
// recomputed from sibling position on render. This reverses migration
// 002's original LB2 stance ("no such column exists"): a display number
// computed fresh from position on every render was not stable once
// entities were appended out of order, reordered, or superseded -- see
// issue #2969.
package render

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// Source is the read-only surface Render depends on (FR15). Every method
// is a read against krill's current (valid_to IS NULL) rows; there is no
// write method here for the same reason there is no *store.Store field
// anywhere in this package -- a caller cannot accidentally give Render a
// way to mutate krill even by mistake, because the type it is handed
// simply has no such method to call.
type Source interface {
	// GetProductSlice returns productID's whole-product slice (FR8) --
	// the Product itself, every FeatureSet, Feature, and
	// LoadBearingDecision beneath it. *slice.Querier satisfies this
	// directly.
	GetProductSlice(ctx context.Context, productID uuid.UUID) (slice.Document, error)

	// ListPersonas returns every current Persona under productID.
	ListPersonas(ctx context.Context, productID uuid.UUID) ([]store.Persona, error)

	// ListNonGoals returns every current NonGoal (both kinds) under
	// productID.
	ListNonGoals(ctx context.Context, productID uuid.UUID) ([]store.NonGoal, error)

	// ListMilestoneRefs returns every MilestoneRef under (scopeID,
	// productID) -- the bare `M<n>` identifiers a product's roadmap
	// names (LB6). May return rows of any store.MilestoneKind --
	// renderMilestones (below) is what filters to
	// store.MilestoneKindMilestone before rendering, so a later kind
	// (milepebble, backlog bucket -- migration 010, issue #2683) can never
	// silently appear in the roadmap doc even if a Source implementation
	// forgets to filter itself.
	ListMilestoneRefs(ctx context.Context, scopeID, productID uuid.UUID) ([]store.MilestoneRef, error)

	// ListMilestoneAssociations returns every EntityMilestone row for
	// milestoneID -- the rows a `Delivers:`/`Must not foreclose:` line is
	// reconstructed from (LB6), never stored prose.
	ListMilestoneAssociations(ctx context.Context, milestoneID uuid.UUID) ([]store.EntityMilestone, error)

	// ListMilestoneDeferrals returns every MilestoneDeferral row for
	// milestoneID (migration 010, issue #2683, FR1) -- the rows a
	// `Deliberately deferred:` line is reconstructed from.
	ListMilestoneDeferrals(ctx context.Context, milestoneID uuid.UUID) ([]store.MilestoneDeferral, error)

	// ListMilestoneShipsAlongside returns milestoneID's Ships alongside
	// rows (migration 032) -- non-capability work shipping with it.
	ListMilestoneShipsAlongside(ctx context.Context, milestoneID uuid.UUID) ([]store.MilestoneShipsAlongside, error)

	// ListMilestoneStatuses returns each id's *current* delivery status --
	// the latest `milestone_status_event` row per id, batched, because a
	// whole-product roadmap renders every milestone in one pass and a
	// per-milestone round trip is the wrong shape (migration 012, issue
	// #2685, FR11). An id with no status history is expected to be
	// present in the map as store.MilestoneStatusNotStarted: krill
	// derives "not started" from the absence of history rather than
	// seeding a row (FR8). A Source that omits an id entirely is still
	// read honestly by the renderer -- see milestoneStatusLabel.
	ListMilestoneStatuses(ctx context.Context, milestoneIDs []uuid.UUID) (map[uuid.UUID]store.MilestoneStatus, error)

	// ListProductNotes returns every note recorded against the Product
	// itself, oldest first, with its kind and lifecycle status intact.
	// Notes are the one place krill holds prose that no entity field
	// carries -- a capability renumbering, a scope-note recording a
	// divergence between krill and a hand-authored file -- and a
	// rendered brief that omits them sends readers to pointers whose
	// targets are not in the document. Only the Product's own notes for
	// now: notes on a Feature or Requirement are a separate read.
	ListProductNotes(ctx context.Context, scopeID, productID uuid.UUID) ([]store.Note, error)

	// ListActiveProtects returns every active (non-withdrawn) LB-protects-
	// Feature edge whose feature is in featureIDs -- the recorded data the
	// roadmap's Later coverage section is built from.
	ListActiveProtects(ctx context.Context, featureIDs []uuid.UUID) ([]store.LBProtectsFeature, error)
}

// GeneratedMarker is the exact sentence NFR3's AGENTS.md carve-out and
// every generated file's own header both carry, so a contributor who opens
// a rendered file sees the same instruction a doc reader gets from
// AGENTS.md itself. Keep this string identical in both places -- it is
// grepped by test and by contributors deciding whether a file is safe to
// hand-edit.
const GeneratedMarker = "GENERATED by krill/render -- do not hand-edit; edit krill's entities and re-render instead"

// Files is the rendered four-file doc set (see
// README.md): PRODUCT.md is the index (Vision, Personas,
// Load-bearing decisions, Non-goals inline, per that section's own
// carve-out for short/stable/cross-referenced content); the other three
// split out because they have no natural ceiling.
type Files struct {
	ProductMD       string // <domain>/PRODUCT.md
	CurrentStateMD  string // <domain>/product/01-current-state.md
	CapabilityMapMD string // <domain>/product/02-capability-map.md
	RoadmapMD       string // <domain>/product/03-roadmap.md

	// Stamp is the source revision/time every file above is stamped with.
	Stamp SourceStamp
}

// FileMap returns f as a map from the path each file is written at,
// relative to the domain's own root directory (e.g. "krill/" for krill
// itself) -- the shape krill/render/cmd writes to disk.
func (f Files) FileMap() map[string]string {
	return map[string]string{
		"PRODUCT.md":                   f.ProductMD,
		"product/01-current-state.md":  f.CurrentStateMD,
		"product/02-capability-map.md": f.CapabilityMapMD,
		"product/03-roadmap.md":        f.RoadmapMD,
	}
}

// Render assembles Files for productID by reading src alone (FR13, FR15).
// scopeID is productID's owning scope (LB1) -- needed to enumerate its
// milestone refs, which are keyed (scope, product, name) rather than by
// product alone (see migration 004's comment on why: two different
// products under the same scope may each have their own "M1").
// FeatureNoteSource is an optional Source capability: the notes recorded
// against a Feature, oldest first. A Source that lacks it renders the
// capability map without 'cheap-expensive-later' statements.
type FeatureNoteSource interface {
	ListFeatureNotes(ctx context.Context, scopeID, featureID uuid.UUID) ([]store.Note, error)
}

func Render(ctx context.Context, src Source, scopeID, productID uuid.UUID, opts ...Option) (Files, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	doc, err := src.GetProductSlice(ctx, productID)
	if err != nil {
		return Files{}, fmt.Errorf("get product slice: %w", err)
	}
	if doc.Product == nil {
		return Files{}, fmt.Errorf("product %s: no current row (GetProductSlice returned an empty Product)", productID)
	}

	personas, err := src.ListPersonas(ctx, productID)
	if err != nil {
		return Files{}, fmt.Errorf("list personas: %w", err)
	}
	nonGoals, err := src.ListNonGoals(ctx, productID)
	if err != nil {
		return Files{}, fmt.Errorf("list non-goals: %w", err)
	}
	milestones, err := renderMilestones(ctx, src, scopeID, productID, doc)
	if err != nil {
		return Files{}, fmt.Errorf("assemble milestones: %w", err)
	}
	notes, err := src.ListProductNotes(ctx, scopeID, productID)
	if err != nil {
		return Files{}, fmt.Errorf("list product notes: %w", err)
	}
	later, err := renderLaterCoverage(ctx, src, scopeID, productID, doc)
	if err != nil {
		return Files{}, fmt.Errorf("assemble later coverage: %w", err)
	}

	cheapExpensive := map[uuid.UUID][]string{}
	if fns, ok := src.(FeatureNoteSource); ok {
		for _, f := range doc.Features {
			fnotes, err := fns.ListFeatureNotes(ctx, scopeID, f.ID)
			if err != nil {
				return Files{}, fmt.Errorf("list feature notes: %w", err)
			}
			for _, n := range fnotes {
				if n.Kind == store.NoteKindCheapExpensiveLater && n.CurrentStatus != store.NoteLifecycleStatus("closed") {
					cheapExpensive[f.ID] = append(cheapExpensive[f.ID], strings.TrimSpace(n.Body))
				}
			}
		}
	}

	revision := doc.Product.RevisionID.String()
	name := doc.Product.Name

	stamp := SourceStamp{Revision: revision}
	if ss, ok := src.(StampSource); ok {
		if stamp.Time, err = ss.ProductSourceTime(ctx, productID); err != nil {
			return Files{}, fmt.Errorf("source time: %w", err)
		}
	}

	return Files{
		ProductMD:       stampFile(renderProductMD(name, revision, doc, personas, nonGoals, notes, o.detail), stamp),
		CurrentStateMD:  stampFile(renderCurrentStateMD(name, revision, doc.Product.CurrentState), stamp),
		CapabilityMapMD: stampFile(renderCapabilityMapMD(name, revision, doc, o.detail, cheapExpensive), stamp),
		RoadmapMD:       stampFile(renderRoadmapMD(name, revision, milestones, later), stamp),
		Stamp:           stamp,
	}, nil
}

type options struct {
	detail bool
}

// Option tunes Render.
type Option func(*options)

// WithDetail renders every Requirement body under its capability in
// product/02-capability-map.md. By default the map is headlines only --
// krill's MCP surface (get_feature_slice) is the read path for full bodies,
// and a full dump is too large to load into an agent's context.
func WithDetail() Option { return func(o *options) { o.detail = true } }

// header is the provenance line every generated file carries (LB5): the
// entity this file was rendered from (a Product, by name and immutable
// id), the exact SCD2 revision it came from, and a render timestamp, plus
// NFR3's non-hand-editable marker. now is a parameter (not time.Now()
// called inline) so callers -- and this package's own tests -- get a
// deterministic header to assert against.
func header(productName, revisionID string, now time.Time) string {
	return fmt.Sprintf(
		"<!-- %s\n     Rendered by krill/render from Product %q, revision %s, at %s. -->\n",
		GeneratedMarker, productName, revisionID, now.UTC().Format(time.RFC3339))
}

// nowFunc is overridden in tests that need a fixed timestamp; production
// callers never need to touch it.
var nowFunc = time.Now

// firstSentence returns the first sentence of s (capped at 200 runes), for
// headline-only renders.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

func renderProductMD(name, revision string, doc slice.Document, personas []store.Persona, nonGoals []store.NonGoal, notes []store.Note, detail bool) string {
	var b strings.Builder

	b.WriteString(header(name, revision, nowFunc()))
	b.WriteString("\n# ")
	b.WriteString(name)
	b.WriteString(" — Product brief\n\n")
	b.WriteString("This file is the index. Vision, Personas, Load-bearing decisions, Non-goals, and Notes are inline; the three sections with no natural ceiling are split out:\n\n")
	b.WriteString("| Section | File |\n|---|---|\n")
	b.WriteString("| Current state | [`product/01-current-state.md`](product/01-current-state.md) |\n")
	b.WriteString("| Capability map | [`product/02-capability-map.md`](product/02-capability-map.md) |\n")
	b.WriteString("| Roadmap | [`product/03-roadmap.md`](product/03-roadmap.md) |\n\n")

	if !detail {
		b.WriteString("_Product notes are headlines only here. Read them from krill (`list_entity_notes`), or re-render with detail._\n\n")
	}

	b.WriteString("## Vision\n\n")
	b.WriteString(strings.TrimSpace(doc.Product.Vision))
	b.WriteString("\n\n")

	b.WriteString("## Personas\n\n")
	for _, p := range personas {
		b.WriteString("- **")
		b.WriteString(p.Name)
		b.WriteString("**")
		if p.Description != nil && strings.TrimSpace(*p.Description) != "" {
			b.WriteString(" — ")
			b.WriteString(strings.TrimSpace(*p.Description))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## Load-bearing decisions\n\n")
	for _, d := range doc.Decisions {
		title := cleanDecisionTitle(d.Name)
		b.WriteString(fmt.Sprintf("### LB%d — %s\n\n", d.DisplayNumber, title))
		if d.Body != nil && strings.TrimSpace(*d.Body) != "" {
			b.WriteString(strings.TrimSpace(*d.Body))
			b.WriteString("\n\n")
		}
	}

	b.WriteString("## Non-goals\n\n")
	b.WriteString("**Permanent:**\n\n")
	for _, ng := range nonGoals {
		if ng.Kind != store.NonGoalKindPermanent {
			continue
		}
		writeNonGoalBullet(&b, ng)
	}
	b.WriteString("\n**Explicitly *not* non-goals — deferred, not foreclosed:**\n\n")
	for _, ng := range nonGoals {
		if ng.Kind != store.NonGoalKindDeferred {
			continue
		}
		writeNonGoalBullet(&b, ng)
	}

	b.WriteString("\n")
	renderNotesSection(&b, notes, detail)

	return b.String()
}

// renderNotesSection emits the Product's own notes. It exists because
// several entity bodies point at them -- whagent_net's three
// LoadBearingDecisions each end "See the mapping note on this Product",
// and the renumbering mapping those pointers mean exists nowhere else --
// so omitting the section leaves the document directing readers at
// content it does not contain, which is worse than an omission.
//
// Each note is emitted under a bold label rather than a heading, so a body
// containing its own markdown structure renders verbatim instead of
// being reinterpreted as part of this document's outline. The lifecycle
// status is always shown: a `closed` or `deferred` note is history, and
// presenting its body as current fact would be a lie.
func renderNotesSection(b *strings.Builder, notes []store.Note, detail bool) {
	b.WriteString("## Notes\n\n")
	if len(notes) == 0 {
		b.WriteString("_No notes are recorded against this Product in krill._\n")
		return
	}
	b.WriteString("Notes recorded against this Product in krill, oldest first. The status after each id is the note's own lifecycle status (`store.NoteLifecycleStatus`); anything other than `noted` is rendered for the record, not as current fact.\n")
	if !detail {
		b.WriteString("\n")
		for _, n := range notes {
			b.WriteString(fmt.Sprintf("- `%s` — %s — status: %s — %s\n", n.ID, n.Kind, n.CurrentStatus, firstSentence(strings.TrimLeft(n.Body, "# \n"))))
		}
		return
	}
	for _, n := range notes {
		b.WriteString("\n**`")
		b.WriteString(n.ID.String())
		b.WriteString("`** — ")
		b.WriteString(string(n.Kind))
		b.WriteString(" — status: ")
		b.WriteString(string(n.CurrentStatus))
		b.WriteString("\n\n")
		b.WriteString(demoteHeadings(strings.TrimSpace(n.Body)))
		b.WriteString("\n")
	}
}

var headingLineRe = regexp.MustCompile(`^(#{1,6})\s`)

// demoteHeadings pushes any markdown heading in a note body down to level 4
// or deeper so it nests under the document's own "## Notes" outline. Fenced
// code blocks are left untouched.
func demoteHeadings(body string) string {
	lines := strings.Split(body, "\n")
	inFence := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := headingLineRe.FindStringSubmatch(l); m != nil {
			lvl := min(len(m[1])+3, 6)
			lines[i] = strings.Repeat("#", lvl) + l[len(m[1]):]
		}
	}
	return strings.Join(lines, "\n")
}

func writeNonGoalBullet(b *strings.Builder, ng store.NonGoal) {
	b.WriteString("- **")
	b.WriteString(ng.Name)
	b.WriteString(".**")
	if ng.Body != nil && strings.TrimSpace(*ng.Body) != "" {
		b.WriteString(" ")
		b.WriteString(strings.TrimSpace(*ng.Body))
	}
	b.WriteString("\n")
}

// leadingLBLabelRe strips a leading "LB<n> — " (or "--"/"-") token from a
// stored LoadBearingDecision.Name -- krill/importer/parse.go's
// parseDecisions keeps the whole source title line (including its own
// `LBn —` prefix) as Name, so a decision imported as "LB1" carries that
// literal text in its stored Name forever, even after a later insert
// renumbers it to LB2 at render time (FR14). Rendering must never trust
// that embedded prefix as the citation -- only the freshly computed
// position does -- so this strips it before re-prefixing with the
// recomputed number.
var leadingLBLabelRe = regexp.MustCompile(`^LB\d+\s*[—–-]\s*`)

func cleanDecisionTitle(name string) string {
	return leadingLBLabelRe.ReplaceAllString(strings.TrimSpace(name), "")
}

// leadingCLabelRe strips a leading "C<n> — " (or "--"/"-") token from a
// stored Feature.Name, mirroring leadingLBLabelRe above -- a Feature
// imported (or hand-created pre-#2961) with its own "Cn —" prefix baked
// into Name must not carry that stale prefix into a re-render, which
// recomputes the citation from the stored DisplayNumber instead.
var leadingCLabelRe = regexp.MustCompile(`^C\d+\s*[—–-]\s*`)

func cleanFeatureTitle(name string) string {
	return leadingCLabelRe.ReplaceAllString(strings.TrimSpace(name), "")
}

// currentStatePlaceholderBody is the whole of what
// product/01-current-state.md ever says. It is not a stub waiting to be
// filled: krill's renderer is scoped to the product doc set (a vision,
// personas, capabilities, requirements, load-bearing decisions, non-goals,
// milestones, notes), and a current-state survey of a running system is
// none of those. It is a static description of a deployment that changes
// on its own schedule, not a spec of record an agent contributes to, so
// it is hand-authored at `<domain>/ARCHITECTURE.md` -- which is where the
// repository's own documentation conventions put system design,
// component relationships, and data flow.
//
// The wording is deliberate about that. The earlier text said no entity
// backed this section, which reads as "krill's model is missing
// something" and invites a reader to propose a new entity type. Nothing
// is missing and nothing is planned to arrive here.
const currentStatePlaceholderBody = "This section is intentionally not rendered.\n\n" +
	"`krill/render` is scoped to the product doc set — vision, personas, " +
	"capabilities and their requirements, load-bearing decisions, non-goals, " +
	"milestones, and notes — all of which are entities an agent authors " +
	"through krill's own API. A current-state survey of a running system is " +
	"none of those: it is a static description of a deployment that changes on " +
	"its own schedule, not a spec of record anyone contributes to. So it is " +
	"hand-authored, and it lives in this domain's `ARCHITECTURE.md`.\n\n" +
	"A survey that was hand-authored here before the migration is not carried " +
	"over: it remains in this file's git history, and no future entity type is " +
	"planned to bring it into krill. See `krill/render/README.md` for the same " +
	"boundary stated in full.\n"

func renderCurrentStateMD(name, revision string, stored *string) string {
	var b strings.Builder
	b.WriteString(header(name, revision, nowFunc()))
	b.WriteString("\n# Current state\n\n")
	if stored != nil {
		// Stored survey is emitted verbatim: no escaping, trimming, or truncation.
		b.WriteString(*stored)
		return b.String()
	}
	b.WriteString(currentStatePlaceholderBody)
	return b.String()
}

// leadingFRLabelRe strips a leading "FR<n> — " / "NFR<n> — " token baked
// into a stored Requirement.Name, mirroring leadingCLabelRe and
// leadingLBLabelRe. A Requirement imported (or hand-created) with its own
// citation in Name must not carry that stale prefix into a re-render, which
// recomputes the citation from render-time sibling position instead.
var leadingFRLabelRe = regexp.MustCompile(`^N?FR\d+\s*[—–-]\s*`)

func cleanRequirementTitle(name string) string {
	return leadingFRLabelRe.ReplaceAllString(strings.TrimSpace(name), "")
}

// requirementCitations assigns each Requirement its `FRn`/`NFRn` number.
// Unlike `Cn` and `LBn` there is no stored display number for a
// Requirement -- migration 017 gave one to Features and LoadBearingDecisions
// and not to these -- so per LB2 the number comes from render-time sibling
// position instead, counted per kind.
//
// The sibling order is the one doc.Requirements already arrives in:
// feature_set.position/name, feature.position/name, requirement.kind,
// requirement.position, requirement.name (krill/store/slice.go's
// ListRequirementsByProduct). That is exactly the order the capability map
// walks below -- FeatureSets in order, Features in order, requirements
// under their Feature -- so counting down the file reproduces these
// numbers, and a reader counting `FR`s off this page lands on the same
// requirement. A Requirement whose parent Feature is not in the slice is
// left unnumbered, because it has no position in the document to count
// from.
func requirementCitations(doc slice.Document) map[uuid.UUID]string {
	byFeature := make(map[uuid.UUID]int, len(doc.Features))
	for _, f := range doc.Features {
		byFeature[f.ID] = f.DisplayNumber
	}
	counters := map[string]int{}
	out := make(map[uuid.UUID]string, len(doc.Requirements))
	for _, rq := range doc.Requirements {
		if _, ok := byFeature[rq.FeatureID]; !ok {
			continue
		}
		counters[rq.Kind]++
		out[rq.ID] = fmt.Sprintf("%s%d", rq.Kind, counters[rq.Kind])
	}
	return out
}

func renderCapabilityMapMD(name, revision string, doc slice.Document, detail bool, cheapExpensive map[uuid.UUID][]string) string {
	var b strings.Builder
	b.WriteString(header(name, revision, nowFunc()))
	b.WriteString("\n# Capability map\n\n")

	// Group Features by their parent FeatureSet, in the order FeatureSets
	// and Features are both already returned (feature_set.position/name,
	// then feature.position/name -- krill/store/slice.go's
	// ListFeaturesByProduct query). A FeatureSet with no Features (e.g.
	// the importer's synthetic "Load-bearing decisions" holder) is
	// skipped entirely -- it has nothing to number.
	featuresBySet := map[uuid.UUID][]slice.FeatureEntity{}
	for _, f := range doc.Features {
		featuresBySet[f.FeatureSetID] = append(featuresBySet[f.FeatureSetID], f)
	}
	requirementsByFeature := map[uuid.UUID][]slice.RequirementEntity{}
	for _, rq := range doc.Requirements {
		requirementsByFeature[rq.FeatureID] = append(requirementsByFeature[rq.FeatureID], rq)
	}
	citations := requirementCitations(doc)

	if !detail {
		b.WriteString("Headlines only: each `Cn` is a capability, with the count of Requirements that specify it. Requirement bodies are not rendered here — read them with krill's `get_feature_slice` / `get_requirement_slice` MCP tools, or re-render with detail.\n\n")
	}
	if detail && len(doc.Requirements) > 0 {
		b.WriteString("Each `Cn` is a capability. Beneath it, the Requirements that specify it: `FRn` (functional) and `NFRn` (non-functional), with their bodies in full — a body carries the prohibitions and the refuted-hypothesis records, so it is never truncated or summarized here.\n\n")
		b.WriteString("krill stores no display number for a Requirement, so `FRn`/`NFRn` are assigned at render time, per kind, counting down this file in the order the requirements appear. The bracketed id after each name resolves a citation exactly.\n")
	}

	for _, fs := range doc.FeatureSets {
		features := featuresBySet[fs.ID]
		if len(features) == 0 {
			continue
		}
		b.WriteString("## ")
		b.WriteString(fs.Name)
		b.WriteString("\n\n")
		for _, f := range features {
			reqs := requirementsByFeature[f.ID]
			if !detail {
				nFR, nNFR := 0, 0
				for _, rq := range reqs {
					if rq.Kind == "NFR" {
						nNFR++
					} else {
						nFR++
					}
				}
				b.WriteString(fmt.Sprintf("- **C%d** — %s (%d FR, %d NFR)\n", f.DisplayNumber, cleanFeatureTitle(f.Name), nFR, nNFR))
				writeCheapExpensive(&b, cheapExpensive[f.ID])
				continue
			}
			if len(reqs) == 0 {
				b.WriteString(fmt.Sprintf("- **C%d** — %s\n", f.DisplayNumber, cleanFeatureTitle(f.Name)))
				writeCheapExpensive(&b, cheapExpensive[f.ID])
				continue
			}
			// A Feature with Requirements gets a heading, so each
			// requirement's body can follow raw at the top level rather
			// than indented into a list -- indenting would rewrite the
			// body, and a body is a record, not formatting.
			b.WriteString(fmt.Sprintf("### C%d — %s\n\n", f.DisplayNumber, cleanFeatureTitle(f.Name)))
			writeCheapExpensive(&b, cheapExpensive[f.ID])
			for _, rq := range reqs {
				writeRequirement(&b, rq, citations[rq.ID])
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}

// writeCheapExpensive emits a Feature's recorded 'Stays cheap/expensive
// later' statements, in a form that is a block under a heading and an
// indented continuation under a list bullet.
func writeCheapExpensive(b *strings.Builder, bodies []string) {
	for _, body := range bodies {
		b.WriteString("  - **Stays cheap / expensive later:** ")
		b.WriteString(strings.ReplaceAll(body, "\n", "\n    "))
		b.WriteString("\n")
	}
}

// writeRequirement emits one Requirement under a bold label, not a
// heading, so a body containing its own markdown renders as authored
// instead of being folded into this document's outline. A nil or
// whitespace-only body says so explicitly rather than leaving the reader
// to wonder whether the renderer dropped it.
func writeRequirement(b *strings.Builder, rq slice.RequirementEntity, citation string) {
	if citation == "" {
		citation = rq.Kind
	}
	b.WriteString("**")
	b.WriteString(citation)
	b.WriteString("** — ")
	b.WriteString(cleanRequirementTitle(rq.Name))
	b.WriteString(" (`")
	b.WriteString(rq.ID.String())
	b.WriteString("`)\n\n")
	if rq.Body == nil || strings.TrimSpace(*rq.Body) == "" {
		b.WriteString("_No body recorded._\n\n")
		return
	}
	b.WriteString(strings.TrimSpace(*rq.Body))
	b.WriteString("\n\n")
}

// milestoneEntry is one milestone's rendered content, reconstructed
// entirely from milestone_ref, entity_milestone, and milestone_deferral rows
// (LB6, migration 010) -- never authored prose. It still omits the
// freeform per-milestone prose a hand-authored roadmap entry carries (the
// "Notes for design" and "Pre-agreed over-budget cut" paragraphs, and any
// explanatory prose attached to an individual `Must not foreclose` LB or
// deferral) -- nothing in krill's schema backs that prose today.
type milestoneEntry struct {
	Number           int
	ID               string // "M1".."Mn"
	Outcome          *string
	Status           store.MilestoneStatus
	FRBudget         *int
	Delivers         []string
	MustNotForeclose []string
	Deferrals        []store.MilestoneDeferral
	Notes            string // markdown design notes, rendered verbatim; empty emits nothing
	ShipsAlongside   []store.MilestoneShipsAlongside
}

// milestoneNumRe extracts the numeric suffix of a bare "M<n>" identifier.
var milestoneNumRe = regexp.MustCompile(`^M(\d+)$`)

func renderMilestones(ctx context.Context, src Source, scopeID, productID uuid.UUID, doc slice.Document) ([]milestoneEntry, error) {
	refs, err := src.ListMilestoneRefs(ctx, scopeID, productID)
	if err != nil {
		return nil, fmt.Errorf("list milestone refs: %w", err)
	}
	if len(refs) == 0 {
		return nil, nil
	}

	featureNumbers := make(map[uuid.UUID]int, len(doc.Features))
	for _, f := range doc.Features {
		featureNumbers[f.ID] = f.DisplayNumber
	}
	decisionNumbers := make(map[uuid.UUID]int, len(doc.Decisions))
	for _, d := range doc.Decisions {
		decisionNumbers[d.ID] = d.DisplayNumber
	}

	entries := make([]milestoneEntry, 0, len(refs))
	rendered := make([]store.MilestoneRef, 0, len(refs))
	for _, ref := range refs {
		// A later kind (milepebble, backlog bucket -- migration 010,
		// issue #2683) must never silently render as a roadmap milestone
		// -- see this file's Source.ListMilestoneRefs doc comment for why
		// this filter belongs here rather than trusting every Source
		// implementation to apply it itself.
		if ref.Kind != store.MilestoneKindMilestone {
			continue
		}
		rendered = append(rendered, ref)
	}

	// One batched status read for the whole roadmap, not one round trip
	// per milestone -- a product's roadmap renders every milestone in one
	// pass, so the set form is the right shape (FR11).
	ids := make([]uuid.UUID, len(rendered))
	for i, ref := range rendered {
		ids[i] = ref.ID
	}
	statuses, err := src.ListMilestoneStatuses(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list milestone statuses: %w", err)
	}

	for _, ref := range rendered {
		associations, err := src.ListMilestoneAssociations(ctx, ref.ID)
		if err != nil {
			return nil, fmt.Errorf("list associations for milestone %s: %w", ref.Name, err)
		}

		var delivers, mustNot []int
		for _, a := range associations {
			if n, ok := featureNumbers[a.EntityID]; ok {
				delivers = append(delivers, n)
				continue
			}
			if n, ok := decisionNumbers[a.EntityID]; ok {
				mustNot = append(mustNot, n)
			}
		}
		sort.Ints(delivers)
		sort.Ints(mustNot)

		deferrals, err := src.ListMilestoneDeferrals(ctx, ref.ID)
		if err != nil {
			return nil, fmt.Errorf("list deferrals for milestone %s: %w", ref.Name, err)
		}

		ships, err := src.ListMilestoneShipsAlongside(ctx, ref.ID)
		if err != nil {
			return nil, fmt.Errorf("list ships alongside for milestone %s: %w", ref.Name, err)
		}

		num := 0
		if m := milestoneNumRe.FindStringSubmatch(ref.Name); m != nil {
			num, _ = strconv.Atoi(m[1])
		}
		entries = append(entries, milestoneEntry{
			Number:           num,
			ID:               ref.Name,
			Outcome:          ref.Outcome,
			Status:           milestoneStatusLabel(statuses, ref.ID),
			FRBudget:         ref.FRBudget,
			Delivers:         prefixEach("C", delivers),
			MustNotForeclose: prefixEach("LB", mustNot),
			Deferrals:        deferrals,
			Notes:            derefString(ref.Notes),
			ShipsAlongside:   ships,
		})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Number < entries[j].Number })
	return entries, nil
}

// milestoneStatusLabel reads one milestone's current status honestly.
// store.MilestoneStatusEventStore.CurrentStatuses already returns
// MilestoneStatusNotStarted for an id with no status history (FR8: "not
// started" is derived from absence, never seeded), so the missing-key and
// empty-value cases collapse to the same honest answer rather than
// rendering an empty `Status:` line or implying a status nobody recorded.
func milestoneStatusLabel(statuses map[uuid.UUID]store.MilestoneStatus, id uuid.UUID) store.MilestoneStatus {
	if s, ok := statuses[id]; ok && s != "" {
		return s
	}
	return store.MilestoneStatusNotStarted
}

func prefixEach(prefix string, nums []int) []string {
	if len(nums) == 0 {
		return nil
	}
	out := make([]string, len(nums))
	for i, n := range nums {
		out[i] = fmt.Sprintf("%s%d", prefix, n)
	}
	return out
}

func renderRoadmapMD(name, revision string, milestones []milestoneEntry, later []laterEntry) string {
	var b strings.Builder
	b.WriteString(header(name, revision, nowFunc()))
	b.WriteString("\n# Roadmap\n\n")
	b.WriteString("_`Status` is each milestone's **current** delivery status, derived from krill's append-only `milestone_status_event` history (`store.MilestoneStatusEventStore.CurrentStatuses`). A milestone with no recorded transition is `not started` — that is krill's own derivation from the absence of history, not a rendered default._\n\n")

	abandoned := 0
	for _, m := range milestones {
		if m.Status == store.MilestoneStatusAbandoned {
			abandoned++
		}
	}
	// Count is computed from the rendered set, never hardcoded.
	b.WriteString(fmt.Sprintf("%d milestones (%d abandoned), one block each below.\n\n", len(milestones), abandoned))

	for _, m := range milestones {
		b.WriteString("### ")
		b.WriteString(m.ID)
		if m.Outcome != nil && *m.Outcome != "" {
			b.WriteString(" — ")
			b.WriteString(*m.Outcome)
		}
		b.WriteString("\n\n")
		b.WriteString("- Status: ")
		b.WriteString(string(m.Status))
		b.WriteString("\n")
		if len(m.Delivers) > 0 {
			b.WriteString("- Delivers: ")
			b.WriteString(strings.Join(m.Delivers, ", "))
			b.WriteString("\n")
		}
		if len(m.MustNotForeclose) > 0 {
			b.WriteString("- Must not foreclose: ")
			b.WriteString(strings.Join(m.MustNotForeclose, ", "))
			b.WriteString("\n")
		}
		if len(m.Deferrals) > 0 {
			items := make([]string, len(m.Deferrals))
			for i, d := range m.Deferrals {
				body := d.Body
				if d.CapabilityDisplayNumber != nil {
					body = fmt.Sprintf("%s [C%d]", body, *d.CapabilityDisplayNumber)
				}
				items[i] = fmt.Sprintf("%s (→ %s)", body, d.Destination)
			}
			b.WriteString("- Deliberately deferred: ")
			b.WriteString(strings.Join(items, "; "))
			b.WriteString("\n")
		}
		if len(m.ShipsAlongside) > 0 {
			items := make([]string, len(m.ShipsAlongside))
			for i, s := range m.ShipsAlongside {
				items[i] = s.Body
			}
			b.WriteString("- Ships alongside: ")
			b.WriteString(strings.Join(items, "; "))
			b.WriteString("\n")
		}
		if m.FRBudget != nil {
			b.WriteString(fmt.Sprintf("- FR budget: %d\n", *m.FRBudget))
		}
		if m.Notes != "" {
			b.WriteString("\n")
			b.WriteString(m.Notes)
			if !strings.HasSuffix(m.Notes, "\n") {
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	renderLaterCoverageMD(&b, later)
	return b.String()
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// laterEntry is one Later capability (a Feature no milestone or milepebble
// delivers) with the decisions recorded as protecting it. Protectors is
// empty for an uncovered capability.
type laterEntry struct {
	Capability string // "C<n>"
	Name       string
	Protectors []string // "LB<n>"
}

// renderLaterCoverage lists every Feature with no active delivers edge from
// a milestone or milepebble, with its protecting decisions read from recorded
// lb_protects_feature edges (never inferred).
func renderLaterCoverage(ctx context.Context, src Source, scopeID, productID uuid.UUID, doc slice.Document) ([]laterEntry, error) {
	refs, err := src.ListMilestoneRefs(ctx, scopeID, productID)
	if err != nil {
		return nil, fmt.Errorf("list milestone refs: %w", err)
	}
	delivered := map[uuid.UUID]bool{}
	for _, ref := range refs {
		if ref.Kind == store.MilestoneKindBacklog {
			continue
		}
		assocs, err := src.ListMilestoneAssociations(ctx, ref.ID)
		if err != nil {
			return nil, fmt.Errorf("list associations for milestone %s: %w", ref.Name, err)
		}
		for _, a := range assocs {
			if a.Relation != store.MilestoneRelationMustNotForeclose {
				delivered[a.EntityID] = true
			}
		}
	}

	var laterIDs []uuid.UUID
	var later []slice.FeatureEntity
	for _, f := range doc.Features {
		if !delivered[f.ID] {
			later = append(later, f)
			laterIDs = append(laterIDs, f.ID)
		}
	}
	if len(later) == 0 {
		return nil, nil
	}

	edges, err := src.ListActiveProtects(ctx, laterIDs)
	if err != nil {
		return nil, fmt.Errorf("list active protects: %w", err)
	}
	decisionNumbers := make(map[uuid.UUID]int, len(doc.Decisions))
	for _, d := range doc.Decisions {
		decisionNumbers[d.ID] = d.DisplayNumber
	}
	protectors := map[uuid.UUID][]int{}
	for _, e := range edges {
		if n, ok := decisionNumbers[e.DecisionID]; ok {
			protectors[e.FeatureID] = append(protectors[e.FeatureID], n)
		}
	}

	sort.Slice(later, func(i, j int) bool { return later[i].DisplayNumber < later[j].DisplayNumber })
	out := make([]laterEntry, len(later))
	for i, f := range later {
		nums := protectors[f.ID]
		sort.Ints(nums)
		out[i] = laterEntry{Capability: fmt.Sprintf("C%d", f.DisplayNumber), Name: f.Name, Protectors: prefixEach("LB", nums)}
	}
	return out, nil
}

func renderLaterCoverageMD(b *strings.Builder, later []laterEntry) {
	if len(later) == 0 {
		return
	}
	b.WriteString("## Later coverage\n\n")
	b.WriteString("_Capabilities no milestone delivers, with the load-bearing decisions recorded as protecting them (krill `lb_protects_feature` edges). A capability with none is uncovered._\n\n")
	for _, e := range later {
		b.WriteString(fmt.Sprintf("- %s — %s: ", e.Capability, e.Name))
		if len(e.Protectors) == 0 {
			b.WriteString("uncovered\n")
		} else {
			b.WriteString(strings.Join(e.Protectors, ", "))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
}
