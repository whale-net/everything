// Package authz is the per-entity authorization layer FR4/FR5/NFR2
// require: every RPC handler resolves the entity it's asked about and
// checks it against the caller's Scope, never a bare household id
// comparison. See EntityRef/Resolution (resolver.go) for entity
// resolution and Scope (this file) for the authorization predicate.
package authz

import (
	"strconv"
	"strings"
)

// EntityKind identifies which table/entity type an EntityRef names.
type EntityKind string

const (
	EntityBoard   EntityKind = "board"
	EntityRegion  EntityKind = "region"
	EntityPlant   EntityKind = "plant"
	EntitySensor  EntityKind = "sensor"
	EntityReading EntityKind = "reading"
	// EntityHousehold identifies a household itself as the entity being
	// checked against a Scope -- used by leaflab/api/households.go's
	// GetHousehold/ListHouseholdMembers read-path authorization, and by
	// FR7's grant RPCs (GrantHouseholdAccess, RevokeHouseholdAccess,
	// ListHouseholdGrants), which act on the household directly rather
	// than on a board/region/plant/sensor/reading resolving into one.
	// Unlike the entity kinds above, a household is never resolved via
	// Resolver.Resolve -- the caller already has the household id in hand
	// (from the request, or from the household_grant row being revoked),
	// so this kind exists only as a Scope.Permits/EntityRef label,
	// alongside a Resolution{HouseholdID: <id>} built directly rather than
	// resolved.
	EntityHousehold EntityKind = "household"
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

// UnionScope combines zero or more Scopes with OR semantics: an entity is
// permitted if any component Scope permits it. This is how a principal
// holding more than one current household_membership row gets a single
// Scope to carry down through a handler -- FR75 explicitly permits
// multi-household membership ("V1 does not specify a switching
// experience", so every RPC must honor all of a caller's households at
// once, not just the first one found). Handlers never juggle a slice of
// HouseholdScopes past ScopeForPrincipal (resolver.go) -- this is that
// slice, packaged as one Scope.
//
// A UnionScope with zero component scopes (a principal currently in no
// household) permits nothing, and its Filter matches no row: ListBoards
// renders that as an empty list, not an error (FR5.1) -- never as
// "everything", which is exactly the atom-of-authorization mistake FR4.3
// forbids.
type UnionScope struct {
	scopes []Scope
}

// NewUnionScope builds a UnionScope over scopes. Called with zero
// arguments for "no household" -- see UnionScope's doc comment.
func NewUnionScope(scopes ...Scope) UnionScope {
	return UnionScope{scopes: scopes}
}

func (u UnionScope) Permits(ref EntityRef, res Resolution) bool {
	for _, s := range u.scopes {
		if s.Permits(ref, res) {
			return true
		}
	}
	return false
}

// Filter ORs every component scope's fragment together, each given its
// own argStart offset so placeholders never collide. Zero component
// scopes produces the always-false "FALSE" fragment and no args, matching
// Permits' "permits nothing" behavior for a principal in no household.
func (u UnionScope) Filter(argStart int) (string, []any) {
	if len(u.scopes) == 0 {
		return "FALSE", nil
	}
	fragments := make([]string, 0, len(u.scopes))
	var args []any
	next := argStart
	for _, s := range u.scopes {
		frag, fargs := s.Filter(next)
		fragments = append(fragments, "("+frag+")")
		args = append(args, fargs...)
		next += len(fargs)
	}
	return strings.Join(fragments, " OR "), args
}

// AdminScope is the standing (non-elevated) admin Scope (FR10.2). It is a
// Scope like any other -- constructing it for an eligible admin, rather
// than reaching for HouseholdScope or a bare UnionScope, is what lets the
// standing lane's "whole lane is resolution, nothing else" rule stay
// enforced structurally: AdminScope permits no entity at all and its
// Filter matches no row, the same shape UnionScope already has for a
// principal in no household. FR10.2's ResolveToHousehold RPC does not
// route through Scope.Permits/Filter in the first place -- it returns
// FR79's own minimal AdminBoardHealth projection directly -- so this type
// exists to make "an admin without elevation is refused on every other
// read and every write" the *only* thing a Scope constructed for a
// standing admin can do, rather than something each handler has to
// remember to special-case.
type AdminScope struct{}

// NewAdminScope builds an AdminScope. Handlers construct one for a
// principal whose Claims carry the leaflab-admin realm role (FR12,
// auth.go's isAdminEligible) and who has not (or not yet, for this
// request's target household) elevated -- never as a substitute for
// checking elevation before granting reach past the standing lane.
func NewAdminScope() AdminScope {
	return AdminScope{}
}

// Permits always reports false: the standing lane confers no entity
// access (FR10.2 -- "that is the whole lane").
func (AdminScope) Permits(ref EntityRef, res Resolution) bool {
	return false
}

// Filter always returns the always-false fragment and no args, mirroring
// UnionScope's zero-component behavior (see UnionScope.Filter's doc
// comment).
func (AdminScope) Filter(argStart int) (string, []any) {
	return "FALSE", nil
}

// ElevatedScope is the Scope an admin holds against exactly one household
// while elevated (FR10.1, FR10.3). Constructing one is not itself proof of
// elevation -- it carries no expiry, reason or admin subject of its own,
// and performs no I/O (Scope's contract). A handler must independently
// verify an unexpired admin_elevation row exists for the caller against
// targetHouseholdID (migration 029_admin_elevation; the query and
// audit-stamping wiring are Implementation-phase work) before constructing
// one, exactly as scopeForCaller (leaflab/api/auth.go) resolves a
// HouseholdScope from household_membership before a member ever reaches a
// handler's business logic.
//
// Once constructed, ElevatedScope behaves exactly like HouseholdScope:
// reach is exactly targetHouseholdID and nothing else. An elevation
// against household A does not open household B (FR10.3) -- that
// guarantee lives entirely in the per-target-household gate a handler
// performs before construction, not in this type, which (like
// HouseholdScope) has no notion of "any household" to begin with.
type ElevatedScope struct {
	householdID int64
}

// NewElevatedScope builds an ElevatedScope for targetHouseholdID. See the
// type's doc comment for the elevation check that must precede this call.
func NewElevatedScope(targetHouseholdID int64) ElevatedScope {
	return ElevatedScope{householdID: targetHouseholdID}
}

// Permits reports whether res resolves to this scope's target household.
// Identical predicate to HouseholdScope.Permits, including the Unclaimed
// exception -- see that method's doc comment.
func (s ElevatedScope) Permits(ref EntityRef, res Resolution) bool {
	return !res.Unclaimed && res.HouseholdID == s.householdID
}

// Filter returns "household_id = $<argStart>" against this scope's target
// household id. See HouseholdScope.Filter's doc comment for the column
// convention this assumes.
func (s ElevatedScope) Filter(argStart int) (string, []any) {
	return "household_id = $" + strconv.Itoa(argStart), []any{s.householdID}
}
