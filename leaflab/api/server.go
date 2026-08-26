package main

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/whale-net/everything/leaflab/api/apierrors"
	"github.com/whale-net/everything/leaflab/canonkey"
	"github.com/whale-net/everything/leaflab/api/pagetoken"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/rmq"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
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
		// device_id validation failure
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"device_id",
			"device_id",
			apierrors.InvalidDeviceID,
		)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
	}
	// Canonicalize sensors on ingress at the proto/JSON boundary
	for _, sensor := range req.Sensors {
		if err := canonkey.ValidateAndCanonicalizeSensorConfig(sensor); err != nil {
			// Sensor validation failure
			detail := apierrors.NewErrorDetail(
				pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
				"sensors",
				"sensor_type",
				apierrors.InvalidSensorConfig,
			)
			return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
		}
	}


	boardID, err := s.repo.GetOrCreateBoard(ctx, req.DeviceId)
	if err != nil {
		// Internal database error
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"board",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// Build the proto with a placeholder version; we need configJSON for the
	// atomic insert that returns the real version, so marshal without version first.
	cfgProto := &configpb.DeviceConfig{
		DeviceId: req.DeviceId,
		Sensors:  req.Sensors,
	}
	configJSON, err := protojson.Marshal(cfgProto)
	if err != nil {
		// Internal serialization error
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"device_config",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// Atomically assign version and record the pending push before publishing.
	// This ensures the DB row always exists before the device can ack.
	version, err := s.repo.InsertDeviceConfigNextVersion(ctx, boardID, configJSON)
	if err != nil {
		// Internal database error
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"device_config",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// Re-marshal with the real version for the wire payload.
	cfgProto.Version = uint64(version)
	wire, err := proto.Marshal(cfgProto)
	if err != nil {
		// Internal serialization error
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"device_config",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// MQTT '/' → AMQP '.'; device_id should not contain '/' but sanitize to be safe.
	routingKey := fmt.Sprintf("leaflab.%s.config", strings.ReplaceAll(req.DeviceId, "/", "."))
	if err := s.publisher.Publish(ctx, mqttExchange, routingKey, wire); err != nil {
		// Row is in DB but publish failed — device never received the push.
		// The row stays accepted=FALSE, which is correct: no ack will arrive.
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"device_config",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	s.logger.Info("device config pushed",
		"device_id", req.DeviceId,
		"version", version,
		"sensors", len(req.Sensors))

	return &pb.PushDeviceConfigResponse{Version: uint64(version)}, nil
}

func (s *LeafLabAPIServer) GetDeviceConfig(ctx context.Context, req *pb.GetDeviceConfigRequest) (*pb.GetDeviceConfigResponse, error) {
	if err := validateDeviceID(req.DeviceId); err != nil {
		// device_id validation failure
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"device_id",
			"device_id",
			apierrors.InvalidDeviceID,
		)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
	}

	cfg, err := s.repo.GetLatestAcceptedConfig(ctx, req.DeviceId)
	if err != nil {
		// Internal database error
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"device_config",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}
	if cfg == nil {
		return &pb.GetDeviceConfigResponse{Found: false}, nil
	}
	return &pb.GetDeviceConfigResponse{Config: cfg, Found: true}, nil
}

func (s *LeafLabAPIServer) ListBoards(ctx context.Context, req *pb.ListBoardsRequest) (*pb.ListBoardsResponse, error) {
	// Parse page token
	var decodedToken *pagetoken.Token
	if req.Page != nil && req.Page.PageToken != "" {
		var err error
		decodedToken, err = pagetoken.Decode(req.Page.PageToken)
		if err != nil {
			// Invalid page token
			detail := apierrors.NewErrorDetail(
				pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
				"page",
				"page_token",
				apierrors.InvalidPageToken,
			)
			return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
		}
	}

	// Determine page size
	var pageSize int32 = DefaultPageSize
	if req.Page != nil && req.Page.PageSize > 0 {
		pageSize = req.Page.PageSize
	}

	// Fetch boards from repository
	rows, nextToken, err := s.repo.ListBoards(ctx, pageSize, decodedToken)
	if err != nil {
		// Internal database error
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"board",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// Get total board count for progress display
	totalSize, err := s.repo.GetTotalBoardCount(ctx)
	if err != nil {
		// Internal database error
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"board",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// Convert database rows to proto messages
	boards := make([]*pb.BoardInfo, 0, len(rows))
	for _, r := range rows {
		boards = append(boards, &pb.BoardInfo{
			DeviceId:   r.DeviceID,
			BoardId:    r.BoardID,
			RecordedAt: r.RecordedAt,
		})
	}

	// Encode next page token
	var nextPageToken string
	if nextToken != nil {
		encoded, err := pagetoken.Encode(nextToken)
		if err != nil {
			// This should not happen in normal operation; log and return empty token
			s.logger.Error("failed to encode next page token", "error", err)
			nextPageToken = ""
		} else {
			nextPageToken = encoded
		}
	}

	return &pb.ListBoardsResponse{
		Boards: boards,
		Page: &pb.PageResponse{
			NextPageToken: nextPageToken,
			TotalSize:     totalSize,
		},
	}, nil
}

func (s *LeafLabAPIServer) GetSensorTimelines(ctx context.Context, req *pb.GetSensorTimelinesRequest) (*pb.GetSensorTimelinesResponse, error) {
	if req.SensorId <= 0 {
		// Invalid sensor_id
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"sensor",
			"sensor_id",
			apierrors.InvalidSensorConfig,
		)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "sensor_id must be positive", detail)
	}

	timelines, err := s.repo.GetSensorTimelines(ctx, req.SensorId)
	if err != nil {
		// Internal database error
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"sensor",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// Convert database results to proto messages
	pbTimelines := &pb.SensorTimelines{
		SensorId:         timelines.SensorID,
		NameTimeline:     make([]*pb.SensorNameEntry, len(timelines.NameTimeline)),
		HardwareTimeline: make([]*pb.SensorHardwareEntry, len(timelines.HardwareTimeline)),
		RegionTimeline:   make([]*pb.SensorRegionEntry, len(timelines.RegionTimeline)),
	}

	for i, entry := range timelines.NameTimeline {
		pbTimelines.NameTimeline[i] = &pb.SensorNameEntry{
			Name:      entry.Name,
			ValidFrom: entry.ValidFrom,
			ValidTo:   entry.ValidTo,
		}
	}

	for i, entry := range timelines.HardwareTimeline {
		pbTimelines.HardwareTimeline[i] = &pb.SensorHardwareEntry{
			I2CAddress: entry.I2CAddress,
			MuxPath:    entry.MuxPath,
			ValidFrom:  entry.ValidFrom,
			ValidTo:    entry.ValidTo,
		}
	}

	for i, entry := range timelines.RegionTimeline {
		pbTimelines.RegionTimeline[i] = &pb.SensorRegionEntry{
			RegionId:   entry.RegionID,
			RegionName: entry.RegionName,
			ValidFrom:  entry.ValidFrom,
			ValidTo:    entry.ValidTo,
		}
	}

	return &pb.GetSensorTimelinesResponse{
		Timelines: pbTimelines,
	}, nil
}

