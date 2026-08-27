package main

import (
	"context"
	"errors"
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
	// FR1.3 push-time validation: validate every region reference against the board's household.
	// Reject the entire push if any region doesn't belong to the household (no partial application).
	for i, sensor := range req.Sensors {
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

	// FR8: every write produces an append-only audit record.
	if err := s.repo.RecordAuditWithConfig(ctx, subject, householdID, "push_config", "device_config",
		version, &version, nil, nil, ""); err != nil {
		s.logger.Error("record audit failed",
			"device_id", req.DeviceId,
			"version", version,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
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

// ============================================================================
// Plant lifecycle (FR54, FR24, FR22.3, FR22.5)
// ============================================================================

// resolvePlantForAccess fetches a plant, refuses it as not-found if it does
// not exist or is out of the caller's reach (NFR2), and returns it. Callers
// that need to further refuse a retired plant (FR22.5: no new writes) check
// plant.RemovedAt themselves — this helper is shared by both read and write
// paths, and reads are allowed against a retired plant (FR22.3).
func (s *LeafLabAPIServer) resolvePlantForAccess(ctx context.Context, auth *AuthorizationDecision, plantID int64) (*PlantRecord, error) {
	plant, err := s.repo.GetPlant(ctx, plantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, refusedAsNotFound()
		}
		return nil, status.Errorf(codes.Internal, "get plant: %v", err)
	}
	if !auth.ContainsHousehold(plant.HouseholdID) {
		// FR4: Out-of-reach entity. Refuse as if it doesn't exist (NFR2).
		return nil, refusedAsNotFound()
	}
	return plant, nil
}

// plantRetiredError builds the FailureClass_PRECONDITION error for a write
// attempted against a retired plant (FR22.5: a retired plant accepts no new writes).
func plantRetiredError(plantID int64) error {
	detail := apierrors.NewErrorDetail(
		pb.FailureClass_FAILURE_CLASS_PRECONDITION,
		"plant",
		"",
		apierrors.PlantRetired,
	)
	return apierrors.StatusWithDetail(codes.FailedPrecondition,
		fmt.Sprintf("plant %d is retired and accepts no new writes", plantID), detail)
}

func (s *LeafLabAPIServer) CreatePlant(ctx context.Context, req *pb.CreatePlantRequest) (*pb.CreatePlantResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	if req.Name == "" {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"plant",
			"name",
			apierrors.InvalidPlantRequest,
		)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "name is required", detail)
	}
	if req.PlantTypeId <= 0 {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"plant",
			"plant_type_id",
			apierrors.InvalidPlantRequest,
		)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "plant_type_id is required", detail)
	}
	if req.RegionId <= 0 {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"plant",
			"region_id",
			apierrors.InvalidPlantRequest,
		)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "region_id is required", detail)
	}

	// NFR2: No existence oracle — build authorization decision first, before checking
	// region existence. FR4: Per-entity authorization is based on principal's reach.
	auth, err := s.getAuthorizationDecision(ctx, subject)
	if err != nil {
		s.logger.Error("authorization decision failed",
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}

	// Resolve the target region's household. A region in another household is
	// refused as not-found (FR1.2, NFR2) — this doubles as existence checking.
	regionHouseholdID, err := s.repo.GetRegionHousehold(ctx, req.RegionId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, refusedAsNotFound()
		}
		s.logger.Error("region household lookup failed",
			"region_id", req.RegionId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "region lookup: %v", err)
	}
	if !auth.ContainsHousehold(regionHouseholdID) {
		return nil, refusedAsNotFound()
	}

	plantTypeExists, err := s.repo.PlantTypeExists(ctx, req.PlantTypeId)
	if err != nil {
		s.logger.Error("plant_type existence check failed",
			"plant_type_id", req.PlantTypeId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "plant_type lookup: %v", err)
	}
	if !plantTypeExists {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_PRECONDITION,
			"plant_type",
			"plant_type_id",
			apierrors.PlantTypeNotFound,
		)
		return nil, apierrors.StatusWithDetail(codes.FailedPrecondition,
			fmt.Sprintf("plant_type %d does not exist", req.PlantTypeId), detail)
	}

	plantID, err := s.repo.CreatePlant(ctx, req.Name, req.PlantTypeId, req.RegionId, regionHouseholdID)
	if err != nil {
		s.logger.Error("create plant failed",
			"region_id", req.RegionId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"plant",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// FR8: every write produces an append-only audit record.
	if err := s.repo.RecordAudit(ctx, subject, regionHouseholdID, "create_plant", "plant", plantID, ""); err != nil {
		s.logger.Error("record audit failed",
			"plant_id", plantID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
	}

	s.logger.Info("plant created",
		"plant_id", plantID,
		"region_id", req.RegionId,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.CreatePlantResponse{PlantId: plantID}, nil
}

func (s *LeafLabAPIServer) CorrectPlant(ctx context.Context, req *pb.CorrectPlantRequest) (*pb.CorrectPlantResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	// FR24: correct is a distinct, named operation from move — at least one
	// correctable field must be set, and this never opens a placement interval.
	if req.CorrectedName == "" && req.CorrectedPlantTypeId == 0 {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"plant",
			"",
			apierrors.InvalidPlantRequest,
		)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument,
			"at least one of corrected_name or corrected_plant_type_id must be set", detail)
	}

	auth, err := s.getAuthorizationDecision(ctx, subject)
	if err != nil {
		s.logger.Error("authorization decision failed",
			"plant_id", req.PlantId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}

	plant, err := s.resolvePlantForAccess(ctx, auth, req.PlantId)
	if err != nil {
		return nil, err
	}
	// FR22.5: a retired plant accepts no new writes.
	if plant.RemovedAt != nil {
		return nil, plantRetiredError(req.PlantId)
	}

	if req.CorrectedPlantTypeId != 0 {
		plantTypeExists, err := s.repo.PlantTypeExists(ctx, req.CorrectedPlantTypeId)
		if err != nil {
			s.logger.Error("plant_type existence check failed",
				"plant_type_id", req.CorrectedPlantTypeId,
				"subject", subject,
				"correlation_id", corrID,
				"error", err)
			return nil, status.Errorf(codes.Internal, "plant_type lookup: %v", err)
		}
		if !plantTypeExists {
			detail := apierrors.NewErrorDetail(
				pb.FailureClass_FAILURE_CLASS_PRECONDITION,
				"plant_type",
				"corrected_plant_type_id",
				apierrors.PlantTypeNotFound,
			)
			return nil, apierrors.StatusWithDetail(codes.FailedPrecondition,
				fmt.Sprintf("plant_type %d does not exist", req.CorrectedPlantTypeId), detail)
		}
	}

	if err := s.repo.CorrectPlant(ctx, req.PlantId, req.CorrectedName, req.CorrectedPlantTypeId); err != nil {
		s.logger.Error("correct plant failed",
			"plant_id", req.PlantId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"plant",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// FR8: every write produces an append-only audit record.
	if err := s.repo.RecordAudit(ctx, subject, plant.HouseholdID, "correct_plant", "plant", req.PlantId, ""); err != nil {
		s.logger.Error("record audit failed",
			"plant_id", req.PlantId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
	}

	s.logger.Info("plant corrected",
		"plant_id", req.PlantId,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.CorrectPlantResponse{PlantId: req.PlantId}, nil
}

func (s *LeafLabAPIServer) MovePlant(ctx context.Context, req *pb.MovePlantRequest) (*pb.MovePlantResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	if req.NewRegionId <= 0 {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT,
			"plant",
			"new_region_id",
			apierrors.InvalidPlantRequest,
		)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "new_region_id is required", detail)
	}

	auth, err := s.getAuthorizationDecision(ctx, subject)
	if err != nil {
		s.logger.Error("authorization decision failed",
			"plant_id", req.PlantId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}

	plant, err := s.resolvePlantForAccess(ctx, auth, req.PlantId)
	if err != nil {
		return nil, err
	}
	// FR22.5: a retired plant accepts no new writes.
	if plant.RemovedAt != nil {
		return nil, plantRetiredError(req.PlantId)
	}

	// FR1.2: the target region must belong to the plant's household; a region
	// in another household is refused.
	regionOK, err := s.repo.ValidateRegionBelongsToHousehold(ctx, req.NewRegionId, plant.HouseholdID)
	if err != nil {
		s.logger.Error("region household validation failed",
			"plant_id", req.PlantId,
			"new_region_id", req.NewRegionId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "region validation: %v", err)
	}
	if !regionOK {
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_PRECONDITION,
			"region",
			"new_region_id",
			apierrors.InvalidPlantRequest,
		)
		return nil, apierrors.StatusWithDetail(codes.FailedPrecondition,
			fmt.Sprintf("region %d does not belong to your household", req.NewRegionId), detail)
	}

	// FR24: move is a distinct, named operation from correct — this always closes
	// the current interval and opens a new one. relocationInduced is false: this
	// is a plant-initiated move, not a subtree relocation (FR74, #1223).
	if err := s.repo.MovePlantRegion(ctx, req.PlantId, req.NewRegionId, false); err != nil {
		s.logger.Error("move plant failed",
			"plant_id", req.PlantId,
			"new_region_id", req.NewRegionId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"plant",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// FR8: every write produces an append-only audit record.
	if err := s.repo.RecordAudit(ctx, subject, plant.HouseholdID, "move_plant", "plant", req.PlantId, ""); err != nil {
		s.logger.Error("record audit failed",
			"plant_id", req.PlantId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
	}

	s.logger.Info("plant moved",
		"plant_id", req.PlantId,
		"new_region_id", req.NewRegionId,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.MovePlantResponse{PlantId: req.PlantId, NewRegionId: req.NewRegionId}, nil
}

func (s *LeafLabAPIServer) RetirePlant(ctx context.Context, req *pb.RetirePlantRequest) (*pb.RetirePlantResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	auth, err := s.getAuthorizationDecision(ctx, subject)
	if err != nil {
		s.logger.Error("authorization decision failed",
			"plant_id", req.PlantId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}

	plant, err := s.resolvePlantForAccess(ctx, auth, req.PlantId)
	if err != nil {
		return nil, err
	}
	if plant.RemovedAt != nil {
		return nil, plantRetiredError(req.PlantId)
	}

	const retireOperation = "retire_plant"
	retiredAt, err := s.repo.RetirePlant(ctx, req.PlantId, retireOperation, subject)
	if err != nil {
		s.logger.Error("retire plant failed",
			"plant_id", req.PlantId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"plant",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// FR8: every write produces an append-only audit record.
	if err := s.repo.RecordAudit(ctx, subject, plant.HouseholdID, retireOperation, "plant", req.PlantId, ""); err != nil {
		s.logger.Error("record audit failed",
			"plant_id", req.PlantId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
	}

	s.logger.Info("plant retired",
		"plant_id", req.PlantId,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.RetirePlantResponse{
		PlantId:          req.PlantId,
		RetiredOperation: retireOperation,
		RetiredPrincipal: subject,
		RetiredAt:        retiredAt.Unix(),
	}, nil
}

func (s *LeafLabAPIServer) GetPlantPlacementTimeline(ctx context.Context, req *pb.GetPlantPlacementTimelineRequest) (*pb.GetPlantPlacementTimelineResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	auth, err := s.getAuthorizationDecision(ctx, subject)
	if err != nil {
		s.logger.Error("authorization decision failed",
			"plant_id", req.PlantId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}

	// FR22.3: readable for a retired plant, so resolvePlantForAccess (which
	// only checks existence/reach, not retirement state) is reused here.
	plant, err := s.resolvePlantForAccess(ctx, auth, req.PlantId)
	if err != nil {
		return nil, err
	}

	entries, err := s.repo.GetPlantPlacementTimeline(ctx, req.PlantId)
	if err != nil {
		s.logger.Error("get plant placement timeline failed",
			"plant_id", req.PlantId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		detail := apierrors.NewErrorDetail(
			pb.FailureClass_FAILURE_CLASS_INTERNAL,
			"plant",
			"",
			apierrors.InternalError,
		)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	placements := make([]*pb.PlantPlacementEntry, len(entries))
	for i, e := range entries {
		var validTo int64
		if e.ValidTo != nil {
			validTo = e.ValidTo.Unix()
		}
		placements[i] = &pb.PlantPlacementEntry{
			RegionId:          e.RegionID,
			RegionName:        e.RegionName,
			ValidFrom:         e.ValidFrom.Unix(),
			ValidTo:           validTo,
			RelocationInduced: e.RelocationInduced,
		}
	}

	var retiredAt int64
	var retiredOperation, retiredPrincipal string
	if plant.RemovedAt != nil {
		retiredAt = plant.RemovedAt.Unix()
	}
	if plant.RetiredOperation != nil {
		retiredOperation = *plant.RetiredOperation
	}
	if plant.RetiredPrincipal != nil {
		retiredPrincipal = *plant.RetiredPrincipal
	}

	return &pb.GetPlantPlacementTimelineResponse{
		PlantId:          req.PlantId,
		Placements:       placements,
		RetiredAt:        retiredAt,
		RetiredOperation: retiredOperation,
		RetiredPrincipal: retiredPrincipal,
	}, nil
}
