package main

// The Capabilities table's Milestone column (FR 18afc5a8's badge
// paragraph): which milestone delivers a feature, a neutral count when
// several deliver its requirements, a blank cell when nothing does, and an
// explicit message -- never a blank -- when the delivery listing could not
// be read.
//
// The derivation is exercised over a HAND-BUILT slice.DeliveryListing
// rather than a database, because every property that matters here is a
// property of the rule and not of the SQL: which milestone wins, what N
// counts, and which rows must not bleed into each other. The failed-read
// case runs through the real handler, so the tolerance the browser sees is
// the tolerance under test.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
	"github.com/whale-net/everything/libs/go/htmxui"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// milestoneBadgeIDs are the ids the fixture's entities carry, spelled out so
// a test names a feature or a milestone rather than indexing a slice.
var milestoneBadgeIDs = struct {
	product     string
	featureSet  string
	named       string // delivered by exactly one milestone, directly
	viaOneReq   string // delivered by no milestone, its requirement by exactly one
	viaManyReq  string // delivered by no milestone, its requirements by two
	undelivered string // delivered by nothing at all
	milepebble  string // delivered by a milepebble whose parent does not deliver it
	multiOwner  string // delivered by TWO milestones directly
	sharedReqs  string // two requirements, BOTH delivered by one milestone
	reqNamed    string // the requirement under named/viaOneReq
	reqViaOne   string // the requirement under viaOneReq
	reqViaManyA string
	reqViaManyB string
	reqUndeld   string
	reqMulti    string // under multiOwner, delivered by nobody
	reqSharedA  string // the two under sharedReqs, both by milestoneA
	reqSharedB  string
	milestoneA  string // the one milestone under every "exactly one" case
	milestoneB  string
	milestoneC  string
	milepebble1 string
}{
	product:     "11111111-0000-0000-0000-000000000001",
	featureSet:  "11111111-0000-0000-0000-000000000002",
	named:       "11111111-0000-0000-0000-000000000003",
	viaOneReq:   "11111111-0000-0000-0000-000000000004",
	viaManyReq:  "11111111-0000-0000-0000-000000000005",
	undelivered: "11111111-0000-0000-0000-000000000006",
	milepebble:  "11111111-0000-0000-0000-000000000007",
	multiOwner:  "11111111-0000-0000-0000-000000000008",
	sharedReqs:  "11111111-0000-0000-0000-000000000009",
	reqNamed:    "11111111-0000-0000-0000-000000000011",
	reqViaOne:   "11111111-0000-0000-0000-000000000012",
	reqViaManyA: "11111111-0000-0000-0000-000000000013",
	reqViaManyB: "11111111-0000-0000-0000-000000000014",
	reqUndeld:   "11111111-0000-0000-0000-000000000015",
	reqMulti:    "11111111-0000-0000-0000-000000000016",
	reqSharedA:  "11111111-0000-0000-0000-000000000017",
	reqSharedB:  "11111111-0000-0000-0000-000000000018",
	milestoneA:  "11111111-0000-0000-0000-000000000021",
	milestoneB:  "11111111-0000-0000-0000-000000000022",
	milestoneC:  "11111111-0000-0000-0000-000000000023",
	milepebble1: "11111111-0000-0000-0000-000000000024",
}

// milestoneBadgeDoc is a product slice carrying every feature shape the
// Milestone rule has to answer for, each with a distinct requirement set so
// a count that bled between rows would be visible.
func milestoneBadgeDoc(t *testing.T) (slice.Document, uuid.UUID) {
	t.Helper()
	id := func(s string) uuid.UUID { return mustID(t, s) }
	product := id(milestoneBadgeIDs.product)
	featureSet := id(milestoneBadgeIDs.featureSet)

	doc := slice.Document{
		Product: &slice.ProductEntity{
			EntityRef: slice.EntityRef{ID: product},
			Name:      "krill",
			Vision:    "the substrate",
		},
		FeatureSets: []slice.FeatureSetEntity{{
			EntityRef: slice.EntityRef{ID: featureSet}, ProductID: product, Name: "Spec axis",
		}},
		Features: []slice.FeatureEntity{
			{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.named)}, FeatureSetID: featureSet, Name: "Named directly", DisplayNumber: 1},
			{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.viaOneReq)}, FeatureSetID: featureSet, Name: "Named via one requirement", DisplayNumber: 2},
			{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.viaManyReq)}, FeatureSetID: featureSet, Name: "Delivered by several", DisplayNumber: 3},
			{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.undelivered)}, FeatureSetID: featureSet, Name: "Delivered by nothing", DisplayNumber: 4},
			{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.milepebble)}, FeatureSetID: featureSet, Name: "Delivered by a milepebble only", DisplayNumber: 5},
			{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.multiOwner)}, FeatureSetID: featureSet, Name: "Delivered by two milestones", DisplayNumber: 6},
			{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.sharedReqs)}, FeatureSetID: featureSet, Name: "Two requirements one milestone", DisplayNumber: 7},
		},
	}
	doc.Requirements = []slice.RequirementEntity{
		{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.reqNamed)}, FeatureID: id(milestoneBadgeIDs.named), Kind: "FR", Name: "direct"},
		{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.reqViaOne)}, FeatureID: id(milestoneBadgeIDs.viaOneReq), Kind: "FR", Name: "single deliverer"},
		{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.reqViaManyA)}, FeatureID: id(milestoneBadgeIDs.viaManyReq), Kind: "FR", Name: "first of two"},
		{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.reqViaManyB)}, FeatureID: id(milestoneBadgeIDs.viaManyReq), Kind: "NFR", Name: "second of two"},
		{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.reqUndeld)}, FeatureID: id(milestoneBadgeIDs.undelivered), Kind: "FR", Name: "nobody delivers this"},
		{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.reqMulti)}, FeatureID: id(milestoneBadgeIDs.multiOwner), Kind: "FR", Name: "nobody delivers this either"},
		{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.reqSharedA)}, FeatureID: id(milestoneBadgeIDs.sharedReqs), Kind: "FR", Name: "shared deliverer, first"},
		{EntityRef: slice.EntityRef{ID: id(milestoneBadgeIDs.reqSharedB)}, FeatureID: id(milestoneBadgeIDs.sharedReqs), Kind: "NFR", Name: "shared deliverer, second"},
	}
	return doc, product
}

// milestoneBadgeListing is the delivery listing for milestoneBadgeDoc,
// covering every branch of the rule:
//
//   - milestoneA delivers the `named` feature itself, the `viaOneReq`
//     feature's one requirement, one of `viaManyReq`'s two, AND its own
//     milepebble delivers `milepebble`;
//   - milestoneB delivers the other of `viaManyReq`'s requirements, and
//     with milestoneC co-delivers `multiOwner` directly;
//   - nothing delivers `undelivered`.
//
// milestoneA deliberately delivers BOTH a feature and one of its
// requirements: the cell must name it once, not count the two associations.
func milestoneBadgeListing(t *testing.T) slice.DeliveryListing {
	t.Helper()
	id := func(s string) uuid.UUID { return mustID(t, s) }
	ref := func(s string) slice.EntityRef { return slice.EntityRef{ID: id(s)} }

	return slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{
		{
			ID:     id(milestoneBadgeIDs.milestoneA),
			Name:   "Milestone A",
			Status: store.MilestoneStatusInProgress,
			Delivers: slice.Document{
				Features:     []slice.FeatureEntity{{EntityRef: ref(milestoneBadgeIDs.named)}},
				Requirements: []slice.RequirementEntity{{EntityRef: ref(milestoneBadgeIDs.reqViaOne)}, {EntityRef: ref(milestoneBadgeIDs.reqViaManyA)}, {EntityRef: ref(milestoneBadgeIDs.reqSharedA)}, {EntityRef: ref(milestoneBadgeIDs.reqSharedB)}},
			},
			Milepebbles: []slice.MilepebbleListingEntry{{
				ID:     id(milestoneBadgeIDs.milepebble1),
				Name:   "Pebble A1",
				Status: store.MilestoneStatusNotStarted,
				Delivers: slice.Document{
					Features: []slice.FeatureEntity{{EntityRef: ref(milestoneBadgeIDs.milepebble)}},
				},
			}},
		},
		{
			ID:     id(milestoneBadgeIDs.milestoneB),
			Name:   "Milestone B",
			Status: store.MilestoneStatusShipped,
			Delivers: slice.Document{
				Requirements: []slice.RequirementEntity{{EntityRef: ref(milestoneBadgeIDs.reqViaManyB)}},
			},
		},
		{
			ID:     id(milestoneBadgeIDs.milestoneC),
			Name:   "Milestone C",
			Status: store.MilestoneStatusPlanned,
			Delivers: slice.Document{
				Features: []slice.FeatureEntity{{EntityRef: ref(milestoneBadgeIDs.multiOwner)}},
			},
		},
		{
			ID:     id("11111111-0000-0000-0000-000000000025"),
			Name:   "Milestone D",
			Status: store.MilestoneStatusNotStarted,
			Delivers: slice.Document{
				Features: []slice.FeatureEntity{{EntityRef: ref(milestoneBadgeIDs.multiOwner)}},
			},
		},
	}}
}

// milestoneBadgePage builds the capability map the handler builds, from the
// fixture slice and listing.
func milestoneBadgePage(t *testing.T, listing slice.DeliveryListing, deliveryErr error) pages.CapabilityPage {
	t.Helper()
	doc, product := milestoneBadgeDoc(t)
	return capabilityPageWithMilestonesOf(doc, product,
		capabilityExpansion{Path: productPath(product)}, listing, deliveryErr)
}

// milestoneBadgeFeatures maps each test's short feature key to its id
// spelling, so a test names a feature rather than repeating a uuid.
var milestoneBadgeFeatures = map[string]string{
	"named":       milestoneBadgeIDs.named,
	"viaOneReq":   milestoneBadgeIDs.viaOneReq,
	"viaManyReq":  milestoneBadgeIDs.viaManyReq,
	"undelivered": milestoneBadgeIDs.undelivered,
	"milepebble":  milestoneBadgeIDs.milepebble,
	"multiOwner":  milestoneBadgeIDs.multiOwner,
	"sharedReqs":  milestoneBadgeIDs.sharedReqs,
}

// milestoneCellOf finds one feature's Milestone cell in the page, so a
// test reads the cell by feature rather than by row position.
func milestoneCellOf(t *testing.T, page pages.CapabilityPage, featureKey string) pages.CapabilityMilestone {
	t.Helper()
	spelling, ok := milestoneBadgeFeatures[featureKey]
	if !ok {
		t.Fatalf("unknown fixture feature key %q", featureKey)
	}
	want := mustID(t, spelling)
	for _, fs := range page.FeatureSets {
		for _, f := range fs.Features {
			if f.ID == want.String() {
				return f.Milestone
			}
		}
	}
	t.Fatalf("no feature row for %s rendered", featureKey)
	return pages.CapabilityMilestone{}
}

// renderMilestoneBadgeHTML renders the capability map with delivery data, so
// the markup assertions below see what a browser would.
func renderMilestoneBadgeHTML(t *testing.T, listing slice.DeliveryListing, deliveryErr error) string {
	t.Helper()
	return mustRenderComponent(pages.CapabilityMap(milestoneBadgePage(t, listing, deliveryErr)))
}

// ---------------------------------------------------------------------------
// the derivation
// ---------------------------------------------------------------------------

// TestMilestoneBadgeNamesTheOneMilestoneDeliveringTheFeature pins the
// requirement's first clause: exactly one milestone delivering the Feature
// itself is named in the cell, with its status carried through for the
// badge's colour.
func TestMilestoneBadgeNamesTheOneMilestoneDeliveringTheFeature(t *testing.T) {
	cell := milestoneCellOf(t, milestoneBadgePage(t, milestoneBadgeListing(t), nil), "named")

	if cell.Kind != pages.CapabilityMilestoneNamed {
		t.Fatalf("Kind = %q, want %q", cell.Kind, pages.CapabilityMilestoneNamed)
	}
	if cell.Name != "Milestone A" {
		t.Errorf("Name = %q, want %q", cell.Name, "Milestone A")
	}
	if cell.Status != string(store.MilestoneStatusInProgress) {
		t.Errorf("Status = %q, want %q", cell.Status, string(store.MilestoneStatusInProgress))
	}
}

// TestMilestoneBadgeFallsBackToTheRequirementsSingleMilestone pins the
// second clause: no milestone delivers the Feature, but exactly one
// delivers its requirement, and that milestone is named.
func TestMilestoneBadgeFallsBackToTheRequirementsSingleMilestone(t *testing.T) {
	cell := milestoneCellOf(t, milestoneBadgePage(t, milestoneBadgeListing(t), nil), "viaOneReq")

	if cell.Kind != pages.CapabilityMilestoneNamed || cell.Name != "Milestone A" {
		t.Errorf("cell = %+v, want the named cell for Milestone A", cell)
	}
}

// TestMilestoneBadgeCountsSeveralDeliveringMilestones pins the third
// clause: two milestones delivering one feature's two requirements give the
// neutral "2 milestones" badge -- a count of milestones, not of the two
// requirements that happened to be delivered.
func TestMilestoneBadgeCountsSeveralDeliveringMilestones(t *testing.T) {
	cell := milestoneCellOf(t, milestoneBadgePage(t, milestoneBadgeListing(t), nil), "viaManyReq")

	if cell.Kind != pages.CapabilityMilestoneSeveral {
		t.Fatalf("Kind = %q, want %q", cell.Kind, pages.CapabilityMilestoneSeveral)
	}
	if cell.Count != 2 {
		t.Errorf("Count = %d, want 2 (the two milestones, not the two requirements)", cell.Count)
	}
	if cell.Name != "" {
		t.Errorf("Name = %q, want empty: several milestones have no single one to name", cell.Name)
	}
}

// TestMilestoneBadgeBlankWhenNothingDeliversIt pins the fourth clause: a
// feature nothing delivers is a blank cell, which is a real answer and
// distinct from a read that failed.
func TestMilestoneBadgeBlankWhenNothingDeliversIt(t *testing.T) {
	cell := milestoneCellOf(t, milestoneBadgePage(t, milestoneBadgeListing(t), nil), "undelivered")

	if cell.Kind != pages.CapabilityMilestoneNone {
		t.Fatalf("Kind = %q, want %q", cell.Kind, pages.CapabilityMilestoneNone)
	}
	if cell.Name != "" || cell.Message != "" {
		t.Errorf("cell = %+v, want an empty cell with neither a name nor a message", cell)
	}
}

// TestMilestoneBadgeIgnoresMilepebblesWhenDecidingDelivery pins the
// top-level-only rule: a milepebble delivering a feature does not make that
// feature delivered, because a milepebble's Delivers is a subset of its
// parent's and counting one would inflate N for a feature the cut has one
// delivering milestone for.
func TestMilestoneBadgeIgnoresMilepebblesWhenDecidingDelivery(t *testing.T) {
	cell := milestoneCellOf(t, milestoneBadgePage(t, milestoneBadgeListing(t), nil), "milepebble")

	if cell.Kind != pages.CapabilityMilestoneNone {
		t.Errorf("Kind = %q, want %q: only a milepebble delivers this feature",
			cell.Kind, pages.CapabilityMilestoneNone)
	}
}

// TestMilestoneBadgeCountsOneMilestoneDeliveringFeatureAndRequirementOnce
// pins the deduplication: milestoneA delivers the `named` feature itself
// AND that feature's requirement. That is one milestone, so the cell names
// it rather than counting two associations.
func TestMilestoneBadgeCountsOneMilestoneDeliveringFeatureAndRequirementOnce(t *testing.T) {
	cell := milestoneCellOf(t, milestoneBadgePage(t, milestoneBadgeListing(t), nil), "named")

	if cell.Kind != pages.CapabilityMilestoneNamed || cell.Name != "Milestone A" {
		t.Errorf("cell = %+v, want the named cell for Milestone A, not a count of two associations", cell)
	}
}

// TestMilestoneBadgeCountsSeveralDeliveringTheFeatureItself pins the case
// the requirement leaves unspelled: two milestones deliver the FEATURE
// itself. The cell is the same neutral count badge as the several-
// requirements case -- there is no single milestone to name -- and it is
// decided from the feature's own delivery, never from its requirements.
func TestMilestoneBadgeCountsSeveralDeliveringTheFeatureItself(t *testing.T) {
	cell := milestoneCellOf(t, milestoneBadgePage(t, milestoneBadgeListing(t), nil), "multiOwner")

	if cell.Kind != pages.CapabilityMilestoneSeveral {
		t.Fatalf("Kind = %q, want %q", cell.Kind, pages.CapabilityMilestoneSeveral)
	}
	if cell.Count != 2 {
		t.Errorf("Count = %d, want 2 (milestones C and D)", cell.Count)
	}
}

// TestMilestoneBadgeCountsOneMilestoneDeliveringTwoRequirementsOnce pins
// the dedup on the requirements path: milestoneA delivers BOTH of
// sharedReqs' two requirements, which is ONE delivering milestone. Without
// the by-id dedup the cell would report "2 milestones" for a cut that has
// one -- a count of associations dressed up as a count of milestones.
func TestMilestoneBadgeCountsOneMilestoneDeliveringTwoRequirementsOnce(t *testing.T) {
	cell := milestoneCellOf(t, milestoneBadgePage(t, milestoneBadgeListing(t), nil), "sharedReqs")

	if cell.Kind != pages.CapabilityMilestoneNamed {
		t.Fatalf("Kind = %q, want %q: one milestone delivered both requirements", cell.Kind, pages.CapabilityMilestoneNamed)
	}
	if cell.Name != "Milestone A" {
		t.Errorf("Name = %q, want %q", cell.Name, "Milestone A")
	}
}

// TestMilestoneBadgeCountsDoNotBleedBetweenRows pins the per-row isolation:
// `viaOneReq` is delivered by exactly one milestone and `viaManyReq` by
// two, and neither figure may reach the other. The fixture gives them
// different feature ids, different requirement ids and different
// milestones precisely so a shared accumulator would be caught here.
func TestMilestoneBadgeCountsDoNotBleedBetweenRows(t *testing.T) {
	page := milestoneBadgePage(t, milestoneBadgeListing(t), nil)

	one := milestoneCellOf(t, page, "viaOneReq")
	many := milestoneCellOf(t, page, "viaManyReq")
	if one.Kind != pages.CapabilityMilestoneNamed {
		t.Errorf("viaOneReq Kind = %q, want %q", one.Kind, pages.CapabilityMilestoneNamed)
	}
	if many.Kind != pages.CapabilityMilestoneSeveral || many.Count != 2 {
		t.Errorf("viaManyReq cell = %+v, want several with Count 2", many)
	}
	if one.Name == "" && many.Name == "" {
		t.Error("both cells lost their milestone name; the fixture's single-deliverer cases stopped resolving")
	}
}

// TestMilestoneBadgeIgnoresFeatureSetAndDecisionEntities pins that the index
// is built from the delivery listing's Features and Requirements only: an
// entity id that appears in neither cannot resolve a cell, so a stray id in
// some other Document field cannot invent a badge.
func TestMilestoneBadgeIgnoresFeatureSetAndDecisionEntities(t *testing.T) {
	listing := milestoneBadgeListing(t)
	listing.Milestones[0].Delivers.FeatureSets = []slice.FeatureSetEntity{{
		EntityRef: slice.EntityRef{ID: mustID(t, milestoneBadgeIDs.featureSet)},
	}}

	cell := milestoneCellOf(t, milestoneBadgePage(t, listing, nil), "undelivered")
	if cell.Kind != pages.CapabilityMilestoneNone {
		t.Errorf("Kind = %q, want %q: a feature SET is not a feature",
			cell.Kind, pages.CapabilityMilestoneNone)
	}
}

// ---------------------------------------------------------------------------
// the failed read
// ---------------------------------------------------------------------------

// TestMilestoneBadgeFailedReadIsNeverBlank is the load-bearing safety
// property: a delivery listing that could not be read must produce a cell
// that says so in EVERY row. A blank cell would assert that nothing
// delivers the feature -- a claim about delivery nobody could make.
func TestMilestoneBadgeFailedReadIsNeverBlank(t *testing.T) {
	page := milestoneBadgePage(t, slice.DeliveryListing{}, errors.New("the delivery store is unavailable"))

	for _, key := range []string{"named", "viaOneReq", "viaManyReq", "undelivered", "milepebble", "multiOwner", "sharedReqs"} {
		cell := milestoneCellOf(t, page, key)
		if cell.Kind != pages.CapabilityMilestoneUnread {
			t.Errorf("%s: Kind = %q, want %q", key, cell.Kind, pages.CapabilityMilestoneUnread)
			continue
		}
		if cell.Message == "" {
			t.Errorf("%s: the unread cell carries no message", key)
		}
	}
}

// milestoneUnreadReader is a specReadClient whose ProductSlice read succeeds
// with real data and whose Delivery read FAILS -- the case the page has to
// tolerate.
type milestoneUnreadReader struct {
	specStubReader
	doc slice.Document
}

func (r milestoneUnreadReader) ProductSlice(context.Context, uuid.UUID) (slice.Document, error) {
	return r.doc, nil
}

func (r milestoneUnreadReader) Delivery(context.Context, uuid.UUID, []store.MilestoneStatus) (slice.DeliveryListing, error) {
	return slice.DeliveryListing{}, errors.New("the delivery store is unavailable")
}

// TestCapabilityPageSurvivesAFailedDeliveryRead drives the real handler: the
// page still answers 200, the feature rows still render, and every Milestone
// cell states that the read failed.
func TestCapabilityPageSurvivesAFailedDeliveryRead(t *testing.T) {
	doc, _ := milestoneBadgeDoc(t)
	app := &App{spec: milestoneUnreadReader{doc: doc}, scopes: chromeScopes{}, tasks: chromeTaskCounter{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+specProductPath, app.handleCapabilityMap)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, capabilityRequest(t, mustID(t, milestoneBadgeIDs.product), ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a failed delivery read costs the Milestone column only", rec.Code)
	}
	body := rec.Body.String()
	// The rest of the table is still there...
	for _, want := range []string{
		"<th>Milestone</th>",
		`data-krill="feature-name">C1 — Named directly`,
		`data-krill="feature-name">C4 — Delivered by nothing`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %q; a failed delivery read must not cost the rest of the table", want)
		}
	}
	// ...and no cell claims nothing is delivered.
	if strings.Contains(body, `data-krill="feature-milestone-none"`) {
		t.Errorf("a failed read rendered a blank cell, which asserts nothing delivers the feature")
	}
	if !strings.Contains(body, `data-krill="feature-milestone-unread"`) {
		t.Error("the page carries no unread marker")
	}
	if n := strings.Count(body, capabilityMilestoneUnreadMessage); n != 7 {
		t.Errorf("%d cells carry the unread message, want one per feature (7)", n)
	}
}

// ---------------------------------------------------------------------------
// the rendered markup
// ---------------------------------------------------------------------------

// TestMilestoneBadgeRendersNamedAndCountedCells pins the RENDERED table, not
// the view model: the Milestone header exists, the named cell shows the
// milestone's name, and the counted cell shows "N milestones" -- so a
// template that dropped the column cannot pass on a correct builder.
func TestMilestoneBadgeRendersNamedAndCountedCells(t *testing.T) {
	html := renderMilestoneBadgeHTML(t, milestoneBadgeListing(t), nil)

	for _, want := range []string{
		"<th>Milestone</th>",
		`data-krill="feature-milestone-name">Milestone A</span>`,
		`data-krill="feature-milestone-count">2 milestones</span>`,
		// The blank cell, once for `undelivered` and once for the
		// milepebble-only feature -- neither of which any milestone
		// delivers.
		`data-krill="feature-milestone-none"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the rendered table is missing %q", want)
		}
	}
}

// TestMilestoneNamedBadgeMatchesTheMilestonesPageColour pins the one
// property the task exists to guarantee: the milestone named here wears the
// colour components.MilestoneStatusStyle gives that status everywhere else,
// so this page can never disagree with the Milestones table about it. The
// assertion is against the SHARED mapper, not a literal class.
func TestMilestoneNamedBadgeMatchesTheMilestonesPageColour(t *testing.T) {
	html := renderMilestoneBadgeHTML(t, milestoneBadgeListing(t), nil)

	// "Milestone A" is in progress; the count badge for the two-milestone
	// feature is neutral. Both are asserted against the mappers the
	// Milestones page uses -- a BadgeVariant's own value is its daisyUI
	// class, so this is the shared mapper's answer read straight off the
	// markup, not a class spelled out here.
	inProgress := string(components.MilestoneStatusStyle(string(store.MilestoneStatusInProgress)).Variant)
	if !strings.Contains(html, inProgress) {
		t.Errorf("the named milestone's cell carries no %q class, the colour MilestoneStatusStyle gives in progress", inProgress)
	}
	neutral := string(components.MilestoneCountStyle().Variant)
	if !strings.Contains(html, neutral) {
		t.Errorf("the count cell carries no %q class", neutral)
	}
	// The count badge must NOT wear a status colour: it asserts no state,
	// so it must not read as shipped, in-flight or given-up.
	for _, v := range []htmxui.BadgeVariant{htmxui.BadgeSuccess, htmxui.BadgeWarning, htmxui.BadgeError} {
		if string(v) == neutral {
			t.Errorf("the count badge shares the %q status variant", string(v))
		}
	}
}
