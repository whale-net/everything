package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/claim"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"google.golang.org/protobuf/encoding/protojson"
)

// -- FR79/A23: ListFleetHealth ------------------------------------------------
//
// Unit coverage against fakeRepo (server_test.go's ListFleetHealth fake),
// proving requireAdminEligible's gate, the response field mapping, filter
// pass-through, reporting_state classification (including the A23
// floor/multiplier fixtures and FR22.4's retired-board exclusion), and the
// keyset-with-cap pagination shape -- all without a live Postgres
// connection. Real-SQL coverage of fleetBoardHealthQuery itself (joins,
// outstanding_push semantics, sensor counts) is out of this file's scope.

// fleetConfigJSON protojson-encodes a DeviceConfig carrying one SensorConfig
// per pollIntervalMs, mirroring FleetBoardHealthRow.AcceptedConfigJSON's
// shape (repository.go's doc comment: "the caller unmarshals it ... against
// the same configpb.DeviceConfig shape").
func fleetConfigJSON(t *testing.T, pollIntervalsMs ...uint32) []byte {
	t.Helper()
	cfg := &configpb.DeviceConfig{}
	for _, ms := range pollIntervalsMs {
		cfg.Sensors = append(cfg.Sensors, &configpb.SensorConfig{PollIntervalMs: ms})
	}
	b, err := protojson.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal fixture DeviceConfig: %v", err)
	}
	return b
}

// TestListFleetHealth_RequiresAdmin proves ListFleetHealth gates on
// requireAdminEligible before ever reaching the repository, the same
// discipline server_admin_test.go's
// TestRequireAdminEligible_RefusesNonAdmin_AllFiveRPCs proves for the other
// five admin RPCs -- ListFleetHealth is the sixth.
func TestListFleetHealth_RequiresAdmin(t *testing.T) {
	repo := &fakeRepo{}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))
	_, err := server.ListFleetHealth(nonAdminCtx("mallory"), &pb.ListFleetHealthRequest{})
	wantPermissionDenied(t, err)
	if len(repo.listFleetHealthCalls) != 0 {
		t.Errorf("repo was reached before the admin gate: listFleetHealthCalls=%d", len(repo.listFleetHealthCalls))
	}
}

// TestListFleetHealth_FieldsPopulated_FromFixture proves every FR79 field
// on FleetBoardHealth is populated from the repository row, including
// OutstandingPushSince only when OutstandingPush is true.
func TestListFleetHealth_FieldsPopulated_FromFixture(t *testing.T) {
	pushedAt := time.Now().Add(-45 * time.Minute)
	lastSeen := time.Now().Add(-1 * time.Minute)
	repo := &fakeRepo{
		listFleetHealthPages: [][]FleetBoardHealthRow{{
			{
				BoardID:              7,
				DeviceID:             "device-7",
				BoardDisplayName:     "device-7",
				HouseholdID:          3,
				HouseholdName:        "The Smiths",
				LastSeenAt:           lastSeen,
				ActiveVersion:        4,
				OutstandingPush:      true,
				OutstandingPushSince: pushedAt,
				SensorCount:          5,
				Retired:              false,
				AcceptedConfigJSON:   fleetConfigJSON(t, 60000),
			},
		}},
	}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))

	resp, err := server.ListFleetHealth(adminCtx("root"), &pb.ListFleetHealthRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Boards) != 1 {
		t.Fatalf("got %d boards, want 1", len(resp.Boards))
	}
	b := resp.Boards[0]

	if b.DeviceId != "device-7" {
		t.Errorf("DeviceId = %q, want %q", b.DeviceId, "device-7")
	}
	if b.BoardDisplayName != "device-7" {
		t.Errorf("BoardDisplayName = %q, want %q", b.BoardDisplayName, "device-7")
	}
	if b.HouseholdId != 3 {
		t.Errorf("HouseholdId = %d, want 3", b.HouseholdId)
	}
	if b.HouseholdName != "The Smiths" {
		t.Errorf("HouseholdName = %q, want %q", b.HouseholdName, "The Smiths")
	}
	if b.ActiveVersion != 4 {
		t.Errorf("ActiveVersion = %d, want 4", b.ActiveVersion)
	}
	if !b.OutstandingPush {
		t.Error("OutstandingPush = false, want true")
	}
	if b.OutstandingPushSince == nil {
		t.Error("OutstandingPushSince is nil despite OutstandingPush=true")
	}
	if b.SensorCount != 5 {
		t.Errorf("SensorCount = %d, want 5", b.SensorCount)
	}
	if b.Retired {
		t.Error("Retired = true, want false")
	}
	if b.LastSeenAt == nil {
		t.Error("LastSeenAt is nil")
	}
	if b.ReportingState != pb.ReportingState_REPORTING_STATE_REPORTING {
		t.Errorf("ReportingState = %v, want REPORTING (last seen 1 minute ago)", b.ReportingState)
	}
	if resp.ServerNow == nil {
		t.Error("ServerNow is nil")
	}
}

// TestListFleetHealth_OutstandingPushSince_UnsetWhenNotOutstanding proves
// OutstandingPushSince is left unset (nil) when OutstandingPush is false,
// rather than leaking a zero-value time.Time as a misleadingly-real Instant
// -- FleetBoardHealth's proto doc comment convention.
func TestListFleetHealth_OutstandingPushSince_UnsetWhenNotOutstanding(t *testing.T) {
	repo := &fakeRepo{
		listFleetHealthPages: [][]FleetBoardHealthRow{{
			{BoardID: 1, DeviceID: "d1", LastSeenAt: time.Now(), OutstandingPush: false},
		}},
	}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))
	resp, err := server.ListFleetHealth(adminCtx("root"), &pb.ListFleetHealthRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Boards[0].OutstandingPushSince != nil {
		t.Errorf("OutstandingPushSince = %v, want nil when OutstandingPush is false", resp.Boards[0].OutstandingPushSince)
	}
}

// TestListFleetHealth_FiltersPassedThroughUnmodified proves
// device_id_prefix, household_id, region_id and the decoded page cursor
// reach the repository exactly as requested -- narrowing, never widening
// (fleetBoardHealthQuery's doc comment).
func TestListFleetHealth_FiltersPassedThroughUnmodified(t *testing.T) {
	repo := &fakeRepo{}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))

	token := contract.EncodeBoardCursor(41)
	_, err := server.ListFleetHealth(adminCtx("root"), &pb.ListFleetHealthRequest{
		DeviceIdPrefix: "leaf-",
		HouseholdId:    9,
		RegionId:       2,
		Page:           &pb.PageRequest{PageToken: token, PageSize: 10},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.listFleetHealthCalls) == 0 {
		t.Fatal("repository was never called")
	}
	call := repo.listFleetHealthCalls[0]
	if call.afterBoardID != 41 {
		t.Errorf("afterBoardID = %d, want 41 (decoded from page_token)", call.afterBoardID)
	}
	if call.devicePrefix != "leaf-" {
		t.Errorf("devicePrefix = %q, want %q", call.devicePrefix, "leaf-")
	}
	if call.householdID != 9 {
		t.Errorf("householdID = %d, want 9", call.householdID)
	}
	if call.regionID != 2 {
		t.Errorf("regionID = %d, want 2", call.regionID)
	}
}

// TestListFleetHealth_UnhealthyOnly_ReturnsExactlyTheUnhealthySet proves
// reporting_state=REPORTING_STATE_NOT_REPORTING narrows the response to
// exactly the boards A23 classifies as not-reporting, excluding both
// healthy boards and a retired-but-very-stale board (FR22.4).
func TestListFleetHealth_UnhealthyOnly_ReturnsExactlyTheUnhealthySet(t *testing.T) {
	now := time.Now()
	repo := &fakeRepo{
		listFleetHealthPages: [][]FleetBoardHealthRow{{
			{BoardID: 1, DeviceID: "healthy", LastSeenAt: now.Add(-1 * time.Minute)},
			{BoardID: 2, DeviceID: "unhealthy", LastSeenAt: now.Add(-1 * time.Hour)},
			{BoardID: 3, DeviceID: "retired-stale", LastSeenAt: now.Add(-24 * time.Hour), Retired: true},
			{BoardID: 4, DeviceID: "also-unhealthy", LastSeenAt: now.Add(-2 * time.Hour)},
		}},
	}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))

	resp, err := server.ListFleetHealth(adminCtx("root"), &pb.ListFleetHealthRequest{
		ReportingState: pb.ReportingState_REPORTING_STATE_NOT_REPORTING,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var gotIDs []string
	for _, b := range resp.Boards {
		gotIDs = append(gotIDs, b.DeviceId)
		if b.ReportingState != pb.ReportingState_REPORTING_STATE_NOT_REPORTING {
			t.Errorf("board %s: ReportingState = %v, want NOT_REPORTING (unhealthy_only)", b.DeviceId, b.ReportingState)
		}
	}
	want := map[string]bool{"unhealthy": true, "also-unhealthy": true}
	if len(gotIDs) != len(want) {
		t.Fatalf("got boards %v, want exactly %v", gotIDs, want)
	}
	for _, id := range gotIDs {
		if !want[id] {
			t.Errorf("unexpected board %q in unhealthy_only result", id)
		}
	}
}

// TestListFleetHealth_RetiredBoard_NeverNotReporting proves a retired
// board is always classified REPORTING_STATE_REPORTING, no matter how
// stale its last_seen_at is -- FR22.4/FR79: "excluded from its offline
// counts". Deliberately breaking this (see the paired assertion below) is
// this task's red/green proof.
func TestListFleetHealth_RetiredBoard_NeverNotReporting(t *testing.T) {
	repo := &fakeRepo{
		listFleetHealthPages: [][]FleetBoardHealthRow{{
			{BoardID: 1, DeviceID: "retired", LastSeenAt: time.Now().Add(-90 * 24 * time.Hour), Retired: true},
		}},
	}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))
	resp, err := server.ListFleetHealth(adminCtx("root"), &pb.ListFleetHealthRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Boards) != 1 {
		t.Fatalf("got %d boards, want 1", len(resp.Boards))
	}
	if got := resp.Boards[0].ReportingState; got != pb.ReportingState_REPORTING_STATE_REPORTING {
		t.Errorf("retired board ReportingState = %v, want REPORTING regardless of staleness (FR22.4)", got)
	}
	if !resp.Boards[0].Retired {
		t.Error("Retired = false, want true")
	}
}

// TestReportingStateFor_OneMinuteInterval_FloorNotMultiplier and
// TestReportingStateFor_TenMinuteInterval_MultiplierNotFloor are this
// task's Testing-section fixtures exercised through reportingStateFor
// itself (the function server.go's ListFleetHealth calls per row),
// proving the config-decoding step (accepted config JSON -> longest
// configured poll interval) feeds leaflab/api/health.Threshold correctly,
// on top of the health package's own direct coverage of Threshold/IsStale.

func TestReportingStateFor_OneMinuteInterval_FloorNotMultiplier(t *testing.T) {
	now := time.Now()
	row := FleetBoardHealthRow{
		LastSeenAt:         now.Add(-10 * time.Minute), // > 3x1m=3m, still under the 15m floor
		AcceptedConfigJSON: fleetConfigJSON(t, 60000),  // 1 minute
	}
	if got := reportingStateFor(row, now); got != pb.ReportingState_REPORTING_STATE_REPORTING {
		t.Errorf("1-minute-interval board at 10 minutes stale = %v, want REPORTING (under the 15-minute floor)", got)
	}

	row.LastSeenAt = now.Add(-16 * time.Minute)
	if got := reportingStateFor(row, now); got != pb.ReportingState_REPORTING_STATE_NOT_REPORTING {
		t.Errorf("1-minute-interval board at 16 minutes stale = %v, want NOT_REPORTING (past the 15-minute floor)", got)
	}
}

func TestReportingStateFor_TenMinuteInterval_MultiplierNotFloor(t *testing.T) {
	now := time.Now()
	row := FleetBoardHealthRow{
		LastSeenAt:         now.Add(-20 * time.Minute), // past the 15m floor, still < 3x10m=30m
		AcceptedConfigJSON: fleetConfigJSON(t, 600000), // 10 minutes
	}
	if got := reportingStateFor(row, now); got != pb.ReportingState_REPORTING_STATE_REPORTING {
		t.Errorf("10-minute-interval board at 20 minutes stale = %v, want REPORTING (under 3x = 30 minutes)", got)
	}

	row.LastSeenAt = now.Add(-31 * time.Minute)
	if got := reportingStateFor(row, now); got != pb.ReportingState_REPORTING_STATE_NOT_REPORTING {
		t.Errorf("10-minute-interval board at 31 minutes stale = %v, want NOT_REPORTING (past 3x = 30 minutes)", got)
	}
}

// TestReportingStateFor_LongestOfMultipleSensorsWins proves A23's
// "longest configured poll interval" picks the max across a board's
// sensors, not the first or the shortest.
func TestReportingStateFor_LongestOfMultipleSensorsWins(t *testing.T) {
	now := time.Now()
	row := FleetBoardHealthRow{
		LastSeenAt:         now.Add(-20 * time.Minute),
		AcceptedConfigJSON: fleetConfigJSON(t, 60000, 600000, 120000), // longest = 10 minutes
	}
	if got := reportingStateFor(row, now); got != pb.ReportingState_REPORTING_STATE_REPORTING {
		t.Errorf("board with a 10-minute longest interval at 20 minutes stale = %v, want REPORTING (under 3x = 30 minutes)", got)
	}
}

// TestListFleetHealth_PageSizeAboveCap_ClampedNotUnbounded proves FR61's
// cap: a request for far more than contract.PageCap boards is clamped, not
// honored as an unpaginated dump -- "no unpaginated form exists".
func TestListFleetHealth_PageSizeAboveCap_ClampedNotUnbounded(t *testing.T) {
	var rows []FleetBoardHealthRow
	for i := int64(1); i <= 250; i++ {
		rows = append(rows, FleetBoardHealthRow{BoardID: i, DeviceID: "d", LastSeenAt: time.Now()})
	}
	repo := &fakeRepo{listFleetHealthPages: [][]FleetBoardHealthRow{rows}}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))

	resp, err := server.ListFleetHealth(adminCtx("root"), &pb.ListFleetHealthRequest{
		Page: &pb.PageRequest{PageSize: contract.PageCap * 1000},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if int32(len(resp.Boards)) != contract.PageCap {
		t.Errorf("got %d boards for an over-cap page_size, want exactly PageCap %d", len(resp.Boards), contract.PageCap)
	}
	if resp.Page.GetNextPageToken() == "" {
		t.Error("NextPageToken is empty despite more boards remaining beyond the cap")
	}
}

// TestListFleetHealth_KeysetResume_NoSkipNoDuplicate proves paging through
// two fakeRepo-scripted calls via NextPageToken returns every board
// exactly once, in order, with no skip and no repeat -- the same keyset
// discipline TestListBoards_KeysetPagination_NoDuplicatesNoSkips_UnderConcurrentInserts
// proves against real Postgres for ListBoards. The first repository call
// returns 3 rows against a 2-row requested page (a batch smaller than
// listFleetHealthDBBatch but larger than the requested limit), so
// next_page_token is derived from truncating the excess match, not from
// exhaustion -- see ListFleetHealth's doc comment on the two ways a next
// token can be produced.
func TestListFleetHealth_KeysetResume_NoSkipNoDuplicate(t *testing.T) {
	firstCallRows := []FleetBoardHealthRow{
		{BoardID: 1, DeviceID: "d1", LastSeenAt: time.Now()},
		{BoardID: 2, DeviceID: "d2", LastSeenAt: time.Now()},
		{BoardID: 3, DeviceID: "d3", LastSeenAt: time.Now()},
	}
	secondCallRows := []FleetBoardHealthRow{
		{BoardID: 3, DeviceID: "d3", LastSeenAt: time.Now()},
	}
	repo := &fakeRepo{listFleetHealthPages: [][]FleetBoardHealthRow{firstCallRows, secondCallRows}}
	server := NewLeafLabAPIServer(repo, &fakeAuthz{}, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))

	first, err := server.ListFleetHealth(adminCtx("root"), &pb.ListFleetHealthRequest{Page: &pb.PageRequest{PageSize: 2}})
	if err != nil {
		t.Fatalf("first page: unexpected error: %v", err)
	}
	if len(first.Boards) != 2 || first.Boards[0].DeviceId != "d1" || first.Boards[1].DeviceId != "d2" {
		t.Fatalf("first page boards = %+v, want [d1 d2]", first.Boards)
	}
	if first.Page.GetNextPageToken() == "" {
		t.Fatal("first page: NextPageToken is empty despite a third (untruncated) board remaining in the batch")
	}

	second, err := server.ListFleetHealth(adminCtx("root"), &pb.ListFleetHealthRequest{
		Page: &pb.PageRequest{PageSize: 2, PageToken: first.Page.GetNextPageToken()},
	})
	if err != nil {
		t.Fatalf("second page: unexpected error: %v", err)
	}
	if len(second.Boards) != 1 || second.Boards[0].DeviceId != "d3" {
		t.Fatalf("second page boards = %+v, want [d3]", second.Boards)
	}
	if second.Page.GetNextPageToken() != "" {
		t.Errorf("second page NextPageToken = %q, want empty (fleet exhausted)", second.Page.GetNextPageToken())
	}
	if repo.listFleetHealthCalls[1].afterBoardID != 2 {
		t.Errorf("second call afterBoardID = %d, want 2 (the last board actually returned on the first page, not the third scanned-but-truncated board)", repo.listFleetHealthCalls[1].afterBoardID)
	}
}

// fleetBoardHealthForbiddenFieldSubstrings names field-name fragments that
// must never appear on FleetBoardHealth or ListFleetHealthResponse -- FR79
// explicitly excludes a computed health score or severity ranking.
var fleetBoardHealthForbiddenFieldSubstrings = []string{"score", "rank", "severity"}

// TestListFleetHealthResponse_CarriesNoScoreOrRankingField is a structural
// proof over the proto descriptor (not a particular instance) that
// FleetBoardHealth carries no field suggesting a computed health score or
// severity ranking -- FR79: "No computed health score or severity
// ranking."
func TestListFleetHealthResponse_CarriesNoScoreOrRankingField(t *testing.T) {
	desc := (&pb.FleetBoardHealth{}).ProtoReflect().Descriptor()
	var fields []string
	for i := 0; i < desc.Fields().Len(); i++ {
		fields = append(fields, string(desc.Fields().Get(i).Name()))
	}
	for _, f := range fields {
		for _, forbidden := range fleetBoardHealthForbiddenFieldSubstrings {
			if containsFold(f, forbidden) {
				t.Errorf("FleetBoardHealth has field %q, which looks like it carries %q -- FR79 explicitly excludes a computed health score or severity ranking", f, forbidden)
			}
		}
	}

	// Positive control: reporting_state is the two-valued classification
	// FR79 does permit -- must still be present.
	found := false
	for _, f := range fields {
		if f == "reporting_state" {
			found = true
		}
	}
	if !found {
		t.Error("FleetBoardHealth has no reporting_state field")
	}
}

// TestReportingStateFor_IsTheOnlyA23CallSiteInLeafLabAPI is a
// source-analysis proof (per this task's Testing section: "assert with a
// source-analysis test") that leaflab/api/health.Threshold and
// leaflab/api/health.IsStale are called from exactly one place across
// leaflab/api's own package sources -- reportingStateFor, in server.go.
// Every other A23 consumer (FR62, FR42) must import and call through this
// same file's package, or through leaflab/api/health directly -- never
// re-derive the 3x/floor arithmetic locally within leaflab/api.
func TestReportingStateFor_IsTheOnlyA23CallSiteInLeafLabAPI(t *testing.T) {
	dir := leaflabAPIDir(t)

	fset := token.NewFileSet()
	callSites := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		file, parseErr := parser.ParseFile(fset, path, src, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "health" {
				return true
			}
			if sel.Sel.Name == "Threshold" || sel.Sel.Name == "IsStale" {
				callSites++
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if callSites != 1 {
		t.Errorf("found %d call site(s) of health.Threshold/health.IsStale under leaflab/api, want exactly 1 (reportingStateFor in server.go) -- do not let a second copy of A23's arithmetic exist", callSites)
	}
}

// leaflabAPIDir locates leaflab/api using its own BUILD.bazel as a marker
// file, the same convention leaflab/ui/nfr18_conformance_test.go's
// leaflabUIDir uses.
func leaflabAPIDir(t *testing.T) string {
	t.Helper()
	for _, c := range []string{".", "..", "../..", "../../.."} {
		marker := filepath.Join(c, "leaflab", "api", "BUILD.bazel")
		if st, statErr := os.Stat(marker); statErr == nil && !st.IsDir() {
			return filepath.Join(c, "leaflab", "api")
		}
	}
	// Also try resolving relative to this file's own directory, since this
	// test file already lives in leaflab/api.
	if st, statErr := os.Stat("BUILD.bazel"); statErr == nil && !st.IsDir() {
		return "."
	}
	t.Fatal("could not locate leaflab/api/BUILD.bazel -- check the working directory / data dependency")
	return ""
}
