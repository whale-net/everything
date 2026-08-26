package main

import (
	"fmt"
	"testing"
)

// TestAuthorizationDecision_ContainsHousehold tests per-entity household containment.
// FR4: Authorization is per entity, based on the principal's reach.
func TestAuthorizationDecision_ContainsHousehold(t *testing.T) {
	tests := []struct {
		name          string
		householdIDs  []int64
		queryID       int64
		expectedFound bool
	}{
		{
			name:          "household in reach",
			householdIDs:  []int64{1, 2, 3},
			queryID:       2,
			expectedFound: true,
		},
		{
			name:          "household not in reach",
			householdIDs:  []int64{1, 3},
			queryID:       2,
			expectedFound: false,
		},
		{
			name:          "empty reach",
			householdIDs:  []int64{},
			queryID:       1,
			expectedFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scopes := make([]Scope, len(tt.householdIDs))
			for i, hid := range tt.householdIDs {
				scopes[i] = NewHouseholdScope(hid)
			}
			auth := NewAuthorizationDecision("principal-1", scopes...)

			found := auth.ContainsHousehold(tt.queryID)
			if found != tt.expectedFound {
				t.Errorf("expected %v, got %v", tt.expectedFound, found)
			}
		})
	}
}

// TestAuthorizationDecision_HasReach tests reach detection.
// FR4: Authorization decision must report whether principal has any reach.
func TestAuthorizationDecision_HasReach(t *testing.T) {
	tests := []struct {
		name          string
		householdIDs  []int64
		expectedReach bool
	}{
		{
			name:          "with reach",
			householdIDs:  []int64{1},
			expectedReach: true,
		},
		{
			name:          "no reach",
			householdIDs:  []int64{},
			expectedReach: false,
		},
		{
			name:          "multiple households",
			householdIDs:  []int64{1, 2, 3},
			expectedReach: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scopes := make([]Scope, len(tt.householdIDs))
			for i, hid := range tt.householdIDs {
				scopes[i] = NewHouseholdScope(hid)
			}
			auth := NewAuthorizationDecision("principal-1", scopes...)

			if auth.HasReach() != tt.expectedReach {
				t.Errorf("expected HasReach %v, got %v", tt.expectedReach, auth.HasReach())
			}
		})
	}
}

// TestAuthorizationDecision_HouseholdScopes returns all household scopes.
// FR5: Aggregates are computed over the reach set.
func TestAuthorizationDecision_HouseholdScopes(t *testing.T) {
	householdIDs := []int64{1, 2, 3}
	scopes := make([]Scope, len(householdIDs))
	for i, hid := range householdIDs {
		scopes[i] = NewHouseholdScope(hid)
	}

	auth := NewAuthorizationDecision("principal-1", scopes...)
	households := auth.HouseholdScopes()

	if len(households) != len(householdIDs) {
		t.Errorf("expected %d households, got %d", len(householdIDs), len(households))
	}

	for i, hid := range householdIDs {
		if households[i].HouseholdID() != hid {
			t.Errorf("household %d: expected ID %d, got %d", i, hid, households[i].HouseholdID())
		}
	}
}

// TestHouseholdScope_ScopeID tests scope identification.
// FR4.3: Scopes must have unique identifiers for proper deduplication.
func TestHouseholdScope_ScopeID(t *testing.T) {
	scope1 := NewHouseholdScope(1)
	scope2 := NewHouseholdScope(2)
	scope1Dup := NewHouseholdScope(1)

	id1 := scope1.ScopeID()
	id2 := scope2.ScopeID()
	id1Dup := scope1Dup.ScopeID()

	if id1 == id2 {
		t.Errorf("different household IDs should have different scope IDs")
	}
	if id1 != id1Dup {
		t.Errorf("same household ID should have same scope ID")
	}
}

// TestAuthorizationDecision_Scopes returns all scopes in the reach set.
// FR4: Authorization decision exposes all scopes for iteration.
func TestAuthorizationDecision_Scopes(t *testing.T) {
	householdIDs := []int64{1, 2, 3}
	scopes := make([]Scope, len(householdIDs))
	for i, hid := range householdIDs {
		scopes[i] = NewHouseholdScope(hid)
	}

	auth := NewAuthorizationDecision("principal-1", scopes...)
	allScopes := auth.Scopes()

	if len(allScopes) != len(scopes) {
		t.Errorf("expected %d scopes, got %d", len(scopes), len(allScopes))
	}
}

// TestAuthorizationDecision_PrincipalID returns the principal making the request.
// FR4: Authorization decision must carry principal identity.
func TestAuthorizationDecision_PrincipalID(t *testing.T) {
	expectedPrincipal := "test-principal-123"
	scope := NewHouseholdScope(1)
	auth := NewAuthorizationDecision(expectedPrincipal, scope)

	if auth.PrincipalID() != expectedPrincipal {
		t.Errorf("expected principal %q, got %q", expectedPrincipal, auth.PrincipalID())
	}
}

// TestNewAuthorizationDecision_NoDuplicateScopes ensures deduplication.
// FR4: Authorization decision should not duplicate scopes with same ID.
func TestNewAuthorizationDecision_NoDuplicateScopes(t *testing.T) {
	// Pass the same household scope twice
	scope := NewHouseholdScope(1)
	auth := NewAuthorizationDecision("principal-1", scope, scope)

	// Should only have one scope due to map-based deduplication
	scopes := auth.Scopes()
	if len(scopes) != 1 {
		t.Errorf("expected 1 unique scope after deduplication, got %d", len(scopes))
	}
}

// testCustomScope is a custom scope type for testing interface flexibility.
// Used to prove FR4.3: design allows narrower scopes without rewriting call sites.
type testCustomScope struct {
	id string
}

func (cs *testCustomScope) ScopeID() string {
	return cs.id
}

// TestScopeInterface_Implementer proves Scope interface is properly implemented.
// FR4.3: Design allows narrower scopes without rewriting call sites.
// This test proves the interface itself is flexible.
func TestScopeInterface_Implementer(t *testing.T) {
	// Prove HouseholdScope implements Scope interface
	var _ Scope = (*HouseholdScope)(nil)

	// Prove a custom scope could be created without modifying call sites
	customScope := &testCustomScope{id: "custom:1"}
	var _ Scope = customScope

	// Prove AuthorizationDecision accepts any Scope implementation
	auth := NewAuthorizationDecision("principal-1", customScope)
	if len(auth.Scopes()) != 1 {
		t.Errorf("expected custom scope to be accepted")
	}
}

// TestAuthorizationDecision_MultipleHouseholds tests multi-household scenarios.
// FR4: A principal may hold multiple scopes (member of multiple households).
func TestAuthorizationDecision_MultipleHouseholds(t *testing.T) {
	// Simulate a principal with membership in 3 households
	householdIDs := []int64{10, 20, 30}
	scopes := make([]Scope, len(householdIDs))
	for i, hid := range householdIDs {
		scopes[i] = NewHouseholdScope(hid)
	}

	auth := NewAuthorizationDecision("multi-member", scopes...)

	// Should be able to access any of the households
	for _, hid := range householdIDs {
		if !auth.ContainsHousehold(hid) {
			t.Errorf("principal should be able to access household %d", hid)
		}
	}

	// Should NOT be able to access other households
	if auth.ContainsHousehold(999) {
		t.Errorf("principal should not be able to access household 999")
	}
}

// TestHouseholdScope_HouseholdID returns the household ID.
// FR4: Scope implementations must expose their entity IDs for queries.
func TestHouseholdScope_HouseholdID(t *testing.T) {
	expectedHouseholdID := int64(42)
	scope := NewHouseholdScope(expectedHouseholdID)

	if scope.HouseholdID() != expectedHouseholdID {
		t.Errorf("expected household ID %d, got %d", expectedHouseholdID, scope.HouseholdID())
	}
}

// TestAuthorizationDecision_ComplexMultiHouseholdScenario tests realistic scenarios.
// Proof that FR4.3's shape requirement allows narrower scopes.
func TestAuthorizationDecision_ComplexMultiHouseholdScenario(t *testing.T) {
	// Simulate principal who is:
	// - Owner of household 1
	// - Member of household 2
	// - No access to household 3

	householdIDs := []int64{1, 2}
	scopes := make([]Scope, len(householdIDs))
	for i, hid := range householdIDs {
		scopes[i] = NewHouseholdScope(hid)
	}

	principal := "alice@example.com"
	auth := NewAuthorizationDecision(principal, scopes...)

	// Verify principal identity and reach
	if auth.PrincipalID() != principal {
		t.Errorf("principal mismatch")
	}

	if !auth.HasReach() {
		t.Error("should have reach")
	}

	// Verify household accessibility
	if !auth.ContainsHousehold(1) {
		t.Error("should access household 1")
	}
	if !auth.ContainsHousehold(2) {
		t.Error("should access household 2")
	}
	if auth.ContainsHousehold(3) {
		t.Error("should NOT access household 3")
	}

	// Verify aggregation scope
	households := auth.HouseholdScopes()
	if len(households) != 2 {
		t.Errorf("expected 2 household scopes, got %d", len(households))
	}
}

// TestScopeInterface_AllowsNarrowerScopes proves FR4.3: design allows narrower scopes.
// This test proves the implementation doesn't hard-code household-level granularity.
func TestScopeInterface_AllowsNarrowerScopes(t *testing.T) {
	// Define a region-scoped implementation to prove narrower scopes work
	regionScope := &testCustomScope{id: fmt.Sprintf("region:%d", 100)}

	// Prove the authorization decision accepts region scopes
	// This compiles and runs, proving no call site would need rewriting
	// if we added region-scoped access in the future
	auth := NewAuthorizationDecision("principal", regionScope)
	if len(auth.Scopes()) != 1 {
		t.Error("region scope should be accepted")
	}

	// Prove the interface itself is flexible
	var _ Scope = regionScope
}

// TestAuthorizationDecision_EmptyReachPrincipal tests principal with no reach.
// FR4: Even principals with zero reach must be distinguishable from errors.
func TestAuthorizationDecision_EmptyReachPrincipal(t *testing.T) {
	principal := "guest-user"
	auth := NewAuthorizationDecision(principal) // No scopes = no reach

	if auth.PrincipalID() != principal {
		t.Error("should preserve principal ID even with no reach")
	}

	if auth.HasReach() {
		t.Error("should report no reach for empty scope list")
	}

	if len(auth.Scopes()) != 0 {
		t.Error("should have zero scopes")
	}

	// Should not be able to access any household
	if auth.ContainsHousehold(1) {
		t.Error("principal with no reach should not access any household")
	}
}
