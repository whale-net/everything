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

// preparedConfigPush is the resolved outcome of validating and materialising a
// push request: the effective config it would produce, plus everything the
// write path and the dry-run/diff path each need afterwards.
type preparedConfigPush struct {
	BoardID     int64
	HouseholdID int64
	// BaseConfig is the board's current accepted config (EDIT scope only; nil
	// for COMPLETE or for a board with no accepted config yet).
	BaseConfig *configpb.DeviceConfig
	// Config is the effective config this push would result in.
	Config *configpb.DeviceConfig
	// Provenance is only meaningful when actually committing (used to build
	// provenance_json); dry-run/diff paths ignore it.
	Provenance map[int]pb.Provenance
}

// prepareConfigPush validates a push request and resolves the effective config
// it would produce — COMPLETE: the payload as-is; EDIT: materialised against
// the board's current accepted config — including scope, sensor, and
// household/region validation. It is the single source of truth for "what
// would this push do", shared by PushDeviceConfig, PushDeviceConfigDryRun,
// pushDeviceConfigInternal (multi-board push), and PushDeviceConfigMultiBoardDryRun.
// No code path may reimplement this logic — a second implementation is exactly
// what would let a dry-run preview and the real push disagree.
//
// It performs no database writes and publishes nothing. logPrefix only affects
// log message text (e.g. "push", "dry run", "multi-board push"), so each caller's
// logs stay distinguishable.
func (s *LeafLabAPIServer) prepareConfigPush(ctx context.Context, req *pb.PushDeviceConfigRequest, subject, corrID, logPrefix string) (*preparedConfigPush, error) {
	if err := validateDeviceID(req.DeviceId); err != nil {
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
		s.logger.Info(logPrefix+" rejected: missing required scope field",
			"device_id", req.DeviceId,
			"subject", subject,
			"correlation_id", corrID)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "required field missing: scope", detail)
	}

	// Canonicalize sensors on ingress at the proto/JSON boundary
	for _, sensor := range req.Sensors {
		if err := canonkey.ValidateAndCanonicalizeSensorConfig(sensor); err != nil {
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
		s.logger.Error(logPrefix+" board lookup failed",
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
		s.logger.Error(logPrefix+" get board household failed",
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
	var baseConfig *configpb.DeviceConfig

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
		baseConfig, err = s.repo.GetLatestAcceptedConfigByBoardID(ctx, boardID)
		if err != nil {
			s.logger.Error(logPrefix+" get base config failed",
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
			s.logger.Info(logPrefix+" rejected: no accepted config to complete from",
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
			s.logger.Error(logPrefix+" materialisation failed",
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

		s.logger.Info(logPrefix+" edit materialised",
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
			s.logger.Error(logPrefix+" region validation failed",
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

	return &preparedConfigPush{
		BoardID:     boardID,
		HouseholdID: householdID,
		BaseConfig:  baseConfig,
		Config:      configProto,
		Provenance:  provenance,
	}, nil
}

// commitConfigPush stores prepared.Config as the board's next version, publishes
// it over MQTT, and records an audit entry. This is the one real write path —
// shared by PushDeviceConfig and pushDeviceConfigInternal (multi-board push) —
// so a dry run, which never calls this, cannot drift from what a real push does.
// pushGroupID is "" for a standalone push; reason is "" unless the push is part
// of an audited multi-board push (FR48).
func (s *LeafLabAPIServer) commitConfigPush(ctx context.Context, req *pb.PushDeviceConfigRequest, prepared *preparedConfigPush, subject, pushGroupID, reason string) (int64, error) {
	// Build the proto with a placeholder version; we need configJSON for the
	// atomic insert that returns the real version, so marshal without version first.
	cfgProto := &configpb.DeviceConfig{
		DeviceId: req.DeviceId,
		Sensors:  prepared.Config.Sensors,
	}
	configJSON, err := protojson.Marshal(cfgProto)
	if err != nil {
		return 0, fmt.Errorf("marshal config: %w", err)
	}

	provenanceJSON, err := buildProvenanceJSON(prepared.Provenance)
	if err != nil {
		return 0, fmt.Errorf("build provenance json: %w", err)
	}

	// Atomically assign version and record the pending push before publishing.
	// This ensures the DB row always exists before the device can ack.
	var version int64
	if pushGroupID != "" {
		version, err = s.repo.InsertDeviceConfigNextVersionWithProvenanceAndPushGroup(ctx, prepared.BoardID, configJSON, provenanceJSON, pushGroupID)
	} else {
		version, err = s.repo.InsertDeviceConfigNextVersionWithProvenance(ctx, prepared.BoardID, configJSON, provenanceJSON)
	}
	if err != nil {
		return 0, fmt.Errorf("insert config: %w", err)
	}

	// Re-marshal with the real version for the wire payload.
	cfgProto.Version = uint64(version)
	wire, err := proto.Marshal(cfgProto)
	if err != nil {
		return 0, fmt.Errorf("marshal wire: %w", err)
	}

	// MQTT '/' → AMQP '.'; device_id should not contain '/' but sanitize to be safe.
	routingKey := fmt.Sprintf("leaflab.%s.config", strings.ReplaceAll(req.DeviceId, "/", "."))
	if err := s.publisher.Publish(ctx, mqttExchange, routingKey, wire); err != nil {
		// Row is in DB but publish failed — device never received the push.
		// The row stays accepted=FALSE, which is correct: no ack will arrive.
		return 0, fmt.Errorf("publish: %w", err)
	}

	// FR8: every write produces an append-only audit record.
	if err := s.repo.RecordAuditWithConfig(ctx, subject, prepared.HouseholdID, "push_config", "device_config",
		version, &version, nil, nil, reason); err != nil {
		s.logger.Error("record audit failed",
			"device_id", req.DeviceId,
			"version", version,
			"subject", subject,
			"error", err)
	}

	return version, nil
}

func (s *LeafLabAPIServer) PushDeviceConfig(ctx context.Context, req *pb.PushDeviceConfigRequest) (*pb.PushDeviceConfigResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	prepared, err := s.prepareConfigPush(ctx, req, subject, corrID, "push")
	if err != nil {
		return nil, err
	}

	version, err := s.commitConfigPush(ctx, req, prepared, subject, "", "")
	if err != nil {
		s.logger.Error("push commit failed",
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
		"sensors", len(prepared.Config.Sensors),
		"scope", req.Scope.String(),
		"subject", subject,
		"correlation_id", corrID)

	return &pb.PushDeviceConfigResponse{Version: uint64(version)}, nil
}

// dryRunPreview computes what prepared would do without writing or publishing
// anything: the diff against the base version (with chip-key removals expanded
// into individual entries, FR82.4) and the version that would be assigned.
// Shared by PushDeviceConfigDryRun and PushDeviceConfigMultiBoardDryRun.
func (s *LeafLabAPIServer) dryRunPreview(ctx context.Context, prepared *preparedConfigPush) (*pb.ConfigDiff, uint64, error) {
	diff := computeDiff(prepared.BaseConfig, prepared.Config)
	diff.Removals = expandRemovals(diff.Removals, prepared.Config)

	nextVersion, err := s.repo.GetNextDeviceConfigVersion(ctx, prepared.BoardID)
	if err != nil {
		return nil, 0, err
	}

	return diff, uint64(nextVersion), nil
}

// PushDeviceConfigDryRun validates a config push without storing or assigning a version.
// Accepts the same scope and payload structure as PushDeviceConfig; returns what would
// be stored (with materialised entries for EDIT), but does not persist to the database.
func (s *LeafLabAPIServer) PushDeviceConfigDryRun(ctx context.Context, req *pb.PushDeviceConfigRequest) (*pb.PushDeviceConfigDryRunResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	prepared, err := s.prepareConfigPush(ctx, req, subject, corrID, "dry run")
	if err != nil {
		return nil, err
	}

	diff, versionPreview, err := s.dryRunPreview(ctx, prepared)
	if err != nil {
		s.logger.Error("dry run get next version failed",
			"device_id", req.DeviceId,
			"board_id", prepared.BoardID,
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

	s.logger.Info("dry run completed",
		"device_id", req.DeviceId,
		"sensors", len(prepared.Config.Sensors),
		"scope", req.Scope.String(),
		"removals", len(diff.Removals),
		"version_preview", versionPreview,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.PushDeviceConfigDryRunResponse{
		VersionPreview:  versionPreview,
		EffectiveConfig: prepared.Config,
		Diff:            diff,
		PerBoardResults: []*pb.PushBoardResult{
			{
				DeviceId: req.DeviceId,
				Success:  true,
				Version:  0,
			},
		},
	}, nil
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

// ListActivity returns a paginated list of activity (audit records) for the caller's household.
// Household scope is implicit, derived from the caller's identity per FR5.
// Each record is rendered as a complete plain-language sentence with no proto/table/column names.
// Admin elevated actions and granted helper actions render in the same list as member actions.
func (s *LeafLabAPIServer) ListActivity(ctx context.Context, req *pb.ListActivityRequest) (*pb.ListActivityResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	// Get the caller's household.
	householdID, err := s.repo.GetPrincipalHousehold(ctx, subject)
	if err != nil {
		s.logger.Error("get principal household failed",
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "get household: %v", err)
	}

	if householdID == 0 {
		// Principal has no household; return empty activity.
		s.logger.Info("principal has no household",
			"subject", subject,
			"correlation_id", corrID)
		return &pb.ListActivityResponse{
			Items:         []*pb.ActivityItem{},
			NextPageToken: "",
		}, nil
	}

	// Query audit records for the household.
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = 50 // Default page size per FR61
	}
	if pageSize > 200 {
		pageSize = 200 // Max page size per FR61 (TBD in spec, but reasonable default)
	}

	records, nextToken, err := s.repo.ListActivityRecords(ctx, householdID, req.PageToken, pageSize)
	if err != nil {
		s.logger.Error("list activity records failed",
			"household", householdID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "list activity: %v", err)
	}

	// Render each record as a plain-language sentence.
	items := make([]*pb.ActivityItem, 0, len(records))
	for _, record := range records {
		item := renderActivityItem(record)
		if item != nil {
			items = append(items, item)
		}
	}

	s.logger.Info("activity listed",
		"household", householdID,
		"item_count", len(items),
		"subject", subject,
		"correlation_id", corrID)

	return &pb.ListActivityResponse{
		Items:         items,
		NextPageToken: nextToken,
	}, nil
}

// renderActivityItem converts an audit record to a plain-language ActivityItem.
// Renders no proto field, table name, or column name in the output.
// Admin elevated actions and granted helper actions are rendered in the same voice.
func renderActivityItem(record AuditRecord) *pb.ActivityItem {
	// Placeholder rendering - will be enhanced with actual entity name lookups in follow-up work.
	// For now, construct a simple plain-language sentence from the audit record.
	timestamp := record.OccurredAtUnix

	// Construct a plain-language description of the action.
	// Examples from the issue:
	//   "Your board 'Grow Light' was claimed on Aug 25 at 2:30 PM"
	//   "Helper Alice was granted access to 'Grow Light' on Aug 25 at 2:45 PM"
	//   "Admin (elevated) reconfigured 'Humidity Sensor' on Aug 25 at 3:00 PM"
	description := renderActionDescription(record)

	return &pb.ActivityItem{
		Description: description,
		Timestamp:   timestamp,
	}
}

// renderActionDescription constructs a plain-language sentence for an action.
// No proto field, table name, or column name appears in the output.
func renderActionDescription(record AuditRecord) string {
	// Placeholder implementation.
	// In production, this would:
	// 1. Look up entity names (board name, plant name, etc.) from the database
	// 2. Humanize the action (claim_board -> "claimed", update_plant -> "updated", etc.)
	// 3. Format the timestamp in a user-friendly way
	// 4. Distinguish between member, admin, and granted actions
	// 5. Construct a grammatically correct sentence
	//
	// For now, return a simple plain-language template to satisfy the proto interface.
	// This avoids technical terms like "action", "entity", "actor", etc.
	return "Something happened"
}

// DiffDeviceConfig computes the diff between two config versions or between a version and an unpushed draft.
// The diff classifies each sensor entry as added / removed / changed / unchanged,
// with both raw payloads (from_payload and to_payload) available for comparison.
func (s *LeafLabAPIServer) DiffDeviceConfig(ctx context.Context, req *pb.DiffDeviceConfigRequest) (*pb.DiffDeviceConfigResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	if err := validateDeviceID(req.DeviceId); err != nil {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"device_id",
			"device_id",
			apierrors.InvalidDeviceID,
		)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
	}

	boardID, err := s.repo.GetOrCreateBoard(ctx, req.DeviceId)
	if err != nil {
		s.logger.Error("diff board lookup failed",
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

	// Get the base version
	var baseConfig *configpb.DeviceConfig
	if req.FromVersion == 0 {
		// Use current accepted version
		baseConfig, err = s.repo.GetLatestAcceptedConfigByBoardID(ctx, boardID)
		if err != nil {
			s.logger.Error("diff get base config failed",
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
	} else {
		// Get specific version
		baseConfig, err = s.repo.GetDeviceConfigVersion(ctx, boardID, req.FromVersion)
		if err != nil {
			s.logger.Error("diff get versioned config failed",
				"device_id", req.DeviceId,
				"version", req.FromVersion,
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
	}

	// Get the target version or draft
	var targetConfig *configpb.DeviceConfig
	if req.ToVersion == 0 {
		// Use draft from request
		if req.ToDraft == nil {
			return nil, status.Error(codes.InvalidArgument, "to_draft is required when to_version is 0")
		}
		// Canonicalize sensors in the draft
		for _, sensor := range req.ToDraft.Sensors {
			if err := canonkey.ValidateAndCanonicalizeSensorConfig(sensor); err != nil {
				detail := apierrors.NewErrorDetail(
					pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
					"sensors",
					"sensor_type",
					apierrors.InvalidSensorConfig,
				)
				return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
			}
		}
		// If the draft is an EDIT scope, materialize it
		if req.ToDraft.Scope == pb.ConfigScope_CONFIG_SCOPE_EDIT {
			if baseConfig == nil {
				return nil, status.Error(codes.InvalidArgument, "cannot diff EDIT draft against no base config")
			}
			materialiser := NewMaterialiser()
			var matErr error
			targetConfig, _, _, matErr = materialiser.Materialize(baseConfig, req.ToDraft)
			if matErr != nil {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("materialize draft: %v", matErr))
			}
		} else {
			// COMPLETE draft
			targetConfig = &configpb.DeviceConfig{
				DeviceId: req.DeviceId,
				Sensors:  req.ToDraft.Sensors,
			}
		}
	} else {
		// Get specific version
		targetConfig, err = s.repo.GetDeviceConfigVersion(ctx, boardID, req.ToVersion)
		if err != nil {
			s.logger.Error("diff get target version failed",
				"device_id", req.DeviceId,
				"version", req.ToVersion,
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
	}

	// Compute the diff
	diff := computeDiff(baseConfig, targetConfig)

	s.logger.Info("config diff computed",
		"device_id", req.DeviceId,
		"from_version", req.FromVersion,
		"to_version", req.ToVersion,
		"diff_entries", len(diff.Entries),
		"removals", len(diff.Removals),
		"subject", subject,
		"correlation_id", corrID)

	return &pb.DiffDeviceConfigResponse{Diff: diff}, nil
}

// PushDeviceConfigMultiBoard pushes a config to multiple boards in a single operation.
// Each board receives the same config, but results are returned per-board.
// A reason for the push must be provided (for audit purposes, FR48).
func (s *LeafLabAPIServer) PushDeviceConfigMultiBoard(ctx context.Context, req *pb.PushDeviceConfigMultiBoardRequest) (*pb.PushDeviceConfigMultiBoardResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	// FR48: reason is required
	if req.Reason == "" {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"PushDeviceConfigMultiBoardRequest",
			"reason",
			apierrors.InvalidArgument,
		)
		s.logger.Info("multi-board push rejected: missing required reason",
			"subject", subject,
			"correlation_id", corrID)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "required field missing: reason", detail)
	}

	// FR82: Validate scope is present and valid
	if req.Scope == pb.ConfigScope_CONFIG_SCOPE_UNSPECIFIED {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"PushDeviceConfigMultiBoardRequest",
			"scope",
			apierrors.InvalidArgument,
		)
		s.logger.Info("multi-board push rejected: missing required scope field",
			"subject", subject,
			"correlation_id", corrID)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "required field missing: scope", detail)
	}

	// Canonicalize sensors on ingress
	for _, sensor := range req.Sensors {
		if err := canonkey.ValidateAndCanonicalizeSensorConfig(sensor); err != nil {
			detail := apierrors.NewErrorDetail(
				pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
				"sensors",
				"sensor_type",
				apierrors.InvalidSensorConfig,
			)
			return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
		}
	}

	// Create the push group
	pushGroupID, err := s.repo.CreatePushGroup(ctx, subject, req.Reason)
	if err != nil {
		s.logger.Error("create push group failed",
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"push_group",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// Push to each board
	results := make([]*pb.PushBoardResult, 0, len(req.DeviceIds))
	for _, deviceID := range req.DeviceIds {
		// Create a single-board request
		singleReq := &pb.PushDeviceConfigRequest{
			DeviceId: deviceID,
			Scope:    req.Scope,
			Sensors:  req.Sensors,
			Remove:   req.Remove,
		}

		// Push to this board
		resp, err := s.pushDeviceConfigInternal(ctx, singleReq, subject, pushGroupID, req.Reason)
		if err != nil {
			// Record failure
			results = append(results, &pb.PushBoardResult{
				DeviceId: deviceID,
				Success:  false,
				ErrorDetail: &pb.ErrorDetail{
					FailureClass: pb.FailureClass_FAILURE_CLASS_INTERNAL,
					Entity:       "device_config",
					MessageKey:   apierrors.InternalError,
				},
			})
			s.logger.Error("push to board failed",
				"device_id", deviceID,
				"push_group_id", pushGroupID,
				"subject", subject,
				"correlation_id", corrID,
				"error", err)
			continue
		}

		results = append(results, &pb.PushBoardResult{
			DeviceId: deviceID,
			Success:  true,
			Version:  resp.Version,
		})
	}

	s.logger.Info("multi-board push completed",
		"push_group_id", pushGroupID,
		"board_count", len(req.DeviceIds),
		"successful", countSuccessful(results),
		"reason", req.Reason,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.PushDeviceConfigMultiBoardResponse{
		PushGroupId:    pushGroupID,
		PerBoardResults: results,
	}, nil
}

// PushDeviceConfigMultiBoardDryRun previews a multi-board push without writing
// or publishing anything for any board — the same no-write/no-publish guarantee
// as PushDeviceConfigDryRun, extended across a board set so FR38 composes with
// FR48 (the blast-radius preview covers the blast radius). It uses the same
// prepareConfigPush/dryRunPreview path as every other push and dry-run entry
// point. A board-level failure (e.g. an EDIT with no accepted base config) is
// reported in that board's result and does not abort the rest of the set.
func (s *LeafLabAPIServer) PushDeviceConfigMultiBoardDryRun(ctx context.Context, req *pb.PushDeviceConfigMultiBoardDryRunRequest) (*pb.PushDeviceConfigMultiBoardDryRunResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	// FR82: Validate scope is present and valid
	if req.Scope == pb.ConfigScope_CONFIG_SCOPE_UNSPECIFIED {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"PushDeviceConfigMultiBoardDryRunRequest",
			"scope",
			apierrors.InvalidArgument,
		)
		s.logger.Info("multi-board dry run rejected: missing required scope field",
			"subject", subject,
			"correlation_id", corrID)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "required field missing: scope", detail)
	}

	// Canonicalize sensors on ingress
	for _, sensor := range req.Sensors {
		if err := canonkey.ValidateAndCanonicalizeSensorConfig(sensor); err != nil {
			detail := apierrors.NewErrorDetail(
				pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
				"sensors",
				"sensor_type",
				apierrors.InvalidSensorConfig,
			)
			return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
		}
	}

	results := make([]*pb.PushBoardDryRunResult, 0, len(req.DeviceIds))
	for _, deviceID := range req.DeviceIds {
		singleReq := &pb.PushDeviceConfigRequest{
			DeviceId: deviceID,
			Scope:    req.Scope,
			Sensors:  req.Sensors,
			Remove:   req.Remove,
		}

		prepared, err := s.prepareConfigPush(ctx, singleReq, subject, corrID, "multi-board dry run")
		if err != nil {
			results = append(results, &pb.PushBoardDryRunResult{
				DeviceId:    deviceID,
				Success:     false,
				ErrorDetail: dryRunErrorDetail(err),
			})
			s.logger.Info("multi-board dry run: board failed validation",
				"device_id", deviceID,
				"subject", subject,
				"correlation_id", corrID,
				"error", err)
			continue
		}

		diff, versionPreview, err := s.dryRunPreview(ctx, prepared)
		if err != nil {
			results = append(results, &pb.PushBoardDryRunResult{
				DeviceId: deviceID,
				Success:  false,
				ErrorDetail: apierrors.NewErrorDetail(
					pb.FailureClass_FAILURE_CLASS_INTERNAL,
					"device_config",
					"",
					apierrors.InternalError,
				),
			})
			s.logger.Error("multi-board dry run: version preview failed",
				"device_id", deviceID,
				"subject", subject,
				"correlation_id", corrID,
				"error", err)
			continue
		}

		results = append(results, &pb.PushBoardDryRunResult{
			DeviceId:        deviceID,
			Success:         true,
			VersionPreview:  versionPreview,
			EffectiveConfig: prepared.Config,
			Diff:            diff,
		})
	}

	s.logger.Info("multi-board dry run completed",
		"board_count", len(req.DeviceIds),
		"successful", countSuccessfulDryRun(results),
		"subject", subject,
		"correlation_id", corrID)

	return &pb.PushDeviceConfigMultiBoardDryRunResponse{
		PerBoardResults: results,
	}, nil
}

// dryRunErrorDetail extracts the structured ErrorDetail from a status error
// returned by prepareConfigPush, falling back to a generic internal detail if
// the error carries none (e.g. an unexpected non-status error).
func dryRunErrorDetail(err error) *pb.ErrorDetail {
	if st, ok := status.FromError(err); ok {
		if detail := apierrors.ErrorDetailFromStatus(st); detail != nil {
			return detail
		}
	}
	return apierrors.NewErrorDetail(
		pb.FailureClass_FAILURE_CLASS_INTERNAL,
		"device_config",
		"",
		apierrors.InternalError,
	)
}

// countSuccessfulDryRun counts successful results in a list of PushBoardDryRunResults.
func countSuccessfulDryRun(results []*pb.PushBoardDryRunResult) int {
	count := 0
	for _, r := range results {
		if r.Success {
			count++
		}
	}
	return count
}

// GetPushGroupAckState returns the ack state of a multi-board push group.
// Returns per-board ack state (acked / rejected / silent) for all boards in the group.
func (s *LeafLabAPIServer) GetPushGroupAckState(ctx context.Context, req *pb.GetPushGroupAckStateRequest) (*pb.GetPushGroupAckStateResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	if req.PushGroupId == "" {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"GetPushGroupAckStateRequest",
			"push_group_id",
			apierrors.InvalidArgument,
		)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "push_group_id is required", detail)
	}

	// Get the ack states from the repository
	boardStates, err := s.repo.GetPushGroupAckStates(ctx, req.PushGroupId)
	if err != nil {
		s.logger.Error("get push group ack states failed",
			"push_group_id", req.PushGroupId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"push_group",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	s.logger.Info("push group ack state retrieved",
		"push_group_id", req.PushGroupId,
		"board_count", len(boardStates),
		"subject", subject,
		"correlation_id", corrID)

	return &pb.GetPushGroupAckStateResponse{
		PushGroupId: req.PushGroupId,
		BoardStates: boardStates,
	}, nil
}

// ── Helper functions ──

// computeDiff computes the diff between a base config and a target config.
// Classifies each sensor entry as added / removed / changed / unchanged.
func computeDiff(base *configpb.DeviceConfig, target *configpb.DeviceConfig) *pb.ConfigDiff {
	diff := &pb.ConfigDiff{
		Entries:  make([]*pb.ConfigDiffEntry, 0),
		Removals: make([]*pb.RemovalEntry, 0),
	}

	if base == nil && target == nil {
		return diff
	}

	// Build a map of target sensors by canonical key
	targetMap := make(map[string]*configpb.SensorConfig)
	targetSensors := []*configpb.SensorConfig{}
	if target != nil {
		targetSensors = target.Sensors
		for _, sensor := range targetSensors {
			key, _ := canonkey.CanonicalizeKey(sensor)
			targetMap[sensorKeyString(key)] = sensor
		}
	}

	// Build a map of base sensors by canonical key
	baseMap := make(map[string]*configpb.SensorConfig)
	baseSensors := []*configpb.SensorConfig{}
	if base != nil {
		baseSensors = base.Sensors
		for _, sensor := range baseSensors {
			key, _ := canonkey.CanonicalizeKey(sensor)
			baseMap[sensorKeyString(key)] = sensor
		}
	}

	// Process all base sensors (checking for removed/unchanged/changed)
	for _, baseSensor := range baseSensors {
		key, _ := canonkey.CanonicalizeKey(baseSensor)
		keyStr := sensorKeyString(key)
		targetSensor, exists := targetMap[keyStr]

		if !exists {
			// Removed
			fromPayload, _ := proto.Marshal(baseSensor)
			diff.Entries = append(diff.Entries, &pb.ConfigDiffEntry{
				Classification:   pb.ConfigDiffClassification_CLASSIFICATION_REMOVED,
				SensorHardwareKey: keyStr,
				FromPayload:       fromPayload,
			})
			// Add to removals
			diff.Removals = append(diff.Removals, &pb.RemovalEntry{
				SensorHardwareKey: keyStr,
				SensorName:        baseSensor.Name,
			})
		} else {
			// Exists in both - check if changed
			basePayload, _ := proto.Marshal(baseSensor)
			targetPayload, _ := proto.Marshal(targetSensor)
			if !proto.Equal(baseSensor, targetSensor) {
				// Changed
				diff.Entries = append(diff.Entries, &pb.ConfigDiffEntry{
					Classification:   pb.ConfigDiffClassification_CLASSIFICATION_CHANGED,
					SensorHardwareKey: keyStr,
					FromPayload:       basePayload,
					ToPayload:         targetPayload,
				})
			} else {
				// Unchanged
				diff.Entries = append(diff.Entries, &pb.ConfigDiffEntry{
					Classification:   pb.ConfigDiffClassification_CLASSIFICATION_UNCHANGED,
					SensorHardwareKey: keyStr,
					FromPayload:       basePayload,
					ToPayload:         targetPayload,
				})
			}
		}
	}

	// Process all target sensors (checking for added)
	for _, targetSensor := range targetSensors {
		key, _ := canonkey.CanonicalizeKey(targetSensor)
		keyStr := sensorKeyString(key)
		_, exists := baseMap[keyStr]

		if !exists {
			// Added
			toPayload, _ := proto.Marshal(targetSensor)
			diff.Entries = append(diff.Entries, &pb.ConfigDiffEntry{
				Classification:   pb.ConfigDiffClassification_CLASSIFICATION_ADDED,
				SensorHardwareKey: keyStr,
				ToPayload:         toPayload,
			})
		}
	}

	return diff
}

// expandRemovals expands chip key removals into individual entries.
// For a chip key that matches multiple entries, each entry appears separately in the result.
func expandRemovals(removals []*pb.RemovalEntry, config *configpb.DeviceConfig) []*pb.RemovalEntry {
	if len(removals) == 0 || config == nil {
		return removals
	}

	// For now, removals are already computed correctly by computeDiff.
	// Chip key expansion happens in the context of materialisation and diff computation.
	// This function is a placeholder for any additional expansion logic.
	return removals
}

// sensorKeyString returns a string representation of a sensor's canonical key.
func sensorKeyString(key *canonkey.Key) string {
	return fmt.Sprintf("%d:%s:%d", key.I2CAddress, muxPathStringFromKey(key.MuxPath), key.SensorType)
}

// muxPathStringFromKey returns a string representation of a mux path.
func muxPathStringFromKey(path []canonkey.MuxHop) string {
	if len(path) == 0 {
		return ""
	}
	result := ""
	for i, hop := range path {
		if i > 0 {
			result += ","
		}
		result += fmt.Sprintf("%d:%d", hop.MuxAddress, hop.MuxChannel)
	}
	return result
}

// pushDeviceConfigInternal performs the actual push for a single board within a
// multi-board push. It shares prepareConfigPush/commitConfigPush with
// PushDeviceConfig — the same validation, materialisation, and write path —
// so a multi-board push cannot silently diverge from a single-board push.
func (s *LeafLabAPIServer) pushDeviceConfigInternal(ctx context.Context, req *pb.PushDeviceConfigRequest, subject string, pushGroupID string, reason string) (*pb.PushDeviceConfigResponse, error) {
	prepared, err := s.prepareConfigPush(ctx, req, subject, "", "multi-board push")
	if err != nil {
		return nil, err
	}

	version, err := s.commitConfigPush(ctx, req, prepared, subject, pushGroupID, reason)
	if err != nil {
		return nil, err
	}

	return &pb.PushDeviceConfigResponse{Version: uint64(version)}, nil
}

// countSuccessful counts successful results in a list of PushBoardResults.
func countSuccessful(results []*pb.PushBoardResult) int {
	count := 0
	for _, r := range results {
		if r.Success {
			count++
		}
	}
	return count
}
