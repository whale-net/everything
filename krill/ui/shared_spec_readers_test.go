package main

import (
	"context"

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

var _ specReadClient = scopedProductsReader{}