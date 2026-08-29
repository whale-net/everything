//go:build integration

// Real-Postgres integration coverage for FR7's three grant RPCs
// (GrantHouseholdAccess, RevokeHouseholdAccess, ListHouseholdGrants) and
// FR8.1's granted-read audit requirement, exercised end-to-end against a
// real authz.PGResolver and Repository -- reusing newAuthzTestServer/
// authzTestSchema/insertHousehold/insertMembership/authzCtxFor/insertGrant/
// revokeGrantDirectly from authz_scope_integration_test.go (this package),
// per this package's established "list the shared file in both go_test
// targets" convention (see dbtest_helpers_integration_test.go's doc
// comment).
//
// The two exclusions with no RPC surface yet (change membership -- FR75,
// and claim/transfer/release a board -- FR76/FR77) are covered at the
// authz layer instead, in leaflab/api/authz/capability_test.go
// (TestMemberOrGrantee_Grantee_ExcludedCapabilities_PermitsNothing) --
// MemberOrGrantee is the one place any of the three exclusions is
// enforced (capability.go's grantExcludedCapabilities), so that coverage
// applies to every future call site built on it, not just the one RPC
// (GrantHouseholdAccess) that exists today.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:household_grant_integration_test --test_output=all
package main

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// -- Grant confers ordinary write capability, without membership -------------

// TestGrantHouseholdAccess_GranteeGetsOrdinaryWriteCapability_WithoutMembership
// is FR7's core claim exercised end-to-end: alice (a member) grants
// "helper" access; helper -- who holds no household_membership row at all
// -- then performs an ordinary household write (RevokeHouseholdAccess on a
// second grant, authz.CapabilityOrdinary) and it succeeds. This is the
// concrete "grantee can perform an ordinary household write" case this
// phase's RPC surface offers -- FR57 (renaming a board) is a later phase
// and has no RPC yet; RevokeHouseholdAccess is explicitly not one of the
// three exclusions (capability.go), so it stands in as the same kind of
// call site.
func TestGrantHouseholdAccess_GranteeGetsOrdinaryWriteCapability_WithoutMembership(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	household := insertHousehold(t, pool)
	insertMembership(t, pool, household, "alice")

	grantResp, err := server.GrantHouseholdAccess(authzCtxFor("alice"), &pb.GrantHouseholdAccessRequest{
		HouseholdId:    household,
		GranteeSubject: "helper",
		ExpiresAt:      contract.ToInstant(time.Now().Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("GrantHouseholdAccess: %v", err)
	}
	if grantResp.GrantId == 0 {
		t.Fatal("GrantHouseholdAccess returned grant_id = 0, want a real assigned id")
	}

	// A second grant, on the same household, for a different grantee --
	// this is the "ordinary household write" helper (no membership) will
	// perform.
	otherGrantID := insertGrant(t, pool, household, "helper2", "alice", time.Now().Add(time.Hour))

	if _, err := server.RevokeHouseholdAccess(authzCtxFor("helper"), &pb.RevokeHouseholdAccessRequest{GrantId: otherGrantID}); err != nil {
		t.Fatalf("RevokeHouseholdAccess by a grantee with no membership row: %v, want success (FR7: grant confers write capability equal to a member's)", err)
	}

	var revoked bool
	if err := pool.QueryRow(context.Background(),
		`SELECT revoked_at IS NOT NULL FROM household_grant WHERE grant_id = $1`, otherGrantID).Scan(&revoked); err != nil {
		t.Fatalf("read back revoked grant: %v", err)
	}
	if !revoked {
		t.Error("grant revoked by a grantee-caller was not actually marked revoked")
	}
}

// -- FR7's three exclusions: refused, not not-found ---------------------------

// TestGrantHouseholdAccess_GranteeExcluded_RefusedWithAlternative_NotNotFound
// proves the grantExcludedFailure design decision named in this task: a
// grantee's household reach is real and acknowledged (they hold an active
// grant), so attempting the one excluded operation this phase has an RPC
// for -- granting further access -- is refused via contract.Refuse
// (FailureClass = refused_with_alternative), distinct from the
// caller-has-no-reach-at-all contract.NotFound a stranger gets for the
// exact same request.
func TestGrantHouseholdAccess_GranteeExcluded_RefusedWithAlternative_NotNotFound(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	household := insertHousehold(t, pool)
	insertMembership(t, pool, household, "alice")
	insertGrant(t, pool, household, "helper", "alice", time.Now().Add(time.Hour))
	// "stranger" has neither a membership nor a grant on this household.

	granteeErr := grantAccessAttempt(t, server, "helper", household)
	strangerErr := grantAccessAttempt(t, server, "stranger", household)

	if granteeErr == nil {
		t.Fatal("GrantHouseholdAccess called by a grantee succeeded, want it refused (FR7: a grantee may not grant further access)")
	}
	if strangerErr == nil {
		t.Fatal("GrantHouseholdAccess called by a caller with no reach succeeded, want it refused")
	}

	granteeDetail, ok := contract.FromError(granteeErr)
	if !ok {
		t.Fatal("grantee's refusal carries no Failure detail")
	}
	strangerDetail, ok := contract.FromError(strangerErr)
	if !ok {
		t.Fatal("stranger's refusal carries no Failure detail")
	}

	if granteeDetail.Class != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("grantee's Failure class = %q, want %q (FR7 exclusion: real reach, refused with a named alternative)", granteeDetail.Class, contract.FailureRefusedWithAlternative)
	}
	if granteeDetail.Alternative == "" {
		t.Error("grantee's refusal names no alternative, want FR59.3's refuse-and-name-the-alternative contract honored")
	}
	if strangerDetail.Class != string(contract.FailureNotFound) {
		t.Errorf("stranger's Failure class = %q, want %q (no reach at all over this household)", strangerDetail.Class, contract.FailureNotFound)
	}
	if granteeDetail.Class == strangerDetail.Class {
		t.Error("a grantee's excluded-operation refusal and a stranger's no-reach refusal carry the same Failure class, want them distinguishable (grantExcludedFailure vs householdNotFoundFailure)")
	}
}

func grantAccessAttempt(t *testing.T, server *LeafLabAPIServer, subject string, household int64) error {
	t.Helper()
	_, err := server.GrantHouseholdAccess(authzCtxFor(subject), &pb.GrantHouseholdAccessRequest{
		HouseholdId:    household,
		GranteeSubject: "yet-another-helper",
		ExpiresAt:      contract.ToInstant(time.Now().Add(time.Hour)),
	})
	return err
}

// -- Expiry and revocation take effect immediately, at request time ---------

// TestGrantHouseholdAccess_ExpiredGrant_StopsWorkingWithNoRevocationOrJob is
// FR7's "a grant past expires_at stops working with no revocation and no
// background job", exercised through an actual RPC call rather than
// ScopeForPrincipal directly (authz_scope_integration_test.go covers that
// half): a grant inserted with expires_at already in the past (no wall-
// clock sleep needed -- evaluated against the database's real NOW() at
// request time) gives its holder no reach when they call
// ListHouseholdGrants.
func TestGrantHouseholdAccess_ExpiredGrant_StopsWorkingWithNoRevocationOrJob(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	household := insertHousehold(t, pool)
	insertMembership(t, pool, household, "alice")
	insertGrant(t, pool, household, "helper", "alice", time.Now().Add(-time.Hour))

	_, err := server.ListHouseholdGrants(authzCtxFor("helper"), &pb.ListHouseholdGrantsRequest{HouseholdId: household})
	if err == nil {
		t.Fatal("ListHouseholdGrants succeeded for a holder of only an expired grant, want it refused (FR7: expiry with no revocation and no background job)")
	}
	detail, ok := contract.FromError(err)
	if !ok || detail.Class != string(contract.FailureNotFound) {
		t.Errorf("expired-grant refusal Failure = %+v, want class %q (no current reach at all)", detail, contract.FailureNotFound)
	}
}

// TestRevokeHouseholdAccess_TakesEffectOnNextRequest is FR7's "revocation
// takes effect on the next request": a member revokes a grant, and the
// former grantee's very next request against that household is refused --
// no separate propagation step, no delay.
func TestRevokeHouseholdAccess_TakesEffectOnNextRequest(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	household := insertHousehold(t, pool)
	insertMembership(t, pool, household, "alice")
	grantResp, err := server.GrantHouseholdAccess(authzCtxFor("alice"), &pb.GrantHouseholdAccessRequest{
		HouseholdId:    household,
		GranteeSubject: "helper",
		ExpiresAt:      contract.ToInstant(time.Now().Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("GrantHouseholdAccess: %v", err)
	}

	// Before revocation: helper has reach.
	if _, err := server.ListHouseholdGrants(authzCtxFor("helper"), &pb.ListHouseholdGrantsRequest{HouseholdId: household}); err != nil {
		t.Fatalf("test setup: ListHouseholdGrants by an active grantee failed: %v", err)
	}

	if _, err := server.RevokeHouseholdAccess(authzCtxFor("alice"), &pb.RevokeHouseholdAccessRequest{GrantId: grantResp.GrantId}); err != nil {
		t.Fatalf("RevokeHouseholdAccess: %v", err)
	}

	_, err = server.ListHouseholdGrants(authzCtxFor("helper"), &pb.ListHouseholdGrantsRequest{HouseholdId: household})
	if err == nil {
		t.Fatal("ListHouseholdGrants succeeded for a just-revoked grantee's very next request, want it refused immediately")
	}
}

// -- ListHouseholdGrants: visibility, expiry, and identity/expiry fields ----

// TestListHouseholdGrants_MemberSeesActiveGrant_DisappearsOnExpiry is FR7's
// "visible while active with grantee identity and expiry" plus "the grant
// disappears from the active list on expiry": a member sees an active
// grant's grantee_subject/granted_by_subject/expires_at, and a second,
// already-expired grant on the same household does not appear at all.
func TestListHouseholdGrants_MemberSeesActiveGrant_DisappearsOnExpiry(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	household := insertHousehold(t, pool)
	insertMembership(t, pool, household, "alice")
	expiresAt := time.Now().Add(2 * time.Hour)
	insertGrant(t, pool, household, "helper", "alice", expiresAt)
	insertGrant(t, pool, household, "expired-helper", "alice", time.Now().Add(-time.Minute))

	resp, err := server.ListHouseholdGrants(authzCtxFor("alice"), &pb.ListHouseholdGrantsRequest{HouseholdId: household})
	if err != nil {
		t.Fatalf("ListHouseholdGrants: %v", err)
	}
	if len(resp.Grants) != 1 {
		t.Fatalf("len(Grants) = %d, want 1 (the expired grant must not appear)", len(resp.Grants))
	}

	got := resp.Grants[0]
	if got.GranteeSubject != "helper" {
		t.Errorf("GranteeSubject = %q, want %q", got.GranteeSubject, "helper")
	}
	if got.GrantedBySubject != "alice" {
		t.Errorf("GrantedBySubject = %q, want %q", got.GrantedBySubject, "alice")
	}
	if got.ExpiresAt == nil {
		t.Fatal("ExpiresAt is nil, want the grant's expiry")
	}
	if gotExpiry := contract.FromInstant(got.ExpiresAt); gotExpiry.Sub(expiresAt).Abs() > time.Second {
		t.Errorf("ExpiresAt = %v, want approximately %v", gotExpiry, expiresAt)
	}
	for _, g := range resp.Grants {
		if g.GranteeSubject == "expired-helper" {
			t.Error("an already-expired grant appeared in ListHouseholdGrants, want it excluded (FR7: disappears from the active list on expiry)")
		}
	}
}

// -- FR8.1: reads under a grant are audited, member reads are not ------------

// TestListHouseholdGrants_GranteeReadIsAudited_MemberReadIsNot is FR8.1's
// "reads performed under a granted (non-member) identity produce an audit
// record" alongside its converse, both stated explicitly in this task's
// Testing section: a grantee calling ListHouseholdGrants writes an audit
// row (actor_kind = grantee); a member calling the exact same RPC on the
// exact same household does not.
func TestListHouseholdGrants_GranteeReadIsAudited_MemberReadIsNot(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	household := insertHousehold(t, pool)
	insertMembership(t, pool, household, "alice")
	insertGrant(t, pool, household, "helper", "alice", time.Now().Add(time.Hour))

	if before := countRows(t, pool, "audit_log"); before != 0 {
		t.Fatalf("test setup: audit_log has %d rows before any read, want 0", before)
	}

	if _, err := server.ListHouseholdGrants(authzCtxFor("alice"), &pb.ListHouseholdGrantsRequest{HouseholdId: household}); err != nil {
		t.Fatalf("ListHouseholdGrants (member): %v", err)
	}
	if n := countRows(t, pool, "audit_log"); n != 0 {
		t.Errorf("audit_log row count after a member's ListHouseholdGrants read = %d, want 0 (FR8.1 covers granted reads only)", n)
	}

	if _, err := server.ListHouseholdGrants(authzCtxFor("helper"), &pb.ListHouseholdGrantsRequest{HouseholdId: household}); err != nil {
		t.Fatalf("ListHouseholdGrants (grantee): %v", err)
	}
	if n := countRows(t, pool, "audit_log"); n != 1 {
		t.Fatalf("audit_log row count after a grantee's ListHouseholdGrants read = %d, want exactly 1", n)
	}

	var actorSubject, actorKind, action, entityKind string
	var targetHouseholdID int64
	if err := pool.QueryRow(context.Background(), `
		SELECT actor_subject, actor_kind, target_household_id, action, entity_kind FROM audit_log
	`).Scan(&actorSubject, &actorKind, &targetHouseholdID, &action, &entityKind); err != nil {
		t.Fatalf("read audit_log row: %v", err)
	}
	if actorSubject != "helper" {
		t.Errorf("actor_subject = %q, want %q", actorSubject, "helper")
	}
	if actorKind != "grantee" {
		t.Errorf("actor_kind = %q, want %q (FR7: actor_kind distinguishing grantee from member)", actorKind, "grantee")
	}
	if targetHouseholdID != household {
		t.Errorf("target_household_id = %d, want %d", targetHouseholdID, household)
	}
	if action != "ListHouseholdGrants" {
		t.Errorf("action = %q, want %q", action, "ListHouseholdGrants")
	}
	if entityKind != "household_grant" {
		t.Errorf("entity_kind = %q, want %q", entityKind, "household_grant")
	}
}

// -- FR8: GrantHouseholdAccess/RevokeHouseholdAccess are audited -------------

// TestGrantAndRevokeHouseholdAccess_ProduceAuditRows_WithCorrectActorKind
// proves every grant and every revocation writes an audit row (FR8), and
// that actor_kind reflects the caller's actual role: GrantHouseholdAccess
// is always called by a member (it's one of the three exclusions, so a
// grantee never reaches the write itself -- see the excluded-caller test
// above), but RevokeHouseholdAccess is ordinary and callable by either, so
// its audit row's actor_kind must track which one actually called it.
func TestGrantAndRevokeHouseholdAccess_ProduceAuditRows_WithCorrectActorKind(t *testing.T) {
	server, pool := newAuthzTestServer(t)

	household := insertHousehold(t, pool)
	insertMembership(t, pool, household, "alice")

	grantResp, err := server.GrantHouseholdAccess(authzCtxFor("alice"), &pb.GrantHouseholdAccessRequest{
		HouseholdId:    household,
		GranteeSubject: "helper",
		ExpiresAt:      contract.ToInstant(time.Now().Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("GrantHouseholdAccess: %v", err)
	}

	var grantActorKind, grantAction string
	if err := pool.QueryRow(context.Background(), `
		SELECT actor_kind, action FROM audit_log WHERE action = 'GrantHouseholdAccess'
	`).Scan(&grantActorKind, &grantAction); err != nil {
		t.Fatalf("read GrantHouseholdAccess audit row: %v", err)
	}
	if grantActorKind != "member" {
		t.Errorf("GrantHouseholdAccess audit actor_kind = %q, want %q", grantActorKind, "member")
	}

	// A second grant, revoked by the grantee themselves -- proves
	// RevokeHouseholdAccess's audit row tracks the caller's real role.
	secondGrantID := insertGrant(t, pool, household, "helper2", "alice", time.Now().Add(time.Hour))
	if _, err := server.RevokeHouseholdAccess(authzCtxFor("helper"), &pb.RevokeHouseholdAccessRequest{GrantId: secondGrantID}); err != nil {
		t.Fatalf("RevokeHouseholdAccess (by grantee): %v", err)
	}

	var revokeByGranteeActorKind string
	if err := pool.QueryRow(context.Background(), `
		SELECT actor_kind FROM audit_log WHERE action = 'RevokeHouseholdAccess' AND entity_id = $1
	`, formatGrantID(secondGrantID)).Scan(&revokeByGranteeActorKind); err != nil {
		t.Fatalf("read RevokeHouseholdAccess (by grantee) audit row: %v", err)
	}
	if revokeByGranteeActorKind != "grantee" {
		t.Errorf("RevokeHouseholdAccess audit actor_kind when called by a grantee = %q, want %q", revokeByGranteeActorKind, "grantee")
	}

	// The original grant, revoked by the member who created it -- audit
	// actor_kind must reflect member, not grantee.
	if _, err := server.RevokeHouseholdAccess(authzCtxFor("alice"), &pb.RevokeHouseholdAccessRequest{GrantId: grantResp.GrantId}); err != nil {
		t.Fatalf("RevokeHouseholdAccess (by member): %v", err)
	}
	var revokeByMemberActorKind string
	if err := pool.QueryRow(context.Background(), `
		SELECT actor_kind FROM audit_log WHERE action = 'RevokeHouseholdAccess' AND entity_id = $1
	`, formatGrantID(grantResp.GrantId)).Scan(&revokeByMemberActorKind); err != nil {
		t.Fatalf("read RevokeHouseholdAccess (by member) audit row: %v", err)
	}
	if revokeByMemberActorKind != "member" {
		t.Errorf("RevokeHouseholdAccess audit actor_kind when called by a member = %q, want %q", revokeByMemberActorKind, "member")
	}
}

// formatGrantID mirrors server.go's RevokeHouseholdAccess entity_id
// encoding (strconv.FormatInt(req.GrantId, 10)) -- audit_log.entity_id is
// TEXT, keyed by grant_id as a decimal string.
func formatGrantID(id int64) string {
	return strconv.FormatInt(id, 10)
}
