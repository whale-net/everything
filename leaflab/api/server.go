package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
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

// repositoryStore is the subset of *Repository's methods LeafLabAPIServer
// calls, extracted so tests can substitute an in-memory fake (no Postgres)
// while production code keeps passing the real *Repository straight
// through -- *Repository already satisfies this interface, no adapter
// needed. GetBoardIDForSensor is deliberately excluded: no handler in this
// file calls it yet (it exists for the RenameSensor task).
type repositoryStore interface {
	GetLeafLabUserIDBySub(ctx context.Context, oidcSub string) (int64, bool, error)
	GetCurrentBoardOwner(ctx context.Context, boardID int64) (int64, bool, error)
	GetBoardIDForDeviceID(ctx context.Context, deviceID string) (int64, bool, error)
	InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte) (int64, error)
	GetLatestAcceptedConfig(ctx context.Context, deviceID string) (*configpb.DeviceConfig, error)
	ListSensorInventoryForBoard(ctx context.Context, boardID int64) ([]InventorySensor, error)
	ListBoards(ctx context.Context) ([]BoardRow, error)
	ListBoardsWithState(ctx context.Context) ([]BoardWithReadingRow, error)
	GetBoardIdentity(ctx context.Context, boardID int64) (BoardIdentity, error)
	ListSensorDetailsForBoard(ctx context.Context, boardID int64) ([]SensorDetailRow, error)
	SensorExists(ctx context.Context, sensorID int64) (bool, error)
	GetSensorReadingHistory(ctx context.Context, sensorID int64, from, to time.Time) (*SensorReadingHistory, error)
	// ClaimBoard is ClaimBoard's own write path (FR1, FR2, NFR2) -- see
	// repository.go's doc comment for the race-safety argument.
	ClaimBoard(ctx context.Context, boardID, leaflabUserID int64) error
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
	composedSensors := ComposeDesiredSensors(inventory, lastAcceptedSensors, req.Sensors)

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

	s.logger.Info("boards with state listed", "count", len(boards))
	return &pb.ListBoardsWithStateResponse{Boards: boards}, nil
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

// RenameBoard is implemented by the RenameBoard task (FR3).
func (s *LeafLabAPIServer) RenameBoard(ctx context.Context, req *pb.RenameBoardRequest) (*pb.RenameBoardResponse, error) {
	return nil, status.Error(codes.Unimplemented, "RenameBoard: not implemented")
}

// RenameSensor is implemented by the RenameSensor task (FR4).
func (s *LeafLabAPIServer) RenameSensor(ctx context.Context, req *pb.RenameSensorRequest) (*pb.RenameSensorResponse, error) {
	return nil, status.Error(codes.Unimplemented, "RenameSensor: not implemented")
}

// ListOwnedBoards is implemented by the admin ownership list task (FR11).
func (s *LeafLabAPIServer) ListOwnedBoards(ctx context.Context, req *pb.ListOwnedBoardsRequest) (*pb.ListOwnedBoardsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListOwnedBoards: not implemented")
}

// ReassignBoardOwner is implemented by the admin reassign task (FR12).
func (s *LeafLabAPIServer) ReassignBoardOwner(ctx context.Context, req *pb.ReassignBoardOwnerRequest) (*pb.ReassignBoardOwnerResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ReassignBoardOwner: not implemented")
}

// ClearBoardOwner is implemented by the admin clear-owner task (FR13).
func (s *LeafLabAPIServer) ClearBoardOwner(ctx context.Context, req *pb.ClearBoardOwnerRequest) (*pb.ClearBoardOwnerResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ClearBoardOwner: not implemented")
}

// ListUsers is implemented by the admin ownership tasks (FR11, FR12).
func (s *LeafLabAPIServer) ListUsers(ctx context.Context, req *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListUsers: not implemented")
}
