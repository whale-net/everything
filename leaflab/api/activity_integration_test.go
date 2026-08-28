//go:build integration

// Real-Postgres integration coverage for FR9's owner-readable activity list
// (ListHouseholdActivity, activity.go/activity_repository.go): this
// package's other integration files each prove one write path's own
// mechanics (claim_integration_test.go for FR76, admin_elevation_integration_test.go
// for FR10, support_reference_integration_test.go for FR80) at the
// audit_log-row level; this file proves the *reading* half those rows feed
// into -- household-scoped access (including FR10.3's elevation
// extension), "one list, one voice" across admin/member actions, FR76.7's
// no-attempting-principal-identity guarantee, and FR61's keyset pagination
// and page cap.
//
// Schema is self-contained hand-written DDL, mirroring
// admin_elevation_integration_test.go's household/household_membership/
// admin_elevation shape (same rationale: every integration file in this
// package keeps its own schema rather than sharing one with different
// concerns) plus claim_integration_test.go's trimmed board/board_ownership/
// claim_challenge shape (only the columns activity_repository.go's
// ListClaimAttemptActivity and repository.go's GetBoardByID actually read
// -- no claim_challenge_round/claim_cooldown/device_config/sensor, since
// nothing here exercises CompleteClaim's own round bookkeeping or FR79's
// health projection). CreateSupportReference/SupportReferenceResolve and
// ClaimBoard/Elevate rows are inserted directly into audit_log rather than
// exercised through their own RPCs -- those RPCs' write-path mechanics are
// already covered by support_reference_integration_test.go,
// claim_integration_test.go and admin_elevation_integration_test.go; this
// file only needs the rows to exist so ListHouseholdActivity has something
// real to render and merge.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:activity_integration_test --test_output=all
package main

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/claim"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

const activityTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY,
		name         TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE household_membership (
		membership_id     BIGSERIAL PRIMARY KEY,
		household_id      BIGINT NOT NULL REFERENCES household(household_id),
		principal_subject TEXT NOT NULL,
		valid_from        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to          TIMESTAMPTZ
	);

	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		retired_at    TIMESTAMPTZ
	);

	CREATE TABLE board_ownership (
		ownership_id  BIGSERIAL PRIMARY KEY,
		board_id      BIGINT NOT NULL REFERENCES board(board_id),
		household_id  BIGINT NOT NULL REFERENCES household(household_id),
		valid_from    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to      TIMESTAMPTZ
	);

	CREATE TABLE claim_challenge (
		challenge_id      BIGSERIAL PRIMARY KEY,
		handle             TEXT NOT NULL UNIQUE,
		principal_subject  TEXT NOT NULL,
		device_id          TEXT NOT NULL,
		opened_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at         TIMESTAMPTZ NOT NULL,
		state              TEXT NOT NULL DEFAULT 'open'
		                       CHECK (state IN ('open', 'discharged', 'not_discharged')),
		discharged_at      TIMESTAMPTZ NULL
	);

	CREATE TABLE admin_elevation (
		elevation_id         BIGSERIAL PRIMARY KEY,
		admin_subject        TEXT NOT NULL,
		target_household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
		reason                TEXT NOT NULL,
		started_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at            TIMESTAMPTZ NOT NULL,
		ended_at              TIMESTAMPTZ NULL
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
`

// newActivityTestServer starts a real Postgres container with
// activityTestSchema applied and returns a LeafLabAPIServer backed by a
// real Repository and a real authz.PGResolver -- ListHouseholdActivity's
// member-path authorization routes through scopeForCaller, which needs a
// real household_membership query.
func newActivityTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: activityTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return NewLeafLabAPIServer(repo, resolver, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil)), db.Pool
}

func activityMemberCtx(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject})
}

func activityAdminCtx(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject, Roles: []string{RoleAdmin}})
}

func insertActivityHousehold(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `INSERT INTO household (name) VALUES ($1) RETURNING household_id`, name).Scan(&id); err != nil {
		t.Fatalf("insert household %s: %v", name, err)
	}
	return id
}

func insertActivityMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`, householdID, subject); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
}

func insertActivityElevation(t *testing.T, pool *pgxpool.Pool, adminSubject string, targetHouseholdID int64, expiresAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO admin_elevation (admin_subject, target_household_id, reason, expires_at)
		VALUES ($1, $2, 'investigating', $3)
	`, adminSubject, targetHouseholdID, expiresAt); err != nil {
		t.Fatalf("insert elevation: %v", err)
	}
}

// insertActivityBoard inserts a board and an open-ended board_ownership row
// assigning it to householdID -- enough for boardLabelForEntityID
// (activity.go) to resolve a ClaimBoard row's entity_id to a device_id, and
// for ListClaimAttemptActivity's join to scope a claim_challenge row to
// this household.
func insertActivityBoard(t *testing.T, pool *pgxpool.Pool, deviceID string, householdID int64, ownedSince time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	var boardID int64
	if err := pool.QueryRow(ctx, `INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, deviceID).Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO board_ownership (board_id, household_id, valid_from) VALUES ($1, $2, $3)`, boardID, householdID, ownedSince); err != nil {
		t.Fatalf("insert board_ownership: %v", err)
	}
	return boardID
}

// insertActivityClaimChallenge inserts a resolved (never 'open')
// claim_challenge row for deviceID, opened at openedAt by a stranger
// (principalSubject, deliberately a principal belonging to no household
// this test grants access to) -- the row
// activity_repository.go's ListClaimAttemptActivity reads to produce
// FR76.7's claim-attempt entry.
func insertActivityClaimChallenge(t *testing.T, pool *pgxpool.Pool, deviceID, principalSubject, state string, openedAt time.Time) {
	t.Helper()
	handle := "handle-" + deviceID + "-" + principalSubject + "-" + strconv.FormatInt(openedAt.UnixNano(), 10)
	var dischargedAt any
	if state == "discharged" {
		dischargedAt = openedAt.Add(time.Minute)
	}
	expiresAt := openedAt.Add(time.Hour)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO claim_challenge (handle, principal_subject, device_id, opened_at, expires_at, state, discharged_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, handle, principalSubject, deviceID, openedAt, expiresAt, state, dischargedAt); err != nil {
		t.Fatalf("insert claim_challenge: %v", err)
	}
}

// insertActivityAuditRow inserts one audit_log row directly, at occurredAt
// (rather than NOW()), so ordering-sensitive tests (pagination) control
// exactly how rows sort against each other without depending on real wall-
// clock spacing between INSERTs.
func insertActivityAuditRow(t *testing.T, pool *pgxpool.Pool, householdID int64, actorSubject, action, entityKind string, entityID *string, occurredAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO audit_log (actor_subject, actor_kind, target_household_id, action, entity_kind, entity_id, occurred_at)
		VALUES ($1, 'human', $2, $3, $4, $5, $6)
	`, actorSubject, householdID, action, entityKind, entityID, occurredAt); err != nil {
		t.Fatalf("insert audit_log row (%s/%s): %v", action, entityKind, err)
	}
}

func strPtr(s string) *string { return &s }

// TestListHouseholdActivity_MemberReadsOwnHousehold_NonMemberGetsNotFound
// proves the base FR9 access rule: a current member reads their own
// household's activity; a non-member gets the identical NFR2-shaped
// not-found refusal a genuinely nonexistent household_id would produce, so
// the response carries no oracle distinguishing "this household doesn't
// exist" from "you don't belong to it".
func TestListHouseholdActivity_MemberReadsOwnHousehold_NonMemberGetsNotFound(t *testing.T) {
	server, pool := newActivityTestServer(t)
	household := insertActivityHousehold(t, pool, "The Smiths")
	insertActivityMembership(t, pool, household, "alice")
	insertActivityAuditRow(t, pool, household, "alice", "RenameHousehold", "household", nil, time.Now())

	resp, err := server.ListHouseholdActivity(activityMemberCtx("alice"), &pb.ListHouseholdActivityRequest{HouseholdId: household})
	if err != nil {
		t.Fatalf("member ListHouseholdActivity: %v", err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("member got %d entries, want 1", len(resp.Entries))
	}
	if resp.Entries[0].Sentence == "" {
		t.Error("entry sentence is empty")
	}

	_, nonMemberErr := server.ListHouseholdActivity(activityMemberCtx("mallory"), &pb.ListHouseholdActivityRequest{HouseholdId: household})
	if nonMemberErr == nil {
		t.Fatal("non-member ListHouseholdActivity returned nil error, want a refusal")
	}
	_, nonexistentErr := server.ListHouseholdActivity(activityMemberCtx("mallory"), &pb.ListHouseholdActivityRequest{HouseholdId: household + 999999})
	if nonexistentErr == nil {
		t.Fatal("nonexistent-household ListHouseholdActivity returned nil error, want a refusal")
	}

	nonMemberDetail, ok := contract.FromError(nonMemberErr)
	if !ok {
		t.Fatal("non-member error carries no Failure detail")
	}
	nonexistentDetail, ok := contract.FromError(nonexistentErr)
	if !ok {
		t.Fatal("nonexistent-household error carries no Failure detail")
	}
	if !proto.Equal(nonMemberDetail, nonexistentDetail) {
		t.Errorf("Failure details differ: non-member=%v, nonexistent=%v -- NFR2 forbids this being an oracle", nonMemberDetail, nonexistentDetail)
	}
}

// TestListHouseholdActivity_ElevatedAdminReads_UnelevatedAdminRefused proves
// FR9's explicit extension over ordinary membership: an admin-eligible
// caller with no elevation is refused exactly like a non-member; the same
// caller, elevated against this exact household (FR10.3), succeeds; and an
// elevation against a *different* household still refuses.
func TestListHouseholdActivity_ElevatedAdminReads_UnelevatedAdminRefused(t *testing.T) {
	server, pool := newActivityTestServer(t)
	householdA := insertActivityHousehold(t, pool, "household-a")
	householdB := insertActivityHousehold(t, pool, "household-b")
	insertActivityAuditRow(t, pool, householdA, "alice", "RenameHousehold", "household", nil, time.Now())

	adminCtx := activityAdminCtx("root")

	if _, err := server.ListHouseholdActivity(adminCtx, &pb.ListHouseholdActivityRequest{HouseholdId: householdA}); err == nil {
		t.Fatal("unelevated admin ListHouseholdActivity returned nil error, want a refusal")
	}

	insertActivityElevation(t, pool, "root", householdB, time.Now().Add(time.Hour))
	if _, err := server.ListHouseholdActivity(adminCtx, &pb.ListHouseholdActivityRequest{HouseholdId: householdA}); err == nil {
		t.Fatal("admin elevated against a different household succeeded, want a refusal (FR10.3)")
	}

	insertActivityElevation(t, pool, "root", householdA, time.Now().Add(time.Hour))
	resp, err := server.ListHouseholdActivity(adminCtx, &pb.ListHouseholdActivityRequest{HouseholdId: householdA})
	if err != nil {
		t.Fatalf("admin elevated against the right household: %v", err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("elevated admin got %d entries, want 1", len(resp.Entries))
	}
}

// TestListHouseholdActivity_AdminMemberAndClaimAttempt_OneListSameStructure
// is this task's core "one list, one voice" and FR76.7 proof: an admin's
// elevation, a member's household-creation and invite, a claimed board, a
// support reference's creation and an admin's use of it, and a claim
// attempt against the household's board all appear in the same
// []ActivityEntry -- every entry has the same (sentence, entity_kind,
// occurred_at) shape (never more than ActivityEntry's own 3 fields), most
// recent first, and nowhere in the whole response does the attacking
// principal's subject or any raw internal identifier (household_id,
// board_id, admin's real subject) appear.
func TestListHouseholdActivity_AdminMemberAndClaimAttempt_OneListSameStructure(t *testing.T) {
	server, pool := newActivityTestServer(t)
	household := insertActivityHousehold(t, pool, "The Smiths")
	insertActivityMembership(t, pool, household, "alice")
	insertActivityMembership(t, pool, household, "bob")
	base0 := time.Now().Add(-2 * time.Hour)
	boardID := insertActivityBoard(t, pool, "device-abc123", household, base0)
	boardIDStr := strconv.FormatInt(boardID, 10)

	base := time.Now().Add(-1 * time.Hour)
	insertActivityAuditRow(t, pool, household, "alice", "CreateHousehold", "household_membership", nil, base.Add(1*time.Minute))
	insertActivityAuditRow(t, pool, household, "alice", "InviteMember", "household_membership", strPtr("bob"), base.Add(2*time.Minute))
	insertActivityAuditRow(t, pool, household, "bob", "ClaimBoard", "board", &boardIDStr, base.Add(3*time.Minute))
	insertActivityAuditRow(t, pool, household, "root-admin-77", "Elevate", "household", nil, base.Add(4*time.Minute))
	insertActivityAuditRow(t, pool, household, "alice", "CreateSupportReference", "support_reference", nil, base.Add(5*time.Minute))
	insertActivityAuditRow(t, pool, household, "root-admin-77", "SupportReferenceResolve", "support_reference", nil, base.Add(6*time.Minute))
	// A claim attempt against this household's board by "eve", a stranger
	// belonging to no household this test grants any access to -- not
	// discharged, so it renders as a plain failed attempt.
	insertActivityClaimChallenge(t, pool, "device-abc123", "eve", "not_discharged", base.Add(7*time.Minute))

	resp, err := server.ListHouseholdActivity(activityMemberCtx("alice"), &pb.ListHouseholdActivityRequest{HouseholdId: household})
	if err != nil {
		t.Fatalf("ListHouseholdActivity: %v", err)
	}
	if len(resp.Entries) != 7 {
		t.Fatalf("got %d entries, want 7 (6 audit rows + 1 claim attempt)", len(resp.Entries))
	}

	// Every entry has the exact same structural shape -- ActivityEntry
	// carries exactly 3 fields, and every entry here populates all 3
	// (sentence, entity_kind, occurred_at), regardless of whether it came
	// from an admin action, a member action, or the claim-attempt source.
	for i, e := range resp.Entries {
		if got := countPopulatedFieldsActivity(e); got != 3 {
			t.Errorf("entry %d has %d populated fields, want exactly 3 (sentence, entity_kind, occurred_at) -- FR9's one list, one voice", i, got)
		}
		if e.Sentence == "" {
			t.Errorf("entry %d has an empty sentence", i)
		}
	}

	// Descending order: most recent first.
	for i := 1; i < len(resp.Entries); i++ {
		prev := contract.FromInstant(resp.Entries[i-1].OccurredAt)
		cur := contract.FromInstant(resp.Entries[i].OccurredAt)
		if cur.After(prev) {
			t.Errorf("entries not descending: entry %d (%v) is after entry %d (%v)", i, cur, i-1, prev)
		}
	}

	sentences := make([]string, len(resp.Entries))
	for i, e := range resp.Entries {
		sentences[i] = e.Sentence
	}
	joined := strings.Join(sentences, " | ")

	wantSubstrings := []string{
		"created this household",
		"invited",
		"claimed",
		"began a temporary review",
		"created a support reference",
		"used a support reference",
		"tried to prove they were at",
		"couldn't", // the claim attempt failed
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(strings.ToLower(joined), strings.ToLower(want)) {
			t.Errorf("no entry sentence contains %q -- entries: %v", want, sentences)
		}
	}

	// "One list, one voice": the two admin-attributed actions (Elevate,
	// SupportReferenceResolve) render with the exact "an administrator"
	// phrasing every admin action gets -- not the raw subject, and not the
	// generic "another household member" a non-admin actor comparison would
	// produce.
	if !strings.Contains(strings.ToLower(joined), "an administrator began a temporary review") {
		t.Errorf("no entry reads \"an administrator began a temporary review...\" -- entries: %v", sentences)
	}
	if !strings.Contains(strings.ToLower(joined), "an administrator used a support reference") {
		t.Errorf("no entry reads \"an administrator used a support reference...\" -- entries: %v", sentences)
	}

	// A29/FR76.7: the attempting principal ("eve") is never identified,
	// anywhere in the response's human-readable content -- not just in the
	// claim-attempt entry's own sentence. Checked over sentences and entity
	// kinds only (not occurred_at's unix-millis numerals, which are
	// unrelated and would make a substring check on the whole serialized
	// response spuriously match small numbers like a single-digit board_id).
	entityKinds := make([]string, len(resp.Entries))
	for i, e := range resp.Entries {
		entityKinds[i] = e.EntityKind
	}
	humanText := joined + " | " + strings.Join(entityKinds, " | ")

	if strings.Contains(humanText, "eve") {
		t.Errorf("response contains the attempting principal's subject \"eve\" -- A29 forbids identifying them: %s", humanText)
	}
	// The admin's raw subject never appears either -- admin actions render
	// as "an administrator", never the real principal.
	if strings.Contains(humanText, "root-admin-77") {
		t.Errorf("response contains the admin's raw subject -- FR9 requires the same plain-language phrasing every action gets: %s", humanText)
	}
	// A raw board_id/household_id leaking into any sentence would already
	// have made the ListHouseholdActivity call above panic and fail this
	// test outright: every Template's output passes through
	// leaflab/api/activity's mustBeRenderSafe, which rejects any sentence
	// containing the substring "_id" before Render ever returns it (see
	// that package's own exhaustive Registry test). No separate substring
	// check is needed here -- and a bare numeric id substring check would
	// be unreliable anyway, since the ClaimBoard entry's EntityLabel is
	// legitimately the board's device_id ("device-abc123"), which itself
	// contains arbitrary digits.
}

// countPopulatedFieldsActivity counts ActivityEntry fields protoreflect
// considers set -- this file's own copy of the countPopulatedFields helper
// pattern used elsewhere in this package (server_test.go,
// support_reference_integration_test.go), which this integration-tagged
// file can't import directly since those files are excluded from this
// build tag.
func countPopulatedFieldsActivity(e *pb.ActivityEntry) int {
	n := 0
	e.ProtoReflect().Range(func(protoreflect.FieldDescriptor, protoreflect.Value) bool {
		n++
		return true
	})
	return n
}

// TestListHouseholdActivity_Pagination_KeysetAndCapped proves FR61's
// pagination contract end to end: an unspecified page_size falls back to
// contract.DefaultPageSize, a requested page_size above contract.PageCap is
// clamped rather than rejected, walking every page via next_page_token
// visits every row exactly once in strict descending (occurred_at, tag)
// order with no duplicates or gaps, and the final page's next_page_token is
// empty.
func TestListHouseholdActivity_Pagination_KeysetAndCapped(t *testing.T) {
	server, pool := newActivityTestServer(t)
	household := insertActivityHousehold(t, pool, "The Smiths")
	insertActivityMembership(t, pool, household, "alice")

	const totalRows = 110 // > contract.PageCap (100), several pages at the default size (25) too
	base := time.Now()
	for i := 0; i < totalRows; i++ {
		// i=0 is most recent (occurred_at = base), i=totalRows-1 is oldest --
		// so ORDER BY occurred_at DESC visits i=0, 1, 2, ... in order.
		insertActivityAuditRow(t, pool, household, "alice", "RenameHousehold", "household", nil, base.Add(-time.Duration(i)*time.Second))
	}

	ctx := activityMemberCtx("alice")

	// Unspecified page_size falls back to contract.DefaultPageSize (25).
	firstPage, err := server.ListHouseholdActivity(ctx, &pb.ListHouseholdActivityRequest{HouseholdId: household})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(firstPage.Entries) != int(contract.DefaultPageSize) {
		t.Fatalf("unspecified page_size returned %d entries, want contract.DefaultPageSize = %d", len(firstPage.Entries), contract.DefaultPageSize)
	}
	if firstPage.Page.GetNextPageToken() == "" {
		t.Fatal("first page's next_page_token is empty, want more pages to remain")
	}

	// A page_size above contract.PageCap is clamped, not rejected.
	cappedPage, err := server.ListHouseholdActivity(ctx, &pb.ListHouseholdActivityRequest{
		HouseholdId: household,
		Page:        &pb.PageRequest{PageSize: 100000},
	})
	if err != nil {
		t.Fatalf("oversized page_size request: %v", err)
	}
	if len(cappedPage.Entries) != int(contract.PageCap) {
		t.Fatalf("page_size=100000 returned %d entries, want it clamped to contract.PageCap = %d", len(cappedPage.Entries), contract.PageCap)
	}
	if cappedPage.Page.GetNextPageToken() == "" {
		t.Fatal("capped page's next_page_token is empty, want more pages to remain (110 rows > PageCap)")
	}

	// Walk every page at a page size that divides evenly into neither
	// totalRows nor DefaultPageSize/PageCap, collecting every row exactly
	// once, in strict descending order, with the last page's token empty.
	const walkPageSize = 30
	seen := make(map[string]bool)
	var allTimes []time.Time
	token := ""
	pages := 0
	for {
		pages++
		if pages > totalRows { // guard against an infinite loop on a pagination bug
			t.Fatal("pagination did not terminate after more pages than there are rows")
		}
		resp, err := server.ListHouseholdActivity(ctx, &pb.ListHouseholdActivityRequest{
			HouseholdId: household,
			Page:        &pb.PageRequest{PageToken: token, PageSize: walkPageSize},
		})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, e := range resp.Entries {
			key := e.Sentence + "@" + strconv.FormatInt(e.OccurredAt.GetUnixMillis(), 10)
			// Every row here is a RenameHousehold row with the identical
			// rendered sentence, so occurred_at (millisecond-distinct by
			// construction) is what actually disambiguates them; the
			// sentence half of key just documents what's being compared.
			if seen[key] {
				t.Errorf("row revisited across pages: %s", key)
			}
			seen[key] = true
			allTimes = append(allTimes, contract.FromInstant(e.OccurredAt))
		}
		token = resp.Page.GetNextPageToken()
		if token == "" {
			break
		}
	}

	if len(allTimes) != totalRows {
		t.Fatalf("walked %d rows total, want %d", len(allTimes), totalRows)
	}
	for i := 1; i < len(allTimes); i++ {
		if allTimes[i].After(allTimes[i-1]) {
			t.Errorf("pagination did not preserve descending order at index %d: %v after %v", i, allTimes[i], allTimes[i-1])
		}
	}
}
