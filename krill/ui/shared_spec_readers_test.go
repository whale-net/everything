package main

import (
	"context"

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

// CountConsoleOverview is the Overview stat tiles' read. Every page here
// renders the chrome, and the Overview home renders the tiles too, so this
// fake owes it -- zero figures, which is a real answer for an idle
// deployment rather than a gap that would nil-panic on a nil embed.
func (chromeTaskCounter) CountConsoleOverview(context.Context, store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	return store.ConsoleOverviewCounts{}, nil
}