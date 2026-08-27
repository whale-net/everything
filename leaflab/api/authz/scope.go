// Package authz is the per-entity authorization layer FR4/FR5/NFR2
// require: every RPC handler resolves the entity it's asked about and
// checks it against the caller's Scope, never a bare household id
// comparison. See EntityRef/Resolution (resolver.go) for entity
// resolution and Scope (this file) for the authorization predicate.
package authz

import "strconv"

// EntityKind identifies which table/entity type an EntityRef names.
type EntityKind string

const (
	EntityBoard   EntityKind = "board"
	EntityRegion  EntityKind = "region"
	EntityPlant   EntityKind = "plant"
	EntitySensor  EntityKind = "sensor"
	EntityReading EntityKind = "reading"
)

// EntityRef names one entity to authorize: its kind and its numeric id.
// Handlers build this from request fields before calling Resolve/Permits --
// it is never itself proof of reach.
type EntityRef struct {
	Kind EntityKind
	ID   int64
}

// Scope is the unit of authorization evaluated per entity (FR4.1). It is
// deliberately **not** a household id (FR4.3, SB-1.13 -- the Gawker's
// forward guarantee): V1 constructs exactly one implementation
// (HouseholdScope) and grants it only to household members, but nothing in
// this type constrains a scope to be one household, or its holder to be a
// household member. A narrower-than-household scope (e.g. "this one
// board", held by a non-member) is representable today without changing
// this interface, a struct field, or a handler signature -- see the
// FR4.3 type test in scope_test.go once Testing lands. No implementation
// may treat "one household" as the atom of authorization; every RPC
// handler holds a Scope past the point where a principal's reach is
// established, never a household id.
type Scope interface {
	// Permits reports whether ref falls within the scope. res is ref's
	// already-resolved data (see Resolver.Resolve) -- Permits performs no
	// I/O of its own; a Resolver has already done that in the single query
	// NFR2 requires. Implementations that only care about household reach
	// use res.HouseholdID; a narrower scope may instead key off ref.Kind
	// and ref.ID directly, which is exactly what makes narrower-than-
	// household scopes representable without touching this signature.
	Permits(ref EntityRef, res Resolution) bool

	// Filter returns a SQL WHERE fragment -- using $N placeholders starting
	// at argStart -- and the arguments it references, so a listing or
	// aggregate query applies the scope **inside** the query (FR5.2) rather
	// than as a post-filter. The fragment assumes the query's FROM/JOIN
	// clause exposes a `household_id` column (directly, or aliased) for
	// the entity being listed; see HouseholdScope.Filter's doc comment.
	Filter(argStart int) (fragment string, args []any)
}

// HouseholdScope is the one Scope implementation V1 constructs: reach
// equal to exactly one household, granted to a principal via their current
// household_membership row (FR75). It is a Scope like any other -- nothing
// downstream of Scope may special-case it or assume it's the only kind
// that exists (FR4.3).
type HouseholdScope struct {
	householdID int64
}

// NewHouseholdScope builds a HouseholdScope for householdID. Handlers
// obtain householdID from the caller's current household_membership row
// (household_membership WHERE valid_to IS NULL), never from a request
// field -- a request-supplied household id is exactly the "trust the
// caller's claim" bug FR4 exists to rule out.
func NewHouseholdScope(householdID int64) HouseholdScope {
	return HouseholdScope{householdID: householdID}
}

// Permits reports whether res resolves to this scope's household. An
// unclaimed board (Resolution.Unclaimed) is never permitted by a
// HouseholdScope -- FR1.1's exception is reachable only through the claim
// path and the admin elevated lane, both of which use a different,
// deliberately minimal projection, not this Scope.
func (s HouseholdScope) Permits(ref EntityRef, res Resolution) bool {
	return !res.Unclaimed && res.HouseholdID == s.householdID
}

// Filter returns "household_id = $<argStart>" against this scope's
// household id. The caller's query must expose a `household_id` column
// for the row being filtered -- directly, for board/region-root/plant
// listings, or aliased in a join for sensor/reading listings that inherit
// household through their board. This keeps Filter itself entity-agnostic:
// it only ever compares one column to one value.
func (s HouseholdScope) Filter(argStart int) (string, []any) {
	return "household_id = $" + strconv.Itoa(argStart), []any{s.householdID}
}
