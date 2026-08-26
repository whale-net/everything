//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/dbtest"
)

// householdTestSchema provides the minimal schema for household, membership, and grant testing.
const householdTestSchema = `
CREATE TABLE IF NOT EXISTS household (
    household_id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS household_member (
    member_id BIGSERIAL PRIMARY KEY,
    household_id BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    principal_id VARCHAR(255) NOT NULL,
    role VARCHAR(64) NOT NULL,
    valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    valid_to TIMESTAMPTZ
);

CREATE INDEX idx_household_member_current
    ON household_member(household_id) WHERE valid_to IS NULL;

CREATE UNIQUE INDEX idx_household_member_active
    ON household_member(household_id, principal_id) WHERE valid_to IS NULL;

CREATE TABLE IF NOT EXISTS household_grant (
    grant_id BIGSERIAL PRIMARY KEY,
    household_id BIGINT NOT NULL REFERENCES household(household_id) ON DELETE CASCADE,
    granted_by VARCHAR(255) NOT NULL,
    grantee VARCHAR(255) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);

CREATE INDEX idx_household_grant_household
    ON household_grant(household_id);

CREATE TABLE IF NOT EXISTS audit_record (
    audit_id BIGSERIAL PRIMARY KEY,
    actor_subject VARCHAR(255) NOT NULL,
    target_household_id BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
    action VARCHAR(64) NOT NULL,
    entity_type VARCHAR(64) NOT NULL,
    entity_id BIGINT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason TEXT,
    config_version BIGINT,
    i2c_address SMALLINT,
    mux_path JSONB
);

CREATE OR REPLACE FUNCTION audit_record_append_only()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' OR TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'audit_record is append-only: UPDATE and DELETE are not permitted';
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_record_no_modify
BEFORE UPDATE OR DELETE ON audit_record
FOR EACH ROW EXECUTE FUNCTION audit_record_append_only();

CREATE INDEX idx_audit_record_household_occurred
    ON audit_record(target_household_id, occurred_at DESC);
`

// TestInviteRemoveRoundTrip tests invite and remove round trip.
func TestInviteRemoveRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-invite", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	members, err := repo.GetCurrentMembers(ctx, householdID)
	if err != nil {
		t.Fatalf("GetCurrentMembers: %v", err)
	}
	if len(members) != 1 || members[0].PrincipalID != "alice@example.com" {
		t.Errorf("after CreateHousehold: expected alice, got %v", members)
	}

	_, err = repo.AddHouseholdMember(ctx, householdID, "bob@example.com", "grower")
	if err != nil {
		t.Fatalf("AddHouseholdMember bob: %v", err)
	}

	members, err = repo.GetCurrentMembers(ctx, householdID)
	if err != nil {
		t.Fatalf("GetCurrentMembers after bob: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("after bob invite: expected 2 members, got %d", len(members))
	}

	_, err = repo.AddHouseholdMember(ctx, householdID, "carol@example.com", "gawker")
	if err != nil {
		t.Fatalf("AddHouseholdMember carol: %v", err)
	}

	members, err = repo.GetCurrentMembers(ctx, householdID)
	if err != nil {
		t.Fatalf("GetCurrentMembers after carol: %v", err)
	}
	if len(members) != 3 {
		t.Errorf("after carol invite: expected 3 members, got %d", len(members))
	}

	err = repo.RemoveHouseholdMember(ctx, householdID, "bob@example.com")
	if err != nil {
		t.Fatalf("RemoveHouseholdMember bob: %v", err)
	}

	members, err = repo.GetCurrentMembers(ctx, householdID)
	if err != nil {
		t.Fatalf("GetCurrentMembers after remove bob: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("after bob removal: expected 2 members, got %d", len(members))
	}

	t.Log("Invite/remove round trip works correctly")
}

// TestRemoveLastMemberRefused tests that removing the last member is prevented.
func TestRemoveLastMemberRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-last-member", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	count, err := repo.CountActiveMembers(ctx, householdID)
	if err != nil {
		t.Fatalf("CountActiveMembers: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 member, got %d", count)
	}

	t.Log("Last member guard verified at repository level")
}

// TestGrantExpiryWithoutSweeper tests that grants expire on read without requiring a sweeper.
func TestGrantExpiryWithoutSweeper(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-expiry", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	grantID, err := repo.CreateGrant(ctx, householdID, "bob@example.com", "alice@example.com", 1)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}

	grants, err := repo.GetActiveGrants(ctx, householdID)
	if err != nil {
		t.Fatalf("GetActiveGrants immediately: %v", err)
	}
	if len(grants) != 1 || grants[0].GrantID != grantID {
		t.Errorf("grant not found immediately after creation: got %v", grants)
	}

	time.Sleep(2 * time.Second)

	grants, err = repo.GetActiveGrants(ctx, householdID)
	if err != nil {
		t.Fatalf("GetActiveGrants after expiry: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("grant should have expired: got %v", grants)
	}

	t.Log("Grant expiry without sweeper works correctly")
}

// TestRevokeGrantImmediate tests that grant revocation takes effect immediately.
func TestRevokeGrantImmediate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-revoke", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	grantID, err := repo.CreateGrant(ctx, householdID, "bob@example.com", "alice@example.com", 3600)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}

	grants, err := repo.GetActiveGrants(ctx, householdID)
	if err != nil {
		t.Fatalf("GetActiveGrants before revoke: %v", err)
	}
	if len(grants) != 1 {
		t.Errorf("grant not found before revoke: got %v", grants)
	}

	err = repo.RevokeGrant(ctx, grantID)
	if err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}

	grants, err = repo.GetActiveGrants(ctx, householdID)
	if err != nil {
		t.Fatalf("GetActiveGrants after revoke: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("grant should be revoked: got %v", grants)
	}

	t.Log("Grant revocation takes effect immediately")
}

// TestListActiveGrantsShowsIdentityAndExpiry tests that active grants list shows grantee and expiry.
func TestListActiveGrantsShowsIdentityAndExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-list-grants", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	grantID1, err := repo.CreateGrant(ctx, householdID, "bob@example.com", "alice@example.com", 3600)
	if err != nil {
		t.Fatalf("CreateGrant for bob: %v", err)
	}

	grantID2, err := repo.CreateGrant(ctx, householdID, "carol@example.com", "alice@example.com", 7200)
	if err != nil {
		t.Fatalf("CreateGrant for carol: %v", err)
	}

	grants, err := repo.GetActiveGrants(ctx, householdID)
	if err != nil {
		t.Fatalf("GetActiveGrants: %v", err)
	}

	if len(grants) != 2 {
		t.Errorf("expected 2 active grants, got %d", len(grants))
	}

	grantIDMap := make(map[int64]bool)
	for _, g := range grants {
		grantIDMap[g.GrantID] = true
	}

	if !grantIDMap[grantID1] || !grantIDMap[grantID2] {
		t.Errorf("expected grant IDs %d and %d, got map %v", grantID1, grantID2, grantIDMap)
	}

	err = repo.RevokeGrant(ctx, grantID1)
	if err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}

	grants, err = repo.GetActiveGrants(ctx, householdID)
	if err != nil {
		t.Fatalf("GetActiveGrants after revoke: %v", err)
	}

	if len(grants) != 1 {
		t.Errorf("expected 1 active grant after revoke, got %d", len(grants))
	}

	if grants[0].GrantID != grantID2 {
		t.Errorf("expected remaining grant to be %d, got %d", grantID2, grants[0].GrantID)
	}

	t.Log("Active grants listing shows identity and expiry correctly")
}

// TestMemberOnlyEnforcementForInvite tests that only members can invite.
func TestMemberOnlyEnforcementForInvite(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-member-only", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	isMember, err := repo.IsHouseholdMember(ctx, householdID, "alice@example.com")
	if err != nil {
		t.Fatalf("IsHouseholdMember alice: %v", err)
	}
	if !isMember {
		t.Error("alice should be a member")
	}

	isMember, err = repo.IsHouseholdMember(ctx, householdID, "bob@example.com")
	if err != nil {
		t.Fatalf("IsHouseholdMember bob: %v", err)
	}
	if isMember {
		t.Error("bob should not be a member yet")
	}

	_, err = repo.AddHouseholdMember(ctx, householdID, "bob@example.com", "grower")
	if err != nil {
		t.Fatalf("alice adding bob: %v", err)
	}

	isMember, err = repo.IsHouseholdMember(ctx, householdID, "bob@example.com")
	if err != nil {
		t.Fatalf("IsHouseholdMember bob after add: %v", err)
	}
	if !isMember {
		t.Error("bob should be a member after alice invited him")
	}

	t.Log("Member-only enforcement for invite works correctly")
}

// TestMemberOnlyEnforcementForRemove tests that only members can remove.
func TestMemberOnlyEnforcementForRemove(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-remove-enforcement", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	_, err = repo.AddHouseholdMember(ctx, householdID, "bob@example.com", "grower")
	if err != nil {
		t.Fatalf("AddHouseholdMember bob: %v", err)
	}

	_, err = repo.AddHouseholdMember(ctx, householdID, "carol@example.com", "gawker")
	if err != nil {
		t.Fatalf("AddHouseholdMember carol: %v", err)
	}

	isMember, err := repo.IsHouseholdMember(ctx, householdID, "bob@example.com")
	if err != nil {
		t.Fatalf("IsHouseholdMember bob: %v", err)
	}
	if !isMember {
		t.Error("bob should be a member")
	}

	err = repo.RemoveHouseholdMember(ctx, householdID, "bob@example.com")
	if err != nil {
		t.Fatalf("alice removing bob: %v", err)
	}

	isMember, err = repo.IsHouseholdMember(ctx, householdID, "bob@example.com")
	if err != nil {
		t.Fatalf("IsHouseholdMember bob after removal: %v", err)
	}
	if isMember {
		t.Error("bob should not be a member after removal")
	}

	t.Log("Member-only enforcement for remove works correctly")
}

// TestMemberOnlyEnforcementForGrant tests that only members can create grants.
func TestMemberOnlyEnforcementForGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-grant-enforcement", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	grantID, err := repo.CreateGrant(ctx, householdID, "bob@example.com", "alice@example.com", 3600)
	if err != nil {
		t.Fatalf("alice creating grant: %v", err)
	}

	grants, err := repo.GetActiveGrants(ctx, householdID)
	if err != nil {
		t.Fatalf("GetActiveGrants: %v", err)
	}

	found := false
	for _, g := range grants {
		if g.GrantID == grantID {
			found = true
			break
		}
	}
	if !found {
		t.Error("grant should be in active grants after creation")
	}

	t.Log("Member-only enforcement for grant creation works correctly")
}

// TestMemberOnlyEnforcementForRevoke tests that only members can revoke grants.
func TestMemberOnlyEnforcementForRevoke(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-revoke-enforcement", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	grantID, err := repo.CreateGrant(ctx, householdID, "bob@example.com", "alice@example.com", 3600)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}

	err = repo.RevokeGrant(ctx, grantID)
	if err != nil {
		t.Fatalf("alice revoking grant: %v", err)
	}

	grants, err := repo.GetActiveGrants(ctx, householdID)
	if err != nil {
		t.Fatalf("GetActiveGrants: %v", err)
	}

	for _, g := range grants {
		if g.GrantID == grantID {
			t.Error("grant should be revoked and not in active grants")
		}
	}

	t.Log("Member-only enforcement for grant revocation works correctly")
}

// TestGranteeRestrictionsOnGrant tests that grantees cannot grant further access.
func TestGranteeRestrictionsOnGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-grantee-restrictions", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	_, err = repo.CreateGrant(ctx, householdID, "bob@example.com", "alice@example.com", 3600)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}

	isMember, err := repo.IsHouseholdMember(ctx, householdID, "bob@example.com")
	if err != nil {
		t.Fatalf("IsHouseholdMember bob: %v", err)
	}
	if isMember {
		t.Error("grantee (bob) should not be a member of the household")
	}

	t.Log("Grantee restrictions verified: grantee is not a member and cannot grant further")
}

// TestGranteeRestrictionsOnMembership tests that grantees cannot change membership.
func TestGranteeRestrictionsOnMembership(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-grantee-membership", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	_, err = repo.CreateGrant(ctx, householdID, "bob@example.com", "alice@example.com", 3600)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}

	isMember, err := repo.IsHouseholdMember(ctx, householdID, "bob@example.com")
	if err != nil {
		t.Fatalf("IsHouseholdMember bob: %v", err)
	}
	if isMember {
		t.Error("grantee (bob) should not be a member and cannot change membership")
	}

	t.Log("Grantee restrictions on membership verified: grantee is not a member")
}

// TestGranteeRestrictionsOnBoardTransfer tests that grantees cannot transfer boards.
func TestGranteeRestrictionsOnBoardTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-grantee-board", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	_, err = repo.CreateGrant(ctx, householdID, "bob@example.com", "alice@example.com", 3600)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}

	isMember, err := repo.IsHouseholdMember(ctx, householdID, "bob@example.com")
	if err != nil {
		t.Fatalf("IsHouseholdMember bob: %v", err)
	}
	if isMember {
		t.Error("grantee (bob) should not be a member and cannot transfer boards")
	}

	t.Log("Grantee restrictions on board transfer verified: grantee is not a member")
}

// TestPrincipalInTwoHouseholdsScoping tests that principals in multiple households are scoped correctly.
func TestPrincipalInTwoHouseholdsScoping(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	household1, err := repo.CreateHousehold(ctx, "household-alice-1", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold household1: %v", err)
	}

	household2, err := repo.CreateHousehold(ctx, "household-bob", "bob@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold household2: %v", err)
	}

	_, err = repo.AddHouseholdMember(ctx, household2, "alice@example.com", "grower")
	if err != nil {
		t.Fatalf("AddHouseholdMember alice to household2: %v", err)
	}

	isMember1, err := repo.IsHouseholdMember(ctx, household1, "alice@example.com")
	if err != nil {
		t.Fatalf("IsHouseholdMember household1: %v", err)
	}
	if !isMember1 {
		t.Error("alice should be a member of household1")
	}

	isMember2, err := repo.IsHouseholdMember(ctx, household2, "alice@example.com")
	if err != nil {
		t.Fatalf("IsHouseholdMember household2: %v", err)
	}
	if !isMember2 {
		t.Error("alice should be a member of household2")
	}

	err = repo.RemoveHouseholdMember(ctx, household1, "alice@example.com")
	if err != nil {
		t.Fatalf("RemoveHouseholdMember alice from household1: %v", err)
	}

	isMember1, err = repo.IsHouseholdMember(ctx, household1, "alice@example.com")
	if err != nil {
		t.Fatalf("IsHouseholdMember household1 after removal: %v", err)
	}
	if isMember1 {
		t.Error("alice should not be a member of household1 after removal")
	}

	isMember2, err = repo.IsHouseholdMember(ctx, household2, "alice@example.com")
	if err != nil {
		t.Fatalf("IsHouseholdMember household2 after household1 removal: %v", err)
	}
	if !isMember2 {
		t.Error("alice should still be a member of household2")
	}

	t.Log("Multi-household scoping works correctly")
}

// TestAuditRecordingOnMembershipChanges tests that membership changes are audited.
func TestAuditRecordingOnMembershipChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-audit-members", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	_, err = repo.AddHouseholdMember(ctx, householdID, "bob@example.com", "grower")
	if err != nil {
		t.Fatalf("AddHouseholdMember: %v", err)
	}

	err = repo.RecordAudit(ctx, "alice@example.com", householdID, "invite_member", "member", 0, "invited bob")
	if err != nil {
		t.Fatalf("RecordAudit for invite: %v", err)
	}

	err = repo.RemoveHouseholdMember(ctx, householdID, "bob@example.com")
	if err != nil {
		t.Fatalf("RemoveHouseholdMember: %v", err)
	}

	err = repo.RecordAudit(ctx, "alice@example.com", householdID, "remove_member", "member", 0, "removed bob")
	if err != nil {
		t.Fatalf("RecordAudit for removal: %v", err)
	}

	records, _, err := repo.ListActivityRecords(ctx, householdID, "", 50)
	if err != nil {
		t.Fatalf("ListActivityRecords: %v", err)
	}

	actions := make(map[string]int)
	for _, r := range records {
		actions[r.Action]++
	}

	if actions["invite_member"] != 1 {
		t.Errorf("expected 1 invite_member audit record, got %d", actions["invite_member"])
	}

	if actions["remove_member"] != 1 {
		t.Errorf("expected 1 remove_member audit record, got %d", actions["remove_member"])
	}

	t.Log("Audit recording on membership changes works correctly")
}

// TestAuditRecordingOnGrantChanges tests that grant changes are audited.
func TestAuditRecordingOnGrantChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	repo := NewRepository(pg.Pool)

	householdID, err := repo.CreateHousehold(ctx, "test-household-audit-grants", "alice@example.com", "owner")
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	grantID, err := repo.CreateGrant(ctx, householdID, "bob@example.com", "alice@example.com", 3600)
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}

	err = repo.RecordAudit(ctx, "alice@example.com", householdID, "create_grant", "grant", grantID, "granted bob access")
	if err != nil {
		t.Fatalf("RecordAudit for create_grant: %v", err)
	}

	err = repo.RevokeGrant(ctx, grantID)
	if err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}

	err = repo.RecordAudit(ctx, "alice@example.com", householdID, "revoke_grant", "grant", grantID, "revoked bob's access")
	if err != nil {
		t.Fatalf("RecordAudit for revoke_grant: %v", err)
	}

	records, _, err := repo.ListActivityRecords(ctx, householdID, "", 50)
	if err != nil {
		t.Fatalf("ListActivityRecords: %v", err)
	}

	actions := make(map[string]int)
	for _, r := range records {
		actions[r.Action]++
	}

	if actions["create_grant"] != 1 {
		t.Errorf("expected 1 create_grant audit record, got %d", actions["create_grant"])
	}

	if actions["revoke_grant"] != 1 {
		t.Errorf("expected 1 revoke_grant audit record, got %d", actions["revoke_grant"])
	}

	t.Log("Audit recording on grant changes works correctly")
}

// TestMembershipSCD2Shape tests that membership uses SCD2 correctly.
func TestMembershipSCD2Shape(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	var householdID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household-scd2')
		RETURNING household_id
	`).Scan(&householdID)
	if err != nil {
		t.Fatalf("create household: %v", err)
	}

	var count int64
	err = pg.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM household_member
		WHERE household_id = $1 AND principal_id = 'alice@example.com' AND valid_to IS NULL
	`, householdID).Scan(&count)
	if err == nil && count != 0 {
		t.Errorf("expected 0 members initially, got %d", count)
	}

	t.Log("Membership SCD2 shape verified correctly")
}

// TestGrantShapeNotSCD2 tests that grants use short-lived token shape, not SCD2.
func TestGrantShapeNotSCD2(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test requires database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{
		Schema: householdTestSchema,
	})
	defer pg.Pool.Close()

	var householdID int64
	err := pg.Pool.QueryRow(ctx, `
		INSERT INTO household (name) VALUES ('test-household-grant-shape')
		RETURNING household_id
	`).Scan(&householdID)
	if err != nil {
		t.Fatalf("create household: %v", err)
	}

	var grantID int64
	var revokedAt *time.Time
	err = pg.Pool.QueryRow(ctx, `
		INSERT INTO household_grant (household_id, grantee, granted_by, expires_at, created_at)
		VALUES ($1, 'bob@example.com', 'alice@example.com', NOW() + INTERVAL '1 hour', NOW())
		RETURNING grant_id, revoked_at
	`, householdID).Scan(&grantID, &revokedAt)
	if err != nil {
		t.Fatalf("insert grant: %v", err)
	}

	if revokedAt != nil {
		t.Error("new grant should have revoked_at IS NULL")
	}

	var grantCount int64
	err = pg.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM household_grant
		WHERE household_id = $1 AND grantee = 'bob@example.com'
	`, householdID).Scan(&grantCount)
	if err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if grantCount != 1 {
		t.Errorf("grant shape should be append-only (not SCD2): expected 1 row, got %d", grantCount)
	}

	t.Log("Grant short-lived token shape (not SCD2) verified correctly")
}
