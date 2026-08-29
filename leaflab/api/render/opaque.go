// Package render holds FR1.4's opaque-placeholder rendering: the shape a
// history row or historical attribution takes on the wire when it crosses
// a household boundary. Nothing else lives here today -- see OpaqueRef.
package render

import "time"

// OpaqueRef is FR1.4's rendering for a history row or historical
// attribution that crosses a household boundary: a caller learns only
// that a placement existed and when, never what it was, who owns it, or
// whether it still exists. There is deliberately no id, name or household
// field on this type -- FR1.4 is enforced by what OpaqueRef *cannot*
// represent, not by a convention every render call site has to remember to
// follow.
//
// Applied symmetrically (FR1.4): household A's rendering of a row that
// crosses into B looks exactly like B's rendering of the same row
// crossing into A. It is not resolvable in any lane, including the
// elevated one -- an admin's elevation is entered against one target
// household, and the household on the far side of a boundary-crossing row
// is never that target, so the elevated lane renders OpaqueRef here too,
// not the real entity.
//
// Every history/timeline read path that can return a boundary-crossing
// row renders it as an OpaqueRef instead of its normal entity type --
// wiring that substitution into each read path is Implementation-phase
// work; this type is the shape those call sites converge on.
type OpaqueRef struct {
	// ValidFrom is the crossing placement's SCD2 interval start (see
	// AGENTS.md's SCD2 convention) -- the only fact about *when* FR1.4
	// permits a caller to learn.
	ValidFrom time.Time
	// ValidTo is the interval's end, or nil if the placement was (or, at
	// render time, still is) the live reference -- mirroring valid_to IS
	// NULL's usual meaning in every other SCD2 history table in this
	// domain.
	ValidTo *time.Time
}

// NewOpaqueRef builds an OpaqueRef for the interval [validFrom, validTo).
// The only constructor: OpaqueRef carries no field beyond the interval, by
// design -- see the type's doc comment.
func NewOpaqueRef(validFrom time.Time, validTo *time.Time) OpaqueRef {
	return OpaqueRef{ValidFrom: validFrom, ValidTo: validTo}
}
