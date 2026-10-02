package main

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// The fakes here are shared by every go_test target in this package: the
// targets do not share sources, so a fake that more than one of them needs
// has to live in a file all of them compile.

// emptyScopeSpecReader is the specReadClient for a test whose App has to
// resolve a product but makes no product reads of its own: it lists none,
// which is the empty-scope branch of product_scope.go.
//
// It is a separate type from the delivery tests' fakeSpecReader because
// that one is only compiled into ui_lib_test, and this one has to reach the
// others too.
type emptyScopeSpecReader struct {
	specReadClient
}

func (emptyScopeSpecReader) Products(context.Context) ([]store.Product, error) { return nil, nil }

// Delivery answers "this product delivers nothing". A page that renders the
// Overview asks, and a fake with no delivery fixture should say the product
// has no containers rather than nil-panic on the embedded nil interface.
func (emptyScopeSpecReader) Delivery(context.Context, uuid.UUID, []store.MilestoneStatus) (slice.DeliveryListing, error) {
	return slice.DeliveryListing{}, nil
}

var _ specReadClient = emptyScopeSpecReader{}

// scopedProductsReader answers Products with a fixed list, so the
// resolver's in-scope decision comes from the test rather than a database,
// and every other read is inherited from whatever specReadClient a caller
// embeds (nil when it embeds none).
type scopedProductsReader struct {
	specReadClient
	products    []store.Product
	productsErr error
}

func (r scopedProductsReader) Products(context.Context) ([]store.Product, error) {
	if r.productsErr != nil {
		return nil, r.productsErr
	}
	return r.products, nil
}

// Delivery delegates to the embedded reader, and answers "no containers"
// when there is none -- an App with products but no delivery fixture is a
// product that has delivered nothing so far, which is what the Overview
// then renders.
func (r scopedProductsReader) Delivery(ctx context.Context, pid uuid.UUID, statuses []store.MilestoneStatus) (slice.DeliveryListing, error) {
	if r.specReadClient == nil {
		return slice.DeliveryListing{}, nil
	}
	return r.specReadClient.Delivery(ctx, pid, statuses)
}

var _ specReadClient = scopedProductsReader{}

// chromeScopeID is the sole scope the chrome's Needs-attention badge is
// read under. Any non-nil id does; it is spelled out so a test asserting
// on the badge has a scope it can name.
var chromeScopeID = uuid.MustParse("11111111-2222-3333-4444-555555555555")

// chromeScopes and chromeTaskCounter are the two stores every full-page
// shell render reads for the sidebar's Needs-attention badge. They live
// here, not beside the fixtures that wire them, because every go_test
// target in this package renders chrome now and the targets do not share
// sources.
//
// Each embeds its interface, so a fixture reaching for any other store
// method still nil-panics rather than passing on a fabricated answer.
type chromeScopes struct{ store.ScopeStore }

func (chromeScopes) GetSole(context.Context) (store.Scope, error) {
	return store.Scope{ID: chromeScopeID}, nil
}

type chromeTaskCounter struct{ store.TaskStore }

func (chromeTaskCounter) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}

// ListEscalatedTasks backs the Overview's Needs-attention panel. These
// fixtures are about the chrome, not the queue, so an empty page is the
// right answer: the panel renders its empty state and the assertion under
// test is still about the sidebar or the switcher.
func (chromeTaskCounter) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, nil
}

// CountConsoleOverview is the Overview stat tiles' read. Every page here
// renders the chrome, and the Overview home renders the tiles too, so this
// fake owes it -- zero figures, which is a real answer for an idle
// deployment rather than a gap that would nil-panic on a nil embed.
func (chromeTaskCounter) CountConsoleOverview(context.Context, store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	return store.ConsoleOverviewCounts{}, nil
}

// SummarizeProductTaskProgress is the read the Overview's in-flight panel
// makes, so every test that renders a full page needs it to answer. It
// answers with no containers, which renders the panel's empty state --
// the chrome tests are indifferent to the panel's contents.
func (chromeTaskCounter) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	return store.ProductTaskProgress{ProductID: params.ProductID, Containers: []store.ContainerTaskProgress{}}, nil
}
// emptyListTasks answers the console's four list views and the milestone
// task pages with no rows, which is what a scope holding a product that has
// no tasks yet renders.
//
// It is a separate type from chromeTaskCounter because that one is the
// badge read and deliberately implements nothing else: a test that only
// renders chrome should not silently acquire fabricated task rows. Here
// the empty answer is the point -- the legacy-URL contract is about a
// page resolving, not about what it lists.
//
// Every method here answers the empty list a genuinely empty queue gives,
// never a fabricated row and never a nil interface: a test reaching for a
// store method this does not model panics rather than passing on a
// fabricated answer. legacy_urls_test.go's TestPreRedesignURLsRenderNoReadFailure
// is the other half of that -- it asserts none of the legacy URLs renders
// an error alert, so an empty-but-honest stub cannot be confused with a
// page that failed to read.
type emptyListTasks struct{ store.TaskStore }

// CountEscalatedTasks is the sidebar badge's read. It is here rather than
// left to the nil embedded interface because every shell page renders the
// chrome, so a page reached through the full route set would panic on the
// badge before reaching its own body.
func (emptyListTasks) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}

func (emptyListTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, nil
}

func (emptyListTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, nil
}

func (emptyListTasks) ListCancelledTasks(context.Context, store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	return store.Page[store.CancelledTaskRow]{}, nil
}

func (emptyListTasks) ListOpenNotes(context.Context, store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	return store.Page[store.OpenNoteRow]{}, nil
}

var _ store.TaskStore = emptyListTasks{}

// emptyDesignStores answer the design-session browser. A legacy-URL test
// walks /design/products/{pid}/design-sessions and /design/design-sessions/{id},
// both of which read these two stores, so without them those URLs would
// nil-panic rather than render.
//
// The session list is empty but the one session detail URL resolves is a
// real one: a detail URL naming an id that belongs to no session is a
// correct 404, so testing URL continuity against one would test the 404
// path and call it a pass. NewDesignSessions seeds exactly that session.
//
// GetByID answers ErrNotFound for every other id rather than fabricating a
// session, so the 404 path stays reachable and this stub cannot be the
// thing making a broken session URL look alive.
type emptyDesignSessions struct {
	store.DesignSessionStore
	session store.DesignSession
}

// SummarizeByProduct answers the Overview's Blocking-questions tile. The
// seeded session holds no open questions, so both figures are zero.
func (d emptyDesignSessions) SummarizeByProduct(_ context.Context, productID uuid.UUID) (store.ProductDesignSessionsSummary, error) {
	return store.ProductDesignSessionsSummary{ProductID: productID}, nil
}

func (d emptyDesignSessions) ListByProduct(context.Context, uuid.UUID) ([]store.DesignSession, error) {
	if d.session.ID == uuid.Nil {
		return nil, nil
	}
	return []store.DesignSession{d.session}, nil
}

func (d emptyDesignSessions) GetByID(_ context.Context, id uuid.UUID) (store.DesignSession, error) {
	if d.session.ID == uuid.Nil || id != d.session.ID {
		return store.DesignSession{}, store.ErrNotFound
	}
	return d.session, nil
}

// NewDesignSessions seeds the one session the legacy-URL fixture names.
func NewDesignSessions(id, productID uuid.UUID) emptyDesignSessions {
	return emptyDesignSessions{session: store.DesignSession{
		ID:                id,
		ProductID:         productID,
		OpeningSubmission: "Test submission",
		CreatedAt:         time.Now(),
	}}
}

type emptyRevisionEvents struct{ store.RevisionEventStore }

func (emptyRevisionEvents) ListLatestSignoffBySessionIDs(context.Context, []uuid.UUID) (map[uuid.UUID]store.SignoffStatus, error) {
	return nil, nil
}

func (emptyRevisionEvents) ListBySession(context.Context, uuid.UUID) ([]store.RevisionEvent, error) {
	return nil, nil
}

func (emptyRevisionEvents) ListOpenQuestions(context.Context, uuid.UUID) ([]store.OpenQuestion, error) {
	return nil, nil
}

var (
	_ store.DesignSessionStore = emptyDesignSessions{}
	_ store.RevisionEventStore = emptyRevisionEvents{}
)
