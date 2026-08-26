package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/apierrors"
	"github.com/whale-net/everything/leaflab/api/pagetoken"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/canonkey"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// getSubjectAndCorrelationID extracts the subject and correlation ID from context.
// Subject may be empty for unauthenticated requests (e.g., Health).
func getSubjectAndCorrelationID(ctx context.Context) (subject string, correlationID string) {
	if claims, ok := grpcauth.ClaimsFromContext(ctx); ok {
		subject = claims.Subject
	}
	if corrID, ok := logging.CorrelationIDFromContext(ctx); ok {
		correlationID = corrID
	}
	return subject, correlationID
}

// requireAuthentication ensures the caller is authenticated, returning an error if not.
func requireAuthentication(ctx context.Context) error {
	_, ok := grpcauth.ClaimsFromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	return nil
}

// buildProvenanceJSON builds a JSON representation of the provenance map for storage.
func buildProvenanceJSON(provenance map[int]pb.Provenance) ([]byte, error) {
	if len(provenance) == 0 {
		return json.Marshal(make(map[string]int))
	}

	// Convert index-based map to string-based for JSON serialization
	jsonMap := make(map[string]int)
	for idx, prov := range provenance {
		jsonMap[fmt.Sprintf("%d", idx)] = int(prov)
	}
	return json.Marshal(jsonMap)
}

func (s *LeafLabAPIServer) PushDeviceConfig(ctx context.Context, req *pb.PushDeviceConfigRequest) (*pb.PushDeviceConfigResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

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
	// FR82: Validate scope is present and valid
	if req.Scope == pb.ConfigScope_CONFIG_SCOPE_UNSPECIFIED {
		// Scope is required; reject with a distinct failure class naming the field
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"PushDeviceConfigRequest",
			"scope",
			apierrors.InvalidArgument,
		)
		s.logger.Info("push rejected: missing required scope field",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "required field missing: scope", detail)
	}
	// Canonicalize sensors on ingress at the proto/JSON boundary
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
		s.logger.Error("board lookup failed",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"board",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}
	// Get the board's household for validation (FR1.2, FR1.3 push-time).
	householdID, err := s.repo.GetBoardHousehold(ctx, boardID)
	if err != nil {
		s.logger.Error("get board household failed",
			"device_id", req.DeviceId,
			"board_id", boardID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "get board household: %v", err)
	}

	// FR82: Handle COMPLETE vs EDIT scope
	var configProto *configpb.DeviceConfig
	var provenance map[int]pb.Provenance

	if req.Scope == pb.ConfigScope_CONFIG_SCOPE_COMPLETE {
		// COMPLETE scope: use payload as-is, all entries are AUTHORED
		configProto = &configpb.DeviceConfig{
			DeviceId: req.DeviceId,
			Sensors:  req.Sensors,
		}
		provenance = make(map[int]pb.Provenance)
		for i := range req.Sensors {
			provenance[i] = pb.Provenance_PROVENANCE_AUTHORED
		}
	} else if req.Scope == pb.ConfigScope_CONFIG_SCOPE_EDIT {
		// EDIT scope: materialize from current accepted config
		baseConfig, err := s.repo.GetLatestAcceptedConfigByBoardID(ctx, boardID)
		if err != nil {
			s.logger.Error("get base config failed",
				"device_id", req.DeviceId,
				"board_id", boardID,
				"subject", subject,
				"correlation_id", corrID,
				"error", err)
			detail := apierrors.NewErrorDetail(
				pb.FailureClass_FAILURE_CLASS_INTERNAL,
				"device_config",
				"",
				apierrors.InternalError,
			)
			return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
		}

		if baseConfig == nil {
			// No accepted config to complete from - refuse EDIT
			detail := apierrors.NewErrorDetail(
				pb.FailureClass_FAILURE_CLASS_PRECONDITION,
				"device_config",
				"scope",
				apierrors.InvalidArgument,
			)
			s.logger.Info("edit rejected: no accepted config to complete from",
				"device_id", req.DeviceId,
				"board_id", boardID,
				"subject", subject,
				"correlation_id", corrID)
			return nil, apierrors.StatusWithDetail(codes.InvalidArgument,
				"this board has no accepted config to complete your edit from; send a complete push", detail)
		}

		// Materialize the config
		materialiser := NewMaterialiser()
		var removalResults []RemovalResult
		configProto, provenance, removalResults, err = materialiser.Materialize(baseConfig, req)
		if err != nil {
			s.logger.Error("materialisation failed",
				"device_id", req.DeviceId,
				"board_id", boardID,
				"subject", subject,
				"correlation_id", corrID,
				"error", err)
			detail := apierrors.NewErrorDetail(
				pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
				"device_config",
				"",
				apierrors.InvalidArgument,
			)
			return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
		}

		s.logger.Info("edit materialised",
			"device_id", req.DeviceId,
			"board_id", boardID,
			"authored_sensors", len(req.Sensors),
			"total_sensors", len(configProto.Sensors),
			"removals", len(removalResults),
			"subject", subject,
			"correlation_id", corrID)
		_ = removalResults // Removal forms tracked for logging/future use
	}


	// FR1.3 push-time validation: validate every region reference against the board's household.
	// Reject the entire push if any region doesn't belong to the household (no partial application).
	for i, sensor := range configProto.Sensors {
		if sensor.RegionId == 0 {
			continue // unassigned region is allowed
		}
		ok, err := s.repo.ValidateRegionBelongsToHousehold(ctx, int64(sensor.RegionId), householdID)
		if err != nil {
			s.logger.Error("region validation failed",
				"device_id", req.DeviceId,
				"sensor_index", i,
				"region_id", sensor.RegionId,
				"subject", subject,
				"correlation_id", corrID,
				"error", err)
			return nil, status.Errorf(codes.Internal, "region validation: %v", err)
		}
		if !ok {
			// Reject the entire push, naming the offending entry and field.
			return nil, status.Error(codes.InvalidArgument,
				fmt.Sprintf("invalid region_id in sensor entry %d (name: %q): region %d does not belong to your household",
					i, sensor.Name, sensor.RegionId))
		}
	}

	// Build the proto with a placeholder version; we need configJSON for the
	// atomic insert that returns the real version, so marshal without version first.
	cfgProto := &configpb.DeviceConfig{
		DeviceId: req.DeviceId,
		Sensors:  configProto.Sensors,
	}
	configJSON, err := protojson.Marshal(cfgProto)
	if err != nil {
		s.logger.Error("protojson marshal failed",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"device_config",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// Build provenance JSON
	provenanceJSON, err := buildProvenanceJSON(provenance)
	if err != nil {
		s.logger.Error("build provenance json failed",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
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
	version, err := s.repo.InsertDeviceConfigNextVersionWithProvenance(ctx, boardID, configJSON, provenanceJSON)
	if err != nil {
		s.logger.Error("record config push failed",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
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
		s.logger.Error("proto marshal failed",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
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
		s.logger.Error("publish config failed",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
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
		"sensors", len(configProto.Sensors),
		"scope", req.Scope.String(),
		"subject", subject,
		"correlation_id", corrID)

	return &pb.PushDeviceConfigResponse{Version: uint64(version)}, nil
}

// PushDeviceConfigDryRun validates a config push without storing or assigning a version.
// Accepts the same scope and payload structure as PushDeviceConfig; returns what would
// be stored (with materialised entries for EDIT), but does not persist to the database.
func (s *LeafLabAPIServer) PushDeviceConfigDryRun(ctx context.Context, req *pb.PushDeviceConfigRequest) (*pb.PushDeviceConfigResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

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

	// FR82: Validate scope is present and valid
	if req.Scope == pb.ConfigScope_CONFIG_SCOPE_UNSPECIFIED {
		// Scope is required; reject with a distinct failure class naming the field
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"PushDeviceConfigRequest",
			"scope",
			apierrors.InvalidArgument,
		)
		s.logger.Info("dry run rejected: missing required scope field",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "required field missing: scope", detail)
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
		s.logger.Error("dry run board lookup failed",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"board",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// Get the board's household for validation (FR1.2, FR1.3).
	householdID, err := s.repo.GetBoardHousehold(ctx, boardID)
	if err != nil {
		s.logger.Error("dry run get board household failed",
			"device_id", req.DeviceId,
			"board_id", boardID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "get board household: %v", err)
	}

	// FR82: Handle COMPLETE vs EDIT scope (dry run path)
	var configProto *configpb.DeviceConfig
	var provenance map[int]pb.Provenance

	if req.Scope == pb.ConfigScope_CONFIG_SCOPE_COMPLETE {
		// COMPLETE scope: use payload as-is, all entries are AUTHORED
		configProto = &configpb.DeviceConfig{
			DeviceId: req.DeviceId,
			Sensors:  req.Sensors,
		}
		provenance = make(map[int]pb.Provenance)
		for i := range req.Sensors {
			provenance[i] = pb.Provenance_PROVENANCE_AUTHORED
		}
	} else if req.Scope == pb.ConfigScope_CONFIG_SCOPE_EDIT {
		// EDIT scope: materialize from current accepted config (dry run)
		baseConfig, err := s.repo.GetLatestAcceptedConfigByBoardID(ctx, boardID)
		if err != nil {
			s.logger.Error("dry run get base config failed",
				"device_id", req.DeviceId,
				"board_id", boardID,
				"subject", subject,
				"correlation_id", corrID,
				"error", err)
			detail := apierrors.NewErrorDetail(
				pb.FailureClass_FAILURE_CLASS_INTERNAL,
				"device_config",
				"",
				apierrors.InternalError,
			)
			return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
		}

		if baseConfig == nil {
			// No accepted config to complete from - refuse EDIT
			detail := apierrors.NewErrorDetail(
				pb.FailureClass_FAILURE_CLASS_PRECONDITION,
				"device_config",
				"scope",
				apierrors.InvalidArgument,
			)
			s.logger.Info("dry run edit rejected: no accepted config to complete from",
				"device_id", req.DeviceId,
				"board_id", boardID,
				"subject", subject,
				"correlation_id", corrID)
			return nil, apierrors.StatusWithDetail(codes.InvalidArgument,
				"this board has no accepted config to complete your edit from; send a complete push", detail)
		}

		// Materialize the config
		materialiser := NewMaterialiser()
		configProto, provenance, _, err = materialiser.Materialize(baseConfig, req)
		if err != nil {
			s.logger.Error("dry run materialisation failed",
				"device_id", req.DeviceId,
				"board_id", boardID,
				"subject", subject,
				"correlation_id", corrID,
				"error", err)
			detail := apierrors.NewErrorDetail(
				pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
				"device_config",
				"",
				apierrors.InvalidArgument,
			)
			return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
		}

		s.logger.Info("dry run edit materialised",
			"device_id", req.DeviceId,
			"board_id", boardID,
			"authored_sensors", len(req.Sensors),
			"total_sensors", len(configProto.Sensors),
			"subject", subject,
			"correlation_id", corrID)
	}

	// FR1.3 push-time validation: validate every region reference against the board's household.
	// Reject the entire push if any region doesn't belong to the household (no partial application).
	for i, sensor := range configProto.Sensors {
		if sensor.RegionId == 0 {
			continue // unassigned region is allowed
		}
		ok, err := s.repo.ValidateRegionBelongsToHousehold(ctx, int64(sensor.RegionId), householdID)
		if err != nil {
			s.logger.Error("dry run region validation failed",
				"device_id", req.DeviceId,
				"sensor_index", i,
				"region_id", sensor.RegionId,
				"subject", subject,
				"correlation_id", corrID,
				"error", err)
			return nil, status.Errorf(codes.Internal, "region validation: %v", err)
		}
		if !ok {
			// Reject the entire push, naming the offending entry and field.
			return nil, status.Error(codes.InvalidArgument,
				fmt.Sprintf("invalid region_id in sensor entry %d (name: %q): region %d does not belong to your household",
					i, sensor.Name, sensor.RegionId))
		}
	}

	s.logger.Info("dry run completed",
		"device_id", req.DeviceId,
		"sensors", len(configProto.Sensors),
		"scope", req.Scope.String(),
		"subject", subject,
		"correlation_id", corrID)

	// Return what would be stored, but with version = 0 since no version is assigned in dry run
	return &pb.PushDeviceConfigResponse{Version: 0}, nil
}

func (s *LeafLabAPIServer) GetDeviceConfig(ctx context.Context, req *pb.GetDeviceConfigRequest) (*pb.GetDeviceConfigResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

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
		s.logger.Error("get config failed",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"device_config",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	s.logger.Info("device config retrieved",
		"device_id", req.DeviceId,
		"found", cfg != nil,
		"subject", subject,
		"correlation_id", corrID)

	if cfg == nil {
		return &pb.GetDeviceConfigResponse{Found: false}, nil
	}
	return &pb.GetDeviceConfigResponse{Config: cfg, Found: true}, nil
}

func (s *LeafLabAPIServer) ListBoards(ctx context.Context, req *pb.ListBoardsRequest) (*pb.ListBoardsResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

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

	s.logger.Info("boards listed",
		"board_count", len(boards),
		"subject", subject,
		"correlation_id", corrID)

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

func (s *LeafLabAPIServer) Health(ctx context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	// Health is the only unauthenticated endpoint.
	// It returns no household data, only service health status.
	corrID, _ := logging.CorrelationIDFromContext(ctx)

	// For now, always return UP.
	// In production, this would check downstream dependencies (DB, MQTT, etc).
	s.logger.Info("health check",
		"correlation_id", corrID)

	return &pb.HealthResponse{Status: pb.HealthResponse_UP}, nil
}
