package main

// Wire-driven coverage for the delivery/roadmap view (delivery_page.go).
// The acceptance for this view is field-by-field parity with
// list_product_delivery and get_delivery_breakdown (FR 4398c532) -- a
// dropped field in a rendered roadmap is the recurring bug class here, so
// these tests reflect over the *actual* //krill/slice wire types
// (slice.MilestoneListingEntry, slice.MilepebbleListingEntry, and the
// breakdown's slice.Document Features/Requirements) rather than
// re-stating expected strings. A field added to, removed from, or renamed
// on a wire type fails here until someone decides what the page does
// with it.
//
// The pure deliveryPageOf + pages.Delivery are exercised for parity and
// status/breakdown shape (no database). The handler-level cases -- a
// single container's breakdown read failing, the empty product, and the
// unknown-id 404 -- run against an in-memory fake spec reader through the
// real handleSpecDelivery, so they exercise the same error tolerance the
// browser sees.

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
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
// fake spec reader
// ---------------------------------------------------------------------------

// deliveryBreakdownHook is the stable data-krill attribute the breakdown
// block carries. It is what these tests detect a breakdown by: the block's
// daisyUI classes are presentation and free to change (htmxui
// ARCHITECTURE §14), so coupling to them would break for a cosmetic edit.
const deliveryBreakdownHook = `data-krill="delivery-breakdown"`

// hasClassToken reports whether html carries token as a whole class on
// some element. A substring check would be wrong here: "badge-warning" is
// a substring of nothing else today, but a future class such as
// "badge-warning-outline" would satisfy one, and daisyUI renders the
// variant in the middle of a multi-class attribute, never alone.
func hasClassToken(html, token string) bool {
	for _, attr := range regexp.MustCompile(`class="([^"]*)"`).FindAllStringSubmatch(html, -1) {
		for _, c := range strings.Fields(attr[1]) {
			if c == token {
				return true
			}
		}
	}
	return false
}

// deliveryPair is one container's canned shipped/unshipped breakdown, the
// two slice.Documents get_delivery_breakdown returns.
type deliveryPair struct {
	shipped   slice.Document
	unshipped slice.Document
}

// fakeSpecReader is an in-memory specReadClient. Only the reads the
// delivery handler makes (Product, Delivery, DeliveryBreakdown) are
// meaningful; the rest satisfy the interface and return zero values. It
// records every DeliveryBreakdown container id it was asked for, and can
// fail exactly one container's breakdown, so a test can prove one bad
// container does not take down the page.
type fakeSpecReader struct {
	// products is what Products answers, so a test that renders the
	// chrome can name the product the sidebar's hrefs will carry.
	products    []store.Product
	product     store.Product
	productErr  error
	listing     slice.DeliveryListing
	listingErr  error
	breakdown   map[uuid.UUID]deliveryPair
	breakdownEr map[uuid.UUID]error // per-container injected failure
	queried     []uuid.UUID         // container ids DeliveryBreakdown was called for

	// history and historyErr are the status-history read the Milestone
	// detail's rail makes. The map is keyed by container id so a case can
	// give one container a register and another none; historyErr fails the
	// read outright, which is a different condition from an empty register.
	history    map[uuid.UUID][]store.MilestoneStatusEvent
	historyErr error
}

func (f *fakeSpecReader) ProductSlice(context.Context, uuid.UUID) (slice.Document, error) {
	return slice.Document{}, nil
}

func (f *fakeSpecReader) Personas(context.Context, uuid.UUID) ([]store.Persona, error) {
	return nil, nil
}

func (f *fakeSpecReader) NonGoals(context.Context, uuid.UUID) ([]store.NonGoal, error) {
	return nil, nil
}

func (f *fakeSpecReader) Delivery(context.Context, uuid.UUID, []store.MilestoneStatus) (slice.DeliveryListing, error) {
	if f.listingErr != nil {
		return slice.DeliveryListing{}, f.listingErr
	}
	return f.listing, nil
}

func (f *fakeSpecReader) DeliveryBreakdown(_ context.Context, id uuid.UUID) (slice.Document, slice.Document, error) {
	f.queried = append(f.queried, id)
	if err, bad := f.breakdownEr[id]; bad {
		return slice.Document{}, slice.Document{}, err
	}
	p := f.breakdown[id]
	return p.shipped, p.unshipped, nil
}

func (f *fakeSpecReader) Product(context.Context, uuid.UUID) (store.Product, error) {
	if f.productErr != nil {
		return store.Product{}, f.productErr
	}
	return f.product, nil
}

func (f *fakeSpecReader) Products(context.Context) ([]store.Product, error) {
	return f.products, nil
}

// deliveryReadMux mounts the real delivery handler on a bare mux. The
// handler reads only app.spec and renders through the shared shell, so no
// database and no other store accessor is needed; the shell chrome is
// rendered regardless, which is what the empty/404 cases assert.
func deliveryReadMux(reader specReadClient) *http.ServeMux {
	app := &App{spec: reader, scopes: chromeScopes{}, tasks: chromeTaskCounter{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /spec/products/{id}/delivery", app.handleSpecDelivery)
	return mux
}

// renderDelivery renders one delivery page from a listing + breakdown map.
func renderDelivery(t *testing.T, listing slice.DeliveryListing, breakdowns map[uuid.UUID]pages.DeliveryBreakdown) string {
	t.Helper()
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	product := store.Product{ID: productID, Name: "krill", Vision: "the substrate"}
	page := deliveryPageOf(product, listing, breakdowns, productID)
	return mustRenderComponent(pages.Delivery(page))
}

// milestoneEntry builds one milestone row at a given status, with a nested
// milepebble, using the real slice wire type.
func milestoneEntry(t *testing.T, mName, mOutcome string, mBudget *int, mStatus store.MilestoneStatus, mpName, mpOutcome string, mpStatus store.MilestoneStatus) (slice.MilestoneListingEntry, slice.MilepebbleListingEntry, uuid.UUID, uuid.UUID) {
	t.Helper()
	mID := uuid.New()
	mpID := uuid.New()
	mp := slice.MilepebbleListingEntry{
		ID:      mpID,
		Name:    mpName,
		Outcome: outcomePtr(mpOutcome),
		Status:  mpStatus,
	}
	m := slice.MilestoneListingEntry{
		ID:          mID,
		Name:        mName,
		Outcome:     outcomePtr(mOutcome),
		FRBudget:    mBudget,
		Status:      mStatus,
		Milepebbles: []slice.MilepebbleListingEntry{mp},
	}
	return m, mp, mID, mpID
}

func outcomePtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func budgetPtr(n int) *int { return &n }

// breakdownOf flattens a shipped/unshipped pair into the page's breakdown
// map entry, exactly as deliveryBreakdowns does for a successful read --
// including the StatusDisagrees an empty unshipped list carries under a
// partially-complete badge.
func breakdownOf(shipped, unshipped slice.Document) pages.DeliveryBreakdown {
	unshippedEntities := deliveryEntitiesOf(unshipped)
	return pages.DeliveryBreakdown{
		Shipped:         deliveryEntitiesOf(shipped),
		Unshipped:       unshippedEntities,
		StatusDisagrees: len(unshippedEntities) == 0,
	}
}

// featureDoc / requirementDoc build a breakdown slice.Document carrying the
// given Features and Requirements, using the real wire entity types.
func featureDoc(feats ...slice.FeatureEntity) slice.Document {
	return slice.Document{Features: feats}
}

func requirementDoc(reqs ...slice.RequirementEntity) slice.Document {
	return slice.Document{Requirements: reqs}
}

// ---------------------------------------------------------------------------
// 1-2. field parity: milestone + milepebble
// ---------------------------------------------------------------------------

// TestDeliveryMilestoneCarriesEveryWireField (FR 4398c532): a rendered
// milestone row carries every operator-facing field
// slice.MilestoneListingEntry exposes -- id, name, outcome (the FULL
// uniqueBody, END-MARKER included), FR budget, and status -- driven from
// the real wire type the MCP tool marshals unchanged. The nested
// milepebble's own fields are carried by the milepebble test below.
func TestDeliveryMilestoneCarriesEveryWireField(t *testing.T) {
	outcome := uniqueBody("ms")
	m, _, mID, _ := milestoneEntry(t,
		"Auth rework", deref(outcome), budgetPtr(12), store.MilestoneStatusInProgress,
		"OIDC link", deref(uniqueBody("mp")), store.MilestoneStatusPlanned)

	listing := slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}}
	html := renderDelivery(t, listing, nil)

	requireCarried(t, "MilestoneListingEntry", m, html)

	// The FR budget renders in the operator-facing "FR budget N" form.
	if !strings.Contains(html, "FR budget 12") {
		t.Errorf("milestone did not render the FR budget in its labelled form (missing %q)", "FR budget 12")
	}
	// The status label is present, distinct from the CSS class.
	if !strings.Contains(html, string(store.MilestoneStatusInProgress)) {
		t.Errorf("milestone did not render its status label %q", store.MilestoneStatusInProgress)
	}
	// The full, untruncated outcome (tail marker included).
	if !strings.Contains(html, *m.Outcome) {
		t.Errorf("milestone did not render the full outcome (END-MARKER missing)")
	}
	// The id is the anchor a reader cites.
	if !strings.Contains(html, mID.String()) {
		t.Errorf("milestone did not render its id %s", mID)
	}
}

// TestDeliveryMilepebbleCarriesEveryWireField (FR 4398c532): a nested
// milepebble carries every field slice.MilepebbleListingEntry exposes --
// id, name, outcome, status -- driven from the real wire type.
func TestDeliveryMilepebbleCarriesEveryWireField(t *testing.T) {
	outcome := uniqueBody("mp")
	m, mp, _, mpID := milestoneEntry(t,
		"Parent", "parent outcome", budgetPtr(1), store.MilestoneStatusShipped,
		"OIDC link", deref(outcome), store.MilestoneStatusInDesign)

	listing := slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}}
	html := renderDelivery(t, listing, nil)

	requireCarried(t, "MilepebbleListingEntry", mp, html)

	// The full milepebble outcome, not a truncated prefix.
	if !strings.Contains(html, *mp.Outcome) {
		t.Errorf("milepebble did not render the full outcome (END-MARKER missing)")
	}
	if !strings.Contains(html, string(store.MilestoneStatusInDesign)) {
		t.Errorf("milepebble did not render its status label %q", store.MilestoneStatusInDesign)
	}
	if !strings.Contains(html, mpID.String()) {
		t.Errorf("milepebble did not render its id %s", mpID)
	}
}

// TestDeliveryOmittedOptionalsAreNotFabricated pins the two nil cases the
// parity walker cannot see (it skips empty optionals): an unset Outcome
// renders no outcome block, and an unset FR budget is omitted entirely --
// never a bogus "FR budget 0" -- while the row's other fields still render.
func TestDeliveryOmittedOptionalsAreNotFabricated(t *testing.T) {
	// Outcome nil, FRBudget nil.
	m, mp, mID, _ := milestoneEntry(t,
		"No optionals", "", nil, store.MilestoneStatusNotStarted,
		"Child", "", store.MilestoneStatusPlanned)

	html := renderDelivery(t, slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}}, nil)

	// The row itself still renders (not swallowed by the nil optionals).
	for _, want := range []string{"No optionals", mID.String(), string(store.MilestoneStatusNotStarted)} {
		if !strings.Contains(html, want) {
			t.Errorf("row with nil outcome/budget missing %q", want)
		}
	}
	// The FR budget label never appears when the budget is unset, so no
	// "FR budget 0" is fabricated.
	if strings.Contains(html, "FR budget") {
		t.Errorf("unset FR budget rendered a budget block: %q present", "FR budget")
	}
	// The outcome is simply absent (no stray empty div / placeholder).
	if strings.Contains(html, "parent outcome") {
		t.Errorf("nil outcome fabricated text")
	}
	_ = mp
}

// ---------------------------------------------------------------------------
// 3. breakdown parity over slice.Document
// ---------------------------------------------------------------------------

// TestDeliveryBreakdownCarriesEveryWireEntity (FR 4398c532): for a
// partially-complete container, the shipped and unshipped breakdowns each
// render every entity the get_delivery_breakdown slice.Document carries --
// a Feature in its "Cn -- Name" citation form plus its id, a Requirement
// in its "FR -- Name" / "NFR -- Name" form plus its id. Driven from the
// real wire entity types so a dropped entity (or a dropped field on one)
// fails here.
func TestDeliveryBreakdownCarriesEveryWireEntity(t *testing.T) {
	feat := slice.FeatureEntity{
		EntityRef:     slice.EntityRef{ID: mustID(t, "33333333-3333-3333-3333-333333333333")},
		Name:          "Scoped slice",
		DisplayNumber: 7,
	}
	nfr := slice.RequirementEntity{
		EntityRef: slice.EntityRef{ID: mustID(t, "55555555-5555-5555-5555-555555555555")},
		Kind:      "NFR",
		Name:      "stays cheap",
	}
	shipped := featureDoc(feat)
	unshipped := requirementDoc(nfr)

	m, _, mID, _ := milestoneEntry(t,
		"Auth rework", "outcome", budgetPtr(3), store.MilestoneStatusPartiallyComplete,
		"child", "child outcome", store.MilestoneStatusShipped)

	html := renderDelivery(t,
		slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}},
		map[uuid.UUID]pages.DeliveryBreakdown{mID: breakdownOf(shipped, unshipped)},
	)

	// Both list headings render.
	if !strings.Contains(html, "Shipped") || !strings.Contains(html, "Unshipped") {
		t.Errorf("breakdown did not render both Shipped and Unshipped lists")
	}
	// The shipped Feature in its "Cn -- Name" citation form plus its id.
	if !strings.Contains(html, "C7 -- Scoped slice") {
		t.Errorf("breakdown missing shipped Feature in Cn -- Name form (missing %q)", "C7 -- Scoped slice")
	}
	if !strings.Contains(html, feat.ID.String()) {
		t.Errorf("breakdown missing shipped Feature id %s", feat.ID)
	}
	// The unshipped Requirement in its "Kind -- Name" form plus its id.
	if !strings.Contains(html, "NFR -- stays cheap") {
		t.Errorf("breakdown missing unshipped Requirement in Kind -- Name form (missing %q)", "NFR -- stays cheap")
	}
	if !strings.Contains(html, nfr.ID.String()) {
		t.Errorf("breakdown missing unshipped Requirement id %s", nfr.ID)
	}
}

// TestDeliveryBreakdownRendersFRKind pins the FR (not just NFR) kind form
// renders too, so a reader can tell an FR from an NFR in the list.
func TestDeliveryBreakdownRendersFRKind(t *testing.T) {
	fr := slice.RequirementEntity{
		EntityRef: slice.EntityRef{ID: mustID(t, "44444444-4444-4444-4444-444444444444")},
		Kind:      "FR",
		Name:      "product granularity",
	}
	m, _, mID, _ := milestoneEntry(t,
		"Auth rework", "outcome", budgetPtr(1), store.MilestoneStatusPartiallyComplete,
		"child", "child outcome", store.MilestoneStatusShipped)

	html := renderDelivery(t,
		slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}},
		map[uuid.UUID]pages.DeliveryBreakdown{mID: breakdownOf(requirementDoc(fr), slice.Document{})},
	)
	if !strings.Contains(html, "FR -- product granularity") {
		t.Errorf("breakdown missing shipped FR in Kind -- Name form (missing %q)", "FR -- product granularity")
	}
}

// ---------------------------------------------------------------------------
// 4. two-way drift guard
// ---------------------------------------------------------------------------

// TestDeliveryWireFieldClassesAreComplete is the delivery half of the
// two-way drift guard: every leaf field slice.MilestoneListingEntry and
// slice.MilepebbleListingEntry expose must be classified in wireClasses
// (carried / grouped / structural), and every classified field must still
// exist on the type. A wire type that grows a field fails here until
// someone decides whether the page carries, groups, or ignores it. The
// shared TestSpecWireFieldClassesAreComplete runs the same check over the
// same table including these two types; this test states the delivery
// contract directly so a failure names the delivery type.
func TestDeliveryWireFieldClassesAreComplete(t *testing.T) {
	wires := map[string]any{
		"MilestoneListingEntry":  slice.MilestoneListingEntry{},
		"MilepebbleListingEntry": slice.MilepebbleListingEntry{},
	}
	for typeName, zero := range wires {
		byField, ok := wireClasses[typeName]
		if !ok {
			t.Errorf("delivery wire type %s has no field classification table", typeName)
			continue
		}
		leaves := wireLeaves(t, zero)
		for field := range leaves {
			if _, ok := byField[field]; !ok {
				t.Errorf("delivery wire type %s exposes field %q with no classification; decide carried/grouped/structural", typeName, field)
			}
		}
		for field := range byField {
			if _, ok := leaves[field]; !ok {
				t.Errorf("wireClasses lists field %q for %s, but the type has no such field", field, typeName)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 5. non-vacuity
// ---------------------------------------------------------------------------

// TestDeliveryCarriedWireFieldPresenceIsNonVacuous guards the delivery
// parity tests themselves: it renames a field's value in a copy of the
// wire while keeping the HTML rendered from the original, and confirms the
// same requireCarried check the real tests use flags the mismatch. Without
// this, a bug that made every wire field render empty (or made
// requireCarried a no-op) would silently pass.
func TestDeliveryCarriedWireFieldPresenceIsNonVacuous(t *testing.T) {
	m, _, _, _ := milestoneEntry(t,
		"Auth rework", "outcome", budgetPtr(12), store.MilestoneStatusInProgress,
		"child", "child outcome", store.MilestoneStatusShipped)
	html := renderDelivery(t, slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}}, nil)

	// The real render passes the carried check.
	sane := &testing.T{}
	requireCarried(sane, "MilestoneListingEntry", m, html)
	if sane.Failed() {
		t.Fatalf("baseline carried check failed on the real render; the non-vacuity probe below would be meaningless")
	}

	// Rename the milestone name in the wire but keep the original HTML:
	// requireCarried must now report the mismatch.
	renamed := m
	renamed.Name = "a name the page does not contain"
	fake := &testing.T{}
	requireCarried(fake, "MilestoneListingEntry", renamed, html)
	if !fake.Failed() {
		t.Errorf("requireCarried did not flag a carried milestone field whose value is absent from the render")
	}
}

// TestDeliveryMilestonePresenceIsNonVacuousOnAnEmptyPage is the strongest
// vacuity guard: a page that rendered nothing would make every carried
// assertion vacuously true. This confirms the milestone's carried fields
// are each individually detected when absent, by checking that a
// deliberately emptied render fails requireCarried for at least one field.
func TestDeliveryPresenceFailsOnEmptyPage(t *testing.T) {
	m, _, _, _ := milestoneEntry(t,
		"Auth rework", deref(uniqueBody("ms")), budgetPtr(12), store.MilestoneStatusInProgress,
		"child", "child outcome", store.MilestoneStatusShipped)
	empty := "<html><body></body></html>"

	fake := &testing.T{}
	requireCarried(fake, "MilestoneListingEntry", m, empty)
	if !fake.Failed() {
		t.Errorf("requireCarried passed an empty page; the delivery parity assertions would be vacuous")
	}
}

// ---------------------------------------------------------------------------
// 6. all eight statuses render, distinct classes, neutral fallback
// ---------------------------------------------------------------------------

// deliveryNeutralStyle is the tuple components.MilestoneStatusStyle returns
// for a value it does not know. Spelled out here rather than derived from
// the mapper so a change to the fallback is a deliberate test edit.
var deliveryNeutralStyle = components.StatusStyle{Variant: htmxui.BadgeNeutral, Size: htmxui.BadgeSizeSM, Soft: false}

// TestDeliveryAllStatusesRenderDistinctBadge (FR 4398c532): every one of
// store.MilestoneStatus's eight values renders its human label inside a
// daisyUI badge carrying exactly the variant (and soft treatment) that
// components.MilestoneStatusStyle assigns it, so the page actually shows
// the distinction the mapper makes.
//
// The mapper's own contract -- every status distinct, none falling through
// to the neutral fallback -- is covered at the components level by
// components/status_test.go. What only this page can prove is that the
// rendered row carries those classes as exact tokens, so a future edit that
// swapped the badge for a hand-rolled span would fail here.
func TestDeliveryAllStatusesRenderDistinctBadge(t *testing.T) {
	all := []store.MilestoneStatus{
		store.MilestoneStatusNotStarted,
		store.MilestoneStatusInDesign,
		store.MilestoneStatusDesigned,
		store.MilestoneStatusPlanned,
		store.MilestoneStatusInProgress,
		store.MilestoneStatusShipped,
		store.MilestoneStatusPartiallyComplete,
		store.MilestoneStatusAbandoned,
	}
	if len(all) != 8 {
		t.Fatalf("expected the eight-value MilestoneStatus set, listed %d", len(all))
	}

	seen := map[[2]any]store.MilestoneStatus{}
	for _, s := range all {
		// The fixture is deliberately homogeneous: the nested milepebble
		// carries the same status, and the milestone carries no FR budget.
		// Both would otherwise contribute a second badge, and the budget
		// badge is ghost -- the same class "not started" maps to -- which
		// would satisfy the variant assertion without the status badge
		// ever rendering.
		m, _, _, _ := milestoneEntry(t,
			"Container", "outcome", nil, s,
			"child", "child outcome", s)
		html := renderDelivery(t, slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}}, nil)

		style := components.MilestoneStatusStyle(string(s))
		if style == deliveryNeutralStyle {
			t.Errorf("known status %q fell through to the neutral fallback", s)
			continue
		}
		// The variant and soft treatment are exact class tokens on the
		// rendered badge -- not merely substrings of a longer class list.
		if !hasClassToken(html, string(style.Variant)) {
			t.Errorf("status %q rendered without the %q class token", s, style.Variant)
		}
		if gotSoft := hasClassToken(html, "badge-soft"); gotSoft != style.Soft {
			t.Errorf("status %q soft treatment rendered as %v, want %v", s, gotSoft, style.Soft)
		}
		if !strings.Contains(html, string(s)) {
			t.Errorf("status %q label not rendered", s)
		}
		// Distinct (variant, soft) tuples keep the eight states visually
		// distinct -- notably shipped versus partially complete, which
		// share a hue and are separated only by the soft treatment.
		key := [2]any{style.Variant, style.Soft}
		if prev, dup := seen[key]; dup {
			t.Errorf("statuses %q and %q render identically (variant %q, soft %v); they must be visually distinct", prev, s, style.Variant, style.Soft)
		}
		seen[key] = s
	}
}

// TestDeliveryUnknownStatusUsesNeutralFallback pins that a genuinely
// unknown status value (one outside the eight-value set) renders with the
// neutral badge, so adding a new status without a mapping is caught rather
// than rendering an empty badge.
func TestDeliveryUnknownStatusUsesNeutralFallback(t *testing.T) {
	unknown := store.MilestoneStatus("some-future-status")
	style := components.MilestoneStatusStyle(string(unknown))
	if style != deliveryNeutralStyle {
		t.Errorf("unknown status %q mapped to %+v, want the neutral fallback %+v", unknown, style, deliveryNeutralStyle)
	}
	// And it renders with that class, so the badge is never empty.
	m, _, _, _ := milestoneEntry(t,
		"Container", "outcome", budgetPtr(1), unknown,
		"child", "child outcome", store.MilestoneStatusShipped)
	html := renderDelivery(t, slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}}, nil)
	if !hasClassToken(html, string(htmxui.BadgeNeutral)) {
		t.Errorf("unknown status did not render the neutral badge")
	}
}

// ---------------------------------------------------------------------------
// 7. breakdown only for partially-complete containers
// ---------------------------------------------------------------------------

// TestDeliveryBreakdownOnlyForPartiallyComplete (FR 4398c532): a milestone
// and a milepebble at "partially complete" each render a breakdown with
// both Shipped and Unshipped lists, while a "shipped" and an "in progress"
// container render no breakdown block at all. Also pins the empty-list
// case: an empty shipped list reads as "Nothing shipped yet." rather than
// vanishing.
func TestDeliveryBreakdownOnlyForPartiallyComplete(t *testing.T) {
	// A partially-complete milestone WITH a breakdown.
	partialM, _, partialMID, _ := milestoneEntry(t,
		"Partial milestone", "outcome", budgetPtr(2), store.MilestoneStatusPartiallyComplete,
		"child", "child outcome", store.MilestoneStatusShipped)
	// A shipped milestone with NO breakdown in the map (as deliveryBreakdowns
	// would leave it).
	shippedM, _, shippedMID, _ := milestoneEntry(t,
		"Shipped milestone", "outcome", budgetPtr(2), store.MilestoneStatusShipped,
		"child", "child outcome", store.MilestoneStatusShipped)
	// A partially-complete milepebble nested under a non-partial milestone.
	shippedParent, partialMP, _, partialMPID := milestoneEntry(t,
		"Parent milestone", "outcome", budgetPtr(2), store.MilestoneStatusShipped,
		"Partial child", "child outcome", store.MilestoneStatusPartiallyComplete)
	// An in-progress milepebble with no breakdown.
	inProgParent, inProgMP, _, inProgMPID := milestoneEntry(t,
		"In-progress parent", "outcome", budgetPtr(2), store.MilestoneStatusInProgress,
		"In-progress child", "child outcome", store.MilestoneStatusInProgress)

	feat := slice.FeatureEntity{
		EntityRef:     slice.EntityRef{ID: mustID(t, "33333333-3333-3333-3333-333333333333")},
		Name:          "shipped feature",
		DisplayNumber: 4,
	}
	breakdowns := map[uuid.UUID]pages.DeliveryBreakdown{
		partialMID:  breakdownOf(featureDoc(feat), slice.Document{}),
		partialMPID: breakdownOf(featureDoc(feat), slice.Document{}),
	}
	listing := slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{
		partialM, shippedM, shippedParent, inProgParent,
	}}
	html := renderDelivery(t, listing, breakdowns)

	// Both partial containers render a breakdown. Detect the breakdown by its
	// stable data-krill hook, not the word "Shipped" -- a container's own
	// name may contain that word.
	for _, id := range []uuid.UUID{partialMID, partialMPID} {
		block := containerBlock(html, id)
		if !strings.Contains(block, deliveryBreakdownHook) {
			t.Errorf("partially-complete container %s did not render a breakdown block", id)
			continue
		}
		if !strings.Contains(block, "Shipped") || !strings.Contains(block, "Unshipped") {
			t.Errorf("partially-complete container %s did not render both Shipped and Unshipped lists", id)
		}
	}
	// The shipped milestone renders no breakdown block.
	if block := containerBlock(html, shippedMID); strings.Contains(block, deliveryBreakdownHook) {
		t.Errorf("shipped container %s rendered a breakdown block; only partially-complete containers get one", shippedMID)
	}
	// The in-progress milepebble renders no breakdown block.
	if block := containerBlock(html, inProgMPID); strings.Contains(block, deliveryBreakdownHook) {
		t.Errorf("in-progress milepebble %s rendered a breakdown block; only partially-complete containers get one", inProgMPID)
	}
	_ = partialMP
	_ = inProgMP
}

// TestDeliveryEmptyShippedListReadsAsNothingShipped pins the empty-list
// case: a partially-complete container with an empty shipped list says
// "Nothing shipped yet." rather than dropping the Shipped heading.
func TestDeliveryEmptyShippedListReadsAsNothingShipped(t *testing.T) {
	m, _, mID, _ := milestoneEntry(t,
		"Partial", "outcome", budgetPtr(1), store.MilestoneStatusPartiallyComplete,
		"child", "child outcome", store.MilestoneStatusShipped)
	// Both lists empty.
	html := renderDelivery(t,
		slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}},
		map[uuid.UUID]pages.DeliveryBreakdown{mID: breakdownOf(slice.Document{}, slice.Document{})},
	)
	block := containerBlock(html, mID)
	if !strings.Contains(block, "Shipped") {
		t.Errorf("empty breakdown dropped the Shipped heading")
	}
	if !strings.Contains(block, "Nothing shipped yet.") {
		t.Errorf("empty shipped list did not read as %q", "Nothing shipped yet.")
	}
	if !strings.Contains(block, "Nothing unshipped.") {
		t.Errorf("empty unshipped list did not read as %q", "Nothing unshipped.")
	}
}

// containerBlock returns the HTML slice for one container: from its id
// anchor up to the next container's id anchor (or end of body), so a
// per-container assertion (breakdown present/absent) is not confused by a
// sibling's block.
func containerBlock(html string, id uuid.UUID) string {
	start := strings.Index(html, `id="`+id.String()+`"`)
	if start < 0 {
		return ""
	}
	rest := html[start:]
	// Cut at the next container id anchor after this one, if any.
	if next := strings.Index(rest[1:], `id="`); next >= 0 {
		return rest[:next+1]
	}
	return rest
}

// TestDeliveryBreakdownsQueriesOnlyPartiallyComplete pins the handler-side
// read: deliveryBreakdowns resolves a breakdown only for a
// partially-complete milestone or milepebble, and asks the reader for
// exactly those container ids. A shipped or in-progress container is never
// read, so its shipped/unshipped breakdown is "not applicable", not
// "zero shipped, zero unshipped".
func TestDeliveryBreakdownsQueriesOnlyPartiallyComplete(t *testing.T) {
	partialM, partialMP, partialMID, partialMPID := milestoneEntry(t,
		"Partial", "outcome", budgetPtr(1), store.MilestoneStatusPartiallyComplete,
		"Partial child", "child outcome", store.MilestoneStatusPartiallyComplete)
	shippedM, _, shippedMID, _ := milestoneEntry(t,
		"Shipped", "outcome", budgetPtr(1), store.MilestoneStatusShipped,
		"child", "child outcome", store.MilestoneStatusShipped)
	inProgM, inProgMP, inProgMID, inProgMPID := milestoneEntry(t,
		"In progress", "outcome", budgetPtr(1), store.MilestoneStatusInProgress,
		"In-progress child", "child outcome", store.MilestoneStatusInProgress)

	reader := &fakeSpecReader{breakdown: map[uuid.UUID]deliveryPair{
		partialMID:  {},
		partialMPID: {},
	}}
	app := &App{spec: reader}
	listing := slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{
		partialM, shippedM, inProgM,
	}}

	breakdowns := app.deliveryBreakdowns(context.Background(), listing)

	// Exactly the two partially-complete containers were read.
	if len(reader.queried) != 2 {
		t.Errorf("deliveryBreakdowns queried %d containers (%v), want 2 (only the partially-complete ones)", len(reader.queried), reader.queried)
	}
	for _, queried := range reader.queried {
		if queried != partialMID && queried != partialMPID {
			t.Errorf("deliveryBreakdowns queried non-partial container %s", queried)
		}
	}
	// Non-partial containers got no breakdown entry.
	for _, id := range []uuid.UUID{shippedMID, inProgMID, inProgMPID} {
		if _, ok := breakdowns[id]; ok {
			t.Errorf("non-partial container %s got a breakdown entry; only partially-complete containers do", id)
		}
	}
	// The partial ones did.
	for _, id := range []uuid.UUID{partialMID, partialMPID} {
		if _, ok := breakdowns[id]; !ok {
			t.Errorf("partially-complete container %s got no breakdown entry", id)
		}
	}
	_ = partialMP
	_ = inProgMP
}

// ---------------------------------------------------------------------------
// 8. single-container breakdown failure degrades gracefully
// ---------------------------------------------------------------------------

// TestDeliverySingleBreakdownFailureDegradesGracefully (FR 4398c532): a
// breakdown read that fails for ONE container is non-fatal. The handler
// still returns 200, that container renders status-only with no
// breakdown, and every OTHER container's status and breakdown still
// render. A happy-path-only test would not have caught the original
// one-500-defect.
func TestDeliverySingleBreakdownFailureDegradesGracefully(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	okM, _, okMID, _ := milestoneEntry(t,
		"Healthy container", "outcome", budgetPtr(2), store.MilestoneStatusPartiallyComplete,
		"child", "child outcome", store.MilestoneStatusShipped)
	badM, _, badMID, _ := milestoneEntry(t,
		"Broken container", "outcome", budgetPtr(2), store.MilestoneStatusPartiallyComplete,
		"child", "child outcome", store.MilestoneStatusShipped)
	shippedM, _, shippedMID, _ := milestoneEntry(t,
		"Already shipped", "outcome", budgetPtr(2), store.MilestoneStatusShipped,
		"child", "child outcome", store.MilestoneStatusShipped)

	okFeat := slice.FeatureEntity{
		EntityRef:     slice.EntityRef{ID: mustID(t, "33333333-3333-3333-3333-333333333333")},
		Name:          "healthy shipped feature",
		DisplayNumber: 4,
	}

	reader := &fakeSpecReader{
		product: store.Product{ID: productID, Name: "krill", Vision: "the substrate"},
		listing: slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{okM, badM, shippedM}},
		breakdown: map[uuid.UUID]deliveryPair{
			okMID: {shipped: featureDoc(okFeat)},
		},
		// Only the broken container's breakdown read fails.
		breakdownEr: map[uuid.UUID]error{
			badMID: fmt.Errorf("delivery breakdown: boom"),
		},
	}
	mux := deliveryReadMux(reader)

	rec := fetch(t, mux, "/spec/products/"+productID.String()+"/delivery")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET delivery = %d, want 200 even when one container's breakdown read fails (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The broken container still renders its row: its name and status
	// label appear, its own breakdown block carries an inline error
	// rather than a fabricated entity list, and the entity it could not
	// read is absent.
	badBlock := containerBlock(body, badMID)
	if !strings.Contains(badBlock, "Broken container") {
		t.Errorf("failing container did not render its name (status-only expected)")
	}
	if !strings.Contains(badBlock, string(store.MilestoneStatusPartiallyComplete)) {
		t.Errorf("failing container did not render its status label")
	}
	if !strings.Contains(badBlock, deliveryBreakdownHook) {
		t.Errorf("failing container dropped its breakdown block entirely; it should degrade to an inline error in place of the block")
	}
	if !hasClassToken(badBlock, "alert-error") {
		t.Errorf("failing container's breakdown block did not render an error alert")
	}
	if strings.Contains(badBlock, "broken shipped feature") {
		t.Errorf("failing container rendered a breakdown it could not read")
	}

	// The healthy container still renders its status AND breakdown.
	okBlock := containerBlock(body, okMID)
	if !strings.Contains(okBlock, "Healthy container") || !strings.Contains(okBlock, string(store.MilestoneStatusPartiallyComplete)) {
		t.Errorf("healthy container's status did not render alongside the failure")
	}
	if !strings.Contains(okBlock, "healthy shipped feature") || !strings.Contains(okBlock, "C4 -- healthy shipped feature") {
		t.Errorf("healthy container's breakdown did not render alongside the failing one")
	}

	// The unrelated shipped container is unaffected.
	if block := containerBlock(body, shippedMID); !strings.Contains(block, "Already shipped") {
		t.Errorf("already-shipped container did not render")
	}
}

// ---------------------------------------------------------------------------
// 9. empty product
// ---------------------------------------------------------------------------

// TestDeliveryEmptyProductRendersEmptyState: a product with no milestones
// renders the "No milestones yet." empty state inside the shell chrome --
// a real page, not a blank 200.
func TestDeliveryEmptyProductRendersEmptyState(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	reader := &fakeSpecReader{
		product: store.Product{ID: productID, Name: "krill", Vision: "the substrate"},
		listing: slice.DeliveryListing{}, // no milestones
	}
	mux := deliveryReadMux(reader)

	rec := fetch(t, mux, "/spec/products/"+productID.String()+"/delivery")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET delivery (empty) = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No milestones yet.") {
		t.Errorf("empty product did not render the %q empty state", "No milestones yet.")
	}
	// Rendered inside the shell, not a blank page.
	for _, want := range []string{"<html", "<nav", "</html>"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty delivery page missing %q from the shell chrome", want)
		}
	}
}

// ---------------------------------------------------------------------------
// 10. bad / unknown product id
// ---------------------------------------------------------------------------

// TestDeliveryUnknownProductIsNotFound: an unknown (but well-formed)
// product id gives a shell-wrapped 404, matching the sibling spec pages'
// renderSpecError behaviour -- not a 500 and not a bare http.Error.
func TestDeliveryUnknownProductIsNotFound(t *testing.T) {
	unknownID := mustID(t, "99999999-9999-9999-9999-999999999999")
	reader := &fakeSpecReader{
		productErr: fmt.Errorf("get product: %w", store.ErrNotFound),
	}
	mux := deliveryReadMux(reader)

	rec := fetch(t, mux, "/spec/products/"+unknownID.String()+"/delivery")
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET delivery (unknown id) = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"<html", "<nav", "</html>", "Not found"} {
		if !strings.Contains(body, want) {
			t.Errorf("unknown-id page missing %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// 11. partially complete with nothing outstanding (FR 33d8b20e)
// ---------------------------------------------------------------------------

// deliveryDisagreementHook is the stable data-krill attribute the
// status/breakdown disambiguation carries, so these tests find it without
// depending on the alert's class or copy wording.
const deliveryDisagreementHook = `data-krill="delivery-status-disagreement"`

// TestDeliveryPartiallyCompleteWithNothingOutstandingIsDisambiguated (FR
// 33d8b20e) is the acceptance case: a container whose status reads
// "partially complete" while every entity it delivers has a shipment
// record -- a badge and an empty outstanding list disagreeing in the same
// view -- renders a disambiguation, and renders it ADJACENT to that
// container's own badge, inside that container's own breakdown block.
// Both the milestone and the milepebble form are pinned: deliveryBreakdowns
// populates both, so a milepebble-only fix would pass a milestone-only test.
//
// Driven through the real handleSpecDelivery so deliveryBreakdowns decides
// StatusDisagrees from the read, not from a hand-set view-model field.
func TestDeliveryPartiallyCompleteWithNothingOutstandingIsDisambiguated(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	partialM, _, partialMID, partialMPID := milestoneEntry(t,
		"Nothing outstanding", "outcome", budgetPtr(2), store.MilestoneStatusPartiallyComplete,
		"Nothing outstanding either", "child outcome", store.MilestoneStatusPartiallyComplete)
	// A partially-complete container that DOES have work outstanding, the
	// contrast that keeps the disambiguation from becoming decoration on
	// every partial container.
	busyM, _, busyMID, _ := milestoneEntry(t,
		"Still has scope", "outcome", budgetPtr(2), store.MilestoneStatusPartiallyComplete,
		"child", "child outcome", store.MilestoneStatusShipped)

	shippedFeat := slice.FeatureEntity{
		EntityRef:     slice.EntityRef{ID: mustID(t, "33333333-3333-3333-3333-333333333333")},
		Name:          "recorded feature",
		DisplayNumber: 4,
	}
	outstandingFeat := slice.FeatureEntity{
		EntityRef:     slice.EntityRef{ID: mustID(t, "44444444-4444-4444-4444-444444444444")},
		Name:          "outstanding feature",
		DisplayNumber: 5,
	}
	reader := &fakeSpecReader{
		product: store.Product{ID: productID, Name: "krill", Vision: "the substrate"},
		listing: slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{partialM, busyM}},
		breakdown: map[uuid.UUID]deliveryPair{
			partialMID:  {shipped: featureDoc(shippedFeat)},
			partialMPID: {shipped: featureDoc(shippedFeat)},
			busyMID:     {shipped: featureDoc(shippedFeat), unshipped: featureDoc(outstandingFeat)},
		},
	}
	mux := deliveryReadMux(reader)

	rec := fetch(t, mux, "/spec/products/"+productID.String()+"/delivery")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET delivery = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, tc := range []struct {
		name string
		id   uuid.UUID
	}{
		{"milestone", partialMID},
		{"milepebble", partialMPID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block := containerBlock(body, tc.id)
			if block == "" {
				t.Fatalf("container %s did not render at all", tc.id)
			}
			// The badge is still there, unchanged -- the disambiguation
			// joins it, it does not replace it.
			badge := strings.Index(block, string(store.MilestoneStatusPartiallyComplete))
			if badge < 0 {
				t.Fatalf("container %s lost its partially-complete badge", tc.id)
			}
			if !strings.Contains(block, deliveryBreakdownHook) {
				t.Fatalf("container %s rendered no breakdown block to disambiguate inside", tc.id)
			}
			disagree := strings.Index(block, deliveryDisagreementHook)
			if disagree < 0 {
				t.Fatalf("partially-complete container %s with nothing outstanding rendered no disambiguation", tc.id)
			}
			// Adjacency: after the badge, inside the block that block's own
			// badge sits above, and above the lists it disagrees with.
			if disagree < badge {
				t.Errorf("disambiguation rendered before container %s's own badge, so it is not adjacent to it", tc.id)
			}
			if list := strings.Index(block, "recorded feature"); disagree > list {
				t.Errorf("disambiguation rendered after container %s's breakdown lists, so it is not adjacent to its badge", tc.id)
			}
			// The copy names the inconsistency rather than asserting a
			// cause: both the status and the delivery scope are named.
			for _, want := range []string{"Status and breakdown disagree", "partially complete", "nothing in its breakdown is outstanding"} {
				if !strings.Contains(block, want) {
					t.Errorf("disambiguation copy for %s missing %q", tc.id, want)
				}
			}
			// The lists it disambiguates are still rendered, not replaced.
			if !strings.Contains(block, "Nothing unshipped.") {
				t.Errorf("container %s lost its %q line", tc.id, "Nothing unshipped.")
			}
		})
	}

	// The contrast: a partially-complete container WITH outstanding scope
	// lists the scope and says nothing about a disagreement, because the
	// badge and the list agree.
	busyBlock := containerBlock(body, busyMID)
	if strings.Contains(busyBlock, deliveryDisagreementHook) {
		t.Errorf("partially-complete container %s with %q outstanding rendered the disagreement; the two are consistent", busyMID, "outstanding feature")
	}
	if !strings.Contains(busyBlock, "outstanding feature") {
		t.Errorf("partially-complete container %s dropped its outstanding list", busyMID)
	}
}

// TestDeliveryUnshippedBucketIsLabelledCauseFree (FR 33d8b20e) pins the
// label for a delivered entity with no shipment record: it states what
// every member of the bucket is, and asserts no cause for it. A skipped
// recording step is not observable anywhere in krill, and the same
// sentence is true of a planned milestone's entire scope -- so a label
// that reads true of the whole roadmap teaches an operator to skip it.
func TestDeliveryUnshippedBucketIsLabelledCauseFree(t *testing.T) {
	m, _, mID, _ := milestoneEntry(t,
		"Partial", "outcome", budgetPtr(1), store.MilestoneStatusPartiallyComplete,
		"child", "child outcome", store.MilestoneStatusShipped)
	shippedFeat := slice.FeatureEntity{
		EntityRef:     slice.EntityRef{ID: mustID(t, "33333333-3333-3333-3333-333333333333")},
		Name:          "recorded feature",
		DisplayNumber: 4,
	}
	outstandingFeat := slice.FeatureEntity{
		EntityRef:     slice.EntityRef{ID: mustID(t, "44444444-4444-4444-4444-444444444444")},
		Name:          "outstanding feature",
		DisplayNumber: 5,
	}
	html := renderDelivery(t,
		slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}},
		map[uuid.UUID]pages.DeliveryBreakdown{
			mID: breakdownOf(featureDoc(shippedFeat), featureDoc(outstandingFeat)),
		},
	)
	block := containerBlock(html, mID)

	if !strings.Contains(block, "Delivered to this container, no shipment record.") {
		t.Errorf("the Unshipped bucket did not render its cause-free label %q", "Delivered to this container, no shipment record.")
	}
	// The label sits with the Unshipped list it describes.
	if label, list := strings.Index(block, "Delivered to this container, no shipment record."), strings.Index(block, "outstanding feature"); label > list {
		t.Errorf("the unshipped label rendered after the list it describes")
	}
	// No cause is asserted anywhere in the block: nothing in krill can
	// observe that a recording step was skipped, and no surface should
	// claim to.
	for _, cause := range []string{"skipped", "not yet merged", "unrecorded", "forgot", "overdue"} {
		if strings.Contains(strings.ToLower(block), cause) {
			t.Errorf("the breakdown block asserts a cause nothing can observe: found %q", cause)
		}
	}
}

// TestDeliveryBreakdownErrorPathIsNotADisagreement (FR 33d8b20e): the error
// branch of breakdownBlock is not an instance of this defect and is left
// unchanged. An alert with no list is not a confident badge over an empty
// list -- nothing on the page claims completion -- so a failed read renders
// the error alone, with no disambiguation and no fabricated empty-list copy.
func TestDeliveryBreakdownErrorPathIsNotADisagreement(t *testing.T) {
	m, _, mID, _ := milestoneEntry(t,
		"Partial", "outcome", budgetPtr(1), store.MilestoneStatusPartiallyComplete,
		"child", "child outcome", store.MilestoneStatusShipped)
	html := renderDelivery(t,
		slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}},
		map[uuid.UUID]pages.DeliveryBreakdown{
			mID: {Error: "This container's shipped/unshipped breakdown could not be read. See the logs."},
		},
	)
	block := containerBlock(html, mID)

	if strings.Contains(block, deliveryDisagreementHook) {
		t.Errorf("an unreadable breakdown rendered the status disagreement; the error path is not an instance of it")
	}
	if !hasClassToken(block, "alert-error") {
		t.Errorf("an unreadable breakdown did not render its error alert")
	}
	// The empty-list copy was not moved into the error branch.
	if strings.Contains(block, "Nothing unshipped.") || strings.Contains(block, "Nothing shipped yet.") {
		t.Errorf("an unreadable breakdown fabricated an empty-list line")
	}
}

// TestDeliveryStatusDisagreementGatedOnNothingOutstanding pins the gate at
// the view-model level: StatusDisagrees is a property of the two lists, so
// a container with outstanding scope never carries it no matter how the
// map entry was built.
func TestDeliveryStatusDisagreementGatedOnNothingOutstanding(t *testing.T) {
	outstanding := breakdownOf(slice.Document{}, featureDoc(slice.FeatureEntity{
		EntityRef:     slice.EntityRef{ID: mustID(t, "44444444-4444-4444-4444-444444444444")},
		Name:          "outstanding feature",
		DisplayNumber: 5,
	}))
	if outstanding.StatusDisagrees {
		t.Errorf("a breakdown with an outstanding list was marked as a status disagreement")
	}
	nothing := breakdownOf(slice.Document{}, slice.Document{})
	if !nothing.StatusDisagrees {
		t.Errorf("a breakdown with nothing outstanding was not marked as a status disagreement")
	}
}
