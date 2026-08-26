package main

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
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

// getAuthorizationDecision builds an AuthorizationDecision for the authenticated principal.
// FR4: Authorization is per entity. The decision reflects the scopes reachable by the principal.
// For V1, this is the set of households the principal is a member of.
func (s *LeafLabAPIServer) getAuthorizationDecision(ctx context.Context, principalID string) (*AuthorizationDecision, error) {
	householdIDs, err := s.repo.GetPrincipalHouseholds(ctx, principalID)
	if err != nil {
		return nil, fmt.Errorf("get principal households: %w", err)
	}

	// Build scopes from household memberships.
	scopes := make([]Scope, len(householdIDs))
	for i, hid := range householdIDs {
		scopes[i] = NewHouseholdScope(hid)
	}

	return NewAuthorizationDecision(principalID, scopes...), nil
}

// refusedAsNotFound returns a gRPC status that is byte-identical and timing-equivalent to
// what is returned when an entity does not exist.
// NFR2: "Refusal for an out-of-reach entity is indistinguishable from non-existent".
func refusedAsNotFound() error {
	return status.Error(codes.NotFound, "")
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

	// NFR2: No existence oracle — build authorization decision first, before checking board existence.
	// This ensures identical timing whether board doesn't exist or principal can't access it.
	// FR4: Per-entity authorization is based on principal's reach.
	auth, err := s.getAuthorizationDecision(ctx, subject)
	if err != nil {
		s.logger.Error("authorization decision failed",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}

	// Now check board existence. If it doesn't exist or is out of reach, return the same error.
	boardID, householdID, err := s.repo.GetBoardByDeviceID(ctx, req.DeviceId)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Board does not exist. Return "not found" without distinguishing from out-of-reach.
			return nil, refusedAsNotFound()
		}
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

	// Board exists. Check if principal can access it.
	canAccess := auth.ContainsHousehold(householdID)
	if !canAccess {
		// FR4: Out-of-reach entity. Refuse as if it doesn't exist (NFR2).
		return nil, refusedAsNotFound()
	}

	// Principal can access this board. Proceed with configuration push.
	// Build the proto with a placeholder version; we need configJSON for the
	// atomic insert that returns the real version, so marshal without version first.
	cfgProto := &configpb.DeviceConfig{
		DeviceId: req.DeviceId,
		Sensors:  req.Sensors,
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

	// Atomically assign version and record the pending push before publishing.
	// This ensures the DB row always exists before the device can ack.
	version, err := s.repo.InsertDeviceConfigNextVersion(ctx, boardID, configJSON)
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
		"sensors", len(req.Sensors),
		"subject", subject,
		"correlation_id", corrID)

	return &pb.PushDeviceConfigResponse{Version: uint64(version)}, nil
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

	// NFR2: No existence oracle — build authorization decision first, before checking board existence.
	// This ensures identical timing whether board doesn't exist or principal can't access it.
	// FR4: Per-entity authorization is based on principal's reach.
	auth, err := s.getAuthorizationDecision(ctx, subject)
	if err != nil {
		s.logger.Error("authorization decision failed",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}

	// Now check board existence. If it doesn't exist or is out of reach, return the same error.
	_, householdID, err := s.repo.GetBoardByDeviceID(ctx, req.DeviceId)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Board does not exist. Return "not found" without distinguishing from out-of-reach.
			return nil, refusedAsNotFound()
		}
		s.logger.Error("board lookup failed",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "board lookup: %v", err)
	}

	// Board exists. Check if principal can access it.
	canAccess := auth.ContainsHousehold(householdID)
	if !canAccess {
		// FR4: Out-of-reach entity. Refuse as if it doesn't exist (NFR2).
		return nil, refusedAsNotFound()
	}

	// Principal can access this board. Retrieve the config.
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

	// FR4: Per-entity authorization. Build authorization decision for principal.
	// FR5: Board listings are household-scoped by default.
	auth, err := s.getAuthorizationDecision(ctx, subject)
	if err != nil {
		s.logger.Error("authorization decision failed",
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}

	// Aggregate boards across all reachable households.
	// FR5: Aggregates computed only over entities the caller can reach.
	var allRows []BoardRow
	for _, scope := range auth.HouseholdScopes() {
		rows, err := s.repo.ListBoardsByHousehold(ctx, scope.HouseholdID())
		if err != nil {
			s.logger.Error("list boards failed",
				"household_id", scope.HouseholdID(),
				"subject", subject,
				"correlation_id", corrID,
				"error", err)
			return nil, status.Errorf(codes.Internal, "list boards: %v", err)
		}
		allRows = append(allRows, rows...)
	}

	// Global keyset ordering across all reachable households: (recorded_at DESC, board_id ASC),
	// matching ListBoardsByHousehold's per-household ordering and the pagetoken.Token shape.
	sort.Slice(allRows, func(i, j int) bool {
		if allRows[i].RecordedAt != allRows[j].RecordedAt {
			return allRows[i].RecordedAt > allRows[j].RecordedAt
		}
		return allRows[i].BoardID < allRows[j].BoardID
	})

	// Apply the keyset cursor in-memory: skip rows at or before the token's position.
	startIdx := 0
	if decodedToken != nil {
		for i, r := range allRows {
			if r.RecordedAt < decodedToken.LastRecordedAt ||
				(r.RecordedAt == decodedToken.LastRecordedAt && r.BoardID > decodedToken.LastBoardID) {
				startIdx = i
				goto found
			}
		}
		startIdx = len(allRows)
	found:
	}
	pageRows := allRows[startIdx:]

	totalSize := int32(len(allRows))

	var nextToken *pagetoken.Token
	if int32(len(pageRows)) > pageSize {
		last := pageRows[pageSize-1]
		nextToken = &pagetoken.Token{LastRecordedAt: last.RecordedAt, LastBoardID: last.BoardID}
		pageRows = pageRows[:pageSize]
	}

	// Convert database rows to proto messages
	allBoards := make([]*pb.BoardInfo, 0, len(pageRows))
	for _, r := range pageRows {
		allBoards = append(allBoards, &pb.BoardInfo{
			DeviceId:   r.DeviceID,
			BoardId:    r.BoardID,
			RecordedAt: r.RecordedAt,
		})
	}

	s.logger.Info("boards listed",
		"board_count", len(allBoards),
		"subject", subject,
		"correlation_id", corrID)

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
		Boards: allBoards,
		Page: &pb.PageResponse{
			NextPageToken: nextPageToken,
			TotalSize:     totalSize,
		},
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

func (s *LeafLabAPIServer) GetSensorTimelines(ctx context.Context, req *pb.GetSensorTimelinesRequest) (*pb.GetSensorTimelinesResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

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
		s.logger.Error("get sensor timelines failed",
			"sensor_id", req.SensorId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
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
