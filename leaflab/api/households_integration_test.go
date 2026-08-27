//go:build integration

// Real-Postgres integration coverage for FR75/FR7's households and
// membership write paths, added by #1341: CreateHousehold's first-claim
// entrance, InviteMember/RemoveMember's SCD2 open/close (never a DELETE),
// RenameHousehold, the never-zero-members guard at both the application
// layer (Repository.RemoveMember's lock-and-count) and the database layer
// (migration 020_household_never_zero_members's trigger, reproduced here),
// the concurrency scenario named in the issue's Testing section, FR7's
// member-only exclusion (a grantee or an elevated admin cannot change
// membership even though either may hold a Scope that otherwise permits
// this household), and FR8 audit rows for all four writes.
//
// Schema is self-contained hand-written DDL, mirroring migration
// 015_ownership's household/household_membership shape and 020's trigger
// verbatim (see that migration's comment for why the trigger locks the
// household row before counting) plus audit_log's column set
// (postgres.go's INSERT). Deliberately narrower than the real migrations --
// no board/region/plant tables -- since none of this file's tests need
// them: "second principal's Scope immediately includes the household's
// boards" is proved via authz.Scope.Permits against a constructed
// Resolution, exactly as authz/scope_test.go does, since Scope is
// entity-agnostic and keyed only by household id (see authz/scope.go's
// doc comment) -- no real board row is needed to prove a HouseholdScope
// permits one.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:households_integration_test --test_output=all
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/claim"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

// householdsTestSchema mirrors migration 015_ownership's household /
// household_membership column set and migration
// 020_household_never_zero_members's trigger verbatim (same function body,
// same BEFORE UPDATE OF valid_to ... WHEN clause) -- this file's DB-trigger
// tests must exercise the actual guard, not a stand-in, so the trigger
// definition here is a literal copy, not a paraphrase. audit_log mirrors
// postgres.go's INSERT column set, same as dbtest_helpers_integration_test.go's
// testSchema.
const householdsTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY,
		name         TEXT NOT NULL,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE household_membership (
		household_membership_id BIGSERIAL PRIMARY KEY,
		household_id             BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
		principal_subject        TEXT NOT NULL,
		valid_from                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to                  TIMESTAMPTZ
	);
	CREATE INDEX idx_household_membership_household_id_current
		ON household_membership(household_id) WHERE valid_to IS NULL;
	CREATE INDEX idx_household_membership_principal_subject_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	CREATE TABLE audit_log (
		audit_id             BIGSERIAL PRIMARY KEY,
		actor_subject        TEXT NOT NULL,
		actor_kind           TEXT NOT NULL,
		target_household_id  BIGINT NULL REFERENCES household(household_id),
		action                TEXT NOT NULL,
		entity_kind           TEXT NOT NULL,
		entity_id             TEXT NULL,
		reason                TEXT NULL,
		occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		correlation_id        TEXT NULL
	);

	-- Verbatim copy of migration 020_household_never_zero_members.up.sql's
	-- trigger -- see that file for the doc comment explaining the lock.
	CREATE FUNCTION enforce_household_never_zero_members() RETURNS TRIGGER AS $$
	DECLARE
		remaining INT;
	BEGIN
		PERFORM 1 FROM household WHERE household_id = OLD.household_id FOR UPDATE;

		SELECT COUNT(*) INTO remaining
		FROM household_membership
		WHERE household_id = OLD.household_id
		  AND valid_to IS NULL
		  AND household_membership_id <> OLD.household_membership_id;

		IF remaining = 0 THEN
			RAISE EXCEPTION 'household % would have zero members; refused (FR75)', OLD.household_id
				USING ERRCODE = 'LL001';
		END IF;

		RETURN NEW;
	END;
	$$ LANGUAGE plpgsql;

	CREATE TRIGGER trg_household_membership_never_zero
		BEFORE UPDATE OF valid_to ON household_membership
		FOR EACH ROW
		WHEN (OLD.valid_to IS NULL AND NEW.valid_to IS NOT NULL)
		EXECUTE FUNCTION enforce_household_never_zero_members();
`

// grantOrElevationStubAuthz stands in for FR7's grant Scope and FR10's
// elevation Scope, neither of which exists yet (both land in sibling
// tasks) -- per the issue's "stub the grant"/"stub elevation until FR10
// lands" instructions. It always returns a Scope that Permits the named
// household, regardless of the caller's actual household_membership rows,
// so a test using it proves requireHouseholdMember's refusal comes from
// the direct household_membership check (Repository.IsCurrentHouseholdMember)
// and not from consulting Scope -- exactly the FR7 exclusion this task
// must enforce even though the real grant/elevation Scope types don't
// land here.
type grantOrElevationStubAuthz struct {
	scope authz.Scope
}

func (g grantOrElevationStubAuthz) ScopeForPrincipal(ctx context.Context, principalSubject string) (authz.Scope, error) {
	return g.scope, nil
}

func (g grantOrElevationStubAuthz) ResolveBoardByDeviceID(ctx context.Context, deviceID string) (authz.EntityRef, authz.Resolution, error) {
	panic("not used by this file's tests")
}

// newHouseholdsTestServer starts a real Postgres container with
// householdsTestSchema applied, and returns a LeafLabAPIServer backed by a
// real Repository and a real authz.PGResolver, plus both for direct
// fixture setup/assertions.
func newHouseholdsTestServer(t *testing.T) (*LeafLabAPIServer, *Repository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: householdsTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	server := NewLeafLabAPIServer(repo, resolver, nil, nil, discardLoggerHouseholds(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))
	return server, repo, db.Pool
}

// discardLoggerHouseholds is a local copy of dbtest_helpers_integration_test.go's
// discardLogger -- this file deliberately doesn't share srcs with that file
// (see BUILD.bazel) so it can carry its own schema without a name
// collision on testSchema/newTestServer/insertBoard, none of which this
// file's tests need.
func discardLoggerHouseholds() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func householdsCtxFor(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject})
}

func insertHouseholdRow(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household (name) VALUES ($1) RETURNING household_id`, name).Scan(&id); err != nil {
		t.Fatalf("insert household %q: %v", name, err)
	}
	return id
}

func insertMembershipRow(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

func currentMemberCount(t *testing.T, pool *pgxpool.Pool, householdID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM household_membership WHERE household_id = $1 AND valid_to IS NULL`,
		householdID).Scan(&n); err != nil {
		t.Fatalf("count current members of household %d: %v", householdID, err)
	}
	return n
}

func isCurrentMember(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `
		SELECT EXISTS(
			SELECT 1 FROM household_membership
			WHERE household_id = $1 AND principal_subject = $2 AND valid_to IS NULL
		)
	`, householdID, subject).Scan(&exists); err != nil {
		t.Fatalf("check current membership for %q: %v", subject, err)
	}
	return exists
}

// auditRowFor returns the single audit_log row matching action/entityKind,
// failing the test if there isn't exactly one -- every test below performs
// exactly one audited write, so "exactly one row, with these fields" is
// the assertion FR8 requires.
type auditRow struct {
	actorSubject      string
	targetHouseholdID *int64
	entityID          *string
}

func auditRowFor(t *testing.T, pool *pgxpool.Pool, action, entityKind string) auditRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT actor_subject, target_household_id, entity_id
		FROM audit_log
		WHERE action = $1 AND entity_kind = $2
	`, action, entityKind)
	if err != nil {
		t.Fatalf("query audit_log for action=%s entity_kind=%s: %v", action, entityKind, err)
	}
	defer rows.Close()

	var found []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.actorSubject, &r.targetHouseholdID, &r.entityID); err != nil {
			t.Fatalf("scan audit_log row: %v", err)
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit_log rows: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("audit_log rows for action=%s entity_kind=%s = %d, want exactly 1", action, entityKind, len(found))
	}
	return found[0]
}

// -- CreateHousehold: first-claim entrance -----------------------------------

// TestCreateHousehold_FirstClaim_ExactlyOneMembership is the issue's first
// Testing bullet: a principal with no household ends up a member of
// exactly one after a stubbed first claim (CreateHousehold is the only
// creation entrance -- FR76's real claim path lands in a sibling task, so
// calling CreateHousehold directly stands in for "reached via first
// claim").
func TestCreateHousehold_FirstClaim_ExactlyOneMembership(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)

	resp, err := server.CreateHousehold(householdsCtxFor("son@example.com"), &pb.CreateHouseholdRequest{Name: "The Sons"})
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}
	if resp.GetHousehold().GetHouseholdId() == 0 {
		t.Fatal("CreateHousehold returned a zero household_id")
	}

	if n := currentMemberCount(t, pool, resp.GetHousehold().GetHouseholdId()); n != 1 {
		t.Errorf("current member count after first claim = %d, want exactly 1", n)
	}
	if !isCurrentMember(t, pool, resp.GetHousehold().GetHouseholdId(), "son@example.com") {
		t.Error("the claiming principal is not a current member of the household they just created")
	}
}

// TestCreateHousehold_AlreadyHasHousehold_Refused proves the "no second
// creation entrance" rule: a principal who already holds a current
// household_membership row anywhere is refused, not given a second
// household.
func TestCreateHousehold_AlreadyHasHousehold_Refused(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)
	existing := insertHouseholdRow(t, pool, "Existing")
	insertMembershipRow(t, pool, existing, "son@example.com")

	_, err := server.CreateHousehold(householdsCtxFor("son@example.com"), &pb.CreateHouseholdRequest{})
	if err == nil {
		t.Fatal("CreateHousehold for a principal who already has a household returned nil error, want a refusal")
	}
	failure, ok := contract.FromError(err)
	if !ok {
		t.Fatal("refusal carries no Failure detail")
	}
	if failure.GetClass() != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("failure class = %q, want %q", failure.GetClass(), contract.FailureRefusedWithAlternative)
	}

	if n := countRowsIn(t, pool, "household"); n != 1 {
		t.Errorf("household row count = %d, want 1 -- a second household must not have been created", n)
	}
}

// -- InviteMember: SCD2 open, immediate Scope reach --------------------------

// TestInviteMember_InviteeScopeImmediatelyIncludesHouseholdBoards is the
// issue's second Testing bullet: a member invites a second principal; the
// second principal's Scope immediately includes the household's boards.
// Proved via authz.Scope.Permits against a constructed Resolution -- see
// this file's doc comment for why no real board row is needed.
func TestInviteMember_InviteeScopeImmediatelyIncludesHouseholdBoards(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Household")
	insertMembershipRow(t, pool, household, "member@example.com")

	resp, err := server.InviteMember(householdsCtxFor("member@example.com"), &pb.InviteMemberRequest{
		HouseholdId:      household,
		PrincipalSubject: "invitee@example.com",
	})
	if err != nil {
		t.Fatalf("InviteMember: %v", err)
	}
	if resp.GetMember().GetPrincipalSubject() != "invitee@example.com" {
		t.Errorf("InviteMember response principal_subject = %q, want %q", resp.GetMember().GetPrincipalSubject(), "invitee@example.com")
	}

	resolver := authz.NewPGResolver(pool)
	scope, err := resolver.ScopeForPrincipal(context.Background(), "invitee@example.com")
	if err != nil {
		t.Fatalf("ScopeForPrincipal: %v", err)
	}
	boardRef := authz.EntityRef{Kind: authz.EntityBoard, ID: 999}
	res := authz.Resolution{HouseholdID: household}
	if !scope.Permits(boardRef, res) {
		t.Error("invitee's Scope does not immediately include the household's boards after InviteMember")
	}
}

// TestInviteMember_AlreadyMember_Refused proves the ErrHouseholdAlreadyMember
// guard: inviting a current member again is refused, not a silent no-op or
// a duplicate open row.
func TestInviteMember_AlreadyMember_Refused(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Household")
	insertMembershipRow(t, pool, household, "member@example.com")

	_, err := server.InviteMember(householdsCtxFor("member@example.com"), &pb.InviteMemberRequest{
		HouseholdId:      household,
		PrincipalSubject: "member@example.com",
	})
	if err == nil {
		t.Fatal("InviteMember for an already-current member returned nil error, want a refusal")
	}
	if n := currentMemberCount(t, pool, household); n != 1 {
		t.Errorf("current member count after refused re-invite = %d, want 1 (no duplicate row)", n)
	}
}

// -- RemoveMember: SCD2 close, never-zero-members, never a DELETE -----------

// TestRemoveMember_RemovedScopeExcluded_ValidToSet is the issue's third
// Testing bullet: a member removes another member; the removed
// principal's scope no longer includes the household, and their prior
// household_membership row has valid_to set, not deleted.
func TestRemoveMember_RemovedScopeExcluded_ValidToSet(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Household")
	insertMembershipRow(t, pool, household, "remover@example.com")
	insertMembershipRow(t, pool, household, "removed@example.com")

	_, err := server.RemoveMember(householdsCtxFor("remover@example.com"), &pb.RemoveMemberRequest{
		HouseholdId:      household,
		PrincipalSubject: "removed@example.com",
	})
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	if isCurrentMember(t, pool, household, "removed@example.com") {
		t.Error("removed principal is still a current member after RemoveMember")
	}

	resolver := authz.NewPGResolver(pool)
	scope, err := resolver.ScopeForPrincipal(context.Background(), "removed@example.com")
	if err != nil {
		t.Fatalf("ScopeForPrincipal: %v", err)
	}
	if scope.Permits(authz.EntityRef{Kind: authz.EntityBoard, ID: 1}, authz.Resolution{HouseholdID: household}) {
		t.Error("removed principal's Scope still includes the household after RemoveMember")
	}

	// The row was closed (valid_to set), never deleted.
	var validTo *string
	var rowCount int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*), MAX(valid_to::text)
		FROM household_membership
		WHERE household_id = $1 AND principal_subject = $2
	`, household, "removed@example.com").Scan(&rowCount, &validTo); err != nil {
		t.Fatalf("query closed membership row: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("household_membership row count for removed principal = %d, want 1 (closed, not deleted)", rowCount)
	}
	if validTo == nil {
		t.Error("removed principal's household_membership row has NULL valid_to -- want it set (closed)")
	}
}

// TestRemoveMember_LastMember_RefusedWithAlternative is the issue's fourth
// Testing bullet: removing the last member is refused with a named
// alternative (FR59.3).
func TestRemoveMember_LastMember_RefusedWithAlternative(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Household")
	insertMembershipRow(t, pool, household, "sole@example.com")

	_, err := server.RemoveMember(householdsCtxFor("sole@example.com"), &pb.RemoveMemberRequest{
		HouseholdId:      household,
		PrincipalSubject: "sole@example.com",
	})
	if err == nil {
		t.Fatal("RemoveMember for the last member returned nil error, want a refusal")
	}
	failure, ok := contract.FromError(err)
	if !ok {
		t.Fatal("refusal carries no Failure detail")
	}
	if failure.GetClass() != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("failure class = %q, want %q", failure.GetClass(), contract.FailureRefusedWithAlternative)
	}
	if failure.GetAlternative() == "" {
		t.Error("refusal names no alternative -- FR59.3 requires refuse-and-name-the-alternative")
	}

	if n := currentMemberCount(t, pool, household); n != 1 {
		t.Errorf("current member count after refused last-member removal = %d, want still 1", n)
	}
}

// TestRemoveMember_DBTriggerBackstopsAppLevelGuard proves the issue's
// explicit "also with a database-level guard" requirement independently of
// Repository.RemoveMember's own lock-and-count: a raw SQL UPDATE that
// closes the last remaining member's row -- bypassing the repository
// entirely -- is refused by the trigger, not just by application code.
func TestRemoveMember_DBTriggerBackstopsAppLevelGuard(t *testing.T) {
	_, _, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Household")
	insertMembershipRow(t, pool, household, "sole@example.com")

	var membershipID int64
	if err := pool.QueryRow(context.Background(),
		`SELECT household_membership_id FROM household_membership WHERE household_id = $1 AND principal_subject = $2`,
		household, "sole@example.com").Scan(&membershipID); err != nil {
		t.Fatalf("look up membership row: %v", err)
	}

	_, err := pool.Exec(context.Background(),
		`UPDATE household_membership SET valid_to = NOW() WHERE household_membership_id = $1`, membershipID)
	if err == nil {
		t.Fatal("raw SQL UPDATE closing the last member's row succeeded, want the trigger to refuse it")
	}

	if n := currentMemberCount(t, pool, household); n != 1 {
		t.Errorf("current member count after refused raw-SQL close = %d, want still 1", n)
	}
}

// TestRemoveMember_ConcurrentRemovals_LeavesAtLeastOneMember is the
// issue's named concurrency scenario: two simultaneous removals of the
// last two members leave at least one member.
func TestRemoveMember_ConcurrentRemovals_LeavesAtLeastOneMember(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Household")
	insertMembershipRow(t, pool, household, "alice@example.com")
	insertMembershipRow(t, pool, household, "bob@example.com")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	subjects := []string{"alice@example.com", "bob@example.com"}
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := server.RemoveMember(householdsCtxFor(subjects[i]), &pb.RemoveMemberRequest{
				HouseholdId:      household,
				PrincipalSubject: subjects[i],
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	succeeded, refused := 0, 0
	for _, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		if errors.Is(err, ErrHouseholdLastMember) {
			refused++
			continue
		}
		failure, ok := contract.FromError(err)
		if ok && failure.GetClass() == string(contract.FailureRefusedWithAlternative) {
			refused++
			continue
		}
		t.Fatalf("unexpected error from concurrent RemoveMember: %v", err)
	}
	if succeeded != 1 || refused != 1 {
		t.Errorf("succeeded=%d refused=%d, want exactly one of each -- both must not succeed", succeeded, refused)
	}

	if n := currentMemberCount(t, pool, household); n != 1 {
		t.Errorf("current member count after concurrent removal of the last two members = %d, want exactly 1 (never zero)", n)
	}
}

// -- RenameHousehold ----------------------------------------------------------

func TestRenameHousehold_MemberRenames(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Old Name")
	insertMembershipRow(t, pool, household, "member@example.com")

	resp, err := server.RenameHousehold(householdsCtxFor("member@example.com"), &pb.RenameHouseholdRequest{
		HouseholdId: household,
		Name:        "New Name",
	})
	if err != nil {
		t.Fatalf("RenameHousehold: %v", err)
	}
	if resp.GetHousehold().GetName() != "New Name" {
		t.Errorf("RenameHousehold response name = %q, want %q", resp.GetHousehold().GetName(), "New Name")
	}

	var name string
	if err := pool.QueryRow(context.Background(), `SELECT name FROM household WHERE household_id = $1`, household).Scan(&name); err != nil {
		t.Fatalf("query renamed household: %v", err)
	}
	if name != "New Name" {
		t.Errorf("household.name in DB = %q, want %q", name, "New Name")
	}
}

// -- FR7: member-only, never conferred by a grant or by elevation -----------

// TestInviteMember_NonMemberRefused proves the baseline: a caller with no
// household_membership row at all cannot invite into a household they
// don't belong to.
func TestInviteMember_NonMemberRefused(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Household")
	insertMembershipRow(t, pool, household, "member@example.com")

	_, err := server.InviteMember(householdsCtxFor("stranger@example.com"), &pb.InviteMemberRequest{
		HouseholdId:      household,
		PrincipalSubject: "friend@example.com",
	})
	if err == nil {
		t.Fatal("InviteMember by a non-member returned nil error, want a refusal")
	}
	failure, ok := contract.FromError(err)
	if !ok || failure.GetClass() != string(contract.FailurePermissionDenied) {
		t.Errorf("failure class = %v, want %q", failure, contract.FailurePermissionDenied)
	}
	if n := currentMemberCount(t, pool, household); n != 1 {
		t.Errorf("current member count after refused invite = %d, want still 1", n)
	}
}

// TestInviteMember_GranteeRefused is the issue's "a grantee attempting
// InviteMember/RemoveMember is refused (stub the grant until FR7 lands)"
// bullet: the caller's Scope permits this household (grantOrElevationStubAuthz
// stands in for a not-yet-existing grant Scope), but they hold no
// household_membership row, so requireHouseholdMember must still refuse --
// proving the check is membership-specific, not Scope-based.
func TestInviteMember_GranteeRefused(t *testing.T) {
	_, repo, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Household")
	insertMembershipRow(t, pool, household, "member@example.com")

	grantAuthz := grantOrElevationStubAuthz{scope: authz.NewHouseholdScope(household)}
	server := NewLeafLabAPIServer(repo, grantAuthz, nil, nil, discardLoggerHouseholds(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))

	_, err := server.InviteMember(householdsCtxFor("grantee@example.com"), &pb.InviteMemberRequest{
		HouseholdId:      household,
		PrincipalSubject: "friend@example.com",
	})
	if err == nil {
		t.Fatal("InviteMember by a grantee (Scope permits, no membership row) returned nil error, want a refusal")
	}
	failure, ok := contract.FromError(err)
	if !ok || failure.GetClass() != string(contract.FailurePermissionDenied) {
		t.Errorf("failure class = %v, want %q", failure, contract.FailurePermissionDenied)
	}
	if n := currentMemberCount(t, pool, household); n != 1 {
		t.Errorf("current member count after refused grantee invite = %d, want still 1", n)
	}
}

// TestRemoveMember_ElevatedAdminRefused is the issue's "an elevated admin
// attempting a membership change is refused (stub elevation until FR10
// lands)" bullet -- same mechanic as TestInviteMember_GranteeRefused
// (Scope permits, no membership row), applied to RemoveMember.
func TestRemoveMember_ElevatedAdminRefused(t *testing.T) {
	_, repo, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Household")
	insertMembershipRow(t, pool, household, "member@example.com")

	elevatedAuthz := grantOrElevationStubAuthz{scope: authz.NewHouseholdScope(household)}
	server := NewLeafLabAPIServer(repo, elevatedAuthz, nil, nil, discardLoggerHouseholds(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))

	_, err := server.RemoveMember(householdsCtxFor("admin@example.com"), &pb.RemoveMemberRequest{
		HouseholdId:      household,
		PrincipalSubject: "member@example.com",
	})
	if err == nil {
		t.Fatal("RemoveMember by an elevated admin (Scope permits, no membership row) returned nil error, want a refusal")
	}
	failure, ok := contract.FromError(err)
	if !ok || failure.GetClass() != string(contract.FailurePermissionDenied) {
		t.Errorf("failure class = %v, want %q", failure, contract.FailurePermissionDenied)
	}
	if n := currentMemberCount(t, pool, household); n != 1 {
		t.Errorf("current member count after refused elevated-admin removal = %d, want still 1", n)
	}
}

// TestRenameHousehold_GranteeRefused rounds out the three write RPCs named
// in the requirement text ("InviteMember/RemoveMember/RenameHousehold" all
// use requireHouseholdMember) with the same Scope-permits/no-membership
// mechanic.
func TestRenameHousehold_GranteeRefused(t *testing.T) {
	_, repo, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Household")
	insertMembershipRow(t, pool, household, "member@example.com")

	grantAuthz := grantOrElevationStubAuthz{scope: authz.NewHouseholdScope(household)}
	server := NewLeafLabAPIServer(repo, grantAuthz, nil, nil, discardLoggerHouseholds(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))

	_, err := server.RenameHousehold(householdsCtxFor("grantee@example.com"), &pb.RenameHouseholdRequest{
		HouseholdId: household,
		Name:        "Hijacked Name",
	})
	if err == nil {
		t.Fatal("RenameHousehold by a grantee returned nil error, want a refusal")
	}
	var name string
	if err := pool.QueryRow(context.Background(), `SELECT name FROM household WHERE household_id = $1`, household).Scan(&name); err != nil {
		t.Fatalf("query household: %v", err)
	}
	if name != "Household" {
		t.Errorf("household.name = %q after a refused grantee rename, want unchanged %q", name, "Household")
	}
}

// -- FR8: audit rows for all four writes -------------------------------------

func TestCreateHousehold_ProducesAuditRow(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)

	resp, err := server.CreateHousehold(householdsCtxFor("son@example.com"), &pb.CreateHouseholdRequest{})
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}

	row := auditRowFor(t, pool, "CreateHousehold", "household_membership")
	if row.actorSubject != "son@example.com" {
		t.Errorf("audit actor_subject = %q, want %q", row.actorSubject, "son@example.com")
	}
	if row.targetHouseholdID == nil || *row.targetHouseholdID != resp.GetHousehold().GetHouseholdId() {
		t.Errorf("audit target_household_id = %v, want %d", row.targetHouseholdID, resp.GetHousehold().GetHouseholdId())
	}
	if row.entityID == nil || *row.entityID != "son@example.com" {
		t.Errorf("audit entity_id (subject) = %v, want %q", row.entityID, "son@example.com")
	}
}

func TestInviteMember_ProducesAuditRow(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Household")
	insertMembershipRow(t, pool, household, "member@example.com")

	_, err := server.InviteMember(householdsCtxFor("member@example.com"), &pb.InviteMemberRequest{
		HouseholdId:      household,
		PrincipalSubject: "invitee@example.com",
	})
	if err != nil {
		t.Fatalf("InviteMember: %v", err)
	}

	row := auditRowFor(t, pool, "InviteMember", "household_membership")
	if row.actorSubject != "member@example.com" {
		t.Errorf("audit actor_subject (acting member) = %q, want %q", row.actorSubject, "member@example.com")
	}
	if row.entityID == nil || *row.entityID != "invitee@example.com" {
		t.Errorf("audit entity_id (invited principal) = %v, want %q", row.entityID, "invitee@example.com")
	}
}

func TestRemoveMember_ProducesAuditRow(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Household")
	insertMembershipRow(t, pool, household, "remover@example.com")
	insertMembershipRow(t, pool, household, "removed@example.com")

	_, err := server.RemoveMember(householdsCtxFor("remover@example.com"), &pb.RemoveMemberRequest{
		HouseholdId:      household,
		PrincipalSubject: "removed@example.com",
	})
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	row := auditRowFor(t, pool, "RemoveMember", "household_membership")
	if row.actorSubject != "remover@example.com" {
		t.Errorf("audit actor_subject (acting member) = %q, want %q", row.actorSubject, "remover@example.com")
	}
	if row.entityID == nil || *row.entityID != "removed@example.com" {
		t.Errorf("audit entity_id (removed principal) = %v, want %q", row.entityID, "removed@example.com")
	}
}

func TestRenameHousehold_ProducesAuditRow(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)
	household := insertHouseholdRow(t, pool, "Old Name")
	insertMembershipRow(t, pool, household, "member@example.com")

	_, err := server.RenameHousehold(householdsCtxFor("member@example.com"), &pb.RenameHouseholdRequest{
		HouseholdId: household,
		Name:        "New Name",
	})
	if err != nil {
		t.Fatalf("RenameHousehold: %v", err)
	}

	row := auditRowFor(t, pool, "RenameHousehold", "household")
	if row.actorSubject != "member@example.com" {
		t.Errorf("audit actor_subject = %q, want %q", row.actorSubject, "member@example.com")
	}
	if row.targetHouseholdID == nil || *row.targetHouseholdID != household {
		t.Errorf("audit target_household_id = %v, want %d", row.targetHouseholdID, household)
	}
}

// -- Validation: household_membership is never DELETEd -----------------------

// TestHouseholdMembership_NeverDeleted is the Validation section's "No
// household_membership row is ever DELETEd — assert with a trigger test or
// a repository audit" bullet, satisfied here as a repository-level audit:
// row count only ever increases across a sequence of creates/invites/
// removes, matching the number of household_membership INSERTs performed,
// even though members have been both invited and removed.
func TestHouseholdMembership_NeverDeleted(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)

	created, err := server.CreateHousehold(householdsCtxFor("son@example.com"), &pb.CreateHouseholdRequest{})
	if err != nil {
		t.Fatalf("CreateHousehold: %v", err)
	}
	household := created.GetHousehold().GetHouseholdId()

	if _, err := server.InviteMember(householdsCtxFor("son@example.com"), &pb.InviteMemberRequest{
		HouseholdId: household, PrincipalSubject: "mother@example.com",
	}); err != nil {
		t.Fatalf("InviteMember: %v", err)
	}
	if _, err := server.RemoveMember(householdsCtxFor("son@example.com"), &pb.RemoveMemberRequest{
		HouseholdId: household, PrincipalSubject: "mother@example.com",
	}); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	// Two INSERTs happened: the initial member (CreateHousehold) and the
	// invited-then-removed one (InviteMember). RemoveMember closed, not
	// deleted, the second -- so the total row count must still be 2.
	if n := countRowsIn(t, pool, "household_membership"); n != 2 {
		t.Errorf("household_membership row count = %d, want 2 (one INSERT per create/invite, zero DELETEs)", n)
	}
}

// -- Validation: the son-puts-his-mother-on-the-boards path, no admin -------

// TestSonPutsMotherOnBoards_EndToEnd_NoAdminPrincipal is the Validation
// section's named end-to-end path: a principal with no household claims
// one (CreateHousehold), invites a second principal (InviteMember), and
// that second principal's Scope covers the household -- all without any
// admin/elevated principal appearing anywhere in the sequence.
func TestSonPutsMotherOnBoards_EndToEnd_NoAdminPrincipal(t *testing.T) {
	server, _, pool := newHouseholdsTestServer(t)

	created, err := server.CreateHousehold(householdsCtxFor("son@example.com"), &pb.CreateHouseholdRequest{Name: "Son's Household"})
	if err != nil {
		t.Fatalf("CreateHousehold (son's first claim): %v", err)
	}
	household := created.GetHousehold().GetHouseholdId()

	if _, err := server.InviteMember(householdsCtxFor("son@example.com"), &pb.InviteMemberRequest{
		HouseholdId:      household,
		PrincipalSubject: "mother@example.com",
	}); err != nil {
		t.Fatalf("InviteMember (son invites mother): %v", err)
	}

	resolver := authz.NewPGResolver(pool)
	motherScope, err := resolver.ScopeForPrincipal(context.Background(), "mother@example.com")
	if err != nil {
		t.Fatalf("ScopeForPrincipal (mother): %v", err)
	}
	if !motherScope.Permits(authz.EntityRef{Kind: authz.EntityBoard, ID: 1}, authz.Resolution{HouseholdID: household}) {
		t.Error("mother's Scope does not cover the household's boards after being invited")
	}

	listResp, err := server.ListHouseholdMembers(householdsCtxFor("son@example.com"), &pb.ListHouseholdMembersRequest{HouseholdId: household})
	if err != nil {
		t.Fatalf("ListHouseholdMembers: %v", err)
	}
	subjects := map[string]bool{}
	for _, m := range listResp.GetMembers() {
		subjects[m.GetPrincipalSubject()] = true
	}
	if !subjects["son@example.com"] || !subjects["mother@example.com"] {
		t.Errorf("household members = %v, want both son@example.com and mother@example.com", subjects)
	}

	// No admin/elevated principal was used anywhere above -- every call
	// used son's or mother's own subject via householdsCtxFor, and
	// requireHouseholdMember/authorizeHouseholdAccess were exercised
	// through their ordinary member paths only.
}

func countRowsIn(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	return n
}
