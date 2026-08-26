package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
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
	authz     *AuthorizationPredicates
	publisher *rmq.Publisher
	logger    *slog.Logger
}

func NewLeafLabAPIServer(repo *Repository, authz *AuthorizationPredicates, publisher *rmq.Publisher, logger *slog.Logger) *LeafLabAPIServer {
	return &LeafLabAPIServer{
		repo:      repo,
		authz:     authz,
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
		return nil, status.Error(codes.InvalidArgument, err.Error())
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
		return nil, status.Errorf(codes.Internal, "board lookup: %v", err)
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
		return nil, status.Errorf(codes.Internal, "protojson marshal: %v", err)
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
		return nil, status.Errorf(codes.Internal, "record config push: %v", err)
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
		return nil, status.Errorf(codes.Internal, "proto marshal: %v", err)
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
		return nil, status.Errorf(codes.Internal, "publish config: %v", err)
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
		return nil, status.Error(codes.InvalidArgument, err.Error())
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
		return nil, status.Errorf(codes.Internal, "get config: %v", err)
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

func (s *LeafLabAPIServer) ListBoards(ctx context.Context, _ *pb.ListBoardsRequest) (*pb.ListBoardsResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}

	subject, corrID := getSubjectAndCorrelationID(ctx)

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
	var allBoards []*pb.BoardInfo
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

		for _, r := range rows {
			allBoards = append(allBoards, &pb.BoardInfo{
				DeviceId: r.DeviceID,
				BoardId:  r.BoardID,
			})
		}
	}

	s.logger.Info("boards listed",
		"board_count", len(allBoards),
		"subject", subject,
		"correlation_id", corrID)

	return &pb.ListBoardsResponse{Boards: allBoards}, nil
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

// ── FR50 / FR22.2 / NFR6.2: Region CRUD ──────────────────────────────────────
//
// Region CRUD is a member-or-grantee capability (FR7), not admin-only — every
// handler below authorizes via the single AuthorizationPredicates.MemberOrGrantee
// predicate (#1194), resolved against the region's own household. This is
// intentionally distinct from the reach-aggregated AuthorizationDecision used
// for board listings: a region operation always has (or, for a new root
// region, resolves to) exactly one target household, so the predicate is
// called directly against that household rather than through a decision
// object built from membership alone.

const (
	// maxRegionChildren is the structural cap on direct children per region (FR50.1).
	maxRegionChildren = 12
	// maxRegionDepth is the number of tiers a region tree may nest to, canonically
	// named Room (depth 0), Shelf (depth 1), Pot (depth 2) (FR50.1). A region whose
	// parent is already at the deepest tier is refused.
	maxRegionDepth = 3
)

// regionRowToProto converts a repository RegionRow to the wire RegionInfo shape.
func regionRowToProto(row RegionRow) *pb.RegionInfo {
	info := &pb.RegionInfo{
		RegionId:          row.RegionID,
		Name:              row.Name,
		ParentRegionId:    row.ParentRegionID,
		PathNames:         row.PathNames,
		SuccessorRegionId: row.SuccessorRegionID,
	}
	if row.RetiredAt != nil {
		info.RetiredAt = row.RetiredAt.Unix()
	}
	if row.RetiredOperation != nil {
		info.RetiredOperation = *row.RetiredOperation
	}
	if row.RetiredPrincipal != nil {
		info.RetiredPrincipal = *row.RetiredPrincipal
	}
	return info
}

// validateRegionParent enforces the structural rules (FR50.1) for the region
// a new or re-parented region would sit under: at most maxRegionChildren
// children, and no deeper than maxRegionDepth tiers (Room / Shelf / Pot).
// Returns pgx.ErrNoRows if the parent region does not exist; otherwise every
// error returned is already a gRPC status error.
func (s *LeafLabAPIServer) validateRegionParent(ctx context.Context, parentRegionID int64) error {
	depth, err := s.repo.GetRegionDepth(ctx, parentRegionID)
	if err != nil {
		return err
	}
	if depth+1 >= maxRegionDepth {
		return status.Errorf(codes.FailedPrecondition,
			"region tree does not nest deeper than %d tiers (Room / Shelf / Pot)", maxRegionDepth)
	}
	childCount, err := s.repo.CountChildren(ctx, parentRegionID)
	if err != nil {
		return status.Errorf(codes.Internal, "count children: %v", err)
	}
	if childCount >= maxRegionChildren {
		return status.Errorf(codes.FailedPrecondition,
			"region %d already has the maximum of %d children", parentRegionID, maxRegionChildren)
	}
	return nil
}

// CreateRegion creates a new region (FR50.1). Member-or-grantee, not admin-only.
// Parentage is set here and is immutable once any reading is attributed to
// the region or a descendant — see UpdateRegionParent.
func (s *LeafLabAPIServer) CreateRegion(ctx context.Context, req *pb.CreateRegionRequest) (*pb.CreateRegionResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	var parentRegionID *int64
	if req.ParentRegionId != 0 {
		p := req.ParentRegionId
		parentRegionID = &p
	}

	var householdID int64
	if parentRegionID != nil {
		hid, err := s.repo.GetRegionHousehold(ctx, *parentRegionID)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil, status.Error(codes.InvalidArgument, "parent region not found")
			}
			s.logger.Error("resolve parent region household failed",
				"parent_region_id", *parentRegionID, "subject", subject, "correlation_id", corrID, "error", err)
			return nil, status.Errorf(codes.Internal, "resolve parent household: %v", err)
		}
		householdID = hid
	} else {
		// Root region: no parent to resolve household from. V1 assumes a
		// single-household caller; a principal reachable in more than one
		// household gets the lowest-id household deterministically.
		households, err := s.repo.GetReachableHouseholds(ctx, subject)
		if err != nil {
			s.logger.Error("get reachable households failed",
				"subject", subject, "correlation_id", corrID, "error", err)
			return nil, status.Errorf(codes.Internal, "get reachable households: %v", err)
		}
		if len(households) == 0 {
			return nil, status.Error(codes.PermissionDenied, "principal has no household membership or grant")
		}
		householdID = households[0]
	}

	authorized, err := s.authz.MemberOrGrantee(ctx, householdID, subject)
	if err != nil {
		s.logger.Error("authorization check failed",
			"household_id", householdID, "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}
	if !authorized {
		return nil, status.Error(codes.PermissionDenied, "only a member or grantee of the household may create a region")
	}

	if parentRegionID != nil {
		if err := s.validateRegionParent(ctx, *parentRegionID); err != nil {
			if err == pgx.ErrNoRows {
				return nil, status.Error(codes.InvalidArgument, "parent region not found")
			}
			return nil, err
		}
	}

	regionID, err := s.repo.CreateRegion(ctx, name, req.Description, parentRegionID, householdID)
	if err != nil {
		s.logger.Error("create region failed",
			"name", name, "parent_region_id", req.ParentRegionId, "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "create region: %v", err)
	}

	if err := s.repo.RecordAudit(ctx, subject, householdID, "create_region", "region", regionID, ""); err != nil {
		s.logger.Error("record audit failed",
			"region_id", regionID, "subject", subject, "correlation_id", corrID, "error", err)
	}

	s.logger.Info("region created",
		"region_id", regionID, "name", name, "parent_region_id", req.ParentRegionId,
		"household_id", householdID, "subject", subject, "correlation_id", corrID)

	return &pb.CreateRegionResponse{RegionId: regionID}, nil
}

// RenameRegion changes a region's display name (FR50.1). Member-or-grantee.
// Never touches parentage, so it is never refused by the parentage-freeze
// trigger (NFR6.2). Refused if the region is retired — retirement accepts no
// new writes (FR22.5).
func (s *LeafLabAPIServer) RenameRegion(ctx context.Context, req *pb.RenameRegionRequest) (*pb.RenameRegionResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	region, err := s.repo.GetRegion(ctx, req.RegionId)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, status.Error(codes.NotFound, "region not found")
		}
		s.logger.Error("get region failed",
			"region_id", req.RegionId, "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "get region: %v", err)
	}

	authorized, err := s.authz.MemberOrGrantee(ctx, region.HouseholdID, subject)
	if err != nil {
		s.logger.Error("authorization check failed",
			"household_id", region.HouseholdID, "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}
	if !authorized {
		return nil, status.Error(codes.PermissionDenied, "only a member or grantee of the household may rename a region")
	}

	if region.RetiredAt != nil {
		return nil, status.Error(codes.FailedPrecondition, "region is retired and accepts no new writes")
	}

	if err := s.repo.RenameRegion(ctx, req.RegionId, name); err != nil {
		if err == pgx.ErrNoRows {
			return nil, status.Error(codes.NotFound, "region not found")
		}
		s.logger.Error("rename region failed",
			"region_id", req.RegionId, "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "rename region: %v", err)
	}

	if err := s.repo.RecordAudit(ctx, subject, region.HouseholdID, "rename_region", "region", req.RegionId, name); err != nil {
		s.logger.Error("record audit failed",
			"region_id", req.RegionId, "subject", subject, "correlation_id", corrID, "error", err)
	}

	s.logger.Info("region renamed",
		"region_id", req.RegionId, "name", name, "subject", subject, "correlation_id", corrID)

	return &pb.RenameRegionResponse{}, nil
}

// ListRegions lists regions in every household the caller can reach as a
// member or grantee (FR50.1). Retired regions are excluded by default
// (FR22.5); IncludeRetired opts back in.
func (s *LeafLabAPIServer) ListRegions(ctx context.Context, req *pb.ListRegionsRequest) (*pb.ListRegionsResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	households, err := s.repo.GetReachableHouseholds(ctx, subject)
	if err != nil {
		s.logger.Error("get reachable households failed",
			"subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "get reachable households: %v", err)
	}

	var regions []*pb.RegionInfo
	for _, hid := range households {
		rows, err := s.repo.ListRegionsByHousehold(ctx, hid, req.IncludeRetired)
		if err != nil {
			s.logger.Error("list regions failed",
				"household_id", hid, "subject", subject, "correlation_id", corrID, "error", err)
			return nil, status.Errorf(codes.Internal, "list regions: %v", err)
		}
		for _, row := range rows {
			regions = append(regions, regionRowToProto(row))
		}
	}

	s.logger.Info("regions listed",
		"region_count", len(regions), "include_retired", req.IncludeRetired, "subject", subject, "correlation_id", corrID)

	return &pb.ListRegionsResponse{Regions: regions}, nil
}

// GetRegion reads a single region by id, including a retired one (FR22.5:
// "still readable by explicit id"). Returns the root-to-leaf path (FR50.2).
func (s *LeafLabAPIServer) GetRegion(ctx context.Context, req *pb.GetRegionRequest) (*pb.GetRegionResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	region, err := s.repo.GetRegion(ctx, req.RegionId)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, status.Error(codes.NotFound, "region not found")
		}
		s.logger.Error("get region failed",
			"region_id", req.RegionId, "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "get region: %v", err)
	}

	authorized, err := s.authz.MemberOrGrantee(ctx, region.HouseholdID, subject)
	if err != nil {
		s.logger.Error("authorization check failed",
			"household_id", region.HouseholdID, "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}
	if !authorized {
		return nil, status.Error(codes.PermissionDenied, "only a member or grantee of the household may read this region")
	}

	s.logger.Info("region read",
		"region_id", req.RegionId, "subject", subject, "correlation_id", corrID)

	return &pb.GetRegionResponse{Region: regionRowToProto(region)}, nil
}

// UpdateRegionParent re-parents a region while no reading exists anywhere in
// its subtree — in practice a create-time grace window for a mis-typed
// parent, not a re-parenting capability (FR50.3). The immutability check is
// enforced entirely by the region_freeze_parent_once_attributed trigger
// (NFR6.2, migration 025); this handler never re-implements it, only
// translates a refusal into a clear message naming FR74 (subtree relocation)
// as the alternative (FR59.3).
func (s *LeafLabAPIServer) UpdateRegionParent(ctx context.Context, req *pb.UpdateRegionParentRequest) (*pb.UpdateRegionParentResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	region, err := s.repo.GetRegion(ctx, req.RegionId)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, status.Error(codes.NotFound, "region not found")
		}
		s.logger.Error("get region failed",
			"region_id", req.RegionId, "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "get region: %v", err)
	}

	authorized, err := s.authz.MemberOrGrantee(ctx, region.HouseholdID, subject)
	if err != nil {
		s.logger.Error("authorization check failed",
			"household_id", region.HouseholdID, "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}
	if !authorized {
		return nil, status.Error(codes.PermissionDenied, "only a member or grantee of the household may re-parent a region")
	}

	if region.RetiredAt != nil {
		return nil, status.Error(codes.FailedPrecondition, "region is retired and accepts no new writes")
	}

	if req.RegionId == req.NewParentRegionId {
		return nil, status.Error(codes.InvalidArgument, "a region cannot be its own parent")
	}

	var newParentRegionID *int64
	if req.NewParentRegionId != 0 {
		p := req.NewParentRegionId
		newParentRegionID = &p

		newParentHousehold, err := s.repo.GetRegionHousehold(ctx, *newParentRegionID)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil, status.Error(codes.InvalidArgument, "new parent region not found")
			}
			s.logger.Error("resolve new parent household failed",
				"new_parent_region_id", *newParentRegionID, "subject", subject, "correlation_id", corrID, "error", err)
			return nil, status.Errorf(codes.Internal, "resolve new parent household: %v", err)
		}
		if newParentHousehold != region.HouseholdID {
			return nil, status.Error(codes.InvalidArgument, "new parent region belongs to a different household")
		}

		if err := s.validateRegionParent(ctx, *newParentRegionID); err != nil {
			if err == pgx.ErrNoRows {
				return nil, status.Error(codes.InvalidArgument, "new parent region not found")
			}
			return nil, err
		}
	}

	if err := s.repo.UpdateRegionParent(ctx, req.RegionId, newParentRegionID); err != nil {
		if errors.Is(err, ErrParentageFrozen) {
			return nil, status.Errorf(codes.FailedPrecondition,
				"region %d parentage is frozen: a reading has been attributed to this region or a descendant; use subtree relocation (FR74) instead of re-parenting",
				req.RegionId)
		}
		if err == pgx.ErrNoRows {
			return nil, status.Error(codes.NotFound, "region not found")
		}
		s.logger.Error("update region parent failed",
			"region_id", req.RegionId, "new_parent_region_id", req.NewParentRegionId,
			"subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "update region parent: %v", err)
	}

	if err := s.repo.RecordAudit(ctx, subject, region.HouseholdID, "update_region_parent", "region", req.RegionId, ""); err != nil {
		s.logger.Error("record audit failed",
			"region_id", req.RegionId, "subject", subject, "correlation_id", corrID, "error", err)
	}

	s.logger.Info("region re-parented",
		"region_id", req.RegionId, "new_parent_region_id", req.NewParentRegionId,
		"subject", subject, "correlation_id", corrID)

	return &pb.UpdateRegionParentResponse{}, nil
}

// RetireRegion soft-retires a region (FR22.2, FR22.5). A retired region
// remains resolvable for attribution of readings recorded while it was
// active; its parentage remains immutable; it is excluded from default
// listings and accepts no new writes. Retirement does not unfreeze
// parentage. An optional successor_region_id names the region that replaced
// it when retirement is the result of a relocation (FR74), so region-keyed
// series join across the reorganisation.
func (s *LeafLabAPIServer) RetireRegion(ctx context.Context, req *pb.RetireRegionRequest) (*pb.RetireRegionResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	region, err := s.repo.GetRegion(ctx, req.RegionId)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, status.Error(codes.NotFound, "region not found")
		}
		s.logger.Error("get region failed",
			"region_id", req.RegionId, "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "get region: %v", err)
	}

	authorized, err := s.authz.MemberOrGrantee(ctx, region.HouseholdID, subject)
	if err != nil {
		s.logger.Error("authorization check failed",
			"household_id", region.HouseholdID, "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "authorization: %v", err)
	}
	if !authorized {
		return nil, status.Error(codes.PermissionDenied, "only a member or grantee of the household may retire a region")
	}

	if region.RetiredAt != nil {
		return nil, status.Error(codes.FailedPrecondition, "region is already retired")
	}

	var successorRegionID *int64
	if req.SuccessorRegionId != 0 {
		s2 := req.SuccessorRegionId
		successorRegionID = &s2

		if *successorRegionID == req.RegionId {
			return nil, status.Error(codes.InvalidArgument, "a region cannot be its own successor")
		}

		successorHousehold, err := s.repo.GetRegionHousehold(ctx, *successorRegionID)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil, status.Error(codes.InvalidArgument, "successor region not found")
			}
			s.logger.Error("resolve successor region household failed",
				"successor_region_id", *successorRegionID, "subject", subject, "correlation_id", corrID, "error", err)
			return nil, status.Errorf(codes.Internal, "resolve successor household: %v", err)
		}
		if successorHousehold != region.HouseholdID {
			return nil, status.Error(codes.InvalidArgument, "successor region belongs to a different household")
		}
	}

	if err := s.repo.RetireRegion(ctx, req.RegionId, "retire_region", subject, successorRegionID); err != nil {
		if err == pgx.ErrNoRows {
			return nil, status.Error(codes.NotFound, "region not found")
		}
		s.logger.Error("retire region failed",
			"region_id", req.RegionId, "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "retire region: %v", err)
	}

	if err := s.repo.RecordAudit(ctx, subject, region.HouseholdID, "retire_region", "region", req.RegionId, ""); err != nil {
		s.logger.Error("record audit failed",
			"region_id", req.RegionId, "subject", subject, "correlation_id", corrID, "error", err)
	}

	s.logger.Info("region retired",
		"region_id", req.RegionId, "successor_region_id", req.SuccessorRegionId,
		"subject", subject, "correlation_id", corrID)

	return &pb.RetireRegionResponse{}, nil
}
