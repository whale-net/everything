package main

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/rmq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// validDeviceID allows alphanumeric, hyphens, and underscores.
// Excludes MQTT wildcard characters (+, #) and path separators (/, .).
var validDeviceID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func validateDeviceID(id string) error {
	if id == "" {
		return fmt.Errorf("device_id is required")
	}
	if !validDeviceID.MatchString(id) {
		return fmt.Errorf("device_id %q contains invalid characters: only a-z, A-Z, 0-9, - and _ are allowed", id)
	}
	return nil
}

const mqttExchange = "amq.topic"

// reportingThreshold is the fixed recency threshold behind ReportingState
// (FR5): a board is REPORTING if its most recent sensor_reading.recorded_at
// is within this window, STALE if it has readings older than this, and
// NEVER_REPORTED if it has none at all. Shared with the board-detail path
// (per-sensor state); not configurable per board/sensor and not exposed on
// the request or response.
const reportingThreshold = 10 * time.Minute

type LeafLabAPIServer struct {
	pb.UnimplementedLeafLabAPIServer
	repo      *Repository
	publisher *rmq.Publisher
	logger    *slog.Logger
}

func NewLeafLabAPIServer(repo *Repository, publisher *rmq.Publisher, logger *slog.Logger) *LeafLabAPIServer {
	return &LeafLabAPIServer{
		repo:      repo,
		publisher: publisher,
		logger:    logger,
	}
}

func (s *LeafLabAPIServer) PushDeviceConfig(ctx context.Context, req *pb.PushDeviceConfigRequest) (*pb.PushDeviceConfigResponse, error) {
	if err := validateDeviceID(req.DeviceId); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	boardID, err := s.repo.GetOrCreateBoard(ctx, req.DeviceId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "board lookup: %v", err)
	}

	// Build the proto with a placeholder version; we need configJSON for the
	// atomic insert that returns the real version, so marshal without version first.
	cfgProto := &configpb.DeviceConfig{
		DeviceId: req.DeviceId,
		Sensors:  req.Sensors,
	}
	configJSON, err := protojson.Marshal(cfgProto)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "protojson marshal: %v", err)
	}

	// Atomically assign version and record the pending push before publishing.
	// This ensures the DB row always exists before the device can ack.
	version, err := s.repo.InsertDeviceConfigNextVersion(ctx, boardID, configJSON)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "record config push: %v", err)
	}

	// Re-marshal with the real version for the wire payload.
	cfgProto.Version = uint64(version)
	wire, err := proto.Marshal(cfgProto)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "proto marshal: %v", err)
	}

	// MQTT '/' → AMQP '.'; device_id should not contain '/' but sanitize to be safe.
	routingKey := fmt.Sprintf("leaflab.%s.config", strings.ReplaceAll(req.DeviceId, "/", "."))
	if err := s.publisher.Publish(ctx, mqttExchange, routingKey, wire); err != nil {
		// Row is in DB but publish failed — device never received the push.
		// The row stays accepted=FALSE, which is correct: no ack will arrive.
		return nil, status.Errorf(codes.Internal, "publish config: %v", err)
	}

	s.logger.Info("device config pushed",
		"device_id", req.DeviceId,
		"version", version,
		"sensors", len(req.Sensors))

	return &pb.PushDeviceConfigResponse{Version: uint64(version)}, nil
}

func (s *LeafLabAPIServer) GetDeviceConfig(ctx context.Context, req *pb.GetDeviceConfigRequest) (*pb.GetDeviceConfigResponse, error) {
	if err := validateDeviceID(req.DeviceId); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	cfg, err := s.repo.GetLatestAcceptedConfig(ctx, req.DeviceId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get config: %v", err)
	}
	if cfg == nil {
		return &pb.GetDeviceConfigResponse{Found: false}, nil
	}
	return &pb.GetDeviceConfigResponse{Config: cfg, Found: true}, nil
}

func (s *LeafLabAPIServer) ListBoards(ctx context.Context, _ *pb.ListBoardsRequest) (*pb.ListBoardsResponse, error) {
	rows, err := s.repo.ListBoards(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list boards: %v", err)
	}

	boards := make([]*pb.BoardInfo, 0, len(rows))
	for _, r := range rows {
		boards = append(boards, &pb.BoardInfo{
			DeviceId: r.DeviceID,
			BoardId:  r.BoardID,
		})
	}
	return &pb.ListBoardsResponse{Boards: boards}, nil
}

// ListBoardsWithState returns every board (FR4 — no owner filtering) with a
// reporting state derived purely from sensor_reading recency (FR5).
func (s *LeafLabAPIServer) ListBoardsWithState(ctx context.Context, _ *pb.ListBoardsWithStateRequest) (*pb.ListBoardsWithStateResponse, error) {
	rows, err := s.repo.ListBoardsWithState(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list boards with state: %v", err)
	}

	now := time.Now()
	boards := make([]*pb.BoardWithState, 0, len(rows))
	for _, r := range rows {
		bw := &pb.BoardWithState{
			BoardId:        r.BoardID,
			DeviceId:       r.DeviceID,
			ReportingState: reportingState(r.LastReadingAt, now),
		}
		if r.LastReadingAt != nil {
			bw.LastReadingAt = timestamppb.New(*r.LastReadingAt)
		}
		boards = append(boards, bw)
	}

	s.logger.Info("boards with state listed", "count", len(boards))
	return &pb.ListBoardsWithStateResponse{Boards: boards}, nil
}

// reportingState derives a ReportingState purely from a board's most recent
// reading timestamp (nil if it has none) and the current time. Kept as a
// pure function, independent of the repository/DB, so the threshold boundary
// is unit-testable without a database.
//
// The boundary is inclusive of "reporting": a reading recorded exactly
// reportingThreshold ago is still within "the last 10 minutes" per the
// ReportingState proto doc, not yet stale.
func reportingState(lastReadingAt *time.Time, now time.Time) pb.ReportingState {
	if lastReadingAt == nil {
		return pb.ReportingState_REPORTING_STATE_NEVER_REPORTED
	}
	if now.Sub(*lastReadingAt) <= reportingThreshold {
		return pb.ReportingState_REPORTING_STATE_REPORTING
	}
	return pb.ReportingState_REPORTING_STATE_STALE
}
