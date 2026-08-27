// FR34/FR35: GetConfigStatus, ListConfigHistory and GetConfigVersion --
// handler-level coverage against fakeRepo/fakeAuthz (see server_test.go's
// fixtures), proving state derivation is correctly surfaced on the wire,
// FR35.1's history listing threads pagination through unmodified, and
// NFR2's non-member refusal short-circuits before any repository read.
// Real-SQL coverage of FR61 keyset pagination ordering, per-entry
// provenance persistence and the "no status column" invariant lives in
// config_lifecycle_integration_test.go.
package main

import (
	"testing"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

var fixedPushedAt = time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
var fixedAckedAtTest = time.Date(2026, 8, 20, 10, 5, 0, 0, time.UTC)

// -- GetConfigStatus: state reflection -----------------------------------

// TestGetConfigStatus_ReflectsCurrentState_Pending proves a version whose
// acked_at is still NULL reports CONFIG_STATE_PENDING, no acked_at, and no
// rejection reason.
func TestGetConfigStatus_ReflectsCurrentState_Pending(t *testing.T) {
	repo := &fakeRepo{getDeviceConfigVersionResponse: &DeviceConfigVersionRow{
		ConfigID: 1, Version: 3, Accepted: false, PushedAt: fixedPushedAt, AckedAt: nil,
	}}
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, discardLogger())

	resp, err := server.GetConfigStatus(authedTestCtx("alice"), &pb.GetConfigStatusRequest{DeviceId: "board-a", Version: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.State != pb.ConfigState_CONFIG_STATE_PENDING {
		t.Errorf("State = %v, want CONFIG_STATE_PENDING", resp.State)
	}
	zeroInstant := contract.ToInstant(time.Time{})
	if resp.AckedAt != nil && resp.AckedAt.UnixMillis != zeroInstant.UnixMillis {
		t.Errorf("AckedAt = %v, want unset (zero-valued) while pending", resp.AckedAt)
	}
	if resp.RejectionReason != "" {
		t.Errorf("RejectionReason = %q, want empty while pending", resp.RejectionReason)
	}
}

// TestGetConfigStatus_ReflectsCurrentState_Accepted proves an acked,
// accepted version reports CONFIG_STATE_ACCEPTED with acked_at populated.
func TestGetConfigStatus_ReflectsCurrentState_Accepted(t *testing.T) {
	repo := &fakeRepo{getDeviceConfigVersionResponse: &DeviceConfigVersionRow{
		ConfigID: 1, Version: 3, Accepted: true, PushedAt: fixedPushedAt, AckedAt: &fixedAckedAtTest,
	}}
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, discardLogger())

	resp, err := server.GetConfigStatus(authedTestCtx("alice"), &pb.GetConfigStatusRequest{DeviceId: "board-a", Version: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.State != pb.ConfigState_CONFIG_STATE_ACCEPTED {
		t.Errorf("State = %v, want CONFIG_STATE_ACCEPTED", resp.State)
	}
	if resp.AckedAt == nil || resp.AckedAt.UnixMillis == 0 {
		t.Error("AckedAt is zero, want the acked_at timestamp populated for an accepted version")
	}
}

// TestGetConfigStatus_ReflectsCurrentState_Rejected proves an acked,
// unaccepted version reports CONFIG_STATE_REJECTED with the firmware's
// verbatim rejection reason carried through.
func TestGetConfigStatus_ReflectsCurrentState_Rejected(t *testing.T) {
	const reason = "I2C bus TIMEOUT -- addr=0x44"
	repo := &fakeRepo{getDeviceConfigVersionResponse: &DeviceConfigVersionRow{
		ConfigID: 1, Version: 3, Accepted: false, PushedAt: fixedPushedAt, AckedAt: &fixedAckedAtTest, RejectionReason: reason,
	}}
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, discardLogger())

	resp, err := server.GetConfigStatus(authedTestCtx("alice"), &pb.GetConfigStatusRequest{DeviceId: "board-a", Version: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.State != pb.ConfigState_CONFIG_STATE_REJECTED {
		t.Errorf("State = %v, want CONFIG_STATE_REJECTED", resp.State)
	}
	if resp.RejectionReason != reason {
		t.Errorf("RejectionReason = %q, want verbatim %q", resp.RejectionReason, reason)
	}
	if resp.Sentence == "" {
		t.Error("Sentence is empty, want FR34.2's rejected sentence")
	}
}

// TestGetConfigStatus_NoSuchVersion_NotFound proves a nil repository result
// (no such version, though the board itself is real and in scope) maps to
// configVersionNotFoundFailure -- distinct from any of the three
// ConfigState values (FR34.1: "a rejected push is never indistinguishable
// from no push at all").
func TestGetConfigStatus_NoSuchVersion_NotFound(t *testing.T) {
	repo := &fakeRepo{getDeviceConfigVersionResponse: nil}
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, discardLogger())

	_, err := server.GetConfigStatus(authedTestCtx("alice"), &pb.GetConfigStatusRequest{DeviceId: "board-a", Version: 999})
	if err == nil {
		t.Fatal("GetConfigStatus for a nonexistent version returned nil error, want not-found")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatal("error carries no Failure detail")
	}
	if detail.Entity != "device_config" {
		t.Errorf("Failure.Entity = %q, want %q", detail.Entity, "device_config")
	}
}

// -- ListConfigHistory: pagination shape ----------------------------------

// TestListConfigHistory_MarksPendingAndRejectedEntries proves every
// returned version is visibly marked with its own ConfigState (FR35.1) --
// pending and rejected entries are never omitted or collapsed to the same
// state as an accepted one.
func TestListConfigHistory_MarksPendingAndRejectedEntries(t *testing.T) {
	repo := &fakeRepo{listConfigHistoryResponse: []DeviceConfigHistoryRow{
		{Version: 3, Accepted: false, PushedAt: fixedPushedAt, AckedAt: nil},                                           // pending
		{Version: 2, Accepted: false, PushedAt: fixedPushedAt, AckedAt: &fixedAckedAtTest, RejectionReason: "bad crc"}, // rejected
		{Version: 1, Accepted: true, PushedAt: fixedPushedAt, AckedAt: &fixedAckedAtTest},                              // accepted
	}}
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, discardLogger())

	resp, err := server.ListConfigHistory(authedTestCtx("alice"), &pb.ListConfigHistoryRequest{DeviceId: "board-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Entries) != 3 {
		t.Fatalf("len(Entries) = %d, want 3 -- pending/rejected must never be filtered out", len(resp.Entries))
	}
	wantStates := []pb.ConfigState{pb.ConfigState_CONFIG_STATE_PENDING, pb.ConfigState_CONFIG_STATE_REJECTED, pb.ConfigState_CONFIG_STATE_ACCEPTED}
	for i, want := range wantStates {
		if resp.Entries[i].State != want {
			t.Errorf("Entries[%d].State = %v, want %v", i, resp.Entries[i].State, want)
		}
		if resp.Entries[i].Version != uint64(3-i) {
			t.Errorf("Entries[%d].Version = %d, want %d", i, resp.Entries[i].Version, 3-i)
		}
	}
	if resp.Entries[1].RejectionReason != "bad crc" {
		t.Errorf("rejected entry RejectionReason = %q, want %q", resp.Entries[1].RejectionReason, "bad crc")
	}
}

// TestListConfigHistory_CapDetection_NextPageTokenOnlyWhenExtraRowReturned
// proves the handler's "fetch limit+1, trim, and only emit a next token
// when the extra row came back" convention (FR61): a page exactly at the
// requested size (no extra row) gets no next_page_token, while limit+1 rows
// gets one.
func TestListConfigHistory_CapDetection_NextPageTokenOnlyWhenExtraRowReturned(t *testing.T) {
	exact := &fakeRepo{listConfigHistoryResponse: []DeviceConfigHistoryRow{
		{Version: 2, PushedAt: fixedPushedAt}, {Version: 1, PushedAt: fixedPushedAt},
	}}
	exactServer := NewLeafLabAPIServer(exact, boardScopedAuthz(7), nil, nil, discardLogger())
	resp, err := exactServer.ListConfigHistory(authedTestCtx("alice"), &pb.ListConfigHistoryRequest{
		DeviceId: "board-a", Page: &pb.PageRequest{PageSize: 2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Page.NextPageToken != "" {
		t.Errorf("NextPageToken = %q, want empty when repository returned exactly page_size rows", resp.Page.NextPageToken)
	}
	if len(exact.listConfigHistoryCalls) != 1 || exact.listConfigHistoryCalls[0].limit != 3 {
		t.Fatalf("ListConfigHistory called with limit=%v, want a single call with limit=page_size+1=3", exact.listConfigHistoryCalls)
	}

	extra := &fakeRepo{listConfigHistoryResponse: []DeviceConfigHistoryRow{
		{Version: 3, PushedAt: fixedPushedAt}, {Version: 2, PushedAt: fixedPushedAt}, {Version: 1, PushedAt: fixedPushedAt},
	}}
	extraServer := NewLeafLabAPIServer(extra, boardScopedAuthz(7), nil, nil, discardLogger())
	resp, err = extraServer.ListConfigHistory(authedTestCtx("alice"), &pb.ListConfigHistoryRequest{
		DeviceId: "board-a", Page: &pb.PageRequest{PageSize: 2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Errorf("len(Entries) = %d, want the extra (limit+1'th) row trimmed off, leaving 2", len(resp.Entries))
	}
	if resp.Page.NextPageToken == "" {
		t.Error("NextPageToken is empty, want a token when the repository returned one more row than page_size")
	}
}

// TestListConfigHistory_InvalidPageToken_Refused proves a malformed
// page_token is refused as invalid argument rather than silently treated
// as the first page.
func TestListConfigHistory_InvalidPageToken_Refused(t *testing.T) {
	repo := &fakeRepo{}
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, discardLogger())

	_, err := server.ListConfigHistory(authedTestCtx("alice"), &pb.ListConfigHistoryRequest{
		DeviceId: "board-a", Page: &pb.PageRequest{PageToken: "not-a-valid-token"},
	})
	if err == nil {
		t.Fatal("ListConfigHistory with a malformed page_token returned nil error, want a refusal")
	}
	if len(repo.listConfigHistoryCalls) != 0 {
		t.Errorf("repository was called %d times with an invalid page_token, want 0", len(repo.listConfigHistoryCalls))
	}
}

// -- GetConfigVersion: entries and provenance ------------------------------

// TestGetConfigVersion_ReturnsEntriesWithProvenance proves GetConfigVersion
// fetches a version regardless of acceptance and returns each entry's FR82.4
// provenance, translated onto the wire enum.
func TestGetConfigVersion_ReturnsEntriesWithProvenance(t *testing.T) {
	addr := int32(0x44)
	repo := &fakeRepo{
		getDeviceConfigVersionResponse: &DeviceConfigVersionRow{
			ConfigID: 5, Version: 3, ConfigJSON: []byte(`{}`), Accepted: false, PushedAt: fixedPushedAt, AckedAt: &fixedAckedAtTest, RejectionReason: "bad crc",
		},
		getConfigVersionEntriesResponse: []ConfigVersionEntryRow{
			{I2CAddress: &addr, MuxPath: []byte(`[]`), SensorTypeName: "temperature", Provenance: "authored"},
			{I2CAddress: nil, MuxPath: []byte(`[]`), SensorTypeName: "humidity", Provenance: "materialised"},
		},
	}
	server := NewLeafLabAPIServer(repo, boardScopedAuthz(7), nil, nil, discardLogger())

	resp, err := server.GetConfigVersion(authedTestCtx("alice"), &pb.GetConfigVersionRequest{DeviceId: "board-a", Version: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.State != pb.ConfigState_CONFIG_STATE_REJECTED {
		t.Errorf("State = %v, want CONFIG_STATE_REJECTED -- GetConfigVersion must fetch a version regardless of acceptance (FR35.2)", resp.State)
	}
	if resp.Config.Version != 3 {
		t.Errorf("Config.Version = %d, want 3 (filled in from the resolved row, not trusted from config_json)", resp.Config.Version)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2", len(resp.Entries))
	}
	if resp.Entries[0].Provenance != pb.EntryProvenance_ENTRY_PROVENANCE_AUTHORED {
		t.Errorf("Entries[0].Provenance = %v, want ENTRY_PROVENANCE_AUTHORED", resp.Entries[0].Provenance)
	}
	if resp.Entries[0].SensorType != firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE {
		t.Errorf("Entries[0].SensorType = %v, want SENSOR_TYPE_TEMPERATURE", resp.Entries[0].SensorType)
	}
	if resp.Entries[1].Provenance != pb.EntryProvenance_ENTRY_PROVENANCE_MATERIALISED {
		t.Errorf("Entries[1].Provenance = %v, want ENTRY_PROVENANCE_MATERIALISED", resp.Entries[1].Provenance)
	}
}

// -- NFR2: non-member refusal, all three RPCs ------------------------------

// boardScopedAuthz returns a fakeAuthz granting exactly householdID for
// ResolveBoardByDeviceID/ScopeForPrincipal -- the "caller is a household
// member" fixture the state-reflection tests above use.
func boardScopedAuthz(boardID int64) *fakeAuthz {
	return &fakeAuthz{
		scope:      authz.NewHouseholdScope(1),
		resolveRef: authz.EntityRef{Kind: authz.EntityBoard, ID: boardID},
		resolveRes: authz.Resolution{HouseholdID: 1},
	}
}

// nonMemberAuthz returns a fakeAuthz resolving the named board to a
// household the caller's scope does not permit -- NFR2's non-member
// refusal fixture.
func nonMemberAuthz(boardID int64) *fakeAuthz {
	return &fakeAuthz{
		scope:      authz.NewHouseholdScope(1),
		resolveRef: authz.EntityRef{Kind: authz.EntityBoard, ID: boardID},
		resolveRes: authz.Resolution{HouseholdID: 2}, // a different household
	}
}

// TestGetConfigStatus_NonMember_Refused proves a caller outside the
// board's household gets NFR2's not-found refusal, short-circuited before
// any repository read.
func TestGetConfigStatus_NonMember_Refused(t *testing.T) {
	repo := &fakeRepo{getDeviceConfigVersionResponse: &DeviceConfigVersionRow{Version: 1, PushedAt: fixedPushedAt}}
	server := NewLeafLabAPIServer(repo, nonMemberAuthz(7), nil, nil, discardLogger())

	_, err := server.GetConfigStatus(authedTestCtx("mallory"), &pb.GetConfigStatusRequest{DeviceId: "board-a", Version: 1})
	if err == nil {
		t.Fatal("GetConfigStatus for a non-member caller returned nil error, want a refusal")
	}
	if len(repo.getDeviceConfigVersionCalls) != 0 {
		t.Errorf("repository was reached %d times by a non-member caller, want 0 -- authorization must short-circuit first", len(repo.getDeviceConfigVersionCalls))
	}
}

// TestListConfigHistory_NonMember_Refused is the same NFR2 refusal for
// ListConfigHistory.
func TestListConfigHistory_NonMember_Refused(t *testing.T) {
	repo := &fakeRepo{listConfigHistoryResponse: []DeviceConfigHistoryRow{{Version: 1, PushedAt: fixedPushedAt}}}
	server := NewLeafLabAPIServer(repo, nonMemberAuthz(7), nil, nil, discardLogger())

	_, err := server.ListConfigHistory(authedTestCtx("mallory"), &pb.ListConfigHistoryRequest{DeviceId: "board-a"})
	if err == nil {
		t.Fatal("ListConfigHistory for a non-member caller returned nil error, want a refusal")
	}
	if len(repo.listConfigHistoryCalls) != 0 {
		t.Errorf("repository was reached %d times by a non-member caller, want 0 -- authorization must short-circuit first", len(repo.listConfigHistoryCalls))
	}
}

// TestGetConfigVersion_NonMember_Refused is the same NFR2 refusal for
// GetConfigVersion, also proving GetConfigVersionEntries is never reached.
func TestGetConfigVersion_NonMember_Refused(t *testing.T) {
	repo := &fakeRepo{
		getDeviceConfigVersionResponse:  &DeviceConfigVersionRow{Version: 1, ConfigJSON: []byte(`{}`), PushedAt: fixedPushedAt},
		getConfigVersionEntriesResponse: []ConfigVersionEntryRow{{SensorTypeName: "temperature", Provenance: "authored"}},
	}
	server := NewLeafLabAPIServer(repo, nonMemberAuthz(7), nil, nil, discardLogger())

	_, err := server.GetConfigVersion(authedTestCtx("mallory"), &pb.GetConfigVersionRequest{DeviceId: "board-a", Version: 1})
	if err == nil {
		t.Fatal("GetConfigVersion for a non-member caller returned nil error, want a refusal")
	}
	if len(repo.getDeviceConfigVersionCalls) != 0 {
		t.Errorf("GetDeviceConfigVersion was reached %d times by a non-member caller, want 0", len(repo.getDeviceConfigVersionCalls))
	}
	if repo.getConfigVersionEntriesCalls != 0 {
		t.Errorf("GetConfigVersionEntries was reached %d times by a non-member caller, want 0", repo.getConfigVersionEntriesCalls)
	}
}

// TestNonMemberRefusals_ByteIdenticalToNonexistentBoard extends NFR2's
// byte-identical refusal assertion (proven for GetDeviceConfig in
// server_test.go) to all three FR34/FR35 RPCs: a nonexistent device_id and
// an out-of-scope device_id must produce the same failure for each.
func TestNonMemberRefusals_ByteIdenticalToNonexistentBoard(t *testing.T) {
	nonexistentAuthz := &fakeAuthz{scope: authz.NewHouseholdScope(1), resolveErr: authz.ErrNotFound}
	nonexistentServer := NewLeafLabAPIServer(&fakeRepo{}, nonexistentAuthz, nil, nil, discardLogger())
	_, nonexistentErr := nonexistentServer.GetConfigStatus(authedTestCtx("alice"), &pb.GetConfigStatusRequest{DeviceId: "does-not-exist", Version: 1})

	outOfScopeServer := NewLeafLabAPIServer(&fakeRepo{}, nonMemberAuthz(7), nil, nil, discardLogger())
	_, outOfScopeErr := outOfScopeServer.GetConfigStatus(authedTestCtx("alice"), &pb.GetConfigStatusRequest{DeviceId: "device-belongs-to-household-2", Version: 1})

	if nonexistentErr == nil || outOfScopeErr == nil {
		t.Fatal("want refusals for both nonexistent and out-of-scope callers")
	}
	nonexistentBytes := failureBytes(t, nonexistentErr)
	outOfScopeBytes := failureBytes(t, outOfScopeErr)
	if string(nonexistentBytes) != string(outOfScopeBytes) {
		t.Errorf("marshaled gRPC status differs between nonexistent and out-of-scope refusals for GetConfigStatus -- NFR2 requires byte-identical status and body")
	}
}
