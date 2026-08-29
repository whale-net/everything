package authz

// Capability names the operation being authorized at a "member capability"
// call site (FR7). Most capabilities are not named here at all -- only the
// three FR7 carves a grantee out of ("a grantee may not: grant further
// access, change membership, or claim/transfer/release a board") get a
// named constant. Declaring them here, and nowhere else, is what "the
// three exclusions are declared in exactly one place" (Validation phase)
// means: every other write call site passes CapabilityOrdinary (or simply
// doesn't need to distinguish) and MemberOrGrantee treats it identically
// to a member's own capability.
type Capability string

const (
	// CapabilityOrdinary is every "member capability" call site FR7 does
	// NOT carve a grantee out of -- e.g. renaming a board (FR57). A
	// grantee's write capability equals a member's here.
	CapabilityOrdinary Capability = "ordinary"

	// CapabilityGrantAccess is FR7's "a grantee may not grant further
	// access" exclusion -- GrantHouseholdAccess's own handler.
	CapabilityGrantAccess Capability = "grant_access"

	// CapabilityChangeMembership is FR7's "a grantee may not change
	// membership" exclusion (FR75) -- household membership add/remove
	// handlers.
	CapabilityChangeMembership Capability = "change_membership"

	// CapabilityBoardOwnership is FR7's "a grantee may not claim, transfer
	// or release a board" exclusion (FR76, FR77) -- the claim/transfer/
	// release handlers.
	CapabilityBoardOwnership Capability = "board_ownership"
)

// grantExcludedCapabilities is FR7's three exclusions, keyed for
// MemberOrGrantee's lookup. This is the one place any of them is checked
// -- handlers pass a Capability in, they never re-derive "is this one of
// the three" themselves.
var grantExcludedCapabilities = map[Capability]bool{
	CapabilityGrantAccess:      true,
	CapabilityChangeMembership: true,
	CapabilityBoardOwnership:   true,
}

// MemberOrGrantee builds the Scope a "member capability" call site
// authorizes against (FR7): household reach identical to a member's,
// available to either an actual member or the holder of an active,
// unexpired, unrevoked household_grant. Every RPC handler implementing a
// "member capability" (FR7's phrase for "member-or-grantee unless the
// operation is one of the three exclusions") must call this rather than a
// bare membership check, so a later requirement inherits FR7's grant
// semantics automatically instead of re-deciding them.
//
// household is the caller's HouseholdScope, already established by the
// caller from either a current household_membership row or an active
// household_grant row (ScopeForPrincipal's grant-aware extension,
// Implementation phase, resolves which). isGrantee is true when household
// reach came from a grant rather than membership -- MemberOrGrantee is the
// only place that distinction changes the outcome: for capability values
// other than the three named in grantExcludedCapabilities, a grantee's
// Scope is identical to a member's; for the three, a grantee's Scope
// permits nothing here, even though household reach is otherwise
// identical, per FR7's "write capability equal to a member's, except...".
//
// A member (isGrantee == false) always gets household back unchanged,
// regardless of capability -- the three exclusions apply only to a
// grantee.
func MemberOrGrantee(household HouseholdScope, isGrantee bool, capability Capability) Scope {
	if isGrantee && grantExcludedCapabilities[capability] {
		// UnionScope with zero component scopes permits nothing (see
		// UnionScope's doc comment) -- the same "permits nothing, never
		// widened" shape ScopeForPrincipal already uses for a principal in
		// no household at all.
		return NewUnionScope()
	}
	return household
}
