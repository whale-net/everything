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
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("device_config", "device_id", reason)
	}

	boardID, err := s.repo.GetOrCreateBoard(ctx, req.DeviceId)
	if err != nil {
		s.logger.Error("board lookup failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("board", "", "Could not process this request right now. Please try again.")
	}

	// FR17 pre-write identity check: refuses before anything is written or
	// published if any entry would establish a new sensor identity rather
	// than continue an existing one (or would require an unresolved swap,
	// FR16.4). This is the real push path, not a dry run -- see
	// checkPushConfigIdentity's doc comment.
	if err := s.checkPushConfigIdentity(ctx, boardID, req.Sensors); err != nil {
		return nil, err
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

// RewireSensor is the explicit API rewire path (FR16): it declares that
// the sensor currently named req.Name on req.DeviceId has moved to a new
// hardware location, updating it in place rather than establishing a new
// sensor identity. See leaflab/api/proto/api.proto's RewireSensor doc
// comment and Repository.FindSensorIDByName/FindSensorIDByHWKey.
//
// FR16 case 2 / FR17: the existing sensor is resolved by (board_id,
// req.Name) -- req.Name is the stable anchor, per the proto doc comment.
// If none exists, this is refused (FR17) before writing anything rather
// than silently creating a new sensor identity: there is no rewire
// alternative to name here (this RPC *is* the rewire path), so the
// refusal instead explains there is nothing existing to rewire.
// Otherwise the resolved sensor's hardware key is updated in place and a
// sensor_hw_history interval closed/opened, atomically
// (Repository.RewireSensorHW) -- sensor_id, and everything keyed on it
// (readings, name history, region history), is unchanged by construction.
func (s *LeafLabAPIServer) RewireSensor(ctx context.Context, req *pb.RewireSensorRequest) (*pb.RewireSensorResponse, error) {
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("rewire_sensor", "device_id", reason)
	}
	if req.Name == "" {
		return nil, contract.InvalidArgument("rewire_sensor", "name", "A sensor name is required.")
	}

	boardID, err := s.repo.GetOrCreateBoard(ctx, req.DeviceId)
	if err != nil {
		s.logger.Error("board lookup failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("board", "", "Could not process this request right now. Please try again.")
	}

	sensorID, found, err := s.repo.FindSensorIDByName(ctx, boardID, req.Name)
	if err != nil {
		s.logger.Error("find sensor by name failed", "device_id", req.DeviceId, "name", req.Name, "error", err)
		return nil, contract.Internal("rewire_sensor", "", "Could not process this request right now. Please try again.")
	}
	if !found {
		// FR17: applying this would establish a new sensor identity, not
		// continue one, and its reading history would not follow. Refuse
		// before writing anything.
		return nil, contract.Refuse(
			"rewire_sensor",
			"name",
			fmt.Sprintf("No sensor named %q exists on this device; rewiring it would create a new sensor identity, and its reading history would not follow.", req.Name),
			"Wait for the device to report this sensor in a manifest, which registers it as a new sensor; there is no existing sensor here to rewire.",
		)
	}

	hw := HardwareAddressFromSensorConfig(req.MuxPath, req.I2CAddress)
	if err := s.repo.RewireSensorHW(ctx, sensorID, hw); err != nil {
		s.logger.Error("rewire sensor failed", "device_id", req.DeviceId, "name", req.Name, "sensor_id", sensorID, "error", err)
		return nil, contract.Internal("rewire_sensor", "", "Could not rewire this sensor right now. Please try again.")
	}

	s.logger.Info("sensor rewired", "device_id", req.DeviceId, "name", req.Name, "sensor_id", sensorID)
	return &pb.RewireSensorResponse{SensorId: sensorID}, nil
}

// GetSensorTimelines returns a sensor's three independent history
// timelines -- name, hardware, region (FR53) -- each paginated on its own
// (FR61) and sharing the Interval shape so a caller can lay them side by
// side on one time axis. See leaflab/api/proto/api.proto's
// GetSensorTimelinesResponse doc comment: the three are never merged
// server-side into one combined list.
//
// TODO(Implementation phase, FR53):
//   - Query sensor_name_history, sensor_hw_history, sensor_region_history
//     independently (three separate SELECTs, three separate keyset
//     cursors mirroring contract.EncodeReadingCursor's pattern) -- never
//     join them into one merged timeline.
//   - Render each sensor_hw_history row's i2c_address as absent (not 0)
//     when NULL (pre-migration-013 closed interval, FR16.2/FR18.2); use
//     //leaflab/hwkey's AddressOpt so the absent/present-including-0
//     distinction is canonical, not reinvented here.
//   - Carry the sensor's sensor_type (from the sensor row, FR16.1) on
//     every HardwareInterval.
//   - Apply window_start/window_end as an overlap filter, not an exact
//     match, when either is set.
//   - Household-scope the sensor lookup (FR5); require elevation for
//     cross-household admin access (FR10.3); a non-member (including a
//     nonexistent sensor_id) gets NFR2's not-found, indistinguishable
//     from a not-found for a household member's own missing sensor.
//   - Return an empty (not error) timeline for a sensor with no rows in
//     one or more of the three tables (e.g. never placed in a region).
//   - A sensor dropped from a board's desired state (FR82.3, Phase 4)
//     must still return all three timelines unchanged -- this RPC has no
//     "desired state" concept to filter on, so this should already hold;
//     Testing adds a stubbed-"dropped" assertion now so Phase 4 inherits
//     a passing test.
func (s *LeafLabAPIServer) GetSensorTimelines(ctx context.Context, req *pb.GetSensorTimelinesRequest) (*pb.GetSensorTimelinesResponse, error) {
	if req.SensorId <= 0 {
		return nil, contract.InvalidArgument("sensor_timelines", "sensor_id", "A sensor ID is required.")
	}

	return nil, contract.Internal("sensor_timelines", "", "Not yet implemented.")
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
