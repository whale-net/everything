package authz

import "testing"

// TestMemberOrGrantee_Member_AlwaysPermits_RegardlessOfCapability proves a
// member always gets household reach back unchanged, for every Capability
// including the three exclusions -- FR7's three exclusions apply only to a
// grantee, never to a member acting on their own household.
func TestMemberOrGrantee_Member_AlwaysPermits_RegardlessOfCapability(t *testing.T) {
	household := NewHouseholdScope(1)
	ref := EntityRef{Kind: EntityHousehold, ID: 1}
	res := Resolution{HouseholdID: 1}

	for _, cap := range []Capability{CapabilityOrdinary, CapabilityGrantAccess, CapabilityChangeMembership, CapabilityBoardOwnership} {
		scope := MemberOrGrantee(household, false /* isGrantee */, cap)
		if !scope.Permits(ref, res) {
			t.Errorf("MemberOrGrantee(isGrantee=false, capability=%s) does not permit the member's own household, want it to", cap)
		}
	}
}

// TestMemberOrGrantee_Grantee_Ordinary_PermitsSameAsMember proves FR7's
// "write capability equal to a member's" for the non-excluded case: a
// grantee's Scope under CapabilityOrdinary is identical to a member's.
func TestMemberOrGrantee_Grantee_Ordinary_PermitsSameAsMember(t *testing.T) {
	household := NewHouseholdScope(1)
	ref := EntityRef{Kind: EntityHousehold, ID: 1}
	res := Resolution{HouseholdID: 1}

	scope := MemberOrGrantee(household, true /* isGrantee */, CapabilityOrdinary)
	if !scope.Permits(ref, res) {
		t.Error("MemberOrGrantee(isGrantee=true, capability=CapabilityOrdinary) does not permit the grantee's household, want it to (FR7: grant confers write capability equal to a member's)")
	}
}

// TestMemberOrGrantee_Grantee_ExcludedCapabilities_PermitsNothing is FR7's
// three named exclusions ("a grantee may not: grant further access, change
// membership, or claim/transfer/release a board") at the one place they are
// enforced: for each excluded Capability, a grantee's Scope permits nothing
// over their own (otherwise-reachable) household, even though the same
// household would be permitted under CapabilityOrdinary or for a member.
func TestMemberOrGrantee_Grantee_ExcludedCapabilities_PermitsNothing(t *testing.T) {
	household := NewHouseholdScope(1)
	ref := EntityRef{Kind: EntityHousehold, ID: 1}
	res := Resolution{HouseholdID: 1}

	tests := []struct {
		name string
		cap  Capability
	}{
		{"grant further access", CapabilityGrantAccess},
		{"change membership", CapabilityChangeMembership},
		{"claim/transfer/release a board", CapabilityBoardOwnership},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := MemberOrGrantee(household, true /* isGrantee */, tt.cap)
			if scope.Permits(ref, res) {
				t.Errorf("MemberOrGrantee(isGrantee=true, capability=%s) permits the grantee's household, want it to permit nothing (FR7 exclusion: %s)", tt.cap, tt.name)
			}
			frag, args := scope.Filter(1)
			if frag != "FALSE" || len(args) != 0 {
				t.Errorf("excluded grantee scope Filter = (%q, %v), want (\"FALSE\", nil) -- must never widen a listing query either", frag, args)
			}
		})
	}
}
