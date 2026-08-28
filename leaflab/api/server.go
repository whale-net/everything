package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/readings"
	"github.com/whale-net/everything/leaflab/api/tiers"
	"github.com/whale-net/everything/libs/go/grpcauth"
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
	// ListBoards is household-scoped (FR5.1): scope's SQL fragment
	// (Scope.Filter) is applied inside the query itself, never as a
	// post-filter -- see Repository.ListBoards.
	ListBoards(ctx context.Context, afterBoardID int64, hasAfter bool, limit int32, scope authz.Scope) ([]BoardRow, error)
	Ping(ctx context.Context) error
}

// authzResolver is the subset of *authz.PGResolver LeafLabAPIServer's RPCs
// depend on for FR4/FR5/NFR2 authorization: resolving an entity a
// handler was asked about (ResolveBoardByDeviceID today; board/region/
// plant/sensor/reading via authz.Resolver.Resolve once RPCs exist for
// them) and resolving the caller's own Scope from their current
// household_membership rows. Narrowed to an interface, like
// deviceRepository above, so tests substitute a fake.
type authzResolver interface {
	ResolveBoardByDeviceID(ctx context.Context, deviceID string) (authz.EntityRef, authz.Resolution, error)
	ScopeForPrincipal(ctx context.Context, principalSubject string) (authz.Scope, error)
	// Resolve is the generic entity resolution the bounded-read-path RPCs
	// (GetReadingSeries, GetCurrentValues, GetPeriodSummary, CompareSeries)
	// use to authorize a sensor/board/region/plant ref against the
	// caller's Scope in the single query NFR2 requires -- see
	// authorizeEntity. authz.PGResolver already satisfies this (it
	// implements authz.Resolver).
	Resolve(ctx context.Context, ref authz.EntityRef) (authz.Resolution, error)
}

// readingsReader is the subset of *readings.Reader LeafLabAPIServer's
// bounded-read-path RPCs depend on (FR25, FR27, FR28) -- narrowed to an
// interface, like deviceRepository and authzResolver above, so tests
// substitute a fake without a live Postgres connection.
type readingsReader interface {
	Series(ctx context.Context, entity authz.EntityRef, window readings.Window, measurementTypeID int64, requested tiers.Tier, page readings.Page) (readings.SeriesResult, error)
	CurrentValues(ctx context.Context, entity authz.EntityRef) (readings.CurrentValuesResult, error)
	PeriodSummary(ctx context.Context, regionID int64, period readings.Window, measurementTypeID int64) (readings.PeriodSummaryResult, error)
	Compare(ctx context.Context, entities []authz.EntityRef, window readings.Window, measurementTypeID int64, requested tiers.Tier, page readings.Page) (readings.CompareResult, error)
}

type LeafLabAPIServer struct {
	pb.UnimplementedLeafLabAPIServer
	repo      deviceRepository
	authzSvc  authzResolver
	readings  readingsReader
	publisher *rmq.Publisher
	// rmqConn is the underlying RabbitMQ/MQTT connection GetHealth probes
	// (FR63.1). Held separately from publisher because rmq.Publisher does
	// not expose its connection's liveness. May be nil in tests that don't
	// exercise GetHealth -- see GetHealth's nil handling.
	rmqConn *rmq.Connection
	logger  *slog.Logger
}

func NewLeafLabAPIServer(repo deviceRepository, authzSvc authzResolver, readingsSvc readingsReader, publisher *rmq.Publisher, rmqConn *rmq.Connection, logger *slog.Logger) *LeafLabAPIServer {
	return &LeafLabAPIServer{
		repo:      repo,
		authzSvc:  authzSvc,
		readings:  readingsSvc,
		publisher: publisher,
		rmqConn:   rmqConn,
		logger:    logger,
	}
}

// scopeForCaller resolves the authenticated caller's Scope from their
// current household_membership rows (FR75), per the Implementation
// section: "every RPC handler obtains a Scope from the authenticated
// principal's current memberships ... and applies it." Handlers hold the
// returned Scope past this point, never a bare household id (FR4.3).
//
// The nil-Claims branch is defense in depth, not the primary gate: every
// non-anonymous RPC already has Claims guaranteed in context by
// auth.go's enforcement interceptor before a handler runs. If it were
// ever reached anyway, failing closed to an empty authz.UnionScope (which
// Permits nothing and whose Filter matches no row) is the FR4/NFR1
// posture -- never treat "no principal" as "no restriction".
func (s *LeafLabAPIServer) scopeForCaller(ctx context.Context) (authz.Scope, error) {
	claims, ok := grpcauth.ClaimsFromContext(ctx)
	if !ok || claims == nil {
		return authz.NewUnionScope(), nil
	}
	return s.authzSvc.ScopeForPrincipal(ctx, claims.Subject)
}

// boardNotFoundFailure is the one contract.NotFound value returned for a
// board that doesn't exist and for a board that exists but falls outside
// the caller's Scope (NFR2): using the same constructor call site for
// both keeps status, body and reason text byte-identical between the two
// cases, so a caller cannot distinguish "no such board" from "not yours"
// by response shape.
func boardNotFoundFailure() error {
	return contract.NotFound("board", "device_id", "No board matches this device id.")
}

// authorizeBoardAccess resolves the board named by deviceID and checks it
// against the caller's Scope, per NFR2's "resolve the entity and the
// scope in one query" shape: authzSvc.ResolveBoardByDeviceID is the one
// query, scope.Permits is a cheap in-memory check after it returns, and
// both "doesn't exist" and "exists, out of scope" collapse to the same
// boardNotFoundFailure -- never a SELECT-then-branch, and never a
// separate existence probe ahead of the resolve call.
//
// Wired into read paths only today (GetDeviceConfig; ListBoards uses
// Scope.Filter directly, at the repository query, rather than this
// resolve-one-entity path). PushDeviceConfig is deliberately not gated
// here: it upserts a board on first push (self-registration, FR1.1's
// entrance for a never-claimed board), and until FR76's claim RPC exists
// there's no way to refuse "already claimed by another household" without
// turning that upsert into a fresh existence oracle of its own
// (create-succeeds vs refused becomes distinguishable by response shape
// alone). Tracked as a scope note pending FR76 (task #1342).
func (s *LeafLabAPIServer) authorizeBoardAccess(ctx context.Context, deviceID string) error {
	scope, err := s.scopeForCaller(ctx)
	if err != nil {
		s.logger.Error("resolve caller scope failed", "device_id", deviceID, "error", err)
		return contract.Internal("board", "", "Could not process this request right now. Please try again.")
	}

	ref, res, err := s.authzSvc.ResolveBoardByDeviceID(ctx, deviceID)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			return boardNotFoundFailure()
		}
		s.logger.Error("resolve board failed", "device_id", deviceID, "error", err)
		return contract.Internal("board", "", "Could not process this request right now. Please try again.")
	}
	if !scope.Permits(ref, res) {
		return boardNotFoundFailure()
	}
	return nil
}

// entityNotFoundFailure is the generic analogue of boardNotFoundFailure
// (above) for the bounded-read-path RPCs: the same contract.NotFound value
// is returned whether kind's entity doesn't exist or exists outside the
// caller's Scope (NFR2), so the two are indistinguishable by response
// shape or timing.
func entityNotFoundFailure(kind authz.EntityKind) error {
	return contract.NotFound(string(kind), "", "No "+string(kind)+" matches this request.")
}

// authorizeEntity resolves ref against the caller's Scope in the single
// query NFR2 requires (authzSvc.Resolve, then a cheap in-memory
// scope.Permits check), for every entity kind the bounded-read-path RPCs
// accept (sensor, board, region, plant). Both "doesn't exist" and "exists,
// out of scope" collapse to the same entityNotFoundFailure call site,
// mirroring authorizeBoardAccess's shape above.
func (s *LeafLabAPIServer) authorizeEntity(ctx context.Context, ref authz.EntityRef) error {
	scope, err := s.scopeForCaller(ctx)
	if err != nil {
		s.logger.Error("resolve caller scope failed", "entity_kind", ref.Kind, "entity_id", ref.ID, "error", err)
		return contract.Internal(string(ref.Kind), "", "Could not process this request right now. Please try again.")
	}

	res, err := s.authzSvc.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			return entityNotFoundFailure(ref.Kind)
		}
		s.logger.Error("resolve entity failed", "entity_kind", ref.Kind, "entity_id", ref.ID, "error", err)
		return contract.Internal(string(ref.Kind), "", "Could not process this request right now. Please try again.")
	}
	if !scope.Permits(ref, res) {
		return entityNotFoundFailure(ref.Kind)
	}
	return nil
}

// entityRefFromProto extracts a authz.EntityRef from a wire pb.EntityRef's
// oneof (api.proto: "Exactly one field is set; a request with none ...
// is rejected"). A oneof with nothing set (the zero value a caller sends
// by omission) is the only reachable invalid case -- protobuf's oneof
// wire encoding cannot represent more than one field set at once.
func entityRefFromProto(e *pb.EntityRef) (authz.EntityRef, error) {
	switch v := e.GetEntity().(type) {
	case *pb.EntityRef_SensorId:
		return authz.EntityRef{Kind: authz.EntitySensor, ID: v.SensorId}, nil
	case *pb.EntityRef_BoardId:
		return authz.EntityRef{Kind: authz.EntityBoard, ID: v.BoardId}, nil
	case *pb.EntityRef_RegionId:
		return authz.EntityRef{Kind: authz.EntityRegion, ID: v.RegionId}, nil
	case *pb.EntityRef_PlantId:
		return authz.EntityRef{Kind: authz.EntityPlant, ID: v.PlantId}, nil
	default:
		return authz.EntityRef{}, contract.InvalidArgument("entity_ref", "entity", "A sensor, board, region or plant is required.")
	}
}

// windowFromProto converts a wire pb.TimeWindow to readings.Window. A nil
// start or end converts to the zero time.Time, which readings.Window
// treats as "unbounded" and rejects (NFR3.2) -- this function itself never
// rejects anything; the readings package is the one place that validation
// lives.
func windowFromProto(w *pb.TimeWindow) readings.Window {
	return readings.Window{
		Start: contract.FromInstant(w.GetStart()),
		End:   contract.FromInstant(w.GetEnd()),
	}
}

// readingsErrorToFailure maps a leaflab/api/readings sentinel error to the
// FR59.1 structured failure a caller receives -- NFR3.2's "no caller can
// request an unbounded scan" and FR25.3's entity-count/measurement
// requirements are caller-input problems (FailureInvalidArgument), never
// FailureInternal. Any other error (a real DB failure) is logged and
// collapsed to a generic FailureInternal reason (FR59.2) -- the underlying
// error never reaches the wire.
func readingsErrorToFailure(logger *slog.Logger, err error) error {
	switch {
	case errors.Is(err, readings.ErrUnboundedWindow):
		return contract.InvalidArgument("time_window", "", "A start and end time are both required.")
	case errors.Is(err, readings.ErrInvalidWindow):
		return contract.InvalidArgument("time_window", "", "The end time must be after the start time.")
	case errors.Is(err, readings.ErrTooFewEntities):
		return contract.InvalidArgument("entities", "", "At least two entities are required to compare.")
	case errors.Is(err, readings.ErrMeasurementRequired):
		return contract.InvalidArgument("measurement_type_id", "", "A single measurement type is required to compare.")
	case errors.Is(err, readings.ErrUnsupportedEntityKind):
		return contract.InvalidArgument("entity_ref", "entity", "This entity type is not supported for this request.")
	default:
		logger.Error("readings query failed", "error", err)
		return contract.Internal("reading", "", "Could not process this request right now. Please try again.")
	}
}

// GetReadingSeries answers a bounded-window reading series for one entity
// -- sensor, board, region or plant (FR25.1) -- served from the tiers
// (leaflab/api/readings, never v_sensor_reading_with_plant). Every response
// states which granularity tier answered (FR71); a raw-row response is
// capped at NFR3.2's 48-hour window, coarsening automatically beyond it.
func (s *LeafLabAPIServer) GetReadingSeries(ctx context.Context, req *pb.GetReadingSeriesRequest) (*pb.GetReadingSeriesResponse, error) {
	entity, err := entityRefFromProto(req.GetEntity())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeEntity(ctx, entity); err != nil {
		return nil, err
	}

	window := windowFromProto(req.GetWindow())
	requested := contract.FromGranularity(req.GetRequestedGranularity())
	page := readings.Page{Token: req.GetPage().GetPageToken(), Size: req.GetPage().GetPageSize()}

	result, err := s.readings.Series(ctx, entity, window, req.GetMeasurementTypeId(), requested, page)
	if err != nil {
		return nil, readingsErrorToFailure(s.logger, err)
	}

	return &pb.GetReadingSeriesResponse{
		Points:    toReadingPoints(result.Points),
		Tier:      contract.ToGranularity(result.Tier.Tier),
		Coarsened: result.Tier.Coarsened,
		Page:      &pb.PageResponse{NextPageToken: result.NextPageToken},
		ServerNow: contract.Now(),
	}, nil
}

// toReadingPoints converts readings.Point domain values to their wire
// pb.ReadingPoint twin (api.proto's ReadingPoint comment: value_min/
// value_max/value_avg/reading_count are only meaningful for an aggregated
// tier; a raw point already carries value_min == value_max == value_avg ==
// value and reading_count == 1, per readings.Point's own doc comment).
func toReadingPoints(points []readings.Point) []*pb.ReadingPoint {
	out := make([]*pb.ReadingPoint, len(points))
	for i, p := range points {
		out[i] = &pb.ReadingPoint{
			RecordedAt:      contract.ToInstant(p.RecordedAt),
			Value:           p.Value,
			ValueMin:        p.Min,
			ValueMax:        p.Max,
			ValueAvg:        p.Avg,
			ReadingCount:    p.Count,
			BoundaryPartial: p.BoundaryPartial,
		}
	}
	return out
}

// GetCurrentValues answers the current value per sensor and per plant, in
// one call, from the latest raw readings -- never from a pre-aggregated
// tier (FR27). The response's band field is left unpopulated until Phase 5
// (FR58).
func (s *LeafLabAPIServer) GetCurrentValues(ctx context.Context, req *pb.GetCurrentValuesRequest) (*pb.GetCurrentValuesResponse, error) {
	entity, err := entityRefFromProto(req.GetEntity())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeEntity(ctx, entity); err != nil {
		return nil, err
	}

	result, err := s.readings.CurrentValues(ctx, entity)
	if err != nil {
		return nil, readingsErrorToFailure(s.logger, err)
	}

	values := make([]*pb.CurrentValue, len(result.Values))
	for i, v := range result.Values {
		values[i] = toCurrentValue(v)
	}

	plantValues := make([]*pb.CurrentPlantValue, len(result.PlantValues))
	for i, pv := range result.PlantValues {
		sensorValues := make([]*pb.CurrentValue, len(pv.Values))
		for j, v := range pv.Values {
			sensorValues[j] = toCurrentValue(v)
		}
		plantValues[i] = &pb.CurrentPlantValue{PlantId: pv.PlantID, Values: sensorValues}
	}

	return &pb.GetCurrentValuesResponse{
		Values:      values,
		PlantValues: plantValues,
		ServerNow:   contract.Now(),
	}, nil
}

// toCurrentValue converts a readings.CurrentValue domain value to its wire
// pb.CurrentValue twin. Band is left as the zero-value pb.Band (an unset
// field on the wire) until Phase 5 populates FR58 -- see api.proto's Band
// comment.
func toCurrentValue(v readings.CurrentValue) *pb.CurrentValue {
	return &pb.CurrentValue{
		SensorId:          v.SensorID,
		MeasurementTypeId: v.MeasurementTypeID,
		Value:             v.Value,
		RecordedAt:        contract.ToInstant(v.RecordedAt),
	}
}

// GetPeriodSummary answers a server-side min/max/average summary for one
// region over a period (FR28), including the overnight-low/daytime-high
// framing, exact at the hourly tier for min and max (FR71).
func (s *LeafLabAPIServer) GetPeriodSummary(ctx context.Context, req *pb.GetPeriodSummaryRequest) (*pb.GetPeriodSummaryResponse, error) {
	entity := authz.EntityRef{Kind: authz.EntityRegion, ID: req.GetRegionId()}
	if err := s.authorizeEntity(ctx, entity); err != nil {
		return nil, err
	}

	period := windowFromProto(req.GetPeriod())
	result, err := s.readings.PeriodSummary(ctx, req.GetRegionId(), period, req.GetMeasurementTypeId())
	if err != nil {
		return nil, readingsErrorToFailure(s.logger, err)
	}

	summaries := make([]*pb.PeriodSummary, len(result.Summaries))
	for i, sum := range result.Summaries {
		summaries[i] = toPeriodSummary(sum)
	}

	resp := &pb.GetPeriodSummaryResponse{
		Summaries: summaries,
		Timezone:  result.Timezone,
		Tier:      contract.ToGranularity(result.Tier.Tier),
		ServerNow: contract.Now(),
	}
	if result.OvernightLow != nil {
		resp.OvernightLow = toPeriodSummary(*result.OvernightLow)
	}
	if result.DaytimeHigh != nil {
		resp.DaytimeHigh = toPeriodSummary(*result.DaytimeHigh)
	}
	return resp, nil
}

// toPeriodSummary converts a readings.SummaryStat domain value to its wire
// pb.PeriodSummary twin.
func toPeriodSummary(s readings.SummaryStat) *pb.PeriodSummary {
	out := &pb.PeriodSummary{
		MeasurementTypeId: s.MeasurementTypeID,
		Min:               s.Min,
		Max:               s.Max,
		Avg:               s.Avg,
	}
	if !s.MinAt.IsZero() {
		out.MinAt = contract.ToInstant(s.MinAt)
	}
	if !s.MaxAt.IsZero() {
		out.MaxAt = contract.ToInstant(s.MaxAt)
	}
	return out
}

// CompareSeries answers two or more entities compared over one shared
// window and one measurement (FR25.3). Every named entity is authorized
// individually (NFR2) before any series is queried.
func (s *LeafLabAPIServer) CompareSeries(ctx context.Context, req *pb.CompareSeriesRequest) (*pb.CompareSeriesResponse, error) {
	entities := make([]authz.EntityRef, len(req.GetEntities()))
	for i, e := range req.GetEntities() {
		ref, err := entityRefFromProto(e)
		if err != nil {
			return nil, err
		}
		entities[i] = ref
	}
	for _, ref := range entities {
		if err := s.authorizeEntity(ctx, ref); err != nil {
			return nil, err
		}
	}

	window := windowFromProto(req.GetWindow())
	requested := contract.FromGranularity(req.GetRequestedGranularity())
	page := readings.Page{Token: req.GetPage().GetPageToken(), Size: req.GetPage().GetPageSize()}

	result, err := s.readings.Compare(ctx, entities, window, req.GetMeasurementTypeId(), requested, page)
	if err != nil {
		return nil, readingsErrorToFailure(s.logger, err)
	}

	series := make([]*pb.EntitySeries, len(result.Series))
	for i, es := range result.Series {
		series[i] = &pb.EntitySeries{
			Entity: entityRefToProto(es.Entity),
			Points: toReadingPoints(es.Points),
		}
	}

	return &pb.CompareSeriesResponse{
		Series:    series,
		Tier:      contract.ToGranularity(result.Tier.Tier),
		Coarsened: result.Tier.Coarsened,
		Page:      &pb.PageResponse{NextPageToken: result.NextPageToken},
		ServerNow: contract.Now(),
	}, nil
}

// entityRefToProto is entityRefFromProto's inverse, for echoing an
// authorized entity back on the wire (CompareSeriesResponse.EntitySeries).
func entityRefToProto(ref authz.EntityRef) *pb.EntityRef {
	switch ref.Kind {
	case authz.EntitySensor:
		return &pb.EntityRef{Entity: &pb.EntityRef_SensorId{SensorId: ref.ID}}
	case authz.EntityBoard:
		return &pb.EntityRef{Entity: &pb.EntityRef_BoardId{BoardId: ref.ID}}
	case authz.EntityRegion:
		return &pb.EntityRef{Entity: &pb.EntityRef_RegionId{RegionId: ref.ID}}
	case authz.EntityPlant:
		return &pb.EntityRef{Entity: &pb.EntityRef_PlantId{PlantId: ref.ID}}
	default:
		return &pb.EntityRef{}
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

	// FR4.1: a caller outside this board's household reach gets the exact
	// same refusal a nonexistent device_id would (NFR2) -- see
	// authorizeBoardAccess.
	if err := s.authorizeBoardAccess(ctx, req.DeviceId); err != nil {
		return nil, err
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
	// FR5.1: ListBoards is household-scoped by default -- this Scope is
	// applied inside the repository query (Scope.Filter), never as a
	// post-filter. A caller in no household gets an empty list (below),
	// not an error and not everything (FR4.3 -- this handler never widens
	// on its own; only a caller who supplies a wider Scope, e.g. a future
	// admin lane, would).
	scope, err := s.scopeForCaller(ctx)
	if err != nil {
		s.logger.Error("resolve caller scope failed", "error", err)
		return nil, contract.Internal("board", "", "Could not list boards right now. Please try again.")
	}

	afterBoardID, hasAfter, err := contract.DecodeBoardCursor(req.GetPage().GetPageToken())
	if err != nil {
		return nil, contract.InvalidArgument("list_boards_request", "page_token", "This page link is no longer valid. Start again from the first page.")
	}

	limit := contract.ClampPageSize(req.GetPage().GetPageSize())

	// Fetch one extra row to detect whether a next page exists without a
	// separate COUNT query.
	rows, err := s.repo.ListBoards(ctx, afterBoardID, hasAfter, limit+1, scope)
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
