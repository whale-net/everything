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

// maxHistoryRangeDays is the longest range GetSensorReadingHistory will serve
// (FR8); enforcing it server-side is what makes the UI's own cap honest.
const maxHistoryRangeDays = 30

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

// GetSensorReadingHistory returns one sensor's raw reading history over an
// absolute time range, subject to the point cap and invalid-reading
// accounting (FR9); an empty range is not an error (FR10).
func (s *LeafLabAPIServer) GetSensorReadingHistory(ctx context.Context, req *pb.GetSensorReadingHistoryRequest) (*pb.GetSensorReadingHistoryResponse, error) {
	if req.From == nil || req.To == nil {
		return nil, status.Error(codes.InvalidArgument, "from and to are required")
	}
	from := req.From.AsTime()
	to := req.To.AsTime()

	if !to.After(from) {
		return nil, status.Error(codes.InvalidArgument, "to must be after from")
	}
	if to.Sub(from) > maxHistoryRangeDays*24*time.Hour {
		return nil, status.Errorf(codes.InvalidArgument, "range must not exceed %d days", maxHistoryRangeDays)
	}

	exists, err := s.repo.SensorExists(ctx, req.SensorId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check sensor: %v", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "sensor %d not found", req.SensorId)
	}

	history, err := s.repo.GetSensorReadingHistory(ctx, req.SensorId, from, to)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get sensor reading history: %v", err)
	}

	resp := &pb.GetSensorReadingHistoryResponse{
		Points:               make([]*pb.ReadingPoint, 0, len(history.Points)),
		Capped:               history.Capped,
		PointCap:             historyPointCap,
		ExcludedInvalidCount: history.ExcludedInvalidCount,
	}
	for _, p := range history.Points {
		resp.Points = append(resp.Points, &pb.ReadingPoint{
			RecordedAt: timestamppb.New(p.RecordedAt),
			Value:      p.Value,
		})
	}
	if history.Capped {
		resp.CoveredFrom = timestamppb.New(history.CoveredFrom)
		resp.CoveredTo = timestamppb.New(history.CoveredTo)
	}
	return resp, nil
}
