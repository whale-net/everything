// Coverage for the Milestone detail header (FR ef0a0ded): the breadcrumb,
// the h1 with its status badge, the two scoped links, and the in-shell
// 404 an id this product does not own gets. No database.
//
// The expected URLs are spelled as literals rather than re-derived by
// calling the href builder the page itself calls: a builder that changed
// both the page and this file's expectation would still pass, and the
// FR's whole claim about these two links is what they point at.
package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// milestoneDetailProductID is the product every fixture below is scoped
// to. A literal, so a failure names the same ids every run and the
// expected paths can be written out and read.
var milestoneDetailProductID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

// milestoneDetailContainerID / pebbleID are the two containers of the one
// listing every case below reads: an uncut milestone and, under a second
// one, a milepebble.
var (
	milestoneDetailContainerID = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	milestoneDetailPebbleID     = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	milestoneDetailParentID     = uuid.MustParse("44444444-4444-4444-4444-444444444444")
)

// milestoneDetailProduct is the product the URLs above are scoped to.
var milestoneDetailProduct = store.Product{ID: milestoneDetailProductID, Name: "krill"}

// otherDetailProductID is a second product in scope, so a case can name
// a container that belongs to one product and request it under another.
var otherDetailProductID = uuid.MustParse("66666666-6666-6666-6666-666666666666")

// milestoneDetailListing is the delivery read's answer for that product:
// a milestone the operator can reach, and a cut milestone whose milepebble
// answers at the same URL.
//
// The two are here as separate cases precisely because they are one case:
// {mid} is a single wildcard, so a handler that resolved a milepebble as
// a milestone would still render this page -- it would just render the
// wrong name, the wrong status and two work links scoped to the parent.
var milestoneDetailListing = slice.DeliveryListing{
	Milestones: []slice.MilestoneListingEntry{
		{
			ID:       milestoneDetailContainerID,
			Name:     "M6 UI facelift",
			Status:   store.MilestoneStatusInProgress,
			Position: 3,
		},
		{
			ID:       milestoneDetailParentID,
			Name:     "M7 Console reads",
			Status:   store.MilestoneStatusInDesign,
			Position: 2,
			Milepebbles: []slice.MilepebbleListingEntry{
				{ID: milestoneDetailPebbleID, Name: "P1 Workspace shell", Status: store.MilestoneStatusShipped},
			},
		},
	},
}

// milestoneDetailMux mounts the real product-scoped routes against the
// listing above, so these cases drive the same handler, the same route
// registration and the same shell chrome production serves -- the 404
// cases in particular are only meaningful against the route that is
// actually registered for /milestones/{mid}.
func milestoneDetailMux(t *testing.T, listing slice.DeliveryListing, listingErr error) *http.ServeMux {
	t.Helper()
	return detailMuxWithReader(t, []store.Product{milestoneDetailProduct},
		&fakeSpecReader{listing: listing, listingErr: listingErr})
}

// detailMuxWithReader is the same mount against an arbitrary spec reader
// and product set, for the cases whose subject is WHICH product the reader
// answers for rather than what a single product's listing holds.
func detailMuxWithReader(t *testing.T, products []store.Product, reader specReadClient) *http.ServeMux {
	t.Helper()
	app := newTestApp(t)
	app.spec = scopedProductsReader{specReadClient: reader, products: products}
	app.scopes = productScopeScopes{scope: store.Scope{ID: chromeScopeID}}
	app.tasks = productScopeTasks{}
	app.designSessions = navStubDesignSessions{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// detailOf renders the page for one container and returns its markup,
// which is the shape the handler serves.
func detailOf(t *testing.T, product store.Product, c taskContainer) string {
	t.Helper()
	return mustRenderComponent(pages.MilestoneDetail(buildMilestoneDetailPage(product, c)))
}

// containerOf resolves one id out of the listing the way the handler does,
// so a case can name the container it expects without restating it.
func containerOf(t *testing.T, id uuid.UUID) taskContainer {
	t.Helper()
	c, found := resolveTaskContainer(milestoneDetailListing, id)
	require.True(t, found, "fixture id %s must resolve", id)
	return c
}

// ---------------------------------------------------------------------------
// 1. the view model
// ---------------------------------------------------------------------------

// TestMilestoneDetailPageNamesTheContainerAndLinksToItsWork is the FR's
// header claim, checked on the view model: the name is the title, the
// status is the container's own, and both links are the product-wide
// Tasks and Board scoped to THIS container.
func TestMilestoneDetailPageNamesTheContainerAndLinksToItsWork(t *testing.T) {
	page := buildMilestoneDetailPage(milestoneDetailProduct, containerOf(t, milestoneDetailContainerID))

	assert.Equal(t, "M6 UI facelift", page.Title)
	assert.Equal(t, string(store.MilestoneStatusInProgress), page.Status)
	assert.Equal(t, milestoneDetailContainerID.String(), page.ID)
	assert.Equal(t, string(store.MilestoneKindMilestone), page.Kind)

	// The two links, spelled out. The container travels in the query --
	// scope=milestone and container_id={id} -- and the product in the
	// path, so both views arrive with the scope already preselected.
	assert.Equal(t,
		"/products/"+milestoneDetailProductID.String()+"/tasks?container_id="+
			milestoneDetailContainerID.String()+"&scope=milestone",
		page.TasksPath, "Open tasks lands on the Tasks table scoped to this container")
	assert.Equal(t,
		"/products/"+milestoneDetailProductID.String()+"/board?container_id="+
			milestoneDetailContainerID.String()+"&scope=milestone",
		page.BoardPath, "Board lands on the Board scoped to the same container")
}

// TestMilestoneDetailPageIsTheSameForAMilepebble: a milepebble answers at
// the same URL, so it gets the same header -- and, crucially, the same
// KIND in its links. A milepebble scoped as a milestone would be refused
// by the page it links to (product_task_scope.go checks the kind), so the
// wrong mode is a dead link rather than a visible mistake.
func TestMilestoneDetailPageIsTheSameForAMilepebble(t *testing.T) {
	c := containerOf(t, milestoneDetailPebbleID)
	require.Equal(t, string(store.MilestoneKindMilepebble), c.Kind,
		"the fixture must resolve as a milepebble, or this case proves nothing")

	page := buildMilestoneDetailPage(milestoneDetailProduct, c)

	assert.Equal(t, "P1 Workspace shell", page.Title)
	assert.Equal(t, string(store.MilestoneStatusShipped), page.Status)
	assert.Equal(t, milestoneDetailPebbleID.String(), page.ID)
	assert.Equal(t, string(store.MilestoneKindMilepebble), page.Kind)

	assert.Equal(t,
		"/products/"+milestoneDetailProductID.String()+"/tasks?container_id="+
			milestoneDetailPebbleID.String()+"&scope=milepebble",
		page.TasksPath, "the milepebble's own scope, not its parent's")
	assert.Equal(t,
		"/products/"+milestoneDetailProductID.String()+"/board?container_id="+
			milestoneDetailPebbleID.String()+"&scope=milepebble",
		page.BoardPath)
}

// TestAMilepebbleDetailPageRendersBothOfItsOwnWorkLinks is the rendered
// half of the case above. That one reads the view model, so it cannot
// tell a page that renders both links from a page that holds them and
// drops them -- the field being set is precisely what makes a link
// look finished.
//
// It is rendered through the real registrations rather than the builder,
// because the milepebble and its parent answer at the SAME URL: {mid} is
// one wildcard, so a handler that resolved the cut as the milestone
// renders this page happily, under the right path, with the parent's
// name -- and two links scoped to the parent. The parent milestone's id
// is asserted absent from the anchors for that reason.
func TestAMilepebbleDetailPageRendersBothOfItsOwnWorkLinks(t *testing.T) {
	mux := milestoneDetailMux(t, milestoneDetailListing, nil)
	body := fetch(t, mux, milestoneDetailHref(milestoneDetailProductID, milestoneDetailPebbleID)).Body.String()

	for _, view := range []struct{ suffix, label string }{
		{tasksSuffix, `data-krill="milestone-open-tasks"`},
		{boardSuffix, `data-krill="milestone-board-link"`},
	} {
		assert.Contains(t, body, view.label,
			"a milepebble's page offers the %s view of its own cut", view.suffix)
		assert.Contains(t, body, `href="/products/`+milestoneDetailProductID.String()+
			view.suffix+`?container_id=`+milestoneDetailPebbleID.String()+`&amp;scope=milepebble"`,
			"and it points at THIS milepebble's %s, not its parent's", view.suffix)
	}
	assert.NotContains(t, body, `container_id=`+milestoneDetailParentID.String()+`&amp;scope=`,
		"a milepebble's work links never carry the parent milestone's id")
}

// TestMilestoneDetailCrumbsWalkProductMilestonesName is the FR's
// breadcrumb: product, Milestones, the container's own name.
//
// The name carries no href because the operator is already on it, and the
// Milestones crumb points at the table rather than at the pre-redesign
// delivery page.
func TestMilestoneDetailCrumbsWalkProductMilestonesName(t *testing.T) {
	page := buildMilestoneDetailPage(milestoneDetailProduct, containerOf(t, milestoneDetailContainerID))

	require.Len(t, page.Crumbs, 3)
	assert.Equal(t, "krill", page.Crumbs[0].Label)
	assert.Equal(t, productPath(milestoneDetailProductID), page.Crumbs[0].Href)
	assert.Equal(t, "Milestones", page.Crumbs[1].Label)
	assert.Equal(t, "/products/"+milestoneDetailProductID.String()+"/milestones", page.Crumbs[1].Href)
	assert.Equal(t, "M6 UI facelift", page.Crumbs[2].Label)
	assert.Empty(t, page.Crumbs[2].Href, "the page the operator is already on is not a link")
}

// TestMilestoneDetailCrumbsForAMilepebbleStopAtItsOwnName: the FR names
// three levels, and a milepebble's parent is not one of them. A fourth
// crumb naming the parent would put a milestone between the operator and
// the milepebble they clicked -- which is what the parent-level detail
// task's own milepebbles card is for.
func TestMilestoneDetailCrumbsForAMilepebbleStopAtItsOwnName(t *testing.T) {
	page := buildMilestoneDetailPage(milestoneDetailProduct, containerOf(t, milestoneDetailPebbleID))

	require.Len(t, page.Crumbs, 3)
	assert.Equal(t, "P1 Workspace shell", page.Crumbs[2].Label)
	for _, crumb := range page.Crumbs[:2] {
		assert.NotContains(t, crumb.Label, "M7 Console reads",
			"a milepebble's parent is not a level of this trail")
	}
}

// ---------------------------------------------------------------------------
// 2. rendered markup
// ---------------------------------------------------------------------------

// TestMilestoneDetailRendersTheHeaderTheFRNames checks the markup rather
// than the view model, because a field can sit on the struct and never
// reach the page -- and the header is the one part of this page that ships
// in this task.
func TestMilestoneDetailRendersTheHeaderTheFRNames(t *testing.T) {
	html := detailOf(t, milestoneDetailProduct, containerOf(t, milestoneDetailContainerID))

	// The h1 is the name, and the status rides beside it as a badge --
	// through the shared statusBadge, so this status is this colour here,
	// on the Milestones table and on the delivery page alike.
	assert.Contains(t, html, `data-krill="page-title"`)
	assert.Contains(t, html, "M6 UI facelift")
	assert.Contains(t, html, "badge", "the status renders as a badge")
	assert.Contains(t, html, "in progress")

	// The breadcrumb, with the Milestones level linking to the table.
	assert.Contains(t, html, `data-krill="milestone-breadcrumb"`)
	assert.Contains(t, html, `href="/products/`+milestoneDetailProductID.String()+`/milestones"`)
	assert.Contains(t, html, ">Milestones<")

	// Both work links, at the URLs spelled above.
	assert.Contains(t, html, `href="/products/`+milestoneDetailProductID.String()+
		`/tasks?container_id=`+milestoneDetailContainerID.String()+`&amp;scope=milestone"`)
	assert.Contains(t, html, `href="/products/`+milestoneDetailProductID.String()+
		`/board?container_id=`+milestoneDetailContainerID.String()+`&amp;scope=milestone"`)
	assert.Contains(t, html, ">Open tasks<")
	assert.Contains(t, html, ">Board<")
}

// TestMilestoneDetailMarksOnePrimaryAction: "Open tasks" is the page's
// one primary action and "Board" is not a second one. Two primary buttons
// on a header is how a reader cannot tell which one the page is for.
func TestMilestoneDetailMarksOnePrimaryAction(t *testing.T) {
	html := detailOf(t, milestoneDetailProduct, containerOf(t, milestoneDetailContainerID))

	assert.Contains(t, html, `class="btn btn-primary btn-sm" data-krill="milestone-open-tasks"`)
	assert.Contains(t, html, `class="btn btn-ghost btn-sm" data-krill="milestone-board-link"`)
	assert.Equal(t, 1, strings.Count(html, "btn-primary"),
		"exactly one primary action on the header")
}

// TestMilestoneDetailRegionCarriesItsContainer proves the milepebble and
// the milestone are the same page with different facts: the region names
// which kind it resolved, so a later section (the milepebbles card, the
// delivery tables) can branch without re-resolving the id.
func TestMilestoneDetailRegionCarriesItsContainer(t *testing.T) {
	for _, id := range []uuid.UUID{milestoneDetailContainerID, milestoneDetailPebbleID} {
		html := detailOf(t, milestoneDetailProduct, containerOf(t, id))
		assert.Contains(t, html, `id="`+pages.MilestoneDetailAnchor+`"`)
		assert.Contains(t, html, `data-krill-container-id="`+id.String()+`"`)
		assert.Contains(t, html, `data-krill-container-kind="`+containerOf(t, id).Kind+`"`)
	}
}

// ---------------------------------------------------------------------------
// 3. the handler, through the registered route
// ---------------------------------------------------------------------------

// TestMilestoneDetailRouteServesAMilestone: the URL the Milestones table's
// names and the Overview's in-flight rows already link to answers 200 and
// renders this container's header.
func TestMilestoneDetailRouteServesAMilestone(t *testing.T) {
	mux := milestoneDetailMux(t, milestoneDetailListing, nil)

	rec := fetch(t, mux, milestoneDetailHref(milestoneDetailProductID, milestoneDetailContainerID))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "<html", "the page renders inside the shell")
	assert.Contains(t, body, "M6 UI facelift")
	assert.Contains(t, body, "in progress")
	assert.Contains(t, body, ">Open tasks<")
	assert.Contains(t, body, ">Board<")
}

// TestMilestoneDetailRouteServesAMilepebbleAtTheSameURL: a milepebble is
// reached the way its parent is, and the page it renders is the same one
// with the milepebble's own name, status and scope.
func TestMilestoneDetailRouteServesAMilepebbleAtTheSameURL(t *testing.T) {
	mux := milestoneDetailMux(t, milestoneDetailListing, nil)

	rec := fetch(t, mux, milestoneDetailHref(milestoneDetailProductID, milestoneDetailPebbleID))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "P1 Workspace shell")
	assert.Contains(t, body, "shipped")
	assert.Contains(t, body, `data-krill-container-kind="milepebble"`)
	assert.Contains(t, body, "container_id="+milestoneDetailPebbleID.String()+"&amp;scope=milepebble",
		"the links carry the milepebble's own scope, not its parent's")
	assert.NotContains(t, body, "container_id="+milestoneDetailParentID.String()+"&amp;",
		"the parent milestone is not the scope this page links to")
}

// TestMilestoneDetailUnknownIDIsAnInShell404 is the FR's second half. An
// id this product does not own must be a 404 the operator can navigate out
// of -- inside the shell, with a way back to the table -- and never
// another product's milestone, and never an empty page that would read as
// "this milestone holds nothing".
func TestMilestoneDetailUnknownIDIsAnInShell404(t *testing.T) {
	mux := milestoneDetailMux(t, milestoneDetailListing, nil)

	rec := fetch(t, mux, milestoneDetailHref(milestoneDetailProductID, uuid.New()))
	require.Equal(t, http.StatusNotFound, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "<html", "the 404 is rendered inside the shell")
	assert.Contains(t, body, "No milestone or milepebble with that id belongs to this product.")
	assert.Contains(t, body, `href="/products/`+milestoneDetailProductID.String()+`/milestones"`,
		"and offers a way back to the table")
	assert.NotContains(t, body, "M6 UI facelift",
		"an id this product does not own renders no container at all")
}

// TestMilestoneDetailMalformedIDIsAnInShell404: the wildcard takes any
// segment, so a hand-edited or truncated URL arrives here too.
func TestMilestoneDetailMalformedIDIsAnInShell404(t *testing.T) {
	mux := milestoneDetailMux(t, milestoneDetailListing, nil)

	rec := fetch(t, mux, "/products/"+milestoneDetailProductID.String()+"/milestones/not-a-uuid")
	require.Equal(t, http.StatusNotFound, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "<html")
	assert.Contains(t, body, "That link does not name a milestone or milepebble.")
}

// TestMilestoneDetailUnreadableListingIsA500: a delivery read that fails
// is a genuine read failure, not a 404 -- the container may well exist,
// and telling an operator otherwise would be a lie about the store.
func TestMilestoneDetailUnreadableListingIsA500(t *testing.T) {
	mux := milestoneDetailMux(t, slice.DeliveryListing{}, assert.AnError)

	rec := fetch(t, mux, milestoneDetailHref(milestoneDetailProductID, milestoneDetailContainerID))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "<html",
		"a read failure is rendered inside the shell too")
}

// TestMilestoneDetailOfAnotherProductIsNotReachableHere: a container is
// resolved out of the URL's OWN product's listing, so an id that belongs
// to a different product is simply not found here. That is what makes the
// in-shell 404 above trustworthy rather than a lookup that would follow
// the id wherever in the deployment it lives.
func TestMilestoneDetailOfAnotherProductIsNotReachableHere(t *testing.T) {
	otherProduct := store.Product{ID: otherDetailProductID, Name: "other product"}
	otherID := uuid.MustParse("55555555-5555-5555-5555-555555555555")

	// The reader answers PER PRODUCT, so this product's listing genuinely
	// does not hold the other product's id. A fake that ignored the
	// product id would hand this product the other product's listing and
	// the case would prove nothing -- the handler would be right for the
	// wrong reason.
	mux := detailMuxWithReader(t,
		[]store.Product{milestoneDetailProduct, otherProduct},
		perProductListing{
			otherProduct.ID: slice.DeliveryListing{
				Milestones: []slice.MilestoneListingEntry{
					{ID: otherID, Name: "M0 Other product's milestone", Status: store.MilestoneStatusShipped},
				},
			},
		})

	// The control: on the product that DOES own the id, the same reader
	// renders it. Without this the case would pass even if the reader
	// answered nothing at all.
	owned := fetch(t, mux, milestoneDetailHref(otherProduct.ID, otherID))
	require.Equal(t, http.StatusOK, owned.Code,
		"the owning product's own id must render, or this case is vacuous")
	assert.Contains(t, owned.Body.String(), "M0 Other product")

	// And from the URL that names the FIRST product, that same id is not
	// reachable: it belongs to another product, so it must not render as
	// that product's milestone.
	rec := fetch(t, mux, milestoneDetailHref(milestoneDetailProductID, otherID))
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotContains(t, rec.Body.String(), "M0 Other product",
		"an id this product does not own renders no container at all")
}

// perProductListing answers Delivery with the listing registered for the
// product being asked about, and an empty one for any other product.
//
// It is the product-scoped counterpart to fakeSpecReader, whose Delivery
// ignores the product id entirely. A handler that resolved a container out
// of the URL's OWN product's listing passes against this reader and fails
// against one that answered every product the same thing.
type perProductListing map[uuid.UUID]slice.DeliveryListing

func (m perProductListing) ProductSlice(context.Context, uuid.UUID) (slice.Document, error) {
	return slice.Document{}, nil
}

func (perProductListing) Personas(context.Context, uuid.UUID) ([]store.Persona, error) {
	return nil, nil
}

func (perProductListing) NonGoals(context.Context, uuid.UUID) ([]store.NonGoal, error) {
	return nil, nil
}

func (m perProductListing) Delivery(_ context.Context, pid uuid.UUID, _ []store.MilestoneStatus) (slice.DeliveryListing, error) {
	return m[pid], nil
}

// Products is here only to satisfy specReadClient. scopedProductsReader
// answers Products from its own product list, so this one is never called.
func (perProductListing) Products(context.Context) ([]store.Product, error) {
	return nil, nil
}

func (perProductListing) DeliveryBreakdown(context.Context, uuid.UUID) (slice.Document, slice.Document, error) {
	return slice.Document{}, slice.Document{}, nil
}

func (perProductListing) Product(_ context.Context, pid uuid.UUID) (store.Product, error) {
	return store.Product{ID: pid}, nil
}

var _ specReadClient = perProductListing{}
