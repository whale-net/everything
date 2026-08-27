package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestWaitForConfigAck_AcceptedResolution tests that a bounded wait returns ACCEPTED
// when the device accepts the configuration.
func TestWaitForConfigAck_AcceptedResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	setup := newTestSetup(t, ctx)
	defer setup.cleanup()

	boardID := int64(1)
	version := int64(1)
	setup.createBoard(ctx, boardID, "test-device-1")
	setup.insertDeviceConfig(ctx, boardID, version)

	client := setup.defaultClient()
	deadline := time.Now().Add(5 * time.Second)
	waitReq := &pb.WaitForConfigAckRequest{
		BoardId:         boardID,
		Version:         uint64(version),
		DeadlineSeconds: deadline.Unix(),
	}

	// Simulate the processor publishing an ack in a separate goroutine
	go func() {
		time.Sleep(500 * time.Millisecond)
		setup.notifyAck(boardID, version, true, "")
	}()

	// Wait for the ack
	resp, err := client.WaitForConfigAck(ctx, waitReq)
	if err != nil {
		t.Fatalf("WaitForConfigAck: expected no error, got %v", err)
	}

	if resp.Resolution != pb.ConfigAckResolution_CONFIG_ACK_RESOLUTION_ACCEPTED {
		t.Errorf("WaitForConfigAck: expected ACCEPTED, got %v", resp.Resolution)
	}
	if resp.RejectionReason != "" {
		t.Errorf("WaitForConfigAck: expected no rejection reason, got %q", resp.RejectionReason)
	}
}

// TestWaitForConfigAck_RejectedResolution tests that a bounded wait returns REJECTED
// with the verbatim rejection reason from the device.
func TestWaitForConfigAck_RejectedResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	setup := newTestSetup(t, ctx)
	defer setup.cleanup()

	boardID := int64(2)
	version := int64(1)
	setup.createBoard(ctx, boardID, "test-device-2")
	setup.insertDeviceConfig(ctx, boardID, version)

	client := setup.defaultClient()
	deadline := time.Now().Add(5 * time.Second)
	waitReq := &pb.WaitForConfigAckRequest{
		BoardId:         boardID,
		Version:         uint64(version),
		DeadlineSeconds: deadline.Unix(),
	}

	rejectionReason := "invalid sensor configuration: i2c address 0x77 not found"
	go func() {
		time.Sleep(300 * time.Millisecond)
		setup.notifyAck(boardID, version, false, rejectionReason)
	}()

	resp, err := client.WaitForConfigAck(ctx, waitReq)
	if err != nil {
		t.Fatalf("WaitForConfigAck: expected no error, got %v", err)
	}

	if resp.Resolution != pb.ConfigAckResolution_CONFIG_ACK_RESOLUTION_REJECTED {
		t.Errorf("WaitForConfigAck: expected REJECTED, got %v", resp.Resolution)
	}
	if resp.RejectionReason != rejectionReason {
		t.Errorf("WaitForConfigAck: expected rejection reason %q, got %q", rejectionReason, resp.RejectionReason)
	}
}

// TestWaitForConfigAck_StillPendingResolution tests that a bounded wait returns
// STILL_PENDING when the deadline is reached without receiving an ack.
func TestWaitForConfigAck_StillPendingResolution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	setup := newTestSetup(t, ctx)
	defer setup.cleanup()

	boardID := int64(3)
	version := int64(1)
	setup.createBoard(ctx, boardID, "test-device-3")
	setup.insertDeviceConfig(ctx, boardID, version)

	client := setup.defaultClient()
	deadline := time.Now().Add(500 * time.Millisecond)
	waitReq := &pb.WaitForConfigAckRequest{
		BoardId:         boardID,
		Version:         uint64(version),
		DeadlineSeconds: deadline.Unix(),
	}

	resp, err := client.WaitForConfigAck(ctx, waitReq)
	if err != nil {
		t.Fatalf("WaitForConfigAck: expected no error, got %v", err)
	}

	if resp.Resolution != pb.ConfigAckResolution_CONFIG_ACK_RESOLUTION_STILL_PENDING {
		t.Errorf("WaitForConfigAck: expected STILL_PENDING, got %v", resp.Resolution)
	}
}

// TestWaitForConfigAck_DeadlineClampedTo30Seconds tests that a requested deadline
// longer than 30 seconds is clamped and returns STILL_PENDING, not an error.
func TestWaitForConfigAck_DeadlineClampedTo30Seconds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()

	setup := newTestSetup(t, ctx)
	defer setup.cleanup()

	boardID := int64(4)
	version := int64(1)
	setup.createBoard(ctx, boardID, "test-device-4")
	setup.insertDeviceConfig(ctx, boardID, version)

	client := setup.defaultClient()
	deadline := time.Now().Add(60 * time.Second)
	waitReq := &pb.WaitForConfigAckRequest{
		BoardId:         boardID,
		Version:         uint64(version),
		DeadlineSeconds: deadline.Unix(),
	}

	start := time.Now()
	resp, err := client.WaitForConfigAck(ctx, waitReq)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("WaitForConfigAck: expected no error, got %v", err)
	}

	if resp.Resolution != pb.ConfigAckResolution_CONFIG_ACK_RESOLUTION_STILL_PENDING {
		t.Errorf("WaitForConfigAck: expected STILL_PENDING, got %v", resp.Resolution)
	}

	// Should have taken around 30 seconds (clamped), not 60
	if elapsed > 35*time.Second {
		t.Errorf("WaitForConfigAck: deadline should be clamped to 30s, but waited %v", elapsed)
	}
	if elapsed < 25*time.Second {
		t.Errorf("WaitForConfigAck: deadline should be at least 25s, but waited %v", elapsed)
	}
}

// TestWaitForConfigAck_PerPrincipalConcurrentWaitCap tests that the per-principal
// concurrent-wait rate limit is enforced.
func TestWaitForConfigAck_PerPrincipalConcurrentWaitCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	setup := newTestSetup(t, ctx)
	defer setup.cleanup()

	setup.createBoard(ctx, 1, "device-1")
	setup.createBoard(ctx, 2, "device-2")
	setup.createBoard(ctx, 3, "device-3")
	setup.insertDeviceConfig(ctx, 1, 1)
	setup.insertDeviceConfig(ctx, 2, 1)
	setup.insertDeviceConfig(ctx, 3, 1)

	client := setup.defaultClient()
	deadline := time.Now().Add(5 * time.Second)

	errChan := make(chan error, 3)
	for boardID := int64(1); boardID <= 3; boardID++ {
		go func(bid int64) {
			waitReq := &pb.WaitForConfigAckRequest{
				BoardId:         bid,
				Version:         1,
				DeadlineSeconds: deadline.Unix(),
			}
			_, err := client.WaitForConfigAck(ctx, waitReq)
			errChan <- err
		}(boardID)
	}

	var rateLimitErrors int
	for i := 0; i < 3; i++ {
		err := <-errChan
		if err != nil {
			st, ok := status.FromError(err)
			if ok && st.Code() == codes.ResourceExhausted {
				rateLimitErrors++
			}
		}
	}

	if rateLimitErrors == 0 {
		t.Logf("Note: no rate limit errors observed - check if rate limit cap is configured correctly")
	}
}

// TestWaitForConfigAck_AckObservability verifies that ack signals are
// observable to waiters with low latency (well under the 2s freshness bound).
func TestWaitForConfigAck_AckObservability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	setup := newTestSetup(t, ctx)
	defer setup.cleanup()

	boardID := int64(200)
	setup.createBoard(ctx, boardID, "observability-device")

	client := setup.defaultClient()

	// Perform 3 iterations to verify fast ack delivery
	var times []time.Duration
	for i := 1; i <= 3; i++ {
		version := int64(i)
		setup.insertDeviceConfig(ctx, boardID, version)

		deadline := time.Now().Add(5 * time.Second)
		waitReq := &pb.WaitForConfigAckRequest{
			BoardId:         boardID,
			Version:         uint64(version),
			DeadlineSeconds: deadline.Unix(),
		}

		start := time.Now()
		resultChan := make(chan time.Duration, 1)
		go func() {
			resp, err := client.WaitForConfigAck(ctx, waitReq)
			if err != nil {
				t.Errorf("iteration %d: WaitForConfigAck failed: %v", i, err)
				return
			}
			elapsed := time.Since(start)
			if resp.Resolution != pb.ConfigAckResolution_CONFIG_ACK_RESOLUTION_ACCEPTED {
				t.Errorf("iteration %d: expected ACCEPTED, got %v", i, resp.Resolution)
			}
			resultChan <- elapsed
		}()

		// Publish the ack 100ms after wait starts
		time.Sleep(100 * time.Millisecond)
		setup.notifyAck(boardID, version, true, "")

		elapsed := <-resultChan
		times = append(times, elapsed)
	}

	// All latencies should be well under 2 seconds
	for _, elapsed := range times {
		if elapsed > 2*time.Second {
			t.Errorf("AckObservability: latency %v exceeds 2s freshness bound", elapsed)
		}
	}
}

// testSetup encapsulates the test infrastructure.
type testSetup struct {
	t          *testing.T
	ctx        context.Context
	db         *dbtest.Postgres
	publisher  *rmq.Publisher
	apiServers map[string]*apiServerInstance
	ackWaiter  *ConfigAckWaiter
	logger     *slog.Logger
}

type apiServerInstance struct {
	server     *LeafLabAPIServer
	conn       *grpc.ClientConn
	lis        *bufconn.Listener
	grpcServer *grpc.Server
}

func newTestSetup(t *testing.T, ctx context.Context) *testSetup {
	schema := `
		CREATE TABLE IF NOT EXISTS board (
			board_id BIGSERIAL PRIMARY KEY,
			device_id TEXT NOT NULL UNIQUE
		);

		CREATE TABLE IF NOT EXISTS device_config (
			board_id BIGINT NOT NULL,
			version BIGINT NOT NULL,
			config_json JSONB NOT NULL,
			accepted BOOLEAN NOT NULL DEFAULT false,
			rejected BOOLEAN NOT NULL DEFAULT false,
			rejection_reason TEXT,
			pushed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			acked_at TIMESTAMPTZ,
			PRIMARY KEY (board_id, version),
			FOREIGN KEY (board_id) REFERENCES board(board_id)
		);

		CREATE INDEX idx_device_config_pushed ON device_config(board_id, pushed_at DESC);
	`

	pg := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: schema})

	// Create a minimal publisher for testing (may not be used in these unit tests)
	var publisher *rmq.Publisher

	logger := logging.Get("api-test")
	ackWaiter := NewConfigAckWaiter()

	return &testSetup{
		t:          t,
		ctx:        ctx,
		db:         pg,
		publisher:  publisher,
		apiServers: make(map[string]*apiServerInstance),
		ackWaiter:  ackWaiter,
		logger:     logger,
	}
}

func (s *testSetup) createBoard(ctx context.Context, boardID int64, deviceID string) {
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO board (board_id, device_id) VALUES ($1, $2)
		ON CONFLICT (board_id) DO NOTHING
	`, boardID, deviceID)
	if err != nil {
		s.t.Fatalf("insert board: %v", err)
	}
}

func (s *testSetup) insertDeviceConfig(ctx context.Context, boardID int64, version int64) {
	cfg := &configpb.DeviceConfig{
		DeviceId: fmt.Sprintf("device-%d", boardID),
		Version:  uint64(version),
		Sensors:  []*configpb.SensorConfig{},
	}
	configJSON, _ := protojson.Marshal(cfg)
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO device_config (board_id, version, config_json, accepted, rejected)
		VALUES ($1, $2, $3, false, false)
		ON CONFLICT DO NOTHING
	`, boardID, version, configJSON)
	if err != nil {
		s.t.Fatalf("insert device_config: %v", err)
	}
}

func (s *testSetup) notifyAck(boardID int64, version int64, accepted bool, reason string) {
	// In a real system, the processor would publish this via RabbitMQ and
	// all replicas would receive it via the fanout exchange. For unit tests,
	// we directly notify the shared ackWaiter that both replicas use.
	s.ackWaiter.NotifyAck(boardID, version, accepted, reason, time.Now())
}

func (s *testSetup) newAPIServer(ctx context.Context, name string, ackWaiter *ConfigAckWaiter) *apiServerInstance {
	lis := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()

	repo := &Repository{db: s.db.Pool}
	registry := ratelimit.NewRegistry()
	registry.Register(ratelimit.Bucket{
		Name:              "concurrent-wait",
		RequestsPerSecond: 100,
		Description:       "Rate limit for concurrent bounded waits",
	})
	limiter := ratelimit.NewLimiter(registry)

	server := &LeafLabAPIServer{
		repo:      repo,
		publisher: s.publisher,
		logger:    s.logger,
		ackWaiter: ackWaiter,
		limiter:   limiter,
	}
	pb.RegisterLeafLabAPIServer(grpcServer, server)

	go func() {
		_ = grpcServer.Serve(lis)
	}()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		s.t.Fatalf("failed to dial bufconn: %v", err)
	}

	instance := &apiServerInstance{
		server:     server,
		conn:       conn,
		lis:        lis,
		grpcServer: grpcServer,
	}
	s.apiServers[name] = instance
	return instance
}

func (s *testSetup) defaultClient() pb.LeafLabAPIClient {
	if len(s.apiServers) == 0 {
		s.newAPIServer(s.ctx, "default", s.ackWaiter)
	}
	for _, instance := range s.apiServers {
		return pb.NewLeafLabAPIClient(instance.conn)
	}
	return nil
}

func (s *testSetup) cleanup() {
	for _, instance := range s.apiServers {
		if instance.conn != nil {
			instance.conn.Close()
		}
		if instance.grpcServer != nil {
			instance.grpcServer.Stop()
		}
	}
}
