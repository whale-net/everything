// Coverage for the Milestone detail's Delivery card (FR 1d16afe2): the two
// four-column tables, the no-shipment-record sentence, the disagreement
// warning, and the copy chip. No database.
//
// The table assertions parse the served markup with a real HTML parser and
// read the header and body cells out of it, rather than counting tag
// occurrences. The split IS the FR's claim -- an operator compares two
// columns of the same shape to see what shipped and what did not -- and a
// substring check cannot tell a table with four columns from a page that
// merely mentions the words "Item", "Kind", "Shipment" and "Id".
package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// The three containers every case below reads. They are three cases of one
// thing: a milestone and a milepebble answer at the same URL, and a shipped
// container's empty unshipped list is the normal end state rather than the
// disagreement a partially-complete one would be.
var (
	detailPartialID  = uuid.MustParse("77777777-7777-7777-7777-777777777777")
	detailShippedID  = uuid.MustParse("88888888-8888-8888-8888-888888888888")
	detailPebbleHost = uuid.MustParse("99999999-9999-9999-9999-999999999999")
	detailPartialPeb = uuid.MustParse("10101010-1010-1010-1010-101010101010")
)

// The entities the breakdowns below carry. A Feature and a Requirement in
// each bucket, so the Kind column is exercised on both values and neither
// table can pass with one kind of row alone.
var (
	detailShippedFeature = uuid.MustParse("a1111111-1111-1111-1111-111111111111")
	detailShippedFR      = uuid.MustParse("a2222222-2222-2222-2222-222222222222")
	detailUnshippedFeat  = uuid.MustParse("a3333333-3333-3333-3333-333333333333")
	detailUnshippedNFR   = uuid.MustParse("a4444444-4444-4444-4444-444444444444")
)

var detailCardListing = slice.DeliveryListing{
	Milestones: []slice.MilestoneListingEntry{
		{ID: detailPartialID, Name: "M8 Detail cards", Status: store.MilestoneStatusPartiallyComplete},
		{ID: detailShippedID, Name: "M9 Already shipped", Status: store.MilestoneStatusShipped},
		{
			ID:     detailPebbleHost,
			Name:   "M10 Console reads",
			Status: store.MilestoneStatusInProgress,
			Milepebbles: []slice.MilepebbleListingEntry{
				{ID: detailPartialPeb, Name: "P2 Delivery card", Status: store.MilestoneStatusPartiallyComplete},
			},
		},
	},
}

// detailCardBreakdowns is the breakdown read's answer for the containers
// above: the partially-complete pair both split, and the shipped one has
// everything shipped and nothing outstanding -- which is the case that must
// NOT raise the disagreement warning.
var detailCardBreakdowns = map[uuid.UUID]deliveryPair{
	detailPartialID:  {shipped: detailShippedDoc(), unshipped: detailUnshippedDoc()},
	detailPartialPeb: {shipped: detailShippedDoc(), unshipped: detailUnshippedDoc()},
	detailShippedID:  {shipped: detailShippedDoc(), unshipped: slice.Document{}},
	detailPebbleHost: {},
}

func detailShippedDoc() slice.Document {
	return slice.Document{
		Features: []slice.FeatureEntity{{
			EntityRef:     slice.EntityRef{ID: detailShippedFeature},
			Name:          "Milestones table",
			DisplayNumber: 4,
		}},
		Requirements: []slice.RequirementEntity{{
			EntityRef: slice.EntityRef{ID: detailShippedFR},
			Kind:      "FR",
			Name:      "Milestone detail header",
		}},
	}
}

func detailUnshippedDoc() slice.Document {
	return slice.Document{
		Features: []slice.FeatureEntity{{
			EntityRef:     slice.EntityRef{ID: detailUnshippedFeat},
			Name:          "Delivery card",
			DisplayNumber: 7,
		}},
		Requirements: []slice.RequirementEntity{{
			EntityRef: slice.EntityRef{ID: detailUnshippedNFR},
			Kind:      "NFR",
			Name:      "Delivery card is readable",
		}},
	}
}

// detailCardMux mounts the real route against the listing and breakdowns
// above, so these cases drive the same handler production does.
func detailCardMux(t *testing.T, breakdownErr map[uuid.UUID]error) *http.ServeMux {
	t.Helper()
	return detailMuxWithReader(t, []store.Product{milestoneDetailProduct},
		&fakeSpecReader{listing: detailCardListing, breakdown: detailCardBreakdowns, breakdownEr: breakdownErr})
}

// deliveryCardOf renders the detail page for one container through the real
// handler, and returns both the whole body and the page's own region sliced
// out of it -- the same slice productPageRegion performs, because a card
// that fell outside that region would be invisible to every existing
// product-scope assertion.
func deliveryCardOf(t *testing.T, mux *http.ServeMux, id uuid.UUID) (body, region string) {
	t.Helper()
	rec := fetch(t, mux, milestoneDetailHref(milestoneDetailProductID, id))
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	return body, productPageRegion(body)
}

// cardHTML renders just the card, for the cases whose subject is the card's
// own markup rather than the page around it.
func cardHTML(t *testing.T, b *pages.DeliveryBreakdown) string {
	t.Helper()
	return mustRenderComponent(pages.MilestoneDelivery(b))
}

// splitBreakdown is a container that has genuinely delivered part of its
// scope: one shipped Feature, one unshipped Feature, nothing outstanding
// disputed.
func splitBreakdown() *pages.DeliveryBreakdown {
	return &pages.DeliveryBreakdown{
		Shipped:   []pages.DeliveryEntity{{Label: "C4 -- Milestones table", ID: detailShippedFeature.String(), Kind: "Feature"}},
		Unshipped: []pages.DeliveryEntity{{Label: "C7 -- Delivery card", ID: detailUnshippedFeat.String(), Kind: "Feature"}},
	}
}

// ---------------------------------------------------------------------------
// 1. the two tables and their four columns
// ---------------------------------------------------------------------------

// TestDeliveryCardShowsBothTablesWithTheFourFRColumns is the FR's central
// claim, read off the parsed markup: a shipped table and an unshipped table,
// each headed Item, Kind, Shipment, Id in that order.
func TestDeliveryCardShowsBothTablesWithTheFourFRColumns(t *testing.T) {
	_, region := deliveryCardOf(t, detailCardMux(t, nil), detailPartialID)

	require.NotEmpty(t, region, "the detail page must render its own region, or this case is vacuous")
	require.Contains(t, region, `data-krill="milestone-delivery-card"`,
		"the Delivery card belongs inside the detail page's own region")

	for _, state := range []string{"shipped", "unshipped"} {
		tbl := deliveryTableFor(t, region, state)

		assert.Equal(t, []string{"Item", "Kind", "Shipment", "Id"},
			deliveryHeaderCells(t, tbl),
			"the %s table carries the FR's four columns, in order", state)
	}
}

// TestDeliveryCardRowsCarryItemKindShipmentAndId: the four columns have to
// hold the FR's four things for a row to mean anything. A table with the
// right headers and empty cells would pass the case above.
func TestDeliveryCardRowsCarryItemKindShipmentAndId(t *testing.T) {
	_, region := deliveryCardOf(t, detailCardMux(t, nil), detailPartialID)

	shipped := deliveryTableFor(t, region, "shipped")
	rows := deliveryBodyRows(t, shipped)
	require.Len(t, rows, 2, "the shipped bucket lists both entities the read returned")

	// Item is the display number and the name, which for a Feature is Cn and
	// for a Requirement is its FR/NFR kind -- never a bare name.
	assert.Equal(t, []string{"C4 -- Milestones table", "Feature", "shipped", detailShippedFeature.String()},
		deliveryRowTexts(t, rows[0]))
	// A Requirement's Item carries "FR", and its Kind column says
	// "Requirement": the two are different facts and the FR asks for both.
	assert.Equal(t, []string{"FR -- Milestone detail header", "Requirement", "shipped", detailShippedFR.String()},
		deliveryRowTexts(t, rows[1]))

	unshipped := deliveryTableFor(t, region, "unshipped")
	rows = deliveryBodyRows(t, unshipped)
	require.Len(t, rows, 2)
	assert.Equal(t, []string{"C7 -- Delivery card", "Feature", "unshipped", detailUnshippedFeat.String()},
		deliveryRowTexts(t, rows[0]))
	// An NFR says NFR in its Item and Requirement in its Kind, which is
	// exactly the pair a single-label flattening would have collapsed.
	assert.Equal(t, []string{"NFR -- Delivery card is readable", "Requirement", "unshipped", detailUnshippedNFR.String()},
		deliveryRowTexts(t, rows[1]))
}

// TestDeliveryCardTablesAreSeparate is the split the FR is named for. The
// shipped table must not list an unshipped entity, or the two are one list
// with a heading on it.
func TestDeliveryCardTablesAreSeparate(t *testing.T) {
	_, region := deliveryCardOf(t, detailCardMux(t, nil), detailPartialID)

	shipped := deliveryTableFor(t, region, "shipped")
	unshipped := deliveryTableFor(t, region, "unshipped")

	assert.NotContains(t, deliveryRowsText(t, shipped), detailUnshippedFeat.String(),
		"the unshipped feature is not in the shipped table")
	assert.NotContains(t, deliveryRowsText(t, unshipped), detailShippedFeature.String(),
		"the shipped feature is not in the unshipped table")

	assert.Contains(t, deliveryRowsText(t, shipped), detailShippedFeature.String())
	assert.Contains(t, deliveryRowsText(t, unshipped), detailUnshippedFeat.String())
}

// TestDeliveryCardAppliesToAMilepebbleToo: a milepebble is its own
// milestone_ref row and answers at the same URL, so it gets the same card
// with the same read behind it -- not a parent milestone's scope.
func TestDeliveryCardAppliesToAMilepebbleToo(t *testing.T) {
	_, region := deliveryCardOf(t, detailCardMux(t, nil), detailPartialPeb)

	require.Contains(t, region, `data-krill-container-kind="milepebble"`)
	assert.Contains(t, region, "C7 -- Delivery card",
		"the milepebble's own delivered scope, not its parent's")
}

// TestDeliveryCardReadsOnlyItsOwnContainer: the card is about the container
// in the URL. A read for the host milestone's milepebble would put another
// container's scope on this page.
func TestDeliveryCardReadsOnlyItsOwnContainer(t *testing.T) {
	reader := &fakeSpecReader{listing: detailCardListing, breakdown: detailCardBreakdowns}
	mux := detailMuxWithReader(t, []store.Product{milestoneDetailProduct}, reader)

	_, region := deliveryCardOf(t, mux, detailPartialPeb)

	assert.Equal(t, []uuid.UUID{detailPartialPeb}, reader.queried,
		"exactly one breakdown read, for the container the URL named")
	assert.Contains(t, region, detailUnshippedFeat.String(),
		"and the page shows that container's own rows")
	assert.NotContains(t, region, detailShippedFeature.String()+"</td>",
		"its host milestone's own rows are not on this page")
}

// ---------------------------------------------------------------------------
// 2. the no-shipment-record sentence
// ---------------------------------------------------------------------------

// TestDeliveryCardLabelsTheUnshippedBucket: the FR's exact sentence, over
// the unshipped table. It is asserted as the shared delivery page's own
// rendering of the same constant, so a second copy of the words cannot
// quietly appear here.
func TestDeliveryCardLabelsTheUnshippedBucket(t *testing.T) {
	_, region := deliveryCardOf(t, detailCardMux(t, nil), detailPartialID)

	shared := sharedUnshippedLabel(t)
	require.NotEmpty(t, shared, "the delivery page must render the label, or parity proves nothing")

	assert.Equal(t, 1, strings.Count(region, shared),
		"the sentence appears once, over the unshipped bucket, and is the shared one")

	// And it is over the UNSHIPPED table, not floating above the shipped one.
	label := indexOfMarker(t, region, `data-krill="milestone-delivery-unshipped-label"`)
	table := indexOfMarker(t, region, `data-krill="milestone-delivery-unshipped-table"`)
	shippedTable := indexOfMarker(t, region, `data-krill="milestone-delivery-shipped-table"`)
	assert.Less(t, shippedTable, label, "the sentence belongs to the unshipped bucket")
	assert.Less(t, label, table)
}

// TestDeliveryCardDoesNotLabelTheShippedBucket: a shipped item HAS a
// shipment record, so saying otherwise of it would invert the FR's rule.
func TestDeliveryCardDoesNotLabelTheShippedBucket(t *testing.T) {
	_, region := deliveryCardOf(t, detailCardMux(t, nil), detailPartialID)

	shipped := deliveryTableFor(t, region, "shipped")
	assert.NotContains(t, deliveryRowsText(t, shipped), "no shipment record")
}

// TestDeliveryCardEmptyBucketsSaySo: an empty bucket says so in words
// rather than rendering an empty table, which would read as a read that
// returned nothing to compare.
func TestDeliveryCardEmptyBucketsSaySo(t *testing.T) {
	html := cardHTML(t, &pages.DeliveryBreakdown{})

	assert.Contains(t, html, "Nothing shipped yet.")
	assert.Contains(t, html, "Nothing unshipped.")
	assert.Nil(t, deliveryTableNode(t, html, "shipped"),
		"an empty bucket renders no table, only its sentence")
}

// TestDeliveryCardWithNoBreakdownRendersNoCard: a page with nothing to show
// gets no box saying so.
func TestDeliveryCardWithNoBreakdownRendersNoCard(t *testing.T) {
	assert.Empty(t, strings.TrimSpace(cardHTML(t, nil)))
}

// ---------------------------------------------------------------------------
// 3. the disagreement warning
// ---------------------------------------------------------------------------

// TestDeliveryCardWarnsWhenPartiallyCompleteAndNothingOutstanding is the
// FR's warning case: the badge says partially complete, the unshipped list
// is empty, and the page says so ABOVE the tables rather than letting the
// empty list read as "all done".
func TestDeliveryCardWarnsWhenPartiallyCompleteAndNothingOutstanding(t *testing.T) {
	_, region := deliveryCardOf(t, detailCardMux(t, nil), detailShippedID)

	// detailShippedID is SHIPPED with nothing outstanding: the normal end
	// state, and the control for the case below.
	assert.NotContains(t, region, "milestone-delivery-disagreement",
		"a shipped container with nothing outstanding is not a disagreement")

	html := cardHTML(t, &pages.DeliveryBreakdown{
		Shipped:         splitBreakdown().Shipped,
		StatusDisagrees: true,
	})
	require.Contains(t, html, `data-krill="milestone-delivery-disagreement"`)
	assert.Contains(t, html, "alert-warning", "it is the existing warning alert")

	// Above the tables.
	warn := indexOfMarker(t, html, `data-krill="milestone-delivery-disagreement"`)
	table := indexOfMarker(t, html, `data-krill="milestone-delivery-shipped-table"`)
	assert.Less(t, warn, table, "the warning is read before the tables it contradicts")

	// And it is the SAME alert the roadmap renders, not a second wording.
	assert.Contains(t, html, sharedDisagreementText(t))
}

// TestDeliveryCardDisagreementIsDrivenByTheStatusNotJustTheList is the rule
// itself, checked on the function both surfaces share: an empty unshipped
// list alone is not a disagreement, and the same empty list under a
// partially-complete status is.
func TestDeliveryCardDisagreementIsDrivenByTheStatusNotJustTheList(t *testing.T) {
	empty := slice.Document{}

	assert.True(t,
		deliveryBreakdownOf(store.MilestoneStatusPartiallyComplete, detailShippedDoc(), empty).StatusDisagrees,
		"partially complete with nothing outstanding is the disagreement")
	for _, status := range []store.MilestoneStatus{
		store.MilestoneStatusShipped,
		store.MilestoneStatusAbandoned,
		store.MilestoneStatusPlanned,
		store.MilestoneStatusNotStarted,
	} {
		assert.False(t,
			deliveryBreakdownOf(status, detailShippedDoc(), empty).StatusDisagrees,
			"%q with nothing outstanding is the normal end state, not a disagreement", status)
	}
	// Something outstanding under the same badge is not a disagreement either.
	assert.False(t,
		deliveryBreakdownOf(store.MilestoneStatusPartiallyComplete,
			detailShippedDoc(), detailUnshippedDoc()).StatusDisagrees)
}

// TestDeliveryCardWarningAndErrorNeverShareABranch: the failed read and the
// disagreeing status are different facts, and rendering both at once would
// tell the operator the tables below are good.
func TestDeliveryCardWarningAndErrorNeverShareABranch(t *testing.T) {
	html := cardHTML(t, &pages.DeliveryBreakdown{
		Error:           "This container's delivered scope could not be read. See the logs.",
		StatusDisagrees: true,
	})

	assert.Contains(t, html, "alert-error")
	assert.NotContains(t, html, `data-krill="milestone-delivery-disagreement"`)
	assert.Nil(t, deliveryTableNode(t, html, "shipped"),
		"a failed read shows no tables, which would read as an empty answer")
}

// ---------------------------------------------------------------------------
// 4. the Id copy chip
// ---------------------------------------------------------------------------

// TestDeliveryCardIdIsTheSharedCopyChip: the chip is the task detail's own
// control, bound by the head script from its data-krill hook -- never an
// inline handler, and never a second clipboard implementation.
func TestDeliveryCardIdIsTheSharedCopyChip(t *testing.T) {
	_, region := deliveryCardOf(t, detailCardMux(t, nil), detailPartialID)

	assert.Contains(t, region, `data-krill="copy-task-id" data-task-id="`+detailShippedFeature.String()+`"`)
	assert.Contains(t, region, `data-krill="copy-task-id" data-task-id="`+detailUnshippedNFR.String()+`"`)
	assert.Contains(t, region, `aria-label="Copy Requirement id"`)
	assert.Contains(t, region, `aria-label="Copy Feature id"`)

	for _, forbidden := range []string{"onclick", "navigator.clipboard", "hx-post"} {
		assert.NotContains(t, region, forbidden,
			"the chip's behaviour is bound from the head script, not from the markup")
	}
}

// TestDeliveryCardCopyChipIsLiveOnlyBecauseTheHeadSaysSo: the chip ships
// disabled and is upgraded by copyTaskIdScript, exactly as the task
// detail's is -- a control that cannot work must not look live.
func TestDeliveryCardCopyChipIsLiveOnlyBecauseTheHeadSaysSo(t *testing.T) {
	_, region := deliveryCardOf(t, detailCardMux(t, nil), detailPartialID)

	assert.Contains(t, region, `data-krill="copy-task-id"`)
	assert.Contains(t, region, "disabled")
	// The confirmation the script writes needs a live region in the chip's
	// own <dd>; without one the copy works and confirms nothing.
	assert.Contains(t, region, `<dd class="ml-0">`)
	assert.Contains(t, region, `data-krill="copy-task-id-status" role="status" aria-live="polite"`)
	assert.Equal(t, strings.Count(region, `data-krill="copy-task-id"`),
		strings.Count(region, `data-krill="copy-task-id-status"`),
		"every chip has the live region the script announces into")
}

// ---------------------------------------------------------------------------
// 5. the Shipment badge's colour
// ---------------------------------------------------------------------------

// TestShipmentBadgeResolvesToTheSharedStyle: a green suite is not proof the
// badge resolves. "shipped" must be the SAME tuple the milestone status
// badge uses for the word "shipped", and "unshipped" the ghost treatment --
// asserted against components, not against a string that happens to appear.
func TestShipmentBadgeResolvesToTheSharedStyle(t *testing.T) {
	assert.Equal(t, components.MilestoneStatusStyle("shipped"), components.ShipmentStyle("shipped"),
		"one word, one colour, on every page")
	assert.Equal(t, components.StatusStyle{Variant: "badge-ghost", Size: "badge-sm"}, components.ShipmentStyle("unshipped"),
		"no shipment record is the absence of one, not a failure")
	assert.Equal(t, components.StatusStyle{Variant: "badge-neutral", Size: "badge-sm"}, components.ShipmentStyle("something else"))

	// And the classes that tuple emits are what the card actually renders.
	html := cardHTML(t, splitBreakdown())
	deliveryTableFor(t, html, "shipped")
	deliveryTableFor(t, html, "unshipped")
	assert.Contains(t, deliveryTableHTML(t, html, "shipped"), `class="badge badge-success badge-sm"`)
	assert.Contains(t, deliveryTableHTML(t, html, "unshipped"), `class="badge badge-ghost badge-sm"`)
}

// TestDeliveryCardRendersShipmentAsABadge: an enum is a badge, per the
// design floor. Plain words in a cell would read as prose.
func TestDeliveryCardRendersShipmentAsABadge(t *testing.T) {
	html := cardHTML(t, splitBreakdown())

	for _, state := range []string{"shipped", "unshipped"} {
		assert.Contains(t, deliveryTableHTML(t, html, state), `class="badge badge-`,
			"the %s cell renders through the shared badge primitive", state)
	}
}

// ---------------------------------------------------------------------------
// 6. failure and boundary reads through the real route
// ---------------------------------------------------------------------------

// TestDeliveryCardUnreadableBreakdownIsAnInlineErrorNotA500: one unreadable
// container degrades to its own card saying so. The page still answers 200
// with the name, the status and both work links, because a breakdown read
// failing says nothing about the container existing.
func TestDeliveryCardUnreadableBreakdownIsAnInlineErrorNotA500(t *testing.T) {
	mux := detailCardMux(t, map[uuid.UUID]error{detailPartialID: assert.AnError})

	rec := fetch(t, mux, milestoneDetailHref(milestoneDetailProductID, detailPartialID))
	require.Equal(t, http.StatusOK, rec.Code)

	region := productPageRegion(rec.Body.String())
	assert.Contains(t, region, "M8 Detail cards", "the header still renders")
	assert.Contains(t, region, ">Open tasks<")
	assert.Contains(t, region, "delivered scope could not be read. See the logs.",
		"the card names its own read failure (the sentence is HTML-escaped on the wire)")
	assert.Contains(t, region, "alert-error")
}

// TestDeliveryCardIsInsideTheContentCellNotTheRail: the frame has two
// cells, and the sibling task owns the right one. A card that landed in the
// rail would render at 18rem wide under a two-column layout.
func TestDeliveryCardIsInsideTheContentCellNotTheRail(t *testing.T) {
	_, region := deliveryCardOf(t, detailCardMux(t, nil), detailPartialID)

	main := indexOfMarker(t, region, `data-krill="milestone-detail-main"`)
	card := indexOfMarker(t, region, `data-krill="milestone-delivery-card"`)
	rail := indexOfMarker(t, region, `data-krill="milestone-detail-rail"`)

	require.GreaterOrEqual(t, main, 0, "the content cell is still declared")
	require.GreaterOrEqual(t, card, 0)
	require.GreaterOrEqual(t, rail, 0)
	assert.Greater(t, card, main, "the card renders inside the content cell")
	assert.Less(t, card, rail, "and not in the properties rail")
}

// TestDeliveryCardDoesNotNestASection: productPageRegion slices to the
// first </section> after the region marker, so a card that opened its own
// <section> would truncate every product-scope assertion in the package
// that lands after it.
func TestDeliveryCardDoesNotNestASection(t *testing.T) {
	html := cardHTML(t, splitBreakdown())

	assert.NotContains(t, html, "<section",
		"productPageRegion slices to the first </section> after its marker, so a card that "+
			"opened its own <section> would truncate every product-scope assertion after it")
	assert.Contains(t, html, "milestone-delivery-card")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// deliveryTableFor returns the <table> a bucket renders, failing the case if
// the bucket has no table. A nil result is never returned quietly: a case
// that wanted a table must say so loudly rather than assert against "".
func deliveryTableFor(t *testing.T, htmlStr, state string) *html.Node {
	t.Helper()
	tbl := deliveryTableNode(t, htmlStr, state)
	require.NotNil(t, tbl, "the %s bucket must render a table", state)
	return tbl
}

// deliveryTableNode finds the table carrying this bucket's data-krill hook.
func deliveryTableNode(t *testing.T, htmlStr, state string) *html.Node {
	t.Helper()
	nodes := findAll(htmlStr, func(n *html.Node) bool {
		return n.Data == "table" && attr(n, "data-krill") == "milestone-delivery-"+state+"-table"
	})
	if len(nodes) == 0 {
		return nil
	}
	return nodes[0]
}

// deliveryTableHTML is one bucket's table as MARKUP, not as text. A badge's
// colour class is an attribute, so a case about which badge variant resolved
// has to read the markup -- textOf would hand it a page with no classes on
// it at all.
func deliveryTableHTML(t *testing.T, htmlStr, state string) string {
	t.Helper()
	start := indexOfMarker(t, htmlStr, `data-krill="milestone-delivery-`+state+`-table"`)
	rest := htmlStr[start:]
	end := strings.Index(rest, "</table>")
	require.Greater(t, end, 0, "the %s table must be closed", state)
	return rest[:end]
}

// deliveryHeaderCells is a table's <thead> cells, as the operator reads them.
func deliveryHeaderCells(t *testing.T, tbl *html.Node) []string {
	t.Helper()
	var head *html.Node
	for _, n := range findAllIn(tbl, func(n *html.Node) bool { return n.Data == "thead" }) {
		head = n
		break
	}
	require.NotNil(t, head, "a table with no header row has no columns to check")
	return cellTexts(head, "th")
}

// deliveryBodyRows is a table's <tbody> rows.
func deliveryBodyRows(t *testing.T, tbl *html.Node) []*html.Node {
	t.Helper()
	var body *html.Node
	for _, n := range findAllIn(tbl, func(n *html.Node) bool { return n.Data == "tbody" }) {
		body = n
		break
	}
	require.NotNil(t, body, "a table with no body has no rows to check")
	return findAllIn(body, func(n *html.Node) bool { return n.Data == "tr" })
}

// deliveryRowTexts is one row's cell texts, whitespace-collapsed so the
// assertion reads the cell rather than the indentation around it.
func deliveryRowTexts(t *testing.T, row *html.Node) []string {
	t.Helper()
	return cellTexts(row, "td")
}

// deliveryRowsText is every cell of every body row, as one string: for a
// "this id is not in that table" check, where the point is membership
// rather than position.
func deliveryRowsText(t *testing.T, tbl *html.Node) string {
	t.Helper()
	return textOf(tbl)
}

// cellTexts is one row's cells, read through the package's own collapsed
// textOf so a cell's value is the same string here as in the swap tests.
func cellTexts(row *html.Node, cell string) []string {
	var out []string
	for _, n := range findAllIn(row, func(n *html.Node) bool { return n.Data == cell }) {
		out = append(out, textOf(n))
	}
	return out
}

// indexOfMarker is the offset of a literal in the rendered markup, failing
// rather than returning -1 for a caller that is about to compare offsets.
func indexOfMarker(t *testing.T, body, marker string) int {
	t.Helper()
	i := strings.Index(body, marker)
	require.GreaterOrEqual(t, i, 0, "the marker %s must be in the markup", marker)
	return i
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func findAll(htmlStr string, pred func(*html.Node) bool) []*html.Node {
	node, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return nil
	}
	return findAllIn(node, pred)
}

func findAllIn(n *html.Node, pred func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(cur *html.Node) {
		if pred(cur) {
			out = append(out, cur)
		}
		for c := cur.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// sharedUnshippedLabel renders the roadmap's own unshipped bucket and hands
// back the sentence it shows. The card asserts against THAT rather than a
// literal, so a second copy of the words cannot be introduced beside it.
func sharedUnshippedLabel(t *testing.T) string {
	t.Helper()
	html := mustRenderComponent(pages.Delivery(pages.DeliveryPage{
		Product: pages.ProductHeader{Name: "krill"},
		Milestones: []pages.DeliveryMilestone{{
			ID:     detailPartialID.String(),
			Name:   "M8 Detail cards",
			Status: string(store.MilestoneStatusPartiallyComplete),
			Breakdown: &pages.DeliveryBreakdown{
				Unshipped: []pages.DeliveryEntity{{Label: "C7 -- Delivery card", ID: detailUnshippedFeat.String(), Kind: "Feature"}},
			},
		}},
	}))
	i := strings.Index(html, "Delivered to this container")
	if i < 0 {
		return ""
	}
	rest := html[i:]
	end := strings.Index(rest, "</p>")
	if end < 0 {
		return ""
	}
	return strings.Join(strings.Fields(rest[:end]), " ")
}

// sharedDisagreementText is the roadmap's own warning sentence, read out of
// a rendered disagreeing container.
func sharedDisagreementText(t *testing.T) string {
	t.Helper()
	html := mustRenderComponent(pages.Delivery(pages.DeliveryPage{
		Product: pages.ProductHeader{Name: "krill"},
		Milestones: []pages.DeliveryMilestone{{
			ID:     detailPartialID.String(),
			Name:   "M8 Detail cards",
			Status: string(store.MilestoneStatusPartiallyComplete),
			Breakdown: &pages.DeliveryBreakdown{
				StatusDisagrees: true,
			},
		}},
	}))
	i := strings.Index(html, "Status and breakdown disagree")
	if i < 0 {
		return ""
	}
	rest := html[i:]
	end := strings.Index(rest, "</span>")
	if end < 0 {
		return ""
	}
	return strings.Join(strings.Fields(rest[:end]), " ")
}
