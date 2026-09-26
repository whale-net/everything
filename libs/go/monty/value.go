package monty

// Python values as they cross the sandbox boundary.
//
// Monty sends one flat `Arena` per message -- a post-order node list where
// containers hold child indexes -- so a value passed under two names is one
// node. This file defines the Go side of that mapping; arena.go does the
// encode/decode.
//
// The mapping is deliberately partial. Arms Monty can emit but this client
// cannot represent faithfully (datetime, exceptions-as-values, host class
// instances, repr/cycle placeholders) decode to an *UnsupportedValue error
// rather than to a lossy approximation, so a caller can tell "the sandbox
// returned something I don't model" from "the sandbox returned nil".

import (
	"fmt"
	"math/big"
)

// Tuple is a Python tuple. Distinct from a list because Monty keeps them
// distinct on the wire and Python code can tell them apart.
type Tuple []any

// Set is a Python set. Order is not preserved -- Monty emits items in an
// arbitrary order, so this is a set, not a sequence.
type Set []any

// FrozenSet is a Python frozenset. See [Set].
type FrozenSet []any

// Dict is a Python dict: insertion-ordered, with arbitrary hashable keys.
// A Go map cannot express either property, so a dict is an ordered run of
// key/value pairs rather than a map.
type Dict struct {
	Keys   []any
	Values []any
}

// NewDict returns a Dict from parallel key and value slices.
func NewDict(keys, values []any) *Dict {
	return &Dict{Keys: keys, Values: values}
}

// Len returns the number of entries.
func (d *Dict) Len() int {
	if d == nil {
		return 0
	}
	return len(d.Keys)
}

// UnsupportedValueError reports a Monty node this client does not model. It
// is returned by decoding, and by Run's result when the sandbox's value
// cannot be represented -- never as a panic.
type UnsupportedValueError struct {
	// Node is the Monty node kind that could not be represented, e.g.
	// "datetime" or "class_instance".
	Node string
}

func (e *UnsupportedValueError) Error() string {
	return fmt.Sprintf("monty: unsupported value kind %q", e.Node)
}

// Is lets errors.Is(err, ErrUnsupportedNode) match any unsupported node,
// so a caller can test the sentinel without naming the concrete type.
func (e *UnsupportedValueError) Is(target error) bool {
	return target == ErrUnsupportedNode
}

// ErrUnsupportedNode is a sentinel matching any *UnsupportedValueError, for
// callers that want errors.Is rather than a type assertion.
var ErrUnsupportedNode = fmt.Errorf("monty: unsupported value")

func unsupported(node string) error {
	return &UnsupportedValueError{Node: node}
}

// bigIntOf normalises a Python integer to int64 when it fits, else *big.Int.
func bigIntOf(i *big.Int) any {
	if i.IsInt64() {
		return i.Int64()
	}
	return i
}
