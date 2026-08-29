package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// fakeRepo implements deviceRepository entirely in memory so GetHealth's
// probe logic (FR63.1) is unit-testable without a live Postgres connection.
// Only Ping is exercised by these tests; the other methods exist to satisfy
// the interface and panic if unexpectedly called.
type fakeRepo struct {
	pingErr error
}

func (f *fakeRepo) GetOrCreateBoard(ctx context.Context, deviceID string) (int64, error) {
	panic("not used by GetHealth tests")
}

func (f *fakeRepo) InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte) (int64, error) {
	panic("not used by GetHealth tests")
}

func (f *fakeRepo) GetLatestAcceptedConfig(ctx context.Context, deviceID string) (*configpb.DeviceConfig, error) {
	panic("not used by GetHealth tests")
}

func (f *fakeRepo) ListBoards(ctx context.Context, afterBoardID int64, hasAfter bool, limit int32) ([]BoardRow, error) {
	panic("not used by GetHealth tests")
}

func (f *fakeRepo) Ping(ctx context.Context) error {
	return f.pingErr
}

func (f *fakeRepo) FindSensorIDByName(ctx context.Context, boardID int64, name string) (int64, bool, error) {
	panic("not used by this file's tests")
}

func (f *fakeRepo) resolveSensorTypeID(ctx context.Context, typeName string) (int64, bool, error) {
	panic("not used by this file's tests")
}

func (f *fakeRepo) LoadBoardSensorIdentities(ctx context.Context, boardID int64) ([]BoardSensorIdentity, error) {
	panic("not used by this file's tests")
}

func (f *fakeRepo) RewireSensorHW(ctx context.Context, sensorID int64, hw *HardwareAddress) error {
	panic("not used by this file's tests")
}

func (f *fakeRepo) SensorSensorTypeName(ctx context.Context, sensorID int64) (string, bool, error) {
	panic("not used by this file's tests")
}

func (f *fakeRepo) ListSensorNameIntervals(ctx context.Context, sensorID int64, windowStart, windowEnd *time.Time, afterValidFrom time.Time, afterID int64, hasAfter bool, limit int32) ([]NameIntervalRow, error) {
	panic("not used by this file's tests")
}

func (f *fakeRepo) ListSensorHWIntervals(ctx context.Context, sensorID int64, windowStart, windowEnd *time.Time, afterValidFrom time.Time, afterID int64, hasAfter bool, limit int32) ([]HWIntervalRow, error) {
	panic("not used by this file's tests")
}

func (f *fakeRepo) ListSensorRegionIntervals(ctx context.Context, sensorID int64, windowStart, windowEnd *time.Time, afterValidFrom time.Time, afterID int64, hasAfter bool, limit int32) ([]RegionIntervalRow, error) {
	panic("not used by this file's tests")
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// countPopulatedFields returns how many fields protoreflect considers set
// (non-default) on msg. Used to assert FR63.2's "no version, no dependency
// names, no per-dependency status detail" -- GetHealthResponse must carry
// exactly one populated field (status) no matter which branch produced it.
func countPopulatedFields(msg protoreflect.Message) int {
	n := 0
	msg.Range(func(protoreflect.FieldDescriptor, protoreflect.Value) bool {
		n++
		return true
	})
	return n
}

// TestGetHealth_NoCredential_Succeeds proves GetHealth is callable with an
// empty (unauthenticated) context -- no grpcauth.Claims required -- and
// that the response carries exactly one populated field (FR63.2). rmqConn
// is nil (mirrors a test server with no RabbitMQ dependency; GetHealth's
// nil handling treats that as mqUp=false), so together with a healthy DB
// ping this exercises the DEGRADED branch. See main_test.go's bufconn test
// for the same assertion exercised through the full RPC/interceptor chain,
// including the allowlist itself.
func TestGetHealth_NoCredential_Succeeds(t *testing.T) {
	server := NewLeafLabAPIServer(&fakeRepo{}, nil, nil, discardLogger())

	resp, err := server.GetHealth(context.Background(), &pb.GetHealthRequest{})
	if err != nil {
		t.Fatalf("GetHealth with no credential returned an error, want success (FR63.1): %v", err)
	}
	if got := countPopulatedFields(resp.ProtoReflect()); got != 1 {
		t.Errorf("GetHealthResponse has %d populated fields, want exactly 1 (FR63.2)", got)
	}
}

// TestGetHealth_DatabaseUnreachable_Degraded proves a DB probe failure maps
// to HEALTH_DEGRADED and nothing more specific -- no error, no detail about
// which dependency failed (FR63.2).
func TestGetHealth_DatabaseUnreachable_Degraded(t *testing.T) {
	server := NewLeafLabAPIServer(&fakeRepo{pingErr: errors.New("connection refused")}, nil, nil, discardLogger())

	resp, err := server.GetHealth(context.Background(), &pb.GetHealthRequest{})
	if err != nil {
		t.Fatalf("GetHealth returned an error rather than a degraded status: %v", err)
	}
	if resp.Status != pb.HealthStatus_HEALTH_DEGRADED {
		t.Errorf("Status = %v, want HEALTH_DEGRADED", resp.Status)
	}
	if got := countPopulatedFields(resp.ProtoReflect()); got != 1 {
		t.Errorf("GetHealthResponse has %d populated fields, want exactly 1 (FR63.2)", got)
	}
}

// TestGetHealth_MQConnectionNil_Degraded proves a nil/unavailable
// RabbitMQ-MQTT connection also maps to HEALTH_DEGRADED, independent of DB
// health (FR63.1's "pgx pool or the RabbitMQ/MQTT connection").
func TestGetHealth_MQConnectionNil_Degraded(t *testing.T) {
	server := NewLeafLabAPIServer(&fakeRepo{}, nil, nil, discardLogger())

	resp, err := server.GetHealth(context.Background(), &pb.GetHealthRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != pb.HealthStatus_HEALTH_DEGRADED {
		t.Errorf("Status = %v, want HEALTH_DEGRADED with rmqConn nil", resp.Status)
	}
}

// TestGetHealth_ErrorNeverCarriesDependencyDetail is NFR13/FR63.2's
// leak-nothing guarantee applied to GetHealth specifically: even the error
// path (which cannot happen today -- GetHealth never returns a non-nil
// error) must never surface a dependency-specific reason if one were added
// later. Documented here as a regression tripwire: GetHealth returning a
// non-nil error at all would already violate FR63.1 ("this is our problem,
// not yours" must still answer as a successful RPC), so this test simply
// pins that invariant.
func TestGetHealth_ErrorNeverCarriesDependencyDetail(t *testing.T) {
	server := NewLeafLabAPIServer(&fakeRepo{pingErr: errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")}, nil, nil, discardLogger())

	_, err := server.GetHealth(context.Background(), &pb.GetHealthRequest{})
	if err != nil {
		t.Fatalf("GetHealth returned a transport error %v -- FR63.1 requires GetHealth to always succeed and report DEGRADED instead", err)
	}
}
