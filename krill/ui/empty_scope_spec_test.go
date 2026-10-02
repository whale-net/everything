package main

import (
	"context"

	"github.com/whale-net/everything/krill/store"
)

// emptyScopeSpecReader is the specReadClient for a test whose App has to
// resolve a product but makes no product reads of its own: it lists none,
// which is the empty-scope branch of product_scope.go.
//
// It embeds the interface so a read this fake does not implement nil-panics
// if some future page starts making one -- a test that does not expect a
// read should fail loudly rather than pass on a fabricated zero value.
//
// It is a separate type from the delivery tests' fakeSpecReader because the
// three go_test targets do not share sources, and this one has to reach all
// of them.
type emptyScopeSpecReader struct {
	specReadClient
}

func (emptyScopeSpecReader) Products(context.Context) ([]store.Product, error) { return nil, nil }

var _ specReadClient = emptyScopeSpecReader{}