package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

// TestReportingState is a table-driven test of reportingState as a pure
// function of (last-reading timestamp, now) — no database involved (see
// #1497's Testing section). It covers the boundary exactly at the
// threshold, just inside it, just outside it, and the no-reading-at-all
// case.
func TestReportingState(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name          string
		lastReadingAt *time.Time
		want          pb.ReportingState
	}{
		{
			name:          "no reading at all is never reported",
			lastReadingAt: nil,
			want:          pb.ReportingState_REPORTING_STATE_NEVER_REPORTED,
		},
		{
			name:          "just inside the threshold (9m59s ago) is reporting",
			lastReadingAt: timePtr(now.Add(-(reportingThreshold - time.Second))),
			want:          pb.ReportingState_REPORTING_STATE_REPORTING,
		},
		{
			name:          "exactly at the threshold (10m ago) is still reporting (inclusive boundary)",
			lastReadingAt: timePtr(now.Add(-reportingThreshold)),
			want:          pb.ReportingState_REPORTING_STATE_REPORTING,
		},
		{
			name:          "just outside the threshold (10m1s ago) is stale",
			lastReadingAt: timePtr(now.Add(-(reportingThreshold + time.Second))),
			want:          pb.ReportingState_REPORTING_STATE_STALE,
		},
		{
			name:          "long stale (30m ago) is stale",
			lastReadingAt: timePtr(now.Add(-30 * time.Minute)),
			want:          pb.ReportingState_REPORTING_STATE_STALE,
		},
		{
			name:          "a reading in the future (clock skew) is reporting",
			lastReadingAt: timePtr(now.Add(time.Minute)),
			want:          pb.ReportingState_REPORTING_STATE_REPORTING,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reportingState(tt.lastReadingAt, now)
			if got != tt.want {
				t.Errorf("reportingState(%v, %v) = %v, want %v", tt.lastReadingAt, now, got, tt.want)
			}
		})
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}

// -- M2 ownership/authorization fence tests ----------------------------
//
// These call LeafLabAPIServer's methods directly against a fakeRepository/
// fakePublisher (no Postgres, no RabbitMQ), with caller identity injected
// via grpcauth.ContextWithClaims -- the same context shape the real OIDC
// interceptor (auth.go's selectiveUnaryInterceptor) would have set up by
// the time a handler runs, per grpcauth.ContextWithClaims's own doc
// comment. Interceptor-level behavior (rejecting a call with no claims at
// all) is covered separately in auth_test.go; what's under test here is
// authorizeBoardWrite and its callers once claims are already present.

// insertedConfig is one recorded InsertDeviceConfigNextVersion call.
type insertedConfig struct {
	boardID    int64
	configJSON []byte
}

// renamedBoard is one recorded fakeRepository.RenameBoard call.
type renamedBoard struct {
	boardID int64
	name    string
}

// publishedMessage is one recorded fakePublisher.Publish call.
type publishedMessage struct {
	exchange   string
	routingKey string
	body       interface{}
}

// fakeRepository is an in-memory repositoryStore double. Its zero value
// (via newFakeRepository) has no users, boards, or ownership rows -- every
// lookup miss returns found=false/ok=false, exactly like a fresh database
// would, so a test that forgets to seed a fixture fails as "not found"
// rather than panicking on a nil map.
type fakeRepository struct {
	// users maps an OIDC subject to a leaflab_user_id. A subject absent
	// here has no leaflab_user row (LB1 -- leaflab-api never creates one).
	users map[string]int64
	// devices maps a device_id to its board_id.
	devices map[string]int64
	// owners maps a board_id to its current owner's leaflab_user_id. A
	// board_id absent here is unowned (no open board_owner_history row).
	owners map[int64]int64
	// admins is the set of leaflab_user_ids holding an open 'admin' grant --
	// a user_id absent (or present but false) here has no open admin grant,
	// exactly like a fresh leaflab_user_role table would report.
	admins map[int64]bool

	insertedConfigs []insertedConfig
	nextVersion     int64

	// inventory and lastAccepted back ListSensorInventoryForBoard and
	// GetLatestAcceptedConfig respectively (FR8's compose inputs). Keyed by
	// board_id and device_id, matching the real Repository methods' own
	// keys. An absent key returns a nil slice/nil pointer, exactly like a
	// board with no sensors yet / no config ever pushed.
	inventory    map[int64][]InventorySensor
	lastAccepted map[string]*configpb.DeviceConfig

	// renamedBoards records every RenameBoard(boardID, name) call, in
	// order, so a test can assert exactly what the repository received (or
	// that it received nothing at all on a denied/rejected attempt).
	renamedBoards []renamedBoard

	// Read-path fixtures, returned identically regardless of caller -- FR5
	// leaves reads unscoped by ownership, so these have no per-caller
	// variant to configure.
	boardsWithState []BoardWithReadingRow
	boardIdentity   map[int64]BoardIdentity
	sensorDetails   map[int64][]SensorDetailRow
	sensorExists    map[int64]bool
	sensorHistory   map[int64]*SensorReadingHistory

	// claimedBoards records every successful ClaimBoard call, in order --
	// tests assert on this to prove a refused claim (already owned) issues
	// no write at all, not just that it returns an error.
	claimedBoards []claimedBoard
}

// claimedBoard is one recorded fakeRepository.ClaimBoard call that actually
// opened a new ownership row (an already-owned refusal is never appended
// here).
type claimedBoard struct {
	boardID       int64
	leaflabUserID int64
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		users:         map[string]int64{},
		devices:       map[string]int64{},
		owners:        map[int64]int64{},
		admins:        map[int64]bool{},
		inventory:     map[int64][]InventorySensor{},
		lastAccepted:  map[string]*configpb.DeviceConfig{},
		boardIdentity: map[int64]BoardIdentity{},
		sensorDetails: map[int64][]SensorDetailRow{},
		sensorExists:  map[int64]bool{},
		sensorHistory: map[int64]*SensorReadingHistory{},
	}
}

func (f *fakeRepository) GetLeafLabUserIDBySub(_ context.Context, oidcSub string) (int64, bool, error) {
	id, ok := f.users[oidcSub]
	return id, ok, nil
}

func (f *fakeRepository) GetCurrentBoardOwner(_ context.Context, boardID int64) (int64, bool, error) {
	id, ok := f.owners[boardID]
	return id, ok, nil
}

func (f *fakeRepository) HasRole(_ context.Context, leaflabUserID int64, role string) (bool, error) {
	if role != adminRole {
		return false, nil
	}
	return f.admins[leaflabUserID], nil
}

func (f *fakeRepository) GetBoardIDForDeviceID(_ context.Context, deviceID string) (int64, bool, error) {
	id, ok := f.devices[deviceID]
	return id, ok, nil
}

func (f *fakeRepository) InsertDeviceConfigNextVersion(_ context.Context, boardID int64, configJSON []byte) (int64, error) {
	f.nextVersion++
	f.insertedConfigs = append(f.insertedConfigs, insertedConfig{boardID: boardID, configJSON: configJSON})
	return f.nextVersion, nil
}

func (f *fakeRepository) GetLatestAcceptedConfig(_ context.Context, deviceID string) (*configpb.DeviceConfig, error) {
	return f.lastAccepted[deviceID], nil
}

func (f *fakeRepository) ListSensorInventoryForBoard(_ context.Context, boardID int64) ([]InventorySensor, error) {
	return f.inventory[boardID], nil
}

func (f *fakeRepository) ListBoards(_ context.Context) ([]BoardRow, error) {
	return nil, nil
}

func (f *fakeRepository) ListBoardsWithState(_ context.Context) ([]BoardWithReadingRow, error) {
	return f.boardsWithState, nil
}

func (f *fakeRepository) GetBoardIdentity(_ context.Context, boardID int64) (BoardIdentity, error) {
	identity, ok := f.boardIdentity[boardID]
	if !ok {
		return BoardIdentity{}, pgx.ErrNoRows
	}
	return identity, nil
}

// ClaimBoard mirrors ClaimBoard's production race-safety contract (a
// unique-violation-mapped ErrBoardAlreadyOwned) closely enough for the
// unit-level FR1/FR2 tests below -- the fake has no concurrent callers, so
// it only needs to refuse a second claim, not actually resolve a race
// (that's NFR2, proven against real Postgres in
// repository_integration_test.go).
func (f *fakeRepository) ClaimBoard(_ context.Context, boardID, leaflabUserID int64) error {
	if _, owned := f.owners[boardID]; owned {
		return ErrBoardAlreadyOwned
	}
	f.owners[boardID] = leaflabUserID
	f.claimedBoards = append(f.claimedBoards, claimedBoard{boardID: boardID, leaflabUserID: leaflabUserID})
	return nil
}

func (f *fakeRepository) RenameBoard(_ context.Context, boardID int64, name string) error {
	f.renamedBoards = append(f.renamedBoards, renamedBoard{boardID: boardID, name: name})
	return nil
}

func (f *fakeRepository) ListSensorDetailsForBoard(_ context.Context, boardID int64) ([]SensorDetailRow, error) {
	return f.sensorDetails[boardID], nil
}

func (f *fakeRepository) SensorExists(_ context.Context, sensorID int64) (bool, error) {
	return f.sensorExists[sensorID], nil
}

func (f *fakeRepository) GetSensorReadingHistory(_ context.Context, sensorID int64, _, _ time.Time) (*SensorReadingHistory, error) {
	h, ok := f.sensorHistory[sensorID]
	if !ok {
		return &SensorReadingHistory{}, nil
	}
	return h, nil
}

// fakePublisher is an in-memory configPublisher double: Publish always
// succeeds and records every call so tests can assert a denied write
// published nothing.
type fakePublisher struct {
	published []publishedMessage
}

func (f *fakePublisher) Publish(_ context.Context, exchange, routingKey string, body interface{}) error {
	f.published = append(f.published, publishedMessage{exchange: exchange, routingKey: routingKey, body: body})
	return nil
}

func newOwnershipTestServer(repo *fakeRepository, pub *fakePublisher) *LeafLabAPIServer {
	return NewLeafLabAPIServer(repo, pub, slog.Default())
}

// claimsCtx returns a context carrying grpcauth.Claims for subject, exactly
// as the real interceptor would have set it up before a fenced handler runs
// (see grpcauth.ContextWithClaims's doc comment).
func claimsCtx(subject string, roles ...string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject, Roles: roles})
}

// TestPushDeviceConfig_Owner_Succeeds is Testing criterion 2: the board's
// current owner can push a config to it.
func TestPushDeviceConfig_Owner_Succeeds(t *testing.T) {
	repo := newFakeRepository()
	repo.users["owner-sub"] = 1
	repo.devices["device-a"] = 100
	repo.owners[100] = 1
	pub := &fakePublisher{}
	srv := newOwnershipTestServer(repo, pub)

	resp, err := srv.PushDeviceConfig(claimsCtx("owner-sub"), &pb.PushDeviceConfigRequest{DeviceId: "device-a"})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), resp.Version)
	assert.Len(t, repo.insertedConfigs, 1)
	assert.Len(t, pub.published, 1)
}

// TestPushDeviceConfig_NonOwner_PermissionDenied is Testing criterion 3: an
// authenticated caller who is not the board's owner is denied, and the
// write never reaches the DB or the publisher.
func TestPushDeviceConfig_NonOwner_PermissionDenied(t *testing.T) {
	repo := newFakeRepository()
	repo.users["owner-sub"] = 1
	repo.users["other-sub"] = 2
	repo.devices["device-a"] = 100
	repo.owners[100] = 1
	pub := &fakePublisher{}
	srv := newOwnershipTestServer(repo, pub)

	_, err := srv.PushDeviceConfig(claimsCtx("other-sub"), &pb.PushDeviceConfigRequest{DeviceId: "device-a"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Empty(t, repo.insertedConfigs)
	assert.Empty(t, pub.published)
}

// TestPushDeviceConfig_UnownedBoard_PermissionDenied is Testing criterion 4
// (FR6): an unowned board accepts no write from any signed-in caller.
func TestPushDeviceConfig_UnownedBoard_PermissionDenied(t *testing.T) {
	repo := newFakeRepository()
	repo.users["some-sub"] = 1
	repo.devices["device-a"] = 100
	// No repo.owners[100] entry -- unowned.
	pub := &fakePublisher{}
	srv := newOwnershipTestServer(repo, pub)

	_, err := srv.PushDeviceConfig(claimsCtx("some-sub"), &pb.PushDeviceConfigRequest{DeviceId: "device-a"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Empty(t, repo.insertedConfigs)
	assert.Empty(t, pub.published)
}

// TestPushDeviceConfig_UnknownDeviceID_NotFound is Testing criterion 5: an
// unknown device_id is codes.NotFound and creates no board row (no more
// GetOrCreateBoard on this path).
func TestPushDeviceConfig_UnknownDeviceID_NotFound(t *testing.T) {
	repo := newFakeRepository()
	repo.users["some-sub"] = 1
	pub := &fakePublisher{}
	srv := newOwnershipTestServer(repo, pub)

	_, err := srv.PushDeviceConfig(claimsCtx("some-sub"), &pb.PushDeviceConfigRequest{DeviceId: "unknown-device"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Empty(t, repo.devices, "must not create a board row for an unknown device_id")
	assert.Empty(t, repo.insertedConfigs)
	assert.Empty(t, pub.published)
}

// TestPushDeviceConfig_NoLeafLabUserForSubject_PermissionDenied is Testing
// criterion 6 (LB1): an authenticated caller whose subject has no
// leaflab_user row is denied, and none is created as a side effect.
func TestPushDeviceConfig_NoLeafLabUserForSubject_PermissionDenied(t *testing.T) {
	repo := newFakeRepository()
	repo.devices["device-a"] = 100
	repo.owners[100] = 1
	pub := &fakePublisher{}
	srv := newOwnershipTestServer(repo, pub)

	ctx := claimsCtx("unregistered-sub")
	_, err := srv.PushDeviceConfig(ctx, &pb.PushDeviceConfigRequest{DeviceId: "device-a"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	_, found, _ := repo.GetLeafLabUserIDBySub(ctx, "unregistered-sub")
	assert.False(t, found, "leaflab-api must never create a leaflab_user row (LB1)")
	assert.Empty(t, repo.insertedConfigs)
	assert.Empty(t, pub.published)
}

// TestPushDeviceConfig_AdminRole_NoBypass_PermissionDenied is Testing
// criterion 7 (FR5): holding the admin role grants no write access to a
// board the caller does not own -- authorizeBoardWrite consults no role
// information at all.
func TestPushDeviceConfig_AdminRole_NoBypass_PermissionDenied(t *testing.T) {
	repo := newFakeRepository()
	repo.users["admin-sub"] = 1
	repo.users["owner-sub"] = 2
	repo.devices["device-a"] = 100
	repo.owners[100] = 2 // owned by someone else
	pub := &fakePublisher{}
	srv := newOwnershipTestServer(repo, pub)

	_, err := srv.PushDeviceConfig(claimsCtx("admin-sub", "admin"), &pb.PushDeviceConfigRequest{DeviceId: "device-a"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Empty(t, repo.insertedConfigs)
	assert.Empty(t, pub.published)
}

// TestPushDeviceConfig_PublishesComposedList_NotRawRequest is Testing
// criterion 8 (FR8): PushDeviceConfig publishes the board's full composed
// sensor list, not exactly req.Sensors -- a push that names only one sensor
// must not drop the board's other sensors from the wire payload.
func TestPushDeviceConfig_PublishesComposedList_NotRawRequest(t *testing.T) {
	repo := newFakeRepository()
	repo.users["owner-sub"] = 1
	repo.devices["device-a"] = 100
	repo.owners[100] = 1
	repo.lastAccepted["device-a"] = &configpb.DeviceConfig{
		DeviceId: "device-a",
		Sensors: []*configpb.SensorConfig{
			cfg(0x10, "topsoil"),
			cfg(0x11, "canopy"),
		},
	}
	pub := &fakePublisher{}
	srv := newOwnershipTestServer(repo, pub)

	_, err := srv.PushDeviceConfig(claimsCtx("owner-sub"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Sensors:  []*configpb.SensorConfig{nameOverride(0x11, "canopy_renamed")},
	})
	require.NoError(t, err)
	require.Len(t, pub.published, 1)

	published := &configpb.DeviceConfig{}
	require.NoError(t, proto.Unmarshal(pub.published[0].body.([]byte), published))

	require.Len(t, published.GetSensors(), 2, "the untouched sensor 0x10 must survive the push, not be dropped in favor of only the caller's supplied entry")
	byAddr := indexByI2CAddress(published.GetSensors())
	assert.Equal(t, "topsoil", byAddr[0x10].GetName())
	assert.Equal(t, "canopy_renamed", byAddr[0x11].GetName())
}

// TestPushDeviceConfig_RecordedRowMatchesPublishedPayload is Testing
// criterion 9: the device_config row inserted (InsertDeviceConfigNextVersion's
// configJSON argument) and the payload actually published to MQTT carry the
// same sensor list -- the recorded row is not silently out of sync with
// what the device receives.
func TestPushDeviceConfig_RecordedRowMatchesPublishedPayload(t *testing.T) {
	repo := newFakeRepository()
	repo.users["owner-sub"] = 1
	repo.devices["device-a"] = 100
	repo.owners[100] = 1
	repo.inventory[100] = []InventorySensor{inv(0x20, "root", nil)}
	repo.lastAccepted["device-a"] = &configpb.DeviceConfig{
		DeviceId: "device-a",
		Sensors:  []*configpb.SensorConfig{cfg(0x10, "topsoil")},
	}
	pub := &fakePublisher{}
	srv := newOwnershipTestServer(repo, pub)

	_, err := srv.PushDeviceConfig(claimsCtx("owner-sub"), &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Sensors:  []*configpb.SensorConfig{nameOverride(0x10, "topsoil_renamed")},
	})
	require.NoError(t, err)
	require.Len(t, repo.insertedConfigs, 1)
	require.Len(t, pub.published, 1)

	recorded := &configpb.DeviceConfig{}
	require.NoError(t, protojson.Unmarshal(repo.insertedConfigs[0].configJSON, recorded))
	published := &configpb.DeviceConfig{}
	require.NoError(t, proto.Unmarshal(pub.published[0].body.([]byte), published))

	require.Len(t, recorded.GetSensors(), 2)
	require.Len(t, published.GetSensors(), 2)
	recordedByAddr := indexByI2CAddress(recorded.GetSensors())
	publishedByAddr := indexByI2CAddress(published.GetSensors())
	for _, addr := range []uint32{0x10, 0x20} {
		assert.True(t, proto.Equal(recordedByAddr[addr], publishedByAddr[addr]),
			"recorded row and published payload must carry the same sensor entry for 0x%x", addr)
	}
}

// TestReads_NonOwner_SameContentAsOwner is Testing criterion 8 (FR5): reads
// stay unscoped by ownership -- an authenticated non-owner sees exactly the
// same ListBoardsWithState, GetBoardDetail, and GetSensorReadingHistory
// content the board's owner does.
func TestReads_NonOwner_SameContentAsOwner(t *testing.T) {
	repo := newFakeRepository()
	repo.users["owner-sub"] = 1
	repo.users["other-sub"] = 2
	repo.owners[100] = 1
	repo.boardsWithState = []BoardWithReadingRow{{BoardID: 100, DeviceID: "device-a"}}
	repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-a"}
	repo.sensorDetails[100] = []SensorDetailRow{{SensorID: 1, SensorName: "topsoil"}}
	repo.sensorExists[1] = true
	repo.sensorHistory[1] = &SensorReadingHistory{Points: []ReadingPoint{{Value: 1.0}}}

	srv := newOwnershipTestServer(repo, &fakePublisher{})
	ownerCtx := claimsCtx("owner-sub")
	nonOwnerCtx := claimsCtx("other-sub")

	ownerList, err := srv.ListBoardsWithState(ownerCtx, &pb.ListBoardsWithStateRequest{})
	require.NoError(t, err)
	nonOwnerList, err := srv.ListBoardsWithState(nonOwnerCtx, &pb.ListBoardsWithStateRequest{})
	require.NoError(t, err)
	assert.Equal(t, ownerList, nonOwnerList)

	ownerDetail, err := srv.GetBoardDetail(ownerCtx, &pb.GetBoardDetailRequest{BoardId: 100})
	require.NoError(t, err)
	nonOwnerDetail, err := srv.GetBoardDetail(nonOwnerCtx, &pb.GetBoardDetailRequest{BoardId: 100})
	require.NoError(t, err)
	assert.Equal(t, ownerDetail, nonOwnerDetail)

	now := time.Now()
	historyReq := &pb.GetSensorReadingHistoryRequest{
		SensorId: 1,
		From:     timestamppb.New(now.Add(-time.Hour)),
		To:       timestamppb.New(now),
	}
	ownerHistory, err := srv.GetSensorReadingHistory(ownerCtx, historyReq)
	require.NoError(t, err)
	nonOwnerHistory, err := srv.GetSensorReadingHistory(nonOwnerCtx, historyReq)
	require.NoError(t, err)
	assert.Equal(t, ownerHistory, nonOwnerHistory)
}

// -- #1765 ClaimBoard tests (FR1, FR2, NFR2's unit-level half) --------------
//
// NFR2's actual race-safety proof (two concurrent Postgres INSERTs, the
// partial unique index, exactly one winner) needs a real database and lives
// in repository_integration_test.go -- fakeRepository.ClaimBoard has no
// concurrent callers, so it can only stand in for the read-then-write
// *outcome* (refuse a second claim), not the atomicity mechanism itself.

// TestClaimBoard_UnownedBoard_Succeeds is Testing criterion 1: a signed-in
// user claiming an unowned board succeeds and opens exactly one ownership
// row.
func TestClaimBoard_UnownedBoard_Succeeds(t *testing.T) {
	repo := newFakeRepository()
	repo.users["claimant-sub"] = 1
	repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-a"}
	// No repo.owners[100] entry -- unowned.
	srv := newOwnershipTestServer(repo, &fakePublisher{})

	resp, err := srv.ClaimBoard(claimsCtx("claimant-sub"), &pb.ClaimBoardRequest{BoardId: 100})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, int64(1), repo.owners[100], "expected the claimant to become the board's owner")
	require.Len(t, repo.claimedBoards, 1, "expected exactly one ownership row opened")
	assert.Equal(t, claimedBoard{boardID: 100, leaflabUserID: 1}, repo.claimedBoards[0])
}

// TestClaimBoard_OwnedBoard_FailedPrecondition_NoWrite is Testing criterion
// 2 (FR2): claiming an already-owned board is refused with
// codes.FailedPrecondition and issues no write -- the existing ownership
// record (repo.owners[100]) is left untouched, not reassigned to the
// claimant.
func TestClaimBoard_OwnedBoard_FailedPrecondition_NoWrite(t *testing.T) {
	repo := newFakeRepository()
	repo.users["owner-sub"] = 1
	repo.users["claimant-sub"] = 2
	repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-a"}
	repo.owners[100] = 1
	srv := newOwnershipTestServer(repo, &fakePublisher{})

	_, err := srv.ClaimBoard(claimsCtx("claimant-sub"), &pb.ClaimBoardRequest{BoardId: 100})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Equal(t, int64(1), repo.owners[100], "existing ownership must be untouched by a refused claim")
	assert.Empty(t, repo.claimedBoards, "a refused claim must issue no write")
}

// TestClaimBoard_ReclaimByCurrentOwner_FailedPrecondition is Testing
// criterion 3: a re-claim by the board's own current owner is still a
// refusal, not a no-op success -- per the issue's "the open row's
// valid_from is never disturbed" requirement.
func TestClaimBoard_ReclaimByCurrentOwner_FailedPrecondition(t *testing.T) {
	repo := newFakeRepository()
	repo.users["owner-sub"] = 1
	repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-a"}
	repo.owners[100] = 1
	srv := newOwnershipTestServer(repo, &fakePublisher{})

	_, err := srv.ClaimBoard(claimsCtx("owner-sub"), &pb.ClaimBoardRequest{BoardId: 100})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Empty(t, repo.claimedBoards, "a re-claim by the current owner must issue no write")
}

// TestClaimBoard_NoLeafLabUserForCaller_PermissionDenied is Testing
// criterion 4 (LB1): a caller with no leaflab_user row for their subject is
// denied, with no implicit provisioning and no write.
func TestClaimBoard_NoLeafLabUserForCaller_PermissionDenied(t *testing.T) {
	repo := newFakeRepository()
	repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-a"}
	srv := newOwnershipTestServer(repo, &fakePublisher{})

	ctx := claimsCtx("unregistered-sub")
	_, err := srv.ClaimBoard(ctx, &pb.ClaimBoardRequest{BoardId: 100})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	_, found, _ := repo.GetLeafLabUserIDBySub(ctx, "unregistered-sub")
	assert.False(t, found, "leaflab-api must never create a leaflab_user row (LB1)")
	assert.Empty(t, repo.claimedBoards)
}

// TestClaimBoard_UnknownBoardID_NotFound is Testing criterion 5: claiming
// an unknown board_id returns codes.NotFound and issues no write.
func TestClaimBoard_UnknownBoardID_NotFound(t *testing.T) {
	repo := newFakeRepository()
	repo.users["claimant-sub"] = 1
	// No repo.boardIdentity[999] entry -- unknown board_id.
	srv := newOwnershipTestServer(repo, &fakePublisher{})

	_, err := srv.ClaimBoard(claimsCtx("claimant-sub"), &pb.ClaimBoardRequest{BoardId: 999})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Empty(t, repo.claimedBoards)
}

// TestListBoardsWithState_And_GetBoardDetail_OwnedByCallerOnlyForOwner is
// Testing criterion 6: owned_by_caller is true only for the calling user
// (not for any other authenticated caller, including a different owner
// entirely) and owner is unset (nil) for an unowned board, on both
// ListBoardsWithState and GetBoardDetail.
func TestListBoardsWithState_And_GetBoardDetail_OwnedByCallerOnlyForOwner(t *testing.T) {
	repo := newFakeRepository()
	repo.users["owner-sub"] = 1
	repo.users["other-sub"] = 2
	ownerRow := &OwnerRow{LeafLabUserID: 1, DisplayName: "Board Owner"}

	repo.boardsWithState = []BoardWithReadingRow{
		{BoardID: 100, DeviceID: "device-owned", Owner: ownerRow},
		{BoardID: 200, DeviceID: "device-unowned"},
	}
	repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-owned", Owner: ownerRow}
	repo.boardIdentity[200] = BoardIdentity{DeviceID: "device-unowned"}

	srv := newOwnershipTestServer(repo, &fakePublisher{})

	ownerList, err := srv.ListBoardsWithState(claimsCtx("owner-sub"), &pb.ListBoardsWithStateRequest{})
	require.NoError(t, err)
	otherList, err := srv.ListBoardsWithState(claimsCtx("other-sub"), &pb.ListBoardsWithStateRequest{})
	require.NoError(t, err)

	ownedByOwner := boardWithStateByID(ownerList.Boards, 100)
	ownedByOther := boardWithStateByID(otherList.Boards, 100)
	assert.True(t, ownedByOwner.GetOwnedByCaller(), "the board's actual owner must see owned_by_caller=true")
	assert.False(t, ownedByOther.GetOwnedByCaller(), "a different authenticated caller must see owned_by_caller=false")
	assert.NotNil(t, ownedByOwner.GetOwner(), "an owned board must carry its owner")

	unownedByOwner := boardWithStateByID(ownerList.Boards, 200)
	assert.False(t, unownedByOwner.GetOwnedByCaller(), "an unowned board must never report owned_by_caller=true")
	assert.Nil(t, unownedByOwner.GetOwner(), "an unowned board must leave owner unset, never a sentinel user id")

	ownerDetail, err := srv.GetBoardDetail(claimsCtx("owner-sub"), &pb.GetBoardDetailRequest{BoardId: 100})
	require.NoError(t, err)
	otherDetail, err := srv.GetBoardDetail(claimsCtx("other-sub"), &pb.GetBoardDetailRequest{BoardId: 100})
	require.NoError(t, err)
	assert.True(t, ownerDetail.GetOwnedByCaller())
	assert.False(t, otherDetail.GetOwnedByCaller())

	unownedDetail, err := srv.GetBoardDetail(claimsCtx("owner-sub"), &pb.GetBoardDetailRequest{BoardId: 200})
	require.NoError(t, err)
	assert.False(t, unownedDetail.GetOwnedByCaller())
	assert.Nil(t, unownedDetail.GetOwner())
}

// boardWithStateByID finds the board with boardID in boards, failing loudly
// (a nil dereference downstream) if it's ever missing -- every test that
// uses this seeds every board_id it looks up.
func boardWithStateByID(boards []*pb.BoardWithState, boardID int64) *pb.BoardWithState {
	for _, b := range boards {
		if b.GetBoardId() == boardID {
			return b
		}
	}
	return nil
}

// -- RenameBoard (#1767, FR3) --------------------------------------------
//
// These exercise LeafLabAPIServer.RenameBoard directly against the same
// fakeRepository/fakePublisher doubles as the PushDeviceConfig tests
// above, asserting both the returned error/response and, via
// repo.renamedBoards and pub.published, exactly what (if anything) reached
// the repository and the publisher -- a denied or rejected call must
// write and publish nothing.

// TestRenameBoard_Owner_Succeeds is Testing criterion 1: the board's
// current owner can rename it, and the repository receives the exact
// string the caller sent.
func TestRenameBoard_Owner_Succeeds(t *testing.T) {
	repo := newFakeRepository()
	repo.users["owner-sub"] = 1
	repo.owners[100] = 1
	repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-a"}
	srv := newOwnershipTestServer(repo, &fakePublisher{})

	resp, err := srv.RenameBoard(claimsCtx("owner-sub"), &pb.RenameBoardRequest{BoardId: 100, Name: "greenhouse"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, repo.renamedBoards, 1)
	assert.Equal(t, renamedBoard{boardID: 100, name: "greenhouse"}, repo.renamedBoards[0])
}

// TestRenameBoard_NonOwner_PermissionDenied is Testing criterion 2: an
// authenticated caller who is not the board's owner is denied, and the
// write never reaches the repository.
func TestRenameBoard_NonOwner_PermissionDenied(t *testing.T) {
	repo := newFakeRepository()
	repo.users["owner-sub"] = 1
	repo.users["other-sub"] = 2
	repo.owners[100] = 1
	repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-a"}
	srv := newOwnershipTestServer(repo, &fakePublisher{})

	_, err := srv.RenameBoard(claimsCtx("other-sub"), &pb.RenameBoardRequest{BoardId: 100, Name: "greenhouse"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Empty(t, repo.renamedBoards)
}

// TestRenameBoard_UnownedBoard_PermissionDenied is Testing criterion 3: an
// unowned board denies every renamer -- authorizeBoardWrite treats
// "unowned" identically to "owned by someone else", not as a free-for-all.
func TestRenameBoard_UnownedBoard_PermissionDenied(t *testing.T) {
	repo := newFakeRepository()
	repo.users["some-sub"] = 1
	repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-a"}
	// No repo.owners[100] entry -- unowned.
	srv := newOwnershipTestServer(repo, &fakePublisher{})

	_, err := srv.RenameBoard(claimsCtx("some-sub"), &pb.RenameBoardRequest{BoardId: 100, Name: "greenhouse"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Empty(t, repo.renamedBoards)
}

// TestRenameBoard_AdminRole_NoBypass_PermissionDenied is Testing criterion
// 4 (FR5): holding the admin role grants no rename access to a board the
// caller does not own -- authorizeBoardWrite consults no role information
// at all, so RenameBoard has no admin exception either.
func TestRenameBoard_AdminRole_NoBypass_PermissionDenied(t *testing.T) {
	repo := newFakeRepository()
	repo.users["admin-sub"] = 1
	repo.users["owner-sub"] = 2
	repo.owners[100] = 2 // owned by someone else
	repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-a"}
	srv := newOwnershipTestServer(repo, &fakePublisher{})

	_, err := srv.RenameBoard(claimsCtx("admin-sub", "admin"), &pb.RenameBoardRequest{BoardId: 100, Name: "greenhouse"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Empty(t, repo.renamedBoards)
}

// TestRenameBoard_EmptyOrWhitespaceName_InvalidArgument is Testing
// criterion 5: an empty string and a whitespace-only string are both
// rejected as InvalidArgument, and neither reaches the repository.
func TestRenameBoard_EmptyOrWhitespaceName_InvalidArgument(t *testing.T) {
	for _, name := range []string{"", "   ", "\t\n"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			repo := newFakeRepository()
			repo.users["owner-sub"] = 1
			repo.owners[100] = 1
			repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-a"}
			srv := newOwnershipTestServer(repo, &fakePublisher{})

			_, err := srv.RenameBoard(claimsCtx("owner-sub"), &pb.RenameBoardRequest{BoardId: 100, Name: name})
			require.Error(t, err)
			assert.Equal(t, codes.InvalidArgument, status.Code(err))
			assert.Empty(t, repo.renamedBoards)
		})
	}
}

// TestRenameBoard_UnknownBoardID_NotFound covers the RPC section's
// "Unknown board_id -> codes.NotFound" contract point: RenameBoard checks
// existence (GetBoardIdentity) before authorization, so a board_id with no
// row at all is distinguishable from an unowned one.
func TestRenameBoard_UnknownBoardID_NotFound(t *testing.T) {
	repo := newFakeRepository()
	repo.users["some-sub"] = 1
	// No repo.boardIdentity[999] entry -- unknown board_id.
	srv := newOwnershipTestServer(repo, &fakePublisher{})

	_, err := srv.RenameBoard(claimsCtx("some-sub"), &pb.RenameBoardRequest{BoardId: 999, Name: "greenhouse"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Empty(t, repo.renamedBoards)
}

// TestRenameBoard_NoLengthFormatOrUniquenessRule is Testing criterion 6: a
// very long name, a name with Unicode/punctuation/spaces, and a name equal
// to another board's existing name all succeed identically -- no length,
// format, or uniqueness rule exists on this path. The task issue's red/
// green discipline note ("add a uniqueness check and confirm this test
// goes red; remove it and confirm green") was exercised by hand against
// this test during Testing (see the Testing-phase issue comment on #1767)
// rather than left as permanent test code, since a real uniqueness check
// is explicitly a non-goal this test must keep failing forever, not a
// feature toggle to leave lying around.
func TestRenameBoard_NoLengthFormatOrUniquenessRule(t *testing.T) {
	veryLong := strings.Repeat("a", 10_000)
	unicodePunctuation := "Grow-Room #2 (北棟) — \"Alice's\" tent, row 3!"
	sameAsAnotherBoard := "duplicate-name"

	for _, tt := range []struct {
		name string
		want string
	}{
		{name: "very long name", want: veryLong},
		{name: "unicode and punctuation", want: unicodePunctuation},
		{name: "identical to another board's existing name", want: sameAsAnotherBoard},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepository()
			repo.users["owner-sub"] = 1
			repo.owners[100] = 1
			repo.owners[200] = 1
			repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-a"}
			repo.boardIdentity[200] = BoardIdentity{DeviceID: "device-b"}
			srv := newOwnershipTestServer(repo, &fakePublisher{})

			// Board 200 already has this name (conceptually -- the fake
			// repository has no name-storage state to seed, since
			// RenameBoard only ever issues a blind UPDATE; what matters
			// here is that renaming board 100 to the same string is not
			// rejected).
			resp, err := srv.RenameBoard(claimsCtx("owner-sub"), &pb.RenameBoardRequest{BoardId: 100, Name: tt.want})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.Len(t, repo.renamedBoards, 1)
			assert.Equal(t, tt.want, repo.renamedBoards[0].name)
		})
	}
}

// TestRenameBoard_NoPublish is Testing criterion 7: a successful rename
// issues no publish -- the rename path never pushes firmware.DeviceConfig
// (board name is not part of it at all) and never touches RabbitMQ.
func TestRenameBoard_NoPublish(t *testing.T) {
	repo := newFakeRepository()
	repo.users["owner-sub"] = 1
	repo.owners[100] = 1
	repo.boardIdentity[100] = BoardIdentity{DeviceID: "device-a"}
	pub := &fakePublisher{}
	srv := newOwnershipTestServer(repo, pub)

	_, err := srv.RenameBoard(claimsCtx("owner-sub"), &pb.RenameBoardRequest{BoardId: 100, Name: "greenhouse"})
	require.NoError(t, err)
	assert.Empty(t, pub.published)
}
