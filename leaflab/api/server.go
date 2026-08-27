package main

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/apierrors"
	"github.com/whale-net/everything/leaflab/api/pagetoken"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
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
	repo        *Repository
	publisher   *rmq.Publisher
	logger      *slog.Logger
	adminConfig AdminConfig
	// authz backs the FR10 elevation-gate predicates (IsAdmin is a free
	// function; ActiveElevation/RequireElevatedAdmin need the DB pool).
	authz *AuthorizationPredicates
	// limiter is consulted directly (not just via the blanket "read"
	// interceptor in main.go) for the FR80 support-reference resolve path,
	// which needs a narrower bucket than the rest of Resolve's oneof targets.
	limiter *ratelimit.Limiter
}

func NewLeafLabAPIServer(repo *Repository, publisher *rmq.Publisher, logger *slog.Logger, adminConfig AdminConfig, authz *AuthorizationPredicates, limiter *ratelimit.Limiter) *LeafLabAPIServer {
	return &LeafLabAPIServer{
		repo:        repo,
		publisher:   publisher,
		logger:      logger,
		adminConfig: adminConfig,
		authz:       authz,
		limiter:     limiter,
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

// CreateHousehold creates a new household with the caller as the initial member (FR75).
func (s *LeafLabAPIServer) CreateHousehold(ctx context.Context, req *pb.CreateHouseholdRequest) (*pb.CreateHouseholdResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	// Create the household with the caller as the initial member
	householdID, err := s.repo.CreateHousehold(ctx, req.Name, subject, "Owner")
	if err != nil {
		s.logger.Error("create household failed",
			"household_name", req.Name,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "create household: %v", err)
	}

	// Record audit entry for household creation
	if err := s.repo.RecordAudit(ctx, subject, householdID, "create_household", "household", householdID, ""); err != nil {
		s.logger.Error("record audit failed",
			"household_id", householdID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
	}

	s.logger.Info("household created",
		"household_id", householdID,
		"household_name", req.Name,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.CreateHouseholdResponse{HouseholdId: householdID}, nil
}

// InviteMember invites a principal to join the household (FR75, member-only).
func (s *LeafLabAPIServer) InviteMember(ctx context.Context, req *pb.InviteMemberRequest) (*pb.InviteMemberResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	// Get the caller's household
	householdID, err := s.repo.GetPrincipalHousehold(ctx, subject)
	if err != nil {
		s.logger.Error("get principal household failed",
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "get household: %v", err)
	}

	if householdID == 0 {
		return nil, status.Error(codes.PermissionDenied, "principal has no household")
	}

	// Check if caller is a member (memberOnly predicate)
	isMember, err := s.repo.IsHouseholdMember(ctx, householdID, subject)
	if err != nil {
		s.logger.Error("check membership failed",
			"household_id", householdID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "check membership: %v", err)
	}

	if !isMember {
		return nil, status.Error(codes.PermissionDenied, "only members can invite")
	}

	// Add the new member to the household
	_, err = s.repo.AddHouseholdMember(ctx, householdID, req.Principal, req.Role)
	if err != nil {
		s.logger.Error("invite member failed",
			"household_id", householdID,
			"principal", req.Principal,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "invite member: %v", err)
	}

	// Record audit entry for member invitation
	if err := s.repo.RecordAudit(ctx, subject, householdID, "invite_member", "principal", 0, req.Principal); err != nil {
		s.logger.Error("record audit failed",
			"household_id", householdID,
			"principal", req.Principal,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
	}

	s.logger.Info("member invited",
		"household_id", householdID,
		"principal", req.Principal,
		"role", req.Role,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.InviteMemberResponse{}, nil
}

// RemoveMember removes a member from the household (FR75, member-only).
// Fails if removing the last member.
func (s *LeafLabAPIServer) RemoveMember(ctx context.Context, req *pb.RemoveMemberRequest) (*pb.RemoveMemberResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	// Get the caller's household
	householdID, err := s.repo.GetPrincipalHousehold(ctx, subject)
	if err != nil {
		s.logger.Error("get principal household failed",
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "get household: %v", err)
	}

	if householdID == 0 {
		return nil, status.Error(codes.PermissionDenied, "principal has no household")
	}

	// Check if caller is a member (memberOnly predicate)
	isMember, err := s.repo.IsHouseholdMember(ctx, householdID, subject)
	if err != nil {
		s.logger.Error("check membership failed",
			"household_id", householdID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "check membership: %v", err)
	}

	if !isMember {
		return nil, status.Error(codes.PermissionDenied, "only members can remove members")
	}

	// Count active members - can't remove last member
	count, err := s.repo.CountActiveMembers(ctx, householdID)
	if err != nil {
		s.logger.Error("count active members failed",
			"household_id", householdID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "count members: %v", err)
	}

	if count <= 1 {
		return nil, status.Error(codes.FailedPrecondition, "cannot remove the last member from a household")
	}

	// Remove the member
	err = s.repo.RemoveHouseholdMember(ctx, householdID, req.Principal)
	if err != nil {
		s.logger.Error("remove member failed",
			"household_id", householdID,
			"principal", req.Principal,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "remove member: %v", err)
	}

	// Record audit entry for member removal
	if err := s.repo.RecordAudit(ctx, subject, householdID, "remove_member", "principal", 0, req.Principal); err != nil {
		s.logger.Error("record audit failed",
			"household_id", householdID,
			"principal", req.Principal,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
	}

	s.logger.Info("member removed",
		"household_id", householdID,
		"principal", req.Principal,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.RemoveMemberResponse{}, nil
}

// ListMembers lists all current members of the household (FR75).
func (s *LeafLabAPIServer) ListMembers(ctx context.Context, _ *pb.ListMembersRequest) (*pb.ListMembersResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	// Get the caller's household
	householdID, err := s.repo.GetPrincipalHousehold(ctx, subject)
	if err != nil {
		s.logger.Error("get principal household failed",
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "get household: %v", err)
	}

	if householdID == 0 {
		return &pb.ListMembersResponse{Members: []*pb.HouseholdMember{}}, nil
	}

	// Get all current members
	members, err := s.repo.GetCurrentMembers(ctx, householdID)
	if err != nil {
		s.logger.Error("get current members failed",
			"household_id", householdID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "list members: %v", err)
	}

	// Convert to proto
	pbMembers := make([]*pb.HouseholdMember, 0, len(members))
	for _, m := range members {
		pbMembers = append(pbMembers, &pb.HouseholdMember{
			Principal: m.PrincipalID,
			Role:      m.Role,
		})
	}

	s.logger.Info("members listed",
		"household_id", householdID,
		"member_count", len(pbMembers),
		"subject", subject,
		"correlation_id", corrID)

	return &pb.ListMembersResponse{Members: pbMembers}, nil
}

// CreateGrant creates a time-boxed grant for a principal (FR7, member-only).
func (s *LeafLabAPIServer) CreateGrant(ctx context.Context, req *pb.CreateGrantRequest) (*pb.CreateGrantResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	// Get the caller's household
	householdID, err := s.repo.GetPrincipalHousehold(ctx, subject)
	if err != nil {
		s.logger.Error("get principal household failed",
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "get household: %v", err)
	}

	if householdID == 0 {
		return nil, status.Error(codes.PermissionDenied, "principal has no household")
	}

	// Check if caller is a member (memberOnly predicate - only members can grant)
	isMember, err := s.repo.IsHouseholdMember(ctx, householdID, subject)
	if err != nil {
		s.logger.Error("check membership failed",
			"household_id", householdID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "check membership: %v", err)
	}

	if !isMember {
		return nil, status.Error(codes.PermissionDenied, "only members can create grants")
	}

	// Create the grant
	grantID, err := s.repo.CreateGrant(ctx, householdID, req.Grantee, subject, req.DurationSeconds)
	if err != nil {
		s.logger.Error("create grant failed",
			"household_id", householdID,
			"grantee", req.Grantee,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "create grant: %v", err)
	}

	// Record audit entry for grant creation
	if err := s.repo.RecordAudit(ctx, subject, householdID, "create_grant", "grant", grantID, req.Grantee); err != nil {
		s.logger.Error("record audit failed",
			"household_id", householdID,
			"grant_id", grantID,
			"grantee", req.Grantee,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
	}

	s.logger.Info("grant created",
		"household_id", householdID,
		"grant_id", grantID,
		"grantee", req.Grantee,
		"duration_seconds", req.DurationSeconds,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.CreateGrantResponse{GrantId: grantID}, nil
}

// RevokeGrant revokes an active grant immediately (FR7, member-only).
func (s *LeafLabAPIServer) RevokeGrant(ctx context.Context, req *pb.RevokeGrantRequest) (*pb.RevokeGrantResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	// Get the grant's household
	householdID, err := s.repo.GetGrantHousehold(ctx, req.GrantId)
	if err != nil {
		s.logger.Error("get grant household failed",
			"grant_id", req.GrantId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "get grant household: %v", err)
	}

	// Check if caller is a member of the same household (memberOnly predicate)
	isMember, err := s.repo.IsHouseholdMember(ctx, householdID, subject)
	if err != nil {
		s.logger.Error("check membership failed",
			"household_id", householdID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "check membership: %v", err)
	}

	if !isMember {
		return nil, status.Error(codes.PermissionDenied, "only members can revoke grants")
	}

	// Revoke the grant
	err = s.repo.RevokeGrant(ctx, req.GrantId)
	if err != nil {
		s.logger.Error("revoke grant failed",
			"grant_id", req.GrantId,
			"household_id", householdID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "revoke grant: %v", err)
	}

	// Record audit entry for grant revocation
	if err := s.repo.RecordAudit(ctx, subject, householdID, "revoke_grant", "grant", req.GrantId, ""); err != nil {
		s.logger.Error("record audit failed",
			"household_id", householdID,
			"grant_id", req.GrantId,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
	}

	s.logger.Info("grant revoked",
		"household_id", householdID,
		"grant_id", req.GrantId,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.RevokeGrantResponse{}, nil
}

// ListActiveGrants lists all active (non-expired, non-revoked) grants (FR7).
func (s *LeafLabAPIServer) ListActiveGrants(ctx context.Context, _ *pb.ListActiveGrantsRequest) (*pb.ListActiveGrantsResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

	// Get the caller's household
	householdID, err := s.repo.GetPrincipalHousehold(ctx, subject)
	if err != nil {
		s.logger.Error("get principal household failed",
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "get household: %v", err)
	}

	if householdID == 0 {
		return &pb.ListActiveGrantsResponse{Grants: []*pb.Grant{}}, nil
	}

	// Get all active grants
	grants, err := s.repo.GetActiveGrants(ctx, householdID)
	if err != nil {
		s.logger.Error("get active grants failed",
			"household_id", householdID,
			"subject", subject,
			"correlation_id", corrID,
			"error", err)
		return nil, status.Errorf(codes.Internal, "list grants: %v", err)
	}

	// Convert to proto
	pbGrants := make([]*pb.Grant, 0, len(grants))
	for _, g := range grants {
		pbGrants = append(pbGrants, &pb.Grant{
			GrantId:   g.GrantID,
			Grantee:   g.Grantee,
			ExpiresAt: g.ExpiresAt,
		})
	}

	s.logger.Info("active grants listed",
		"household_id", householdID,
		"grant_count", len(pbGrants),
		"subject", subject,
		"correlation_id", corrID)

	return &pb.ListActiveGrantsResponse{Grants: pbGrants}, nil
}
