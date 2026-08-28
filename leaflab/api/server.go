package main

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/rmq"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// validDeviceID allows alphanumeric, hyphens, and underscores.
// Excludes MQTT wildcard characters (+, #) and path separators (/, .).
var validDeviceID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// validateDeviceID returns a persona-appropriate reason (FR59.2) if id is
// invalid, or "" if it's valid. The proto field name is never embedded in
// the reason text -- the caller attaches it separately as Failure.field.
func validateDeviceID(id string) string {
	if id == "" {
		return "A device ID is required."
	}
	if !validDeviceID.MatchString(id) {
		return "This device ID contains invalid characters; only letters, numbers, hyphens and underscores are allowed."
	}
	return ""
}

const mqttExchange = "amq.topic"

// deviceRepository is the subset of *Repository's methods LeafLabAPIServer's
// RPCs depend on. Extracted as an interface -- rather than referencing
// *Repository directly -- so tests can substitute an in-memory fake and
// exercise real handler logic (e.g. GetHealth's DEGRADED branch) without a
// live Postgres connection; see server_test.go. *Repository satisfies this
// with no changes on the production path (NewLeafLabAPIServer is still
// called with *Repository in main.go).
type deviceRepository interface {
	GetOrCreateBoard(ctx context.Context, deviceID string) (int64, error)
	InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte) (int64, error)
	GetLatestAcceptedConfig(ctx context.Context, deviceID string) (*configpb.DeviceConfig, error)
	ListBoards(ctx context.Context, afterBoardID int64, hasAfter bool, limit int32) ([]BoardRow, error)
	Ping(ctx context.Context) error
}

type LeafLabAPIServer struct {
	pb.UnimplementedLeafLabAPIServer
	repo      deviceRepository
	publisher *rmq.Publisher
	// rmqConn is the underlying RabbitMQ/MQTT connection GetHealth probes
	// (FR63.1). Held separately from publisher because rmq.Publisher does
	// not expose its connection's liveness. May be nil in tests that don't
	// exercise GetHealth -- see GetHealth's nil handling.
	rmqConn *rmq.Connection
	logger  *slog.Logger
}

func NewLeafLabAPIServer(repo deviceRepository, publisher *rmq.Publisher, rmqConn *rmq.Connection, logger *slog.Logger) *LeafLabAPIServer {
	return &LeafLabAPIServer{
		repo:      repo,
		publisher: publisher,
		rmqConn:   rmqConn,
		logger:    logger,
	}
}

func (s *LeafLabAPIServer) PushDeviceConfig(ctx context.Context, req *pb.PushDeviceConfigRequest) (*pb.PushDeviceConfigResponse, error) {
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("device_config", "device_id", reason)
	}

	boardID, err := s.repo.GetOrCreateBoard(ctx, req.DeviceId)
	if err != nil {
		s.logger.Error("board lookup failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("board", "", "Could not process this request right now. Please try again.")
	}

	// Build the proto with a placeholder version; we need configJSON for the
	// atomic insert that returns the real version, so marshal without version first.
	cfgProto := &configpb.DeviceConfig{
		DeviceId: req.DeviceId,
		Sensors:  req.Sensors,
	}
	configJSON, err := protojson.Marshal(cfgProto)
	if err != nil {
		s.logger.Error("protojson marshal failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}

	// Atomically assign version and record the pending push before publishing.
	// This ensures the DB row always exists before the device can ack.
	version, err := s.repo.InsertDeviceConfigNextVersion(ctx, boardID, configJSON)
	if err != nil {
		s.logger.Error("record config push failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Could not record this config push right now. Please try again.")
	}

	// Re-marshal with the real version for the wire payload.
	cfgProto.Version = uint64(version)
	wire, err := proto.Marshal(cfgProto)
	if err != nil {
		s.logger.Error("proto marshal failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}

	// MQTT '/' → AMQP '.'; device_id should not contain '/' but sanitize to be safe.
	routingKey := fmt.Sprintf("leaflab.%s.config", strings.ReplaceAll(req.DeviceId, "/", "."))
	if err := s.publisher.Publish(ctx, mqttExchange, routingKey, wire); err != nil {
		// Row is in DB but publish failed — device never received the push.
		// The row stays accepted=FALSE, which is correct: no ack will arrive.
		s.logger.Error("publish config failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Config was recorded but could not be delivered to the device. Please try pushing again.")
	}

	s.logger.Info("device config pushed",
		"device_id", req.DeviceId,
		"version", version,
		"sensors", len(req.Sensors))

	return &pb.PushDeviceConfigResponse{Version: uint64(version)}, nil
}

func (s *LeafLabAPIServer) GetDeviceConfig(ctx context.Context, req *pb.GetDeviceConfigRequest) (*pb.GetDeviceConfigResponse, error) {
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("device_config", "device_id", reason)
	}

	cfg, err := s.repo.GetLatestAcceptedConfig(ctx, req.DeviceId)
	if err != nil {
		s.logger.Error("get config failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Could not look up this device's config right now. Please try again.")
	}
	if cfg == nil {
		return &pb.GetDeviceConfigResponse{Found: false}, nil
	}
	return &pb.GetDeviceConfigResponse{Config: cfg, Found: true}, nil
}

// ListBoards returns all known boards, keyset-paginated on (board_id) per
// FR61: page_token is opaque and carries the last board_id of the previous
// page, never an offset, so pagination stays correct while boards are
// inserted mid-scan. page_size above contract.PageCap is clamped, not
// rejected. Every board carries an absolute last_seen_at Instant alongside
// the response envelope's server_now, so a caller renders elapsed time
// without trusting its own clock (FR64).
func (s *LeafLabAPIServer) ListBoards(ctx context.Context, req *pb.ListBoardsRequest) (*pb.ListBoardsResponse, error) {
	afterBoardID, hasAfter, err := contract.DecodeBoardCursor(req.GetPage().GetPageToken())
	if err != nil {
		return nil, contract.InvalidArgument("list_boards_request", "page_token", "This page link is no longer valid. Start again from the first page.")
	}

	limit := contract.ClampPageSize(req.GetPage().GetPageSize())

	// Fetch one extra row to detect whether a next page exists without a
	// separate COUNT query.
	rows, err := s.repo.ListBoards(ctx, afterBoardID, hasAfter, limit+1)
	if err != nil {
		s.logger.Error("list boards failed", "error", err)
		return nil, contract.Internal("board", "", "Could not list boards right now. Please try again.")
	}

	var nextToken string
	if int32(len(rows)) > limit {
		rows = rows[:limit]
		nextToken = contract.EncodeBoardCursor(rows[len(rows)-1].BoardID)
	}

	boards := make([]*pb.BoardInfo, 0, len(rows))
	for _, r := range rows {
		boards = append(boards, &pb.BoardInfo{
			DeviceId:   r.DeviceID,
			BoardId:    r.BoardID,
			LastSeenAt: contract.ToInstant(r.LastSeenAt),
		})
	}
	return &pb.ListBoardsResponse{
		Boards:    boards,
		Page:      &pb.PageResponse{NextPageToken: nextToken},
		ServerNow: contract.Now(),
	}, nil
}

// GetHealth is the only anonymous RPC in this service (FR63). It reports
// exactly one field -- up or degraded -- and nothing else: no version, no
// dependency names, no per-dependency detail (FR63.2). It probes the pgx
// pool and the RabbitMQ/MQTT connection (FR63.1) but never says which one
// failed, on the response or in an error -- only in the server-side log
// line, for operator debugging.
func (s *LeafLabAPIServer) GetHealth(ctx context.Context, req *pb.GetHealthRequest) (*pb.GetHealthResponse, error) {
	dbErr := s.repo.Ping(ctx)
	if dbErr != nil {
		s.logger.Warn("health check: database unreachable", "error", dbErr)
	}

	mqUp := s.rmqConn != nil && s.rmqConn.GetConnection() != nil && !s.rmqConn.GetConnection().IsClosed()
	if !mqUp {
		s.logger.Warn("health check: rabbitmq/mqtt connection unavailable")
	}

	if dbErr != nil || !mqUp {
		return &pb.GetHealthResponse{Status: pb.HealthStatus_HEALTH_DEGRADED}, nil
	}
	return &pb.GetHealthResponse{Status: pb.HealthStatus_HEALTH_UP}, nil
}
