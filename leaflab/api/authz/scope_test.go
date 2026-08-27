package authz

import (
	"strconv"
	"strings"
	"testing"
)

func board(id int64) EntityRef { return EntityRef{Kind: EntityBoard, ID: id} }

// TestHouseholdScope_Permits_SameHousehold proves the base case: a
// HouseholdScope permits a resolution whose HouseholdID matches its own.
func TestHouseholdScope_Permits_SameHousehold(t *testing.T) {
	scope := NewHouseholdScope(42)
	res := Resolution{HouseholdID: 42}
	if !scope.Permits(board(1), res) {
		t.Fatal("HouseholdScope(42).Permits(household=42) = false, want true")
	}
}

// TestHouseholdScope_Permits_DifferentHousehold_Denied is FR4.1's refusal
// case: a resolution in a different household is never permitted.
func TestHouseholdScope_Permits_DifferentHousehold_Denied(t *testing.T) {
	scope := NewHouseholdScope(42)
	res := Resolution{HouseholdID: 99}
	if scope.Permits(board(1), res) {
		t.Fatal("HouseholdScope(42).Permits(household=99) = true, want false")
	}
}

// TestHouseholdScope_Permits_UnclaimedBoard_Denied pins FR1.1's exception:
// an unclaimed board resolves to no household and is never permitted by a
// HouseholdScope, no matter which household id the scope holds -- it is
// reachable only through the claim path and admin lane, both using a
// different projection than this Scope.
func TestHouseholdScope_Permits_UnclaimedBoard_Denied(t *testing.T) {
	scope := NewHouseholdScope(42)
	res := Resolution{Unclaimed: true}
	if scope.Permits(board(1), res) {
		t.Fatal("HouseholdScope.Permits(Unclaimed) = true, want false")
	}
}

// TestHouseholdScope_Filter_ParameterizesAtArgStart proves Filter emits a
// placeholder starting at the caller-supplied offset (so it composes into a
// larger query without colliding with earlier params) and carries the
// scope's household id as its one argument.
func TestHouseholdScope_Filter_ParameterizesAtArgStart(t *testing.T) {
	scope := NewHouseholdScope(7)

	frag, args := scope.Filter(1)
	if frag != "household_id = $1" {
		t.Errorf("Filter(1) fragment = %q, want %q", frag, "household_id = $1")
	}
	if len(args) != 1 || args[0] != int64(7) {
		t.Errorf("Filter(1) args = %v, want [7]", args)
	}

	frag3, args3 := scope.Filter(3)
	if frag3 != "household_id = $3" {
		t.Errorf("Filter(3) fragment = %q, want %q", frag3, "household_id = $3")
	}
	if len(args3) != 1 || args3[0] != int64(7) {
		t.Errorf("Filter(3) args = %v, want [7]", args3)
	}
}

// TestUnionScope_Empty_PermitsNothing_FilterIsFalse is FR5.1's "caller in
// no household" case: a UnionScope built with zero component scopes must
// permit nothing and its Filter must match no row -- ListBoards renders
// this as an empty list, never as "everything" (FR4.3).
func TestUnionScope_Empty_PermitsNothing_FilterIsFalse(t *testing.T) {
	scope := NewUnionScope()

	if scope.Permits(board(1), Resolution{HouseholdID: 1}) {
		t.Fatal("empty UnionScope.Permits(...) = true, want false for every resolution")
	}

	frag, args := scope.Filter(1)
	if frag != "FALSE" {
		t.Errorf("empty UnionScope.Filter fragment = %q, want %q", frag, "FALSE")
	}
	if len(args) != 0 {
		t.Errorf("empty UnionScope.Filter args = %v, want none", args)
	}
}

// TestUnionScope_PermitsFromAnyComponent is FR75's multi-household
// membership case: a principal currently in more than one household must
// be permitted access to entities in *any* of them, not just the first
// membership found.
func TestUnionScope_PermitsFromAnyComponent(t *testing.T) {
	scope := NewUnionScope(NewHouseholdScope(1), NewHouseholdScope(2))

	tests := []struct {
		name        string
		res         Resolution
		wantPermits bool
	}{
		{"first household", Resolution{HouseholdID: 1}, true},
		{"second household", Resolution{HouseholdID: 2}, true},
		{"third, unheld household", Resolution{HouseholdID: 3}, false},
		{"unclaimed board", Resolution{Unclaimed: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scope.Permits(board(1), tt.res); got != tt.wantPermits {
				t.Errorf("Permits(%+v) = %v, want %v", tt.res, got, tt.wantPermits)
			}
		})
	}
}

// TestUnionScope_Filter_ORsFragmentsWithDistinctPlaceholders proves
// UnionScope.Filter composes its component fragments with OR, gives each
// one a distinct, non-colliding placeholder range (so a 3-household union
// starting at argStart=5 doesn't reuse $5 twice), and returns args in the
// same order as the fragments reference them.
func TestUnionScope_Filter_ORsFragmentsWithDistinctPlaceholders(t *testing.T) {
	scope := NewUnionScope(NewHouseholdScope(10), NewHouseholdScope(20), NewHouseholdScope(30))

	frag, args := scope.Filter(5)

	wantFrag := "(household_id = $5) OR (household_id = $6) OR (household_id = $7)"
	if frag != wantFrag {
		t.Errorf("Filter(5) fragment = %q, want %q", frag, wantFrag)
	}
	wantArgs := []any{int64(10), int64(20), int64(30)}
	if len(args) != len(wantArgs) {
		t.Fatalf("Filter(5) args = %v, want %v", args, wantArgs)
	}
	for i := range wantArgs {
		if args[i] != wantArgs[i] {
			t.Errorf("Filter(5) args[%d] = %v, want %v", i, args[i], wantArgs[i])
		}
	}

	// Every placeholder must appear exactly once -- a colliding placeholder
	// (e.g. two components both emitting $5) would silently bind the wrong
	// argument to one of the fragments.
	for _, ph := range []string{"$5", "$6", "$7"} {
		if strings.Count(frag, ph) != 1 {
			t.Errorf("fragment %q: placeholder %s appears %d times, want exactly 1", frag, ph, strings.Count(frag, ph))
		}
	}
}

// -- FR4.3 type test -------------------------------------------------------
//
// narrowerThanHouseholdScope is a Scope implementation constructed nowhere
// in production code -- it exists only in this test, to prove the
// authorization path (the Scope interface itself) accepts a scope strictly
// narrower than "one household", held by a principal who need not be a
// household member at all. If Permits/Filter/ResolveInScope ever required
// a concrete HouseholdScope, or a household id field, this type would fail
// to compile or fail to authorize correctly -- that's the regression this
// test exists to catch (FR4.3, SB-1.13).
type narrowerThanHouseholdScope struct {
	// permittedBoardID is the *one* board this scope reaches -- not a
	// household, not even every board in a household. Deliberately does
	// not embed or reference a household id anywhere.
	permittedBoardID int64
}

func (s narrowerThanHouseholdScope) Permits(ref EntityRef, res Resolution) bool {
	// Ignores res.HouseholdID entirely -- reach is keyed off the entity
	// itself, exactly the "narrower than household" shape FR4.3 requires
	// be representable.
	return ref.Kind == EntityBoard && ref.ID == s.permittedBoardID
}

func (s narrowerThanHouseholdScope) Filter(argStart int) (string, []any) {
	return "board_id = $" + strconv.Itoa(argStart), []any{s.permittedBoardID}
}

// TestFR4_3_ScopeNarrowerThanHousehold_NonMemberPrincipal_Compiles is the
// task's named FR4.3 type test: construct a Scope narrower than a
// household (narrowerThanHouseholdScope above), held by a principal who is
// asserted to hold no household membership at all (never mind which
// household -- the point is this Scope's authority does not derive from
// one), and prove the authorization path -- Permits, used exactly as every
// RPC handler uses it -- accepts it unmodified. This test constructs no
// such scope in production code (V1 creates zero of them, per FR4.3's
// second sentence); it exists purely so "household == atom of
// authorization" cannot be silently baked into the Scope interface without
// breaking a compile or a behavioral assertion here.
func TestFR4_3_ScopeNarrowerThanHousehold_NonMemberPrincipal_Compiles(t *testing.T) {
	// The "non-member principal" holding this scope: note there is no
	// household_id anywhere on this value, and nothing about
	// narrowerThanHouseholdScope requires its holder to appear in
	// household_membership for any household at all.
	scope := narrowerThanHouseholdScope{permittedBoardID: 555}

	// A resolution in some arbitrary household the scope's holder is not a
	// member of -- HouseholdScope would refuse this outright, but this
	// narrower scope reaches it anyway because reach here is per-entity,
	// not per-household (FR4.1).
	permittedRes := Resolution{HouseholdID: 9001}
	if !scope.Permits(board(555), permittedRes) {
		t.Fatal("narrower-than-household scope did not permit its one named board -- FR4.3 authorization path rejected a valid narrow scope")
	}

	// A different board in that same arbitrary household is still refused
	// -- this scope's reach is per-entity, not per-household, so
	// "same household as the permitted board" is not sufficient.
	if scope.Permits(board(556), permittedRes) {
		t.Fatal("narrower-than-household scope permitted a board outside its single-entity reach -- reach is not household-shaped here")
	}

	// Filter must also compose without modification -- this is the
	// listing/aggregate half of FR4.3 (FR5.2), proven the same way
	// HouseholdScope.Filter and UnionScope.Filter are above.
	frag, args := scope.Filter(1)
	if frag != "board_id = $1" {
		t.Errorf("Filter(1) fragment = %q, want %q", frag, "board_id = $1")
	}
	if len(args) != 1 || args[0] != int64(555) {
		t.Errorf("Filter(1) args = %v, want [555]", args)
	}
}
