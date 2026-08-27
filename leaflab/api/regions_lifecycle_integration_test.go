//go:build integration

// Real-Postgres integration coverage for #1376's region lifecycle (FR50,
// FR22.2, FR22.5, NFR6.2): CreateRegion/RenameRegion/RetireRegion's
// structural rules and soft retirement, and SetRegionParent's
// parentage-immutability enforcement -- both the FR59.3 API-level refusal
// in front of migration 020's trigger, and the trigger itself as a
// backstop against a direct-SQL bypass.
//
// Member-vs-grantee coverage here is limited to member-success and
// non-member-refusal ("an unelevated admin who is not a member is
// refused" -- this branch has no admin-elevation concept that changes
// authorization outcomes, see authz.ScopeForPrincipal). The
// grantee-can-create/rename/retire tests #1376's Testing section also
// names are blocked on a missing dependency (authz.MemberOrGrantee does
// not exist on this branch lineage) -- see regions.go's doc comment and
// scope note #1427.
//
// Schema is self-contained hand-written DDL mirroring the relevant shape of
// migration 012 (v_region_path), migration 015 (household,
// household_membership) and migration 020 (region.retired_at/
// successor_region_id, v_region_household, trg_region_parentage_immutable)
// -- deliberately not shared with dbtest_helpers_integration_test.go's
// testSchema (no region/household concept at all) or
// authz_scope_integration_test.go's authzTestSchema (no region concept
// either). plant/plant_region_history are included in minimal shape so
// TestRetiredRegion_StillResolvesAttribution can exercise the real
// attribution.Resolver against a retired region. See //libs/go/dbtest's
// README for how to run integration tests like this one; same tag set as
// this package's other integration go_test targets.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:regions_lifecycle_integration_test --test_output=all
package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/attribution"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
)

const regionsTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE household_membership (
		household_membership_id BIGSERIAL PRIMARY KEY,
		household_id             BIGINT NOT NULL REFERENCES household(household_id),
		principal_subject        TEXT NOT NULL,
		valid_from                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to                  TIMESTAMPTZ
	);
	CREATE INDEX idx_household_membership_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	CREATE TABLE region (
		region_id            BIGSERIAL PRIMARY KEY,
		parent_region_id     BIGINT REFERENCES region(region_id) ON DELETE RESTRICT,
		name                 VARCHAR(255) NOT NULL,
		description          TEXT,
		household_id         BIGINT REFERENCES household(household_id),
		created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		retired_at           TIMESTAMPTZ,
		successor_region_id  BIGINT REFERENCES region(region_id)
	);
	CREATE INDEX idx_region_active ON region(region_id) WHERE retired_at IS NULL;

	CREATE TABLE sensor_reading (
		reading_id  BIGSERIAL PRIMARY KEY,
		region_id   BIGINT REFERENCES region(region_id),
		recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE plant (
		plant_id BIGSERIAL PRIMARY KEY,
		name     VARCHAR(255) NOT NULL
	);

	CREATE TABLE plant_region_history (
		plant_region_history_id BIGSERIAL PRIMARY KEY,
		plant_id                 BIGINT NOT NULL REFERENCES plant(plant_id),
		region_id                BIGINT NOT NULL REFERENCES region(region_id),
		valid_from                TIMESTAMPTZ NOT NULL,
		valid_to                  TIMESTAMPTZ
	);

	CREATE TABLE audit_log (
		audit_id             BIGSERIAL PRIMARY KEY,
		actor_subject        TEXT NOT NULL,
		actor_kind           TEXT NOT NULL,
		target_household_id  BIGINT NULL,
		action                TEXT NOT NULL,
		entity_kind           TEXT NOT NULL,
		entity_id             TEXT NULL,
		reason                TEXT NULL,
		occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		correlation_id        TEXT NULL
	);

	-- v_region_path: migration 012_views, verbatim shape.
	CREATE VIEW v_region_path AS
	WITH RECURSIVE path AS (
		SELECT
			r.region_id,
			r.name,
			r.parent_region_id,
			ARRAY[r.region_id]::BIGINT[] AS path_ids,
			ARRAY[r.name]::TEXT[]        AS path_names,
			r.name::TEXT                 AS path_name,
			0                            AS depth
		FROM region r
		WHERE r.parent_region_id IS NULL

		UNION ALL

		SELECT
			r.region_id,
			r.name,
			r.parent_region_id,
			p.path_ids   || r.region_id,
			p.path_names || r.name,
			p.path_name  || ' / ' || r.name,
			p.depth + 1
		FROM region r
		JOIN path p ON p.region_id = r.parent_region_id
	)
	SELECT region_id, name, parent_region_id, path_ids, path_names, path_name, depth FROM path;

	-- v_region_household: migration 020_region_lifecycle, verbatim shape.
	CREATE VIEW v_region_household AS
	SELECT
		r.region_id,
		r.parent_region_id,
		r.name,
		r.description,
		r.retired_at,
		r.successor_region_id,
		root.household_id
	FROM region r
	JOIN v_region_path rp ON rp.region_id = r.region_id
	JOIN region root      ON root.region_id = rp.path_ids[1];

	-- trg_region_parentage_immutable: migration 020_region_lifecycle,
	-- verbatim shape (NFR6.2's recursive-descendant-test trigger).
	CREATE FUNCTION enforce_region_parentage_immutable() RETURNS TRIGGER AS $$
	DECLARE
		v_has_reading BOOLEAN;
	BEGIN
		IF NEW.parent_region_id IS NOT DISTINCT FROM OLD.parent_region_id THEN
			RETURN NEW;
		END IF;

		WITH RECURSIVE subtree AS (
			SELECT region_id FROM region WHERE region_id = OLD.region_id

			UNION ALL

			SELECT r.region_id
			FROM region r
			JOIN subtree s ON r.parent_region_id = s.region_id
		)
		SELECT EXISTS (
			SELECT 1 FROM sensor_reading sr WHERE sr.region_id IN (SELECT region_id FROM subtree)
		) INTO v_has_reading;

		IF v_has_reading THEN
			RAISE EXCEPTION 'region % parentage is frozen: a reading has been attributed to it or a descendant region (FR50.3, NFR6.2); relocate the subtree instead (FR74)',
				OLD.region_id;
		END IF;

		RETURN NEW;
	END;
	$$ LANGUAGE plpgsql;

	CREATE TRIGGER trg_region_parentage_immutable
		BEFORE UPDATE OF parent_region_id ON region
		FOR EACH ROW
		EXECUTE FUNCTION enforce_region_parentage_immutable();
`

// newRegionsTestRepository starts a real Postgres container with
// regionsTestSchema applied and returns a *Repository plus a real
// authz.PGResolver (not stubAuthz/allScope -- these tests exercise FR7's
// member-only authorization stand-in, which needs real household/
// household_membership SQL) and the raw pool for fixture setup/assertions.
func newRegionsTestRepository(t *testing.T) (*Repository, *authz.PGResolver, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: regionsTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return repo, resolver, db.Pool
}

func insertHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household DEFAULT VALUES RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func insertMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

// scopeFor resolves subject's real authz.Scope via ScopeForPrincipal --
// exactly what server.go's scopeForCaller does for an authenticated
// caller -- rather than a hand-built Scope value, so these tests exercise
// the real membership-to-scope resolution, not just Repository's own logic.
func scopeFor(t *testing.T, resolver *authz.PGResolver, subject string) authz.Scope {
	t.Helper()
	scope, err := resolver.ScopeForPrincipal(context.Background(), subject)
	if err != nil {
		t.Fatalf("ScopeForPrincipal(%q): %v", subject, err)
	}
	return scope
}

// regionAuditEntry returns a minimal valid audit.Entry for actor -- these
// tests assert on region-lifecycle behavior and (in
// TestRegionLifecycle_EveryWriteIsAudited) on the fact an audit row was
// written, not on audit content beyond actor/entity id, which
// auditedWrite/Repository fill in themselves.
func regionAuditEntry(actor string) audit.Entry {
	return audit.Entry{
		ActorSubject: actor,
		ActorKind:    audit.ActorKindHuman,
		Action:       "RegionWrite",
		EntityKind:   "region",
	}
}

// insertRegionRaw inserts a region row directly via SQL, bypassing
// Repository entirely -- for fixture setup (seeding a tree fast) and for
// TestSetRegionParent_TriggerFiresOnDirectSQLBypass, which needs a raw
// UPDATE that bypasses the API on purpose.
func insertRegionRaw(t *testing.T, pool *pgxpool.Pool, parentRegionID *int64, name string, householdID *int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO region (parent_region_id, name, household_id) VALUES ($1, $2, $3) RETURNING region_id
	`, parentRegionID, name, householdID).Scan(&id); err != nil {
		t.Fatalf("insert region %s: %v", name, err)
	}
	return id
}

func insertReading(t *testing.T, pool *pgxpool.Pool, regionID int64, recordedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO sensor_reading (region_id, recorded_at) VALUES ($1, $2)`, regionID, recordedAt); err != nil {
		t.Fatalf("insert sensor_reading for region %d: %v", regionID, err)
	}
}

// failureOf extracts the pb.Failure detail from err, failing the test if
// none is present -- these tests assert on FR59's structured failure
// (class/entity/field/reason), never on message-string parsing.
func failureOf(t *testing.T, err error) *pb.Failure {
	t.Helper()
	f, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error carries no contract.Failure detail: %v", err)
	}
	return f
}

// -- CreateRegion: member-only authorization, structural rules ---------------

// TestCreateRegion_RootRegion_MemberSucceeds_NonMemberRefused proves FR50.1's
// "member-or-grantee, not admin" capability model down to its member half: a
// household member creates a root region ("Room") and it's anchored to their
// own current household (CreateRegion's no-parent case); a caller who is not
// a member of any household -- "an unelevated admin who is not a member is
// refused", since this branch has no admin-elevation concept that would
// change the outcome -- is refused.
func TestCreateRegion_RootRegion_MemberSucceeds_NonMemberRefused(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")

	aliceScope := scopeFor(t, resolver, "alice")
	row, err := repo.CreateRegion(ctx, nil, "Living Room", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (member, root region): %v", err)
	}
	if row.Name != "Living Room" {
		t.Errorf("Name = %q, want %q", row.Name, "Living Room")
	}
	if row.ParentRegionID != nil {
		t.Errorf("ParentRegionID = %v, want nil (root region)", *row.ParentRegionID)
	}

	var gotHouseholdID int64
	if err := pool.QueryRow(ctx, `SELECT household_id FROM region WHERE region_id = $1`, row.RegionID).Scan(&gotHouseholdID); err != nil {
		t.Fatalf("read region.household_id: %v", err)
	}
	if gotHouseholdID != householdID {
		t.Errorf("region.household_id = %d, want %d (caller's own current household)", gotHouseholdID, householdID)
	}

	// bob has no household_membership row at all -- not a member of any
	// household, and this branch has no separate admin/elevation lane that
	// would grant him one.
	bobScope := scopeFor(t, resolver, "bob")
	_, err = repo.CreateRegion(ctx, nil, "Bob's Room", "", bobScope, regionAuditEntry("bob"))
	if err == nil {
		t.Fatal("CreateRegion for a non-member returned nil error, want a refusal")
	}
	f := failureOf(t, err)
	if f.Class != string(contract.FailurePermissionDenied) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailurePermissionDenied)
	}
}

// TestCreateRegion_NestedRegion_MemberSucceeds_NonMemberRefused covers the
// with-parent path: a member creates a child region under an existing one
// they can reach; a caller outside that household is refused as not-found
// (NFR2 -- authorizeRegionWrite collapses "doesn't exist" and "exists,
// wrong household" into the same failure).
func TestCreateRegion_NestedRegion_MemberSucceeds_NonMemberRefused(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	room, err := repo.CreateRegion(ctx, nil, "Grow Room", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (root): %v", err)
	}

	shelf, err := repo.CreateRegion(ctx, &room.RegionID, "Shelf 1", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (nested, member): %v", err)
	}
	if shelf.ParentRegionID == nil || *shelf.ParentRegionID != room.RegionID {
		t.Errorf("shelf.ParentRegionID = %v, want %d", shelf.ParentRegionID, room.RegionID)
	}

	bobScope := scopeFor(t, resolver, "bob")
	_, err = repo.CreateRegion(ctx, &room.RegionID, "Bob's Shelf", "", bobScope, regionAuditEntry("bob"))
	if !errors.Is(err, ErrRegionNotFound) {
		t.Errorf("CreateRegion under another household's region, non-member error = %v, want ErrRegionNotFound", err)
	}
}

// TestCreateRegion_MaxChildrenEnforced_ThirteenthRefusedNamingParent is
// FR50.1's structural rule: at most 12 (active) children per parent. A 13th
// is refused, naming the parent region and the offending field.
func TestCreateRegion_MaxChildrenEnforced_ThirteenthRefusedNamingParent(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	room, err := repo.CreateRegion(ctx, nil, "Crowded Room", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (root): %v", err)
	}

	for i := 0; i < 12; i++ {
		insertRegionRaw(t, pool, &room.RegionID, "child", nil)
	}

	_, err = repo.CreateRegion(ctx, &room.RegionID, "One Too Many", "", aliceScope, regionAuditEntry("alice"))
	if err == nil {
		t.Fatal("CreateRegion for a 13th child returned nil error, want a refusal")
	}
	f := failureOf(t, err)
	if f.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureInvalidArgument)
	}
	if f.Field != "parent_region_id" {
		t.Errorf("failure field = %q, want %q", f.Field, "parent_region_id")
	}
	if !strings.Contains(f.Reason, "Crowded Room") {
		t.Errorf("failure reason = %q, want it to name the parent region %q", f.Reason, "Crowded Room")
	}
}

// TestCreateRegion_MaxDepthEnforced_RefusesBeneathDeepestLevel is FR50.1's
// other structural rule: minimum depth Room / Shelf / Pot. A region already
// at Pot depth (the deepest level) may not have children; the refusal names
// the violation.
func TestCreateRegion_MaxDepthEnforced_RefusesBeneathDeepestLevel(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	room, err := repo.CreateRegion(ctx, nil, "Deep Room", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room, depth 1): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &room.RegionID, "Deep Shelf", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf, depth 2): %v", err)
	}
	pot, err := repo.CreateRegion(ctx, &shelf.RegionID, "Deep Pot", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Pot, depth 3): %v", err)
	}

	_, err = repo.CreateRegion(ctx, &pot.RegionID, "Too Deep", "", aliceScope, regionAuditEntry("alice"))
	if err == nil {
		t.Fatal("CreateRegion beneath a Pot (depth 3) returned nil error, want a refusal")
	}
	f := failureOf(t, err)
	if f.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureInvalidArgument)
	}
	if f.Field != "parent_region_id" {
		t.Errorf("failure field = %q, want %q", f.Field, "parent_region_id")
	}
	if !strings.Contains(f.Reason, "Deep Pot") {
		t.Errorf("failure reason = %q, want it to name the offending parent %q", f.Reason, "Deep Pot")
	}
	if !strings.Contains(f.Reason, "Room / Shelf / Pot") {
		t.Errorf("failure reason = %q, want it to name the Room / Shelf / Pot depth violation", f.Reason)
	}
}

// -- RenameRegion --------------------------------------------------------------

// TestRenameRegion_MemberSucceeds_RetiredRegionRefused proves rename works
// for a member and never touches parentage (FR50.3), and that a retired
// region refuses the write (FR22.5's "accepts no new writes").
func TestRenameRegion_MemberSucceeds_RetiredRegionRefused(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	room, err := repo.CreateRegion(ctx, nil, "Old Name", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion: %v", err)
	}
	parentBefore := room.ParentRegionID

	renamed, err := repo.RenameRegion(ctx, room.RegionID, "New Name", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("RenameRegion (member): %v", err)
	}
	if renamed.Name != "New Name" {
		t.Errorf("Name = %q, want %q", renamed.Name, "New Name")
	}
	if (renamed.ParentRegionID == nil) != (parentBefore == nil) {
		t.Errorf("RenameRegion changed ParentRegionID from %v to %v, want unchanged", parentBefore, renamed.ParentRegionID)
	}

	if _, err := repo.RetireRegion(ctx, room.RegionID, aliceScope, regionAuditEntry("alice")); err != nil {
		t.Fatalf("RetireRegion: %v", err)
	}

	_, err = repo.RenameRegion(ctx, room.RegionID, "After Retirement", aliceScope, regionAuditEntry("alice"))
	if err == nil {
		t.Fatal("RenameRegion on a retired region returned nil error, want a refusal")
	}
	f := failureOf(t, err)
	if !strings.Contains(strings.ToLower(f.Reason), "retired") {
		t.Errorf("failure reason = %q, want it to name retirement as the reason", f.Reason)
	}
}

// -- RetireRegion ----------------------------------------------------------------

// TestRetireRegion_DistinguishesNotFoundFromAlreadyRetired mirrors
// RetireBoard's own three-way outcome test: a first call succeeds and
// actually persists retired_at, a second call on the same region is refused
// as already-retired (not idempotent-by-design), and retiring a
// never-created region_id is refused as not-found -- a distinct failure
// class from already-retired.
func TestRetireRegion_DistinguishesNotFoundFromAlreadyRetired(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	room, err := repo.CreateRegion(ctx, nil, "Retire Me", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion: %v", err)
	}

	retired, err := repo.RetireRegion(ctx, room.RegionID, aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("first RetireRegion call: %v", err)
	}
	if retired.RetiredAt == nil {
		t.Fatal("RetiredAt is nil after RetireRegion succeeded, want it populated")
	}

	_, err = repo.RetireRegion(ctx, room.RegionID, aliceScope, regionAuditEntry("alice"))
	if !errors.Is(err, ErrRegionAlreadyRetired) {
		t.Errorf("second RetireRegion call error = %v, want ErrRegionAlreadyRetired", err)
	}
	if errors.Is(err, ErrRegionNotFound) {
		t.Error("second RetireRegion call error satisfies ErrRegionNotFound -- must be a distinct class from already-retired")
	}

	const neverInsertedRegionID = int64(999999)
	_, err = repo.RetireRegion(ctx, neverInsertedRegionID, aliceScope, regionAuditEntry("alice"))
	if !errors.Is(err, ErrRegionNotFound) {
		t.Errorf("RetireRegion on a nonexistent region_id error = %v, want ErrRegionNotFound", err)
	}
	if errors.Is(err, ErrRegionAlreadyRetired) {
		t.Error("RetireRegion on a nonexistent region_id error satisfies ErrRegionAlreadyRetired -- must be a distinct class from not-found")
	}
}

// -- ListRegions / GetRegionByID: FR22.5's retired-region guard ----------------

// TestListRegions_ExcludesRetired_GetRegionByID_RetiredStillReadableByID
// proves FR22.5's two-part guard against real SQL: a retired region is
// absent from ListRegions' default listing, but remains fully resolvable by
// explicit id -- with RetiredAt populated so a caller can tell it apart from
// an active region.
func TestListRegions_ExcludesRetired_GetRegionByID_RetiredStillReadableByID(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	active, err := repo.CreateRegion(ctx, nil, "Active Region", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (active): %v", err)
	}
	retired, err := repo.CreateRegion(ctx, nil, "Retired Region", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (to be retired): %v", err)
	}
	if _, err := repo.RetireRegion(ctx, retired.RegionID, aliceScope, regionAuditEntry("alice")); err != nil {
		t.Fatalf("RetireRegion: %v", err)
	}

	rows, err := repo.ListRegions(ctx, 0, false, 10, aliceScope)
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	var sawActive, sawRetired bool
	for _, r := range rows {
		switch r.RegionID {
		case active.RegionID:
			sawActive = true
		case retired.RegionID:
			sawRetired = true
		}
	}
	if !sawActive {
		t.Error("ListRegions did not return the active region")
	}
	if sawRetired {
		t.Error("ListRegions returned the retired region, want it excluded from the default listing")
	}

	got, err := repo.GetRegionByID(ctx, retired.RegionID)
	if err != nil {
		t.Fatalf("GetRegionByID on a retired region: %v", err)
	}
	if got.RetiredAt == nil {
		t.Error("GetRegionByID on a retired region returned nil RetiredAt, want it populated")
	}
	if got.Name != "Retired Region" {
		t.Errorf("Name = %q, want %q", got.Name, "Retired Region")
	}
}

// -- GetRegionPath ---------------------------------------------------------------

// TestGetRegionPath_ReadsRootToLeaf proves FR50.2: a region's path reads
// root-to-leaf, so a nested picker and a breadcrumb have a source.
func TestGetRegionPath_ReadsRootToLeaf(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	room, err := repo.CreateRegion(ctx, nil, "Room", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &room.RegionID, "Shelf", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf): %v", err)
	}
	pot, err := repo.CreateRegion(ctx, &shelf.RegionID, "Pot", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Pot): %v", err)
	}

	path, ok, err := repo.GetRegionPath(ctx, pot.RegionID)
	if err != nil {
		t.Fatalf("GetRegionPath: %v", err)
	}
	if !ok {
		t.Fatal("GetRegionPath ok = false, want true")
	}
	wantIDs := []int64{room.RegionID, shelf.RegionID, pot.RegionID}
	if len(path.PathIDs) != len(wantIDs) {
		t.Fatalf("PathIDs = %v, want %v", path.PathIDs, wantIDs)
	}
	for i, id := range wantIDs {
		if path.PathIDs[i] != id {
			t.Errorf("PathIDs[%d] = %d, want %d", i, path.PathIDs[i], id)
		}
	}
	wantNames := []string{"Room", "Shelf", "Pot"}
	for i, name := range wantNames {
		if path.PathNames[i] != name {
			t.Errorf("PathNames[%d] = %q, want %q", i, path.PathNames[i], name)
		}
	}
	if path.PathName != "Room / Shelf / Pot" {
		t.Errorf("PathName = %q, want %q", path.PathName, "Room / Shelf / Pot")
	}

	_, ok, err = repo.GetRegionPath(ctx, 999999)
	if err != nil {
		t.Fatalf("GetRegionPath for a nonexistent region: %v", err)
	}
	if ok {
		t.Error("GetRegionPath ok = true for a nonexistent region, want false")
	}
}

// -- SetRegionParent: parentage-immutability (FR50.3, FR22.2, NFR6.2) --------

// TestSetRegionParent_NoReadingsSucceeds is FR50.3's create-time grace
// window: re-parenting a region with no readings anywhere beneath it
// succeeds.
func TestSetRegionParent_NoReadingsSucceeds(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf, under Room A): %v", err)
	}

	moved, err := repo.SetRegionParent(ctx, shelf.RegionID, &roomB.RegionID, aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("SetRegionParent with no readings anywhere beneath: %v", err)
	}
	if moved.ParentRegionID == nil || *moved.ParentRegionID != roomB.RegionID {
		t.Errorf("ParentRegionID after re-parent = %v, want %d", moved.ParentRegionID, roomB.RegionID)
	}
}

// TestSetRegionParent_DescendantReadingRefused_NamesFR74Alternative is the
// case NFR6.2's recursive descendant test exists for: a reading attributed
// to a *descendant* of the region being re-parented -- not the region
// itself -- still freezes its parentage. The refusal names FR74/subtree
// relocation as the alternative (FR59.3).
func TestSetRegionParent_DescendantReadingRefused_NamesFR74Alternative(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf, under Room A): %v", err)
	}
	pot, err := repo.CreateRegion(ctx, &shelf.RegionID, "Pot", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Pot, under Shelf): %v", err)
	}

	// The reading is attributed to Pot -- a descendant of Shelf, not Shelf
	// itself -- which is exactly the case the recursive test exists for.
	insertReading(t, pool, pot.RegionID, time.Now())

	_, err = repo.SetRegionParent(ctx, shelf.RegionID, &roomB.RegionID, aliceScope, regionAuditEntry("alice"))
	if err == nil {
		t.Fatal("SetRegionParent for a region with a reading on a descendant returned nil error, want a refusal")
	}
	f := failureOf(t, err)
	if f.Class != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureRefusedWithAlternative)
	}
	if !strings.Contains(f.Reason, "descendant") {
		t.Errorf("failure reason = %q, want it to name the descendant-reading case", f.Reason)
	}
	if !strings.Contains(f.Alternative, "FR74") {
		t.Errorf("failure alternative = %q, want it to name FR74/subtree relocation", f.Alternative)
	}
	if !strings.Contains(strings.ToLower(f.Alternative), "subtree") {
		t.Errorf("failure alternative = %q, want it to name subtree relocation", f.Alternative)
	}

	// Parentage must not have actually changed.
	unchanged, err := repo.GetRegionByID(ctx, shelf.RegionID)
	if err != nil {
		t.Fatalf("GetRegionByID after refused re-parent: %v", err)
	}
	if unchanged.ParentRegionID == nil || *unchanged.ParentRegionID != roomA.RegionID {
		t.Errorf("ParentRegionID after refused re-parent = %v, want unchanged %d", unchanged.ParentRegionID, roomA.RegionID)
	}
}

// TestSetRegionParent_TriggerFiresOnDirectSQLBypass proves NFR6.2's
// backstop: migration 020's trigger fires even on a direct SQL UPDATE that
// bypasses the API's own FR59.3 refusal entirely.
func TestSetRegionParent_TriggerFiresOnDirectSQLBypass(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf, under Room A): %v", err)
	}
	insertReading(t, pool, shelf.RegionID, time.Now())

	_, err = pool.Exec(ctx, `UPDATE region SET parent_region_id = $1 WHERE region_id = $2`, roomB.RegionID, shelf.RegionID)
	if err == nil {
		t.Fatal("direct SQL UPDATE of parent_region_id on a region with an attributed reading succeeded, want the trigger to refuse it")
	}
	if !strings.Contains(err.Error(), "parentage is frozen") {
		t.Errorf("direct SQL UPDATE error = %v, want the trigger's parentage-frozen message", err)
	}

	unchanged, err := repo.GetRegionByID(ctx, shelf.RegionID)
	if err != nil {
		t.Fatalf("GetRegionByID after refused direct-SQL re-parent: %v", err)
	}
	if unchanged.ParentRegionID == nil || *unchanged.ParentRegionID != roomA.RegionID {
		t.Errorf("ParentRegionID after refused direct-SQL re-parent = %v, want unchanged %d", unchanged.ParentRegionID, roomA.RegionID)
	}
}

// TestSetRegionParent_RetirementDoesNotUnfreezeParentage is FR22.2's named
// case: retiring a region does not unfreeze its parentage. Refused with a
// reason naming retirement -- not the reading-attribution reason -- since
// this region has no reading anywhere beneath it at all.
func TestSetRegionParent_RetirementDoesNotUnfreezeParentage(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	roomA, err := repo.CreateRegion(ctx, nil, "Room A", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room A): %v", err)
	}
	roomB, err := repo.CreateRegion(ctx, nil, "Room B", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	shelf, err := repo.CreateRegion(ctx, &roomA.RegionID, "Shelf", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Shelf, under Room A, no readings anywhere): %v", err)
	}

	if _, err := repo.RetireRegion(ctx, shelf.RegionID, aliceScope, regionAuditEntry("alice")); err != nil {
		t.Fatalf("RetireRegion: %v", err)
	}

	_, err = repo.SetRegionParent(ctx, shelf.RegionID, &roomB.RegionID, aliceScope, regionAuditEntry("alice"))
	if err == nil {
		t.Fatal("SetRegionParent on a retired region returned nil error, want a refusal")
	}
	f := failureOf(t, err)
	if f.Class != string(contract.FailureRefusedWithAlternative) {
		t.Errorf("failure class = %q, want %q", f.Class, contract.FailureRefusedWithAlternative)
	}
	if !strings.Contains(strings.ToLower(f.Reason), "retired") {
		t.Errorf("failure reason = %q, want it to name retirement -- not the reading-attribution reason, since this region has no readings anywhere beneath it", f.Reason)
	}
	if !strings.Contains(f.Alternative, "FR74") {
		t.Errorf("failure alternative = %q, want it to name FR74/subtree relocation", f.Alternative)
	}
}

// -- Retired region: attribution still resolves (FR22.2) -----------------------

// TestRetiredRegion_StillResolvesAttribution proves FR22.2's "a retired
// region remains resolvable for attribution of readings recorded while it
// was active": the real attribution.Resolver still attributes a plant to a
// retired region -- migration 020's up.sql comment names this as something
// Implementation "must assert ... rather than assume".
func TestRetiredRegion_StillResolvesAttribution(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	room, err := repo.CreateRegion(ctx, nil, "Formerly Active Room", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion: %v", err)
	}

	var plantID int64
	if err := pool.QueryRow(ctx, `INSERT INTO plant (name) VALUES ($1) RETURNING plant_id`, "Fern").Scan(&plantID); err != nil {
		t.Fatalf("insert plant: %v", err)
	}
	validFrom := time.Now().Add(-24 * time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from) VALUES ($1, $2, $3)
	`, plantID, room.RegionID, validFrom); err != nil {
		t.Fatalf("insert plant_region_history: %v", err)
	}

	if _, err := repo.RetireRegion(ctx, room.RegionID, aliceScope, regionAuditEntry("alice")); err != nil {
		t.Fatalf("RetireRegion: %v", err)
	}

	attrResolver := attribution.NewResolver(pool)
	plants, attributedRegionID, err := attrResolver.ResolvePlants(ctx, room.RegionID, time.Now())
	if err != nil {
		t.Fatalf("ResolvePlants against a retired region: %v", err)
	}
	if attributedRegionID != room.RegionID {
		t.Errorf("attributedRegionID = %d, want %d (the retired region itself)", attributedRegionID, room.RegionID)
	}
	if len(plants) != 1 || plants[0].PlantID != plantID {
		t.Errorf("plants = %+v, want exactly [{PlantID: %d}]", plants, plantID)
	}
}

// -- Audit (FR8) -----------------------------------------------------------------

// TestRegionLifecycle_EveryWriteIsAudited proves every region write --
// create, rename, retire, and the re-parent primitive -- records exactly one
// audit_log row, with entity_id set to the affected region.
func TestRegionLifecycle_EveryWriteIsAudited(t *testing.T) {
	repo, resolver, pool := newRegionsTestRepository(t)
	ctx := context.Background()

	householdID := insertHousehold(t, pool)
	insertMembership(t, pool, householdID, "alice")
	aliceScope := scopeFor(t, resolver, "alice")

	if n := countRows(t, pool, "audit_log"); n != 0 {
		t.Fatalf("test setup: audit_log has %d rows before any write, want 0", n)
	}

	roomA, err := repo.CreateRegion(ctx, nil, "Audited Room A", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion: %v", err)
	}
	if n := countRows(t, pool, "audit_log"); n != 1 {
		t.Fatalf("audit_log rows after CreateRegion = %d, want 1", n)
	}

	roomB, err := repo.CreateRegion(ctx, nil, "Audited Room B", "", aliceScope, regionAuditEntry("alice"))
	if err != nil {
		t.Fatalf("CreateRegion (Room B): %v", err)
	}
	if n := countRows(t, pool, "audit_log"); n != 2 {
		t.Fatalf("audit_log rows after second CreateRegion = %d, want 2", n)
	}

	if _, err := repo.RenameRegion(ctx, roomA.RegionID, "Renamed Room A", aliceScope, regionAuditEntry("alice")); err != nil {
		t.Fatalf("RenameRegion: %v", err)
	}
	if n := countRows(t, pool, "audit_log"); n != 3 {
		t.Fatalf("audit_log rows after RenameRegion = %d, want 3", n)
	}

	if _, err := repo.SetRegionParent(ctx, roomA.RegionID, nil, aliceScope, regionAuditEntry("alice")); err != nil {
		t.Fatalf("SetRegionParent: %v", err)
	}
	if n := countRows(t, pool, "audit_log"); n != 4 {
		t.Fatalf("audit_log rows after SetRegionParent = %d, want 4", n)
	}

	if _, err := repo.RetireRegion(ctx, roomA.RegionID, aliceScope, regionAuditEntry("alice")); err != nil {
		t.Fatalf("RetireRegion: %v", err)
	}
	if n := countRows(t, pool, "audit_log"); n != 5 {
		t.Fatalf("audit_log rows after RetireRegion = %d, want 5", n)
	}

	// A refused write must not add an audit row (RetireRegion is not
	// idempotent-by-design -- retiring roomA again is refused).
	if _, err := repo.RetireRegion(ctx, roomA.RegionID, aliceScope, regionAuditEntry("alice")); !errors.Is(err, ErrRegionAlreadyRetired) {
		t.Fatalf("second RetireRegion on roomA error = %v, want ErrRegionAlreadyRetired", err)
	}
	if n := countRows(t, pool, "audit_log"); n != 5 {
		t.Fatalf("audit_log rows after a refused RetireRegion = %d, want unchanged 5", n)
	}

	var lastEntityID string
	if err := pool.QueryRow(ctx, `SELECT entity_id FROM audit_log ORDER BY audit_id DESC LIMIT 1`).Scan(&lastEntityID); err != nil {
		t.Fatalf("read last audit_log row: %v", err)
	}
	wantEntityID := strconv.FormatInt(roomA.RegionID, 10)
	if lastEntityID != wantEntityID {
		t.Errorf("last audit_log entity_id = %q, want %q (RetireRegion's target)", lastEntityID, wantEntityID)
	}

	_ = roomB // used only to prove a second CreateRegion also produces one row
}
