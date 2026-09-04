package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

	insertedConfigs []insertedConfig
	nextVersion     int64

	// Read-path fixtures, returned identically regardless of caller -- FR5
	// leaves reads unscoped by ownership, so these have no per-caller
	// variant to configure.
	boardsWithState []BoardWithReadingRow
	boardIdentity   map[int64]string
	sensorDetails   map[int64][]SensorDetailRow
	sensorExists    map[int64]bool
	sensorHistory   map[int64]*SensorReadingHistory
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		users:         map[string]int64{},
		devices:       map[string]int64{},
		owners:        map[int64]int64{},
		boardIdentity: map[int64]string{},
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

func (f *fakeRepository) GetBoardIDForDeviceID(_ context.Context, deviceID string) (int64, bool, error) {
	id, ok := f.devices[deviceID]
	return id, ok, nil
}

func (f *fakeRepository) InsertDeviceConfigNextVersion(_ context.Context, boardID int64, configJSON []byte) (int64, error) {
	f.nextVersion++
	f.insertedConfigs = append(f.insertedConfigs, insertedConfig{boardID: boardID, configJSON: configJSON})
	return f.nextVersion, nil
}

func (f *fakeRepository) GetLatestAcceptedConfig(_ context.Context, _ string) (*configpb.DeviceConfig, error) {
	return nil, nil
}

func (f *fakeRepository) ListBoards(_ context.Context) ([]BoardRow, error) {
	return nil, nil
}

func (f *fakeRepository) ListBoardsWithState(_ context.Context) ([]BoardWithReadingRow, error) {
	return f.boardsWithState, nil
}

func (f *fakeRepository) GetBoardIdentity(_ context.Context, boardID int64) (string, error) {
	deviceID, ok := f.boardIdentity[boardID]
	if !ok {
		return "", pgx.ErrNoRows
	}
	return deviceID, nil
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
	repo.boardIdentity[100] = "device-a"
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
