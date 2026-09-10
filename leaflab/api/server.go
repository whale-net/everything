package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/configcompose"
	"github.com/whale-net/everything/libs/go/grpcauth"
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

// reportingThreshold is the fixed recency threshold behind ReportingState
// (FR5): a board is REPORTING if its most recent sensor_reading.recorded_at
// is within this window, STALE if it has readings older than this, and
// NEVER_REPORTED if it has none at all. Shared with the board-detail path
// (per-sensor state); not configurable per board/sensor and not exposed on
// the request or response.
const reportingThreshold = 10 * time.Minute

// adminRole is the one leaflab-local role this milestone uses (FR10). Roles
// are free text in leaflab_user_role; this constant is just this file's
// single point of truth for the string, not a closed enum in the schema.
const adminRole = "admin"

// repositoryStore is the subset of *Repository's methods LeafLabAPIServer
// calls, extracted so tests can substitute an in-memory fake (no Postgres)
// while production code keeps passing the real *Repository straight
// through -- *Repository already satisfies this interface, no adapter
// needed.
type repositoryStore interface {
	GetLeafLabUserIDBySub(ctx context.Context, oidcSub string) (int64, bool, error)
	GetCurrentBoardOwner(ctx context.Context, boardID int64) (int64, bool, error)
	HasRole(ctx context.Context, leaflabUserID int64, role string) (bool, error)
	GetBoardIDForDeviceID(ctx context.Context, deviceID string) (int64, bool, error)
	InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte, resetSensorIDs []int64) (int64, error)
	GetLatestAcceptedConfig(ctx context.Context, deviceID string) (*configpb.DeviceConfig, error)
	ListSensorInventoryForBoard(ctx context.Context, boardID int64) ([]configcompose.InventorySensor, error)
	ListBoards(ctx context.Context) ([]BoardRow, error)
	ListBoardsWithState(ctx context.Context) ([]BoardWithReadingRow, error)
	GetBoardIdentity(ctx context.Context, boardID int64) (BoardIdentity, error)
	RenameBoard(ctx context.Context, boardID int64, name string) error
	ListSensorDetailsForBoard(ctx context.Context, boardID int64) ([]SensorDetailRow, error)
	SensorExists(ctx context.Context, sensorID int64) (bool, error)
	GetSensorReadingHistory(ctx context.Context, sensorID int64, from, to time.Time) (*SensorReadingHistory, error)
	// ClaimBoard is ClaimBoard's own write path (FR1, FR2, NFR2) -- see
	// repository.go's doc comment for the race-safety argument.
	ClaimBoard(ctx context.Context, boardID, leaflabUserID int64) error
	// The following five methods back the admin ownership screen (#1777:
	// FR11-FR14) -- ListOwnedBoards, ReassignBoardOwner, ClearBoardOwner,
	// LeafLabUserExists, and ListUsers.
	ListOwnedBoards(ctx context.Context) ([]OwnedBoardRow, error)
	ReassignBoardOwner(ctx context.Context, boardID, newOwnerUserID int64) error
	ClearBoardOwner(ctx context.Context, boardID int64) error
	LeafLabUserExists(ctx context.Context, leaflabUserID int64) (bool, error)
	ListUsers(ctx context.Context) ([]LeafLabUserRow, error)
	// GetBoardIDForSensor resolves sensor_id -> board_id so RenameSensor
	// (FR4) can authorize the write via authorizeBoardWrite before touching
	// sensor_name_history.
	GetBoardIDForSensor(ctx context.Context, sensorID int64) (int64, bool, error)
	// RenameSensor is RenameSensor's own write path (FR4) -- SCD2
	// close-and-open plus the NFR4 counter reset, one transaction; see
	// repository.go's doc comment.
	RenameSensor(ctx context.Context, sensorID int64, name string) error
	// The following five methods back the M3 region lifecycle RPCs (#2312:
	// FR1-FR3, FR5) -- GetRegionIdentity (existence/ownership reads and the
	// parent-existence check), CreateRegion (region row + initial open
	// region_parent_history row, one transaction), RenameRegion,
	// ReparentRegion (SCD2 close-and-open + mirror update, one
	// transaction), and ReparentCreatesCycle (FR5's ancestor walk).
	GetRegionIdentity(ctx context.Context, regionID int64) (RegionIdentity, error)
	CreateRegion(ctx context.Context, name string, parentRegionID *int64, ownerUserID int64) (int64, error)
	RenameRegion(ctx context.Context, regionID int64, name string) error
	ReparentRegion(ctx context.Context, regionID int64, newParentRegionID *int64) error
	ReparentCreatesCycle(ctx context.Context, regionID, newParentRegionID int64) (bool, error)
}

// configPublisher is the one *rmq.Publisher method PushDeviceConfig calls,
// extracted for the same reason as repositoryStore: *rmq.Publisher
// satisfies it as-is (see libs/go/rmq/publisher.go's Publish signature).
type configPublisher interface {
	Publish(ctx context.Context, exchange, routingKey string, body interface{}) error
}

type LeafLabAPIServer struct {
	pb.UnimplementedLeafLabAPIServer
	repo      repositoryStore
	publisher configPublisher
	logger    *slog.Logger
}

func NewLeafLabAPIServer(repo repositoryStore, publisher configPublisher, logger *slog.Logger) *LeafLabAPIServer {
	return &LeafLabAPIServer{
		repo:      repo,
		publisher: publisher,
		logger:    logger,
	}
}

// -- M2 ownership/authorization helpers --------------------------------------

// callerUserID resolves the authenticated caller (via grpcauth.Claims in
// ctx) to a leaflab_user_id. Returns codes.Unauthenticated when no claims
// are present, and codes.PermissionDenied when the claims' subject resolves
// to no leaflab_user row (leaflab-api never creates one -- LB1).
//
// This applies identically in AuthModeNone: grpcauth always injects dev
// claims there (Subject "dev-user"), so a local/Tilt caller is denied here
// exactly like an OIDC caller would be, until dev-user has a leaflab_user
// row -- see leaflab/README.md's local-dev claim step.
func (s *LeafLabAPIServer) callerUserID(ctx context.Context) (int64, error) {
	claims, ok := grpcauth.ClaimsFromContext(ctx)
	if !ok {
		return 0, status.Error(codes.Unauthenticated, "authentication required")
	}

	userID, found, err := s.repo.GetLeafLabUserIDBySub(ctx, claims.Subject)
	if err != nil {
		return 0, status.Errorf(codes.Internal, "resolve caller identity: %v", err)
	}
	if !found {
		return 0, status.Errorf(codes.PermissionDenied,
			"no leaflab_user found for subject %q -- sign in to leaflab-ui at least once (this never happens automatically; see leaflab/README.md's local-dev claim step)",
			claims.Subject)
	}
	return userID, nil
}

// callerUserIDForRead resolves the caller's leaflab_user_id for computing
// owned_by_caller on a read RPC (ListBoardsWithState, GetBoardDetail).
// Unlike callerUserID (used to authorize a write), a caller with no
// leaflab_user row is not an error here: FR5 keeps reads unscoped by
// ownership, so a caller in that state just sees owned_by_caller false for
// every board -- returning (0, nil) is enough, since leaflab_user_id is
// BIGSERIAL starting at 1 and can never legitimately equal 0.
func (s *LeafLabAPIServer) callerUserIDForRead(ctx context.Context) (int64, error) {
	claims, ok := grpcauth.ClaimsFromContext(ctx)
	if !ok {
		return 0, status.Error(codes.Unauthenticated, "authentication required")
	}

	userID, found, err := s.repo.GetLeafLabUserIDBySub(ctx, claims.Subject)
	if err != nil {
		return 0, status.Errorf(codes.Internal, "resolve caller identity: %v", err)
	}
	if !found {
		return 0, nil
	}
	return userID, nil
}

// ownerToProto converts an *OwnerRow to the wire LeafLabUser message, or nil
// when the board is unowned. Never sets a field derived from oidc_sub (NFR5)
// -- OwnerRow itself carries no such field to begin with.
func ownerToProto(o *OwnerRow) *pb.LeafLabUser {
	if o == nil {
		return nil
	}
	return &pb.LeafLabUser{
		LeaflabUserId:     o.LeafLabUserID,
		DisplayName:       o.DisplayName,
		PreferredUsername: o.PreferredUsername,
		Email:             o.Email,
	}
}

// boardNameOrEmpty renders a *string board name (nil when unnamed) as the
// wire's empty-string convention (api.proto: "board_name is empty when the
// board has no name -- the UI falls back to device_id").
func boardNameOrEmpty(name *string) string {
	if name == nil {
		return ""
	}
	return *name
}

// authorizeBoardWrite returns nil iff the caller is boardID's current
// owner. Returns codes.PermissionDenied both for a different owner AND for
// an unowned board (FR6) -- ClaimBoard is the sole write path that does not
// call this helper. Consults no role information: FR5 has no admin
// exception here.
func (s *LeafLabAPIServer) authorizeBoardWrite(ctx context.Context, boardID int64) (callerUserID int64, err error) {
	callerUserID, err = s.callerUserID(ctx)
	if err != nil {
		return 0, err
	}

	ownerID, owned, err := s.repo.GetCurrentBoardOwner(ctx, boardID)
	if err != nil {
		return 0, status.Errorf(codes.Internal, "get board owner: %v", err)
	}
	if !owned {
		return 0, status.Errorf(codes.PermissionDenied, "board %d is unowned -- claim it before writing to it", boardID)
	}
	if ownerID != callerUserID {
		return 0, status.Error(codes.PermissionDenied, "caller does not own this board")
	}
	return callerUserID, nil
}

// requireAdmin resolves the caller and returns codes.PermissionDenied unless
// they hold an open 'admin' grant in leaflab_user_role (FR14). This is the
// only thing the admin role grants in this milestone: it gates
// ListOwnedBoards, ReassignBoardOwner, ClearBoardOwner, and ListUsers.
// authorizeBoardWrite deliberately never calls this -- FR5 has no admin
// exception, and admin write access to a board it does not own (beyond
// reassign/clear) is out of scope.
//
// Reads leaflab_user_role exclusively; grpcauth.Claims.Roles (OIDC
// realm_access.roles) is never consulted here -- leaflab owns its own roles
// (see leaflab/PRODUCT.md section Non-goals).
func (s *LeafLabAPIServer) requireAdmin(ctx context.Context) (callerUserID int64, err error) {
	callerUserID, err = s.callerUserID(ctx)
	if err != nil {
		return 0, err
	}

	isAdmin, err := s.repo.HasRole(ctx, callerUserID, adminRole)
	if err != nil {
		return 0, status.Errorf(codes.Internal, "check admin role: %v", err)
	}
	if !isAdmin {
		return 0, status.Error(codes.PermissionDenied, "caller does not hold the admin role")
	}
	return callerUserID, nil
}

// authorizeRegionWrite returns nil iff the caller may write to the given
// region (M3 NFR2): its current owner, or an admin acting on the owner's
// behalf out-of-band (the product brief's admin-bypass pattern -- unlike
// authorizeBoardWrite, whose no-admin-exception rule is M2 FR5's, not this
// milestone's; region/placement edits are exactly the writes NFR2 lets an
// admin perform for an owner who asks for help). A region whose
// owner_leaflab_user_id is NULL (pre-M3 legacy rows; CreateRegion always
// sets the owner, FR1) has no owner to match, so only the admin bypass
// passes -- a signed-in non-admin is codes.PermissionDenied. A region_id
// that does not exist never reaches this helper: handlers resolve existence
// first via GetRegionIdentity (codes.NotFound), so "unknown" is
// distinguishable from "not yours", same ordering as RenameBoard.
// Re-parenting never changes ownership, so this helper's verdict is
// identical for every write on a given region.
func (s *LeafLabAPIServer) authorizeRegionWrite(ctx context.Context, region RegionIdentity) error {
	callerUserID, err := s.callerUserID(ctx)
	if err != nil {
		return err
	}

	if region.OwnerLeaflabUserID != nil && *region.OwnerLeaflabUserID == callerUserID {
		return nil
	}

	isAdmin, err := s.repo.HasRole(ctx, callerUserID, adminRole)
	if err != nil {
		return status.Errorf(codes.Internal, "check admin role: %v", err)
	}
	if isAdmin {
		return nil
	}
	return status.Errorf(codes.PermissionDenied, "caller does not own region %d", region.RegionID)
}

// validateRegionName enforces the one rule region.name's column
// (VARCHAR(255), 001_initial_schema.up.sql) plus the M2 name-validation
// precedent put on region names: non-empty after trimming, and at most 255
// *characters* (Postgres counts VARCHAR length in characters, not bytes --
// hence the rune count, not a byte count). No uniqueness rule across
// regions (matches RenameBoard's decided non-goal; two sibling regions may
// share a name).
func validateRegionName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("name must not be empty")
	}
	if utf8.RuneCountInString(name) > maxRegionNameLen {
		return fmt.Errorf("name must be at most %d characters", maxRegionNameLen)
	}
	return nil
}

func (s *LeafLabAPIServer) PushDeviceConfig(ctx context.Context, req *pb.PushDeviceConfigRequest) (*pb.PushDeviceConfigResponse, error) {
	if err := validateDeviceID(req.DeviceId); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	boardID, ok, err := s.repo.GetBoardIDForDeviceID(ctx, req.DeviceId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "board lookup: %v", err)
	}
	if !ok {
		return nil, status.Errorf(codes.NotFound, "device %q not registered", req.DeviceId)
	}

	if _, err := s.authorizeBoardWrite(ctx, boardID); err != nil {
		return nil, err
	}

	// FR8: compose the board's full desired sensor list rather than
	// publishing exactly the caller's supplied entries -- see
	// ComposeDesiredSensors' doc comment. Two indexed reads, no per-sensor
	// round trips (NFR3).
	inventory, err := s.repo.ListSensorInventoryForBoard(ctx, boardID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list sensor inventory: %v", err)
	}
	lastAccepted, err := s.repo.GetLatestAcceptedConfig(ctx, req.DeviceId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get latest accepted config: %v", err)
	}
	var lastAcceptedSensors []*configpb.SensorConfig
	if lastAccepted != nil {
		lastAcceptedSensors = lastAccepted.Sensors
	}
	composedSensors := configcompose.ComposeDesiredSensors(inventory, lastAcceptedSensors, req.Sensors)

	// Build the proto with a placeholder version; we need configJSON for the
	// atomic insert that returns the real version, so marshal without version first.
	cfgProto := &configpb.DeviceConfig{
		DeviceId: req.DeviceId,
		Sensors:  composedSensors,
	}
	configJSON, err := protojson.Marshal(cfgProto)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "protojson marshal: %v", err)
	}

	// NFR4 "Reset": an explicit push re-arms FR9's auto-convergence for
	// exactly the sensors the caller named, same as a fresh FR4 rename does.
	resetSensorIDs := configcompose.TouchedSensorIDs(inventory, req.Sensors)

	// Atomically assign version and record the pending push before publishing.
	// This ensures the DB row always exists before the device can ack.
	version, err := s.repo.InsertDeviceConfigNextVersion(ctx, boardID, configJSON, resetSensorIDs)
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
		"sensors", len(composedSensors),
		"override_sensors", len(req.Sensors))

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
		s.logger.Info("device config requested — none accepted yet", "device_id", req.DeviceId)
		return &pb.GetDeviceConfigResponse{Found: false}, nil
	}
	s.logger.Info("device config requested", "device_id", req.DeviceId, "version", cfg.Version)
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
	s.logger.Info("boards listed", "count", len(boards))
	return &pb.ListBoardsResponse{Boards: boards}, nil
}

// ListBoardsWithState returns every board (FR4 — no owner filtering of which
// boards appear) with a reporting state derived purely from sensor_reading
// recency (FR5), plus M2's read-side ownership fields (board_name,
// owned_by_caller, owner).
func (s *LeafLabAPIServer) ListBoardsWithState(ctx context.Context, _ *pb.ListBoardsWithStateRequest) (*pb.ListBoardsWithStateResponse, error) {
	callerUserID, err := s.callerUserIDForRead(ctx)
	if err != nil {
		return nil, err
	}

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
			BoardName:      boardNameOrEmpty(r.BoardName),
			Owner:          ownerToProto(r.Owner),
			OwnedByCaller:  r.Owner != nil && r.Owner.LeafLabUserID == callerUserID,
		}
		if r.LastReadingAt != nil {
			bw.LastReadingAt = timestamppb.New(*r.LastReadingAt)
		}
		boards = append(boards, bw)
	}

	// caller_is_admin is presentation-only (see the field's proto doc) --
	// this RPC itself has no auth requirement (FR4: no owner filtering), so
	// an unresolved caller (no claims, or no leaflab_user row yet) simply
	// yields false rather than failing the whole listing.
	callerIsAdmin := false
	if callerUserID, err := s.callerUserID(ctx); err == nil {
		callerIsAdmin, err = s.repo.HasRole(ctx, callerUserID, adminRole)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "check admin role: %v", err)
		}
	}

	s.logger.Info("boards with state listed", "count", len(boards))
	return &pb.ListBoardsWithStateResponse{Boards: boards, CallerIsAdmin: callerIsAdmin}, nil
}

// GetBoardDetail returns a board's identity plus every sensor recorded for
// it, each with its own reporting state and (when present) most recent
// reading (FR6, FR7). Unknown board_id returns codes.NotFound.
//
// Non-blocking by design: a stale or never-reported sensor is a normal row
// in the response, not an error, and a board with zero sensors returns an
// OK response with an empty sensor list.
func (s *LeafLabAPIServer) GetBoardDetail(ctx context.Context, req *pb.GetBoardDetailRequest) (*pb.GetBoardDetailResponse, error) {
	callerUserID, err := s.callerUserIDForRead(ctx)
	if err != nil {
		return nil, err
	}

	identity, err := s.repo.GetBoardIdentity(ctx, req.BoardId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "board %d not found", req.BoardId)
		}
		return nil, status.Errorf(codes.Internal, "get board identity: %v", err)
	}

	rows, err := s.repo.ListSensorDetailsForBoard(ctx, req.BoardId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list sensor details: %v", err)
	}

	now := time.Now()
	sensors := make([]*pb.SensorDetail, 0, len(rows))
	for _, r := range rows {
		sd := &pb.SensorDetail{
			SensorId:       r.SensorID,
			SensorName:     r.SensorName,
			Unit:           r.Unit,
			SensorTypeName: r.SensorTypeName,
			ReportingState: reportingState(r.LatestRecordedAt, now),
		}
		if r.LatestRecordedAt != nil {
			sd.LatestReading = &pb.LatestReading{
				Value:      *r.LatestValue,
				RecordedAt: timestamppb.New(*r.LatestRecordedAt),
				Valid:      *r.LatestValid,
			}
		}
		sensors = append(sensors, sd)
	}

	s.logger.Info("board detail listed", "board_id", req.BoardId, "sensor_count", len(sensors))
	return &pb.GetBoardDetailResponse{
		BoardId:       req.BoardId,
		DeviceId:      identity.DeviceID,
		Sensors:       sensors,
		BoardName:     boardNameOrEmpty(identity.BoardName),
		Owner:         ownerToProto(identity.Owner),
		OwnedByCaller: identity.Owner != nil && identity.Owner.LeafLabUserID == callerUserID,
	}, nil
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
		s.logger.Warn("sensor reading history capped — requested range exceeds point cap",
			"sensor_id", req.SensorId,
			"point_cap", historyPointCap,
			"covered_from", history.CoveredFrom,
			"covered_to", history.CoveredTo,
			"requested_from", from,
			"requested_to", to,
		)
	}
	s.logger.Info("sensor reading history served",
		"sensor_id", req.SensorId,
		"points", len(history.Points),
		"excluded_invalid", history.ExcludedInvalidCount,
		"capped", history.Capped,
	)
	return resp, nil
}

// ClaimBoard opens ownership of an unowned board for the calling user (FR1,
// FR2). Deliberately does not call authorizeBoardWrite: that helper denies
// writes to an unowned board, and claiming an unowned board is exactly the
// one write FR6 carves out as the explicit exception to that rule.
//
// Any signed-in user may claim -- no role required, no prior relationship
// to the board required. callerUserID (not callerUserIDForRead) is used
// here on purpose: a claim is a write, so a caller with no leaflab_user row
// is codes.PermissionDenied, not silently treated as "no owner" the way a
// read-side caller would be.
//
// The board-exists check and the claim are two separate statements, not one
// transaction: NFR2's race is between two *claims*, which
// idx_board_owner_history_current (013_ownership.up.sql) resolves at the
// INSERT itself regardless of what this handler did beforehand. This
// existence check only distinguishes codes.NotFound from
// codes.FailedPrecondition for an unknown board_id -- it does not, and does
// not need to, participate in the race.
func (s *LeafLabAPIServer) ClaimBoard(ctx context.Context, req *pb.ClaimBoardRequest) (*pb.ClaimBoardResponse, error) {
	callerUserID, err := s.callerUserID(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := s.repo.GetBoardIdentity(ctx, req.BoardId); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "board %d not found", req.BoardId)
		}
		return nil, status.Errorf(codes.Internal, "get board identity: %v", err)
	}

	if err := s.repo.ClaimBoard(ctx, req.BoardId, callerUserID); err != nil {
		if errors.Is(err, ErrBoardAlreadyOwned) {
			return nil, status.Errorf(codes.FailedPrecondition, "board %d is already owned", req.BoardId)
		}
		return nil, status.Errorf(codes.Internal, "claim board: %v", err)
	}

	s.logger.Info("board claimed", "board_id", req.BoardId, "leaflab_user_id", callerUserID)
	return &pb.ClaimBoardResponse{}, nil
}

// RenameBoard renames a board the calling user owns (FR3). The write goes
// straight to Postgres: it is never gated on the board being online and
// never waits on a device round-trip (LB2) -- an offline board's name
// changes immediately, and no config push happens on this path (board name
// is not part of firmware.DeviceConfig at all).
//
// Existence is checked first (GetBoardIdentity, NotFound on
// pgx.ErrNoRows) -- this must precede authorizeBoardWrite, not follow it:
// authorizeBoardWrite's owned=false covers both "board exists but is
// unowned" and "board_id does not exist" identically (PermissionDenied), so
// checking existence afterward would never be reached for a genuinely
// unknown board_id. Existence is not ownership-gated information here any
// more than it already is on GetBoardDetail (FR5: reads are unscoped by
// ownership), so checking it ahead of authorizeBoardWrite discloses
// nothing new.
//
// Validation is non-empty only (empty or whitespace-only -> InvalidArgument):
// no length limit, no character-set restriction, and no uniqueness check
// across boards -- a decided non-goal for this milestone, not a deferral.
func (s *LeafLabAPIServer) RenameBoard(ctx context.Context, req *pb.RenameBoardRequest) (*pb.RenameBoardResponse, error) {
	if _, err := s.repo.GetBoardIdentity(ctx, req.BoardId); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "board %d not found", req.BoardId)
		}
		return nil, status.Errorf(codes.Internal, "get board identity: %v", err)
	}

	if _, err := s.authorizeBoardWrite(ctx, req.BoardId); err != nil {
		return nil, err
	}

	if strings.TrimSpace(req.Name) == "" {
		return nil, status.Error(codes.InvalidArgument, "name must not be empty")
	}

	if err := s.repo.RenameBoard(ctx, req.BoardId, req.Name); err != nil {
		return nil, status.Errorf(codes.Internal, "rename board: %v", err)
	}

	s.logger.Info("board renamed", "board_id", req.BoardId, "name", req.Name)
	return &pb.RenameBoardResponse{}, nil
}

// RenameSensor renames a sensor via the sensor_name_history SCD2 table
// (FR4). Resolves sensor_id -> board_id (GetBoardIDForSensor) then
// authorizeBoardWrite: only the sensor's board's current owner may rename
// it, and an unowned board is codes.PermissionDenied identically to a
// non-owner (FR5/FR6 -- no admin exception here, same as every other write
// this milestone gates through authorizeBoardWrite).
//
// Non-empty validation only (FR4) -- no length limit, no uniqueness check
// beyond the one narrow same-board-same-name collision
// repository.RenameSensor's ErrSensorNameConflict documents.
//
// Writes directly to Postgres and never waits on, or is gated by, a device
// round trip (LB2): the rename is visible on the very next GetBoardDetail
// regardless of whether the board is online. This RPC deliberately issues
// no config push -- leaflab-processor's handleManifest may transiently
// revert sensor.name from a stale device reconnect until the device is
// told about the rename; that is expected, unmodified existing behavior
// (see this task's issue § "Interaction with manifest ingest"), converged
// by FR9's corrective push (#1772), not by this RPC.
func (s *LeafLabAPIServer) RenameSensor(ctx context.Context, req *pb.RenameSensorRequest) (*pb.RenameSensorResponse, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, status.Error(codes.InvalidArgument, "name must not be empty")
	}

	boardID, ok, err := s.repo.GetBoardIDForSensor(ctx, req.SensorId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get board for sensor: %v", err)
	}
	if !ok {
		return nil, status.Errorf(codes.NotFound, "sensor %d not found", req.SensorId)
	}

	if _, err := s.authorizeBoardWrite(ctx, boardID); err != nil {
		return nil, err
	}

	if err := s.repo.RenameSensor(ctx, req.SensorId, req.Name); err != nil {
		if errors.Is(err, ErrSensorNameConflict) {
			return nil, status.Errorf(codes.FailedPrecondition,
				"sensor name %q is already used by another sensor on this board", req.Name)
		}
		return nil, status.Errorf(codes.Internal, "rename sensor: %v", err)
	}

	s.logger.Info("sensor renamed", "sensor_id", req.SensorId, "board_id", boardID)
	return &pb.RenameSensorResponse{}, nil
}

// ListOwnedBoards returns every currently-owned board and its owner (FR11).
// requireAdmin runs first, before any repository access (FR14) -- a
// non-admin caller never reaches s.repo.ListOwnedBoards.
func (s *LeafLabAPIServer) ListOwnedBoards(ctx context.Context, _ *pb.ListOwnedBoardsRequest) (*pb.ListOwnedBoardsResponse, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}

	rows, err := s.repo.ListOwnedBoards(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list owned boards: %v", err)
	}

	boards := make([]*pb.OwnedBoard, 0, len(rows))
	for _, r := range rows {
		boards = append(boards, &pb.OwnedBoard{
			BoardId:   r.BoardID,
			DeviceId:  r.DeviceID,
			BoardName: boardNameOrEmpty(r.BoardName),
			Owner:     ownerToProto(&r.Owner),
		})
	}

	s.logger.Info("owned boards listed", "count", len(boards))
	return &pb.ListOwnedBoardsResponse{Boards: boards}, nil
}

// ReassignBoardOwner closes boardID's open ownership row and opens one for
// req.NewOwnerLeaflabUserId, in one transaction (FR12: SCD2 close-and-open,
// AGENTS.md section SCD2). requireAdmin runs first, before any repository
// access (FR14).
//
// GetBoardIdentity doubles as both the unknown-board_id check (NotFound)
// and the current-owner read this RPC needs anyway -- one query rather
// than two. bi.Owner == nil means unowned (FailedPrecondition: nothing to
// reassign); a nil Owner also means "no history row to close", so this
// never calls the repository with an unowned board_id. Reassigning to the
// current owner is FailedPrecondition too (it would otherwise churn the
// history with a zero-length interval); an unknown new owner is NotFound,
// checked via LeafLabUserExists before the write.
func (s *LeafLabAPIServer) ReassignBoardOwner(ctx context.Context, req *pb.ReassignBoardOwnerRequest) (*pb.ReassignBoardOwnerResponse, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}

	bi, err := s.repo.GetBoardIdentity(ctx, req.BoardId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "board %d not found", req.BoardId)
		}
		return nil, status.Errorf(codes.Internal, "get board identity: %v", err)
	}
	if bi.Owner == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "board %d is unowned -- there is no owner to reassign", req.BoardId)
	}
	if bi.Owner.LeafLabUserID == req.NewOwnerLeaflabUserId {
		return nil, status.Errorf(codes.FailedPrecondition, "board %d is already owned by user %d", req.BoardId, req.NewOwnerLeaflabUserId)
	}

	exists, err := s.repo.LeafLabUserExists(ctx, req.NewOwnerLeaflabUserId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check new owner exists: %v", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "user %d not found", req.NewOwnerLeaflabUserId)
	}

	previousOwnerID := bi.Owner.LeafLabUserID
	if err := s.repo.ReassignBoardOwner(ctx, req.BoardId, req.NewOwnerLeaflabUserId); err != nil {
		return nil, status.Errorf(codes.Internal, "reassign board owner: %v", err)
	}

	s.logger.Info("board owner reassigned",
		"board_id", req.BoardId,
		"previous_owner_leaflab_user_id", previousOwnerID,
		"new_owner_leaflab_user_id", req.NewOwnerLeaflabUserId)
	return &pb.ReassignBoardOwnerResponse{}, nil
}

// ClearBoardOwner closes boardID's open ownership row and opens none
// (FR13: SCD2 close, no re-open). requireAdmin runs first, before any
// repository access (FR14). After this returns, the board behaves exactly
// as any other unowned board (FR6): open to claim by any signed-in user
// (FR13 -> FR6 -> FR1 continuity), closed to every other write.
//
// GetBoardIdentity doubles as the unknown-board_id check (NotFound) and
// the current-owner read used for the log line; bi.Owner == nil is
// FailedPrecondition (already unowned, nothing to clear) -- the repository
// is never called for an unowned board_id.
func (s *LeafLabAPIServer) ClearBoardOwner(ctx context.Context, req *pb.ClearBoardOwnerRequest) (*pb.ClearBoardOwnerResponse, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}

	bi, err := s.repo.GetBoardIdentity(ctx, req.BoardId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "board %d not found", req.BoardId)
		}
		return nil, status.Errorf(codes.Internal, "get board identity: %v", err)
	}
	if bi.Owner == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "board %d is already unowned", req.BoardId)
	}

	previousOwnerID := bi.Owner.LeafLabUserID
	if err := s.repo.ClearBoardOwner(ctx, req.BoardId); err != nil {
		return nil, status.Errorf(codes.Internal, "clear board owner: %v", err)
	}

	s.logger.Info("board owner cleared", "board_id", req.BoardId, "previous_owner_leaflab_user_id", previousOwnerID)
	return &pb.ClearBoardOwnerResponse{}, nil
}

// ListUsers returns every leaflab_user row, the admin reassign picker's
// candidate list (FR11, FR12). requireAdmin runs first, before any
// repository access (FR14). Never returns oidc_sub (NFR5) --
// LeafLabUserRow carries no such field to begin with, so there is nothing
// to strip here.
func (s *LeafLabAPIServer) ListUsers(ctx context.Context, _ *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}

	rows, err := s.repo.ListUsers(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list users: %v", err)
	}

	users := make([]*pb.LeafLabUser, 0, len(rows))
	for _, r := range rows {
		users = append(users, &pb.LeafLabUser{
			LeaflabUserId:     r.LeafLabUserID,
			DisplayName:       r.DisplayName,
			PreferredUsername: r.PreferredUsername,
			Email:             r.Email,
		})
	}

	s.logger.Info("users listed", "count", len(users))
	return &pb.ListUsersResponse{Users: users}, nil
}

// -- M3 region lifecycle (FR1-FR3, FR5, NFR2) --------------------------------

// CreateRegion creates a region owned by the calling user from the moment it
// exists (FR1, NFR2), with an optional parent (0 = top-level), and writes
// the region's initial open region_parent_history row in the same
// transaction as the region row itself (FR1) -- every region has at least
// one open history row from the moment it exists, never only from its first
// re-parent.
//
// No cycle is possible at creation (nothing can point at a region that does
// not exist yet), so there is deliberately no cycle check on this path. The
// parent-existence check (codes.NotFound) is deliberately NOT an ownership
// check: reads and tree-structure navigation are unscoped by ownership (M2
// FR5 read precedent), so any signed-in user may nest a new region under any
// existing one -- that creates a region they own, it does not edit anyone
// else's. No device round trip is involved or awaited (LB2/NFR1): this is a
// pure Postgres write, visible to the next read immediately.
func (s *LeafLabAPIServer) CreateRegion(ctx context.Context, req *pb.CreateRegionRequest) (*pb.CreateRegionResponse, error) {
	callerUserID, err := s.callerUserID(ctx)
	if err != nil {
		return nil, err
	}

	if err := validateRegionName(req.Name); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	var parentRegionID *int64
	if req.ParentRegionId != 0 {
		if _, err := s.repo.GetRegionIdentity(ctx, req.ParentRegionId); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, status.Errorf(codes.NotFound, "parent region %d not found", req.ParentRegionId)
			}
			return nil, status.Errorf(codes.Internal, "get parent region: %v", err)
		}
		parentRegionID = &req.ParentRegionId
	}

	regionID, err := s.repo.CreateRegion(ctx, req.Name, parentRegionID, callerUserID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create region: %v", err)
	}

	s.logger.Info("region created",
		"region_id", regionID,
		"name", req.Name,
		"parent_region_id", req.ParentRegionId,
		"leaflab_user_id", callerUserID)
	return &pb.CreateRegionResponse{RegionId: regionID}, nil
}

// RenameRegion renames a region (FR2): forward-looking only, since names are
// current-value with no history table (repository.RenameRegion's doc
// comment) -- past readings' attribution and display are unaffected.
// Existence is checked first (codes.NotFound), then authorizeRegionWrite
// (owner, or admin acting on the owner's behalf -- NFR2), then validation;
// same ordering as RenameBoard, so an unknown region_id is distinguishable
// from an unowned one and a denied caller's request reaches no write. Pure
// Postgres write, no device round trip (LB2/NFR1).
func (s *LeafLabAPIServer) RenameRegion(ctx context.Context, req *pb.RenameRegionRequest) (*pb.RenameRegionResponse, error) {
	region, err := s.repo.GetRegionIdentity(ctx, req.RegionId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "region %d not found", req.RegionId)
		}
		return nil, status.Errorf(codes.Internal, "get region identity: %v", err)
	}

	if err := s.authorizeRegionWrite(ctx, region); err != nil {
		return nil, err
	}

	if err := validateRegionName(req.Name); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err := s.repo.RenameRegion(ctx, req.RegionId, req.Name); err != nil {
		return nil, status.Errorf(codes.Internal, "rename region: %v", err)
	}

	s.logger.Info("region renamed", "region_id", req.RegionId, "name", req.Name)
	return &pb.RenameRegionResponse{}, nil
}

// ReparentRegion changes a region's parent (FR3), including to no parent (0
// = top-level). SCD2 close-and-open on region_parent_history plus the
// region.parent_region_id mirror update happen in the repository's single
// transaction (repository.ReparentRegion's doc comment); the region's whole
// subtree moves with it by construction, and no descendant row is touched.
//
// Ordering: existence (NotFound) -> authorization (owner or admin, NFR2) ->
// parent existence (NotFound) -> cycle rejection (FR5, FailedPrecondition)
// -> write. Re-parenting a region under its own current parent (or
// re-parenting a top-level region to top-level) is refused like
// ReassignBoardOwner's reassign-to-current-owner: it would churn the history
// with a zero-length interval without changing anything. Pure Postgres
// write, no device round trip (LB2/NFR1); ownership is untouched by a
// re-parent.
func (s *LeafLabAPIServer) ReparentRegion(ctx context.Context, req *pb.ReparentRegionRequest) (*pb.ReparentRegionResponse, error) {
	region, err := s.repo.GetRegionIdentity(ctx, req.RegionId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "region %d not found", req.RegionId)
		}
		return nil, status.Errorf(codes.Internal, "get region identity: %v", err)
	}

	if err := s.authorizeRegionWrite(ctx, region); err != nil {
		return nil, err
	}

	var newParentRegionID *int64
	if req.ParentRegionId != 0 {
		if _, err := s.repo.GetRegionIdentity(ctx, req.ParentRegionId); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, status.Errorf(codes.NotFound, "parent region %d not found", req.ParentRegionId)
			}
			return nil, status.Errorf(codes.Internal, "get parent region: %v", err)
		}
		newParentRegionID = &req.ParentRegionId
	}

	// FR5: parent = self, or any descendant, is an error rather than a
	// silent cycle. The ancestor walk is a single recursive CTE round trip
	// (repository.ReparentCreatesCycle's doc comment); it also catches the
	// parent = self case, since the walk's seed row is the parent itself.
	if newParentRegionID != nil {
		createsCycle, err := s.repo.ReparentCreatesCycle(ctx, req.RegionId, *newParentRegionID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "check re-parent for cycle: %v", err)
		}
		if createsCycle {
			return nil, status.Errorf(codes.FailedPrecondition,
				"region %d cannot be re-parented under region %d: the new parent is the region itself or one of its descendants",
				req.RegionId, *newParentRegionID)
		}
		if region.ParentRegionID != nil && *region.ParentRegionID == *newParentRegionID {
			return nil, status.Errorf(codes.FailedPrecondition,
				"region %d is already a child of region %d", req.RegionId, *newParentRegionID)
		}
	} else if region.ParentRegionID == nil {
		return nil, status.Error(codes.FailedPrecondition, "region is already top-level")
	}

	if err := s.repo.ReparentRegion(ctx, req.RegionId, newParentRegionID); err != nil {
		return nil, status.Errorf(codes.Internal, "reparent region: %v", err)
	}

	s.logger.Info("region re-parented",
		"region_id", req.RegionId,
		"previous_parent_region_id", region.ParentRegionID,
		"new_parent_region_id", newParentRegionID)
	return &pb.ReparentRegionResponse{}, nil
}
