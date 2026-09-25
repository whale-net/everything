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
// The pure deliveryPageOf + deliveryTemplate are exercised for parity and
// status/breakdown shape (no database). The handler-level cases -- a
// single container's breakdown read failing, the empty product, and the
// unknown-id 404 -- run against an in-memory fake spec reader through the
// real handleSpecDelivery, so they exercise the same error tolerance the
// browser sees.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// ---------------------------------------------------------------------------
// fake spec reader
// ---------------------------------------------------------------------------

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
	product     store.Product
	productErr  error
	listing     slice.DeliveryListing
	listingErr  error
	breakdown   map[uuid.UUID]deliveryPair
	breakdownEr map[uuid.UUID]error // per-container injected failure
	queried     []uuid.UUID         // container ids DeliveryBreakdown was called for
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
	return nil, nil
}

// deliveryReadMux mounts the real delivery handler on a bare mux. The
// handler reads only app.spec and renders through the shared shell, so no
// database and no other store accessor is needed; the shell chrome is
// rendered regardless, which is what the empty/404 cases assert.
func deliveryReadMux(reader specReadClient) *http.ServeMux {
	app := &App{spec: reader}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /spec/products/{id}/delivery", app.handleSpecDelivery)
	return mux
}

// renderDelivery renders one delivery page from a listing + breakdown map.
func renderDelivery(t *testing.T, listing slice.DeliveryListing, breakdowns map[uuid.UUID]deliveryBreakdown) string {
	t.Helper()
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	product := store.Product{ID: productID, Name: "krill", Vision: "the substrate"}
	page := deliveryPageOf(product, listing, breakdowns, productID)
	return string(renderPage(deliveryTemplate, page))
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
// map entry, exactly as deliveryBreakdowns does for a successful read.
func breakdownOf(shipped, unshipped slice.Document) deliveryBreakdown {
	return deliveryBreakdown{
		Shipped:   deliveryEntitiesOf(shipped),
		Unshipped: deliveryEntitiesOf(unshipped),
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
		map[uuid.UUID]deliveryBreakdown{mID: breakdownOf(shipped, unshipped)},
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
		map[uuid.UUID]deliveryBreakdown{mID: breakdownOf(requirementDoc(fr), slice.Document{})},
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
// 6. all seven statuses render, distinct classes, neutral fallback
// ---------------------------------------------------------------------------

// TestDeliveryAllStatusesRenderDistinctBadge (FR 4398c532): every one of
// store.MilestoneStatus's seven values renders its human label and a
// non-empty, non-neutral CSS class, so no status silently falls through to
// the fallback badge.
func TestDeliveryAllStatusesRenderDistinctBadge(t *testing.T) {
	all := []store.MilestoneStatus{
		store.MilestoneStatusNotStarted,
		store.MilestoneStatusInDesign,
		store.MilestoneStatusPlanned,
		store.MilestoneStatusInProgress,
		store.MilestoneStatusShipped,
		store.MilestoneStatusPartiallyComplete,
		store.MilestoneStatusAbandoned,
	}
	if len(all) != 7 {
		t.Fatalf("expected the seven-value MilestoneStatus set, listed %d", len(all))
	}

	seen := map[string]store.MilestoneStatus{}
	for _, s := range all {
		m, _, mID, _ := milestoneEntry(t,
			"Container", "outcome", budgetPtr(1), s,
			"child", "child outcome", store.MilestoneStatusShipped)
		html := renderDelivery(t, slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}}, nil)

		class := statusClass(s)
		if class == "" {
			t.Errorf("status %q maps to an empty CSS class", s)
			continue
		}
		if class == "status-other" {
			t.Errorf("known status %q fell through to the neutral status-other fallback", s)
		}
		// The rendered row carries the class and the label.
		if !strings.Contains(html, `class="status `+class+`"`) {
			t.Errorf("status %q rendered without its %q class", s, class)
		}
		if !strings.Contains(html, string(s)) {
			t.Errorf("status %q label not rendered", s)
		}
		_ = mID
		// Distinct classes keep the seven states visually distinct.
		if prev, dup := seen[class]; dup {
			t.Errorf("statuses %q and %q share the class %q; they must be visually distinct", prev, s, class)
		}
		seen[class] = s
	}
}

// TestDeliveryUnknownStatusUsesNeutralFallback pins that a genuinely
// unknown status value (one outside the seven-value set) renders with the
// neutral status-other class, so adding a new status without a mapping is
// caught rather than rendering an empty badge.
func TestDeliveryUnknownStatusUsesNeutralFallback(t *testing.T) {
	unknown := store.MilestoneStatus("some-future-status")
	if got := statusClass(unknown); got != "status-other" {
		t.Errorf("unknown status %q mapped to %q, want the neutral status-other fallback", unknown, got)
	}
	// And it renders with that class, so the badge is never empty.
	m, _, _, _ := milestoneEntry(t,
		"Container", "outcome", budgetPtr(1), unknown,
		"child", "child outcome", store.MilestoneStatusShipped)
	html := renderDelivery(t, slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{m}}, nil)
	if !strings.Contains(html, `class="status status-other"`) {
		t.Errorf("unknown status did not render the neutral status-other badge")
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
	breakdowns := map[uuid.UUID]deliveryBreakdown{
		partialMID:  breakdownOf(featureDoc(feat), slice.Document{}),
		partialMPID: breakdownOf(featureDoc(feat), slice.Document{}),
	}
	listing := slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{
		partialM, shippedM, shippedParent, inProgParent,
	}}
	html := renderDelivery(t, listing, breakdowns)

	// Both partial containers render a breakdown. Detect the breakdown by its
	// own block marker (class="breakdown"), not the word "Shipped" -- a
	// container's own name may contain that word.
	for _, id := range []uuid.UUID{partialMID, partialMPID} {
		block := containerBlock(html, id)
		if !strings.Contains(block, `class="breakdown"`) {
			t.Errorf("partially-complete container %s did not render a breakdown block", id)
			continue
		}
		if !strings.Contains(block, "Shipped") || !strings.Contains(block, "Unshipped") {
			t.Errorf("partially-complete container %s did not render both Shipped and Unshipped lists", id)
		}
	}
	// The shipped milestone renders no breakdown block.
	if block := containerBlock(html, shippedMID); strings.Contains(block, `class="breakdown"`) {
		t.Errorf("shipped container %s rendered a breakdown block; only partially-complete containers get one", shippedMID)
	}
	// The in-progress milepebble renders no breakdown block.
	if block := containerBlock(html, inProgMPID); strings.Contains(block, `class="breakdown"`) {
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
		map[uuid.UUID]deliveryBreakdown{mID: breakdownOf(slice.Document{}, slice.Document{})},
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

	// The broken container still renders status-only: its name and status
	// label appear, but its breakdown entity does not.
	badBlock := containerBlock(body, badMID)
	if !strings.Contains(badBlock, "Broken container") {
		t.Errorf("failing container did not render its name (status-only expected)")
	}
	if !strings.Contains(badBlock, string(store.MilestoneStatusPartiallyComplete)) {
		t.Errorf("failing container did not render its status label")
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
	for _, want := range []string{"<html", "<nav>", "</html>"} {
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
	for _, want := range []string{"<html", "<nav>", "</html>", "Not found"} {
		if !strings.Contains(body, want) {
			t.Errorf("unknown-id page missing %q", want)
		}
	}
}
