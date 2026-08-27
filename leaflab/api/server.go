package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/config"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/hwkey"
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
	// InsertDeviceConfigNextVersion also records entries' FR82.4 per-entry
	// provenance into device_config_entry, and FR82.6's dropped-entry
	// bookkeeping into device_config_removal, in the same transaction as
	// the device_config row -- see Repository.InsertDeviceConfigNextVersion's
	// doc comment.
	InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte, entries []config.Entry, removed []config.RemovedEntry, entry audit.Entry) (int64, error)
	GetLatestAcceptedConfig(ctx context.Context, deviceID string) (*configpb.DeviceConfig, error)
	// GetRegionApplySkips is FR1.3's caller-visible skip surface: the
	// audit.Entry rows leaflab/processor's ApplyConfigRegions wrote for
	// this board's skipped config entries (household drift or a stale
	// push), most recent first. See Repository.GetRegionApplySkips.
	GetRegionApplySkips(ctx context.Context, deviceID string) ([]RegionApplySkipRow, error)
	// ListBoards is household-scoped (FR5.1): scope's SQL fragment
	// (Scope.Filter) is applied inside the query itself, never as a
	// post-filter -- see Repository.ListBoards.
	ListBoards(ctx context.Context, afterBoardID int64, hasAfter bool, limit int32, scope authz.Scope) ([]BoardRow, error)
	// GetDeviceConfigVersion/GetConfigVersionEntries/ListConfigHistory back
	// FR34's GetConfigStatus/GetConfigVersion and FR35's
	// ListConfigHistory/GetConfigVersion respectively -- see
	// Repository's methods of the same names.
	GetDeviceConfigVersion(ctx context.Context, deviceID string, version int64) (*DeviceConfigVersionRow, error)
	GetConfigVersionEntries(ctx context.Context, configID int64) ([]ConfigVersionEntryRow, error)
	ListConfigHistory(ctx context.Context, deviceID string, beforeVersion int64, hasBefore bool, limit int32) ([]DeviceConfigHistoryRow, error)
	Ping(ctx context.Context) error
	// FR16/FR16.3/FR16.4/FR17 sensor identity resolution -- see identity.go
	// and Repository's implementations.
	FindSensorIDByName(ctx context.Context, boardID int64, name string) (int64, bool, error)
	resolveSensorTypeID(ctx context.Context, typeName string) (int64, bool, error)
	LoadBoardSensorIdentities(ctx context.Context, boardID int64) ([]BoardSensorIdentity, error)
	RewireSensorHW(ctx context.Context, sensorID int64, hw *HardwareAddress) error
}

// authzResolver is the subset of *authz.PGResolver LeafLabAPIServer's RPCs
// depend on for FR4/FR5/NFR2 authorization: resolving an entity a
// handler was asked about (ResolveBoardByDeviceID today; board/region/
// plant/sensor/reading via authz.Resolver.Resolve once RPCs exist for
// them) and resolving the caller's own Scope from their current
// household_membership rows. Narrowed to an interface, like
// deviceRepository above, so tests substitute a fake.
//
// It now also embeds authz.Resolver directly (Resolve), which
// PushDeviceConfig's FR1.2/FR1.3 push-time invariant check needs: resolving
// the pushing board's own household, and each region_id named in the
// payload, through authz.AssertSameHousehold (invariant.go).
type authzResolver interface {
	authz.Resolver
	ResolveBoardByDeviceID(ctx context.Context, deviceID string) (authz.EntityRef, authz.Resolution, error)
	ScopeForPrincipal(ctx context.Context, principalSubject string) (authz.Scope, error)
}

type LeafLabAPIServer struct {
	pb.UnimplementedLeafLabAPIServer
	repo      deviceRepository
	authzSvc  authzResolver
	publisher *rmq.Publisher
	// rmqConn is the underlying RabbitMQ/MQTT connection GetHealth probes
	// (FR63.1). Held separately from publisher because rmq.Publisher does
	// not expose its connection's liveness. May be nil in tests that don't
	// exercise GetHealth -- see GetHealth's nil handling.
	rmqConn *rmq.Connection
	logger  *slog.Logger
}

func NewLeafLabAPIServer(repo deviceRepository, authzSvc authzResolver, publisher *rmq.Publisher, rmqConn *rmq.Connection, logger *slog.Logger) *LeafLabAPIServer {
	return &LeafLabAPIServer{
		repo:      repo,
		authzSvc:  authzSvc,
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

func (s *LeafLabAPIServer) PushDeviceConfig(ctx context.Context, req *pb.PushDeviceConfigRequest) (*pb.PushDeviceConfigResponse, error) {
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("device_config", "device_id", reason)
	}

	// FR82.1: scope is required, with no default and never inferred from
	// payload shape/size/caller/endpoint. Checked before any board
	// bookkeeping or write -- an omitted scope leaves nothing stored,
	// nothing published, and no version assigned (GetOrCreateBoard's
	// self-registration upsert hasn't even run yet at this point).
	if req.Scope == pb.PushScope_PUSH_SCOPE_UNSPECIFIED {
		return nil, contract.InvalidArgument(
			"device_config",
			"scope",
			"A config push must state scope=COMPLETE or scope=EDIT; there is no default.",
		)
	}
	if req.Scope == pb.PushScope_PUSH_SCOPE_COMPLETE && len(req.Removes) > 0 {
		return nil, contract.InvalidArgument(
			"device_config",
			"removes",
			"removes is only used with scope=EDIT; a scope=COMPLETE push removes an entry by omitting it from sensors instead.",
		)
	}

	boardID, err := s.repo.GetOrCreateBoard(ctx, req.DeviceId)
	if err != nil {
		s.logger.Error("board lookup failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("board", "", "Could not process this request right now. Please try again.")
	}

	// FR1.2/FR1.3: resolve the pushing board's own household once, before
	// anything is stored or published -- used both to validate every
	// region_id named in the payload (validatePushRegions, below) and to
	// populate this push's audit entry's TargetHouseholdID (previously
	// left nil pending #1339's household scoping, which has since landed).
	boardRes, err := s.authzSvc.Resolve(ctx, authz.EntityRef{Kind: authz.EntityBoard, ID: boardID})
	if err != nil {
		s.logger.Error("resolve board household failed", "device_id", req.DeviceId, "board_id", boardID, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	writerHousehold := boardRes.HouseholdID
	var targetHouseholdID *int64
	if boardRes.Unclaimed {
		// FR1.1: this board has no household yet -- no region_id in the
		// payload can ever satisfy AssertSameHousehold against it (see
		// validatePushRegions), and the audit entry below records no
		// single target household (TargetHouseholdID stays nil).
		writerHousehold = 0
	} else {
		targetHouseholdID = &boardRes.HouseholdID
	}

	// FR1.3: reject the whole push -- no device_config row stored, nothing
	// published -- naming the offending entry and field, before either of
	// those writes below. GetOrCreateBoard's self-registration upsert
	// above is unaffected either way: it is pre-existing, idempotent
	// board-identity bookkeeping this validation itself depends on to
	// know which household to check against (it must run first to
	// produce boardID), not a config write -- "nothing stored" is about
	// device_config, the thing FR1.3 actually governs.
	if err := s.validatePushRegions(ctx, writerHousehold, req.Sensors); err != nil {
		return nil, err
	}

	// FR17 pre-write identity check: refuses before anything is written or
	// published if any entry would establish a new sensor identity rather
	// than continue an existing one (or would require an unresolved swap,
	// FR16.4). This is the real push path, not a dry run -- see
	// checkPushConfigIdentity's doc comment.
	if err := s.checkPushConfigIdentity(ctx, boardID, req.Sensors); err != nil {
		return nil, err
	}

	// FR82: resolve this push's complete entry set and per-entry provenance,
	// branching on scope. req.Sensors is always the caller's own
	// add/change list (COMPLETE: the whole desired set; EDIT: only what's
	// named) -- validatePushRegions/checkPushConfigIdentity above
	// deliberately only ever see this authored list, never a materialised
	// carry-forward entry (those were already checked when they were
	// themselves accepted).
	adds, err := s.resolveConfigEntries(ctx, req.Sensors)
	if err != nil {
		return nil, err
	}

	var entries []config.Entry
	sensorsForStorage := req.Sensors
	var removedForResponse []*pb.RemovedEntry
	var removedEntries []config.RemovedEntry

	switch req.Scope {
	case pb.PushScope_PUSH_SCOPE_COMPLETE:
		// FR82.2: the payload is the board's entire desired sensor set,
		// stored as submitted -- every entry is authored, and there is no
		// base to materialise against.
		entries = adds
		for i := range entries {
			entries[i].Provenance = config.ProvenanceAuthored
		}

	case pb.PushScope_PUSH_SCOPE_EDIT:
		baseCfg, err := s.repo.GetLatestAcceptedConfig(ctx, req.DeviceId)
		if err != nil {
			s.logger.Error("get latest accepted config for edit failed", "device_id", req.DeviceId, "error", err)
			return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
		}
		// base stays nil (not an empty, non-nil slice) when baseCfg == nil
		// -- config.Materialise's documented signal for "no accepted config
		// exists for this board at all" (FR82.3), distinct from a
		// genuinely-empty accepted config (baseCfg != nil, zero sensors).
		var base []config.Entry
		if baseCfg != nil {
			base, err = s.resolveConfigEntries(ctx, baseCfg.Sensors)
			if err != nil {
				return nil, err
			}
		}

		removeKeys, err := s.resolveRemoveKeys(ctx, req.Removes)
		if err != nil {
			return nil, err
		}

		result, err := config.Materialise(base, adds, removeKeys)
		if err != nil {
			switch {
			case errors.Is(err, config.ErrNoAcceptedConfig):
				// FR82.3's exact stated refusal -- never a generic
				// validation failure.
				return nil, contract.Refuse(
					"device_config",
					"scope",
					"This board has no accepted config to complete your edit from; send a complete push.",
					"Push scope=COMPLETE with this device's entire desired sensor set.",
				)
			case errors.Is(err, config.ErrUnaddressableRemove):
				// FR82.4/FR39: a remove named an entry with no I2C address
				// on record (the legacy "unknown address" sentinel) --
				// stated reason and remedy, not a silent no-op.
				return nil, contract.Refuse(
					"device_config",
					"removes",
					"This entry has no I2C address on record and cannot be removed by an edit push.",
					"Push scope=COMPLETE with this entry omitted from the sensors list.",
				)
			default:
				s.logger.Error("materialise edit push failed", "device_id", req.DeviceId, "error", err)
				return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
			}
		}

		entries = result.Entries
		removedEntries = result.Removed
		sensorsForStorage = make([]*configpb.SensorConfig, len(entries))
		for i, e := range entries {
			sensorsForStorage[i] = e.Sensor
		}
		// FR82.4: state back to the caller which removal form named each
		// dropped entry -- config.Materialise already computed this
		// (RemovedEntry.Form); this is the one place that translates it
		// onto the wire.
		removedForResponse = make([]*pb.RemovedEntry, len(result.Removed))
		for i, re := range result.Removed {
			removedForResponse[i] = &pb.RemovedEntry{
				MuxPath:    re.Entry.Sensor.GetMuxPath(),
				I2CAddress: re.Entry.Sensor.GetI2CAddress(),
				SensorType: re.Entry.Sensor.GetSensorType(),
				Form:       removeFormToProto(re.Form),
			}
		}
		s.logger.Info("edit push materialised",
			"device_id", req.DeviceId,
			"authored", len(adds),
			"removed", len(result.Removed),
			"total", len(entries))
	}

	// Build the proto with a placeholder version; we need configJSON for the
	// atomic insert that returns the real version, so marshal without version first.
	cfgProto := &configpb.DeviceConfig{
		DeviceId: req.DeviceId,
		Sensors:  sensorsForStorage,
	}
	configJSON, err := protojson.Marshal(cfgProto)
	if err != nil {
		s.logger.Error("protojson marshal failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}

	// Atomically assign version and record the pending push before publishing.
	// This ensures the DB row always exists before the device can ack.
	//
	// Action/EntityKind come from auditRegistrations (audit_registry.go)
	// rather than being repeated as literals here, so the two can't drift
	// out of agreement.
	reg := auditRegistrations[pushDeviceConfigFullMethod]
	entry := audit.Entry{
		ActorSubject:      actingSubject(ctx),
		ActorKind:         audit.ActorKindHuman,
		TargetHouseholdID: targetHouseholdID,
		Action:            reg.Action,
		EntityKind:        reg.EntityKind,
		CorrelationID:     CorrelationIDFromContext(ctx),
	}
	version, err := s.repo.InsertDeviceConfigNextVersion(ctx, boardID, configJSON, entries, removedEntries, entry)
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

	return &pb.PushDeviceConfigResponse{Version: uint64(version), Removed: removedForResponse}, nil
}

// removeFormToProto translates config.RemoveForm (leaflab/api/config's
// pure-logic value) onto the wire pb.RemoveForm FR82.4's response states
// back to the caller.
func removeFormToProto(f config.RemoveForm) pb.RemoveForm {
	switch f {
	case config.RemoveFormFullKey:
		return pb.RemoveForm_REMOVE_FORM_FULL_KEY
	case config.RemoveFormChipKey:
		return pb.RemoveForm_REMOVE_FORM_CHIP_KEY
	default:
		return pb.RemoveForm_REMOVE_FORM_UNSPECIFIED
	}
}

// validatePushRegions is FR1.2/FR1.3's push-time layer: every region_id
// named in sensors must resolve to writerHousehold, the pushing board's
// own household -- checked here, before PushDeviceConfig stores or
// publishes anything (FR1.3's "no partial application": one bad entry
// refuses the whole payload, not just that entry).
//
// This closes the defect this task fixes: previously ApplyConfigRegions
// (leaflab/processor/repository.go) wrote sensor.region_id with no
// ownership check at all, letting an authenticated owner point their own
// sensor at another household's region.
//
// writerHousehold is a sentinel no real household id can ever equal
// (household ids are BIGSERIAL, so real values are >= 1) when the pushing
// board itself has no household yet (FR1.1's Unclaimed exception) -- every
// region_id then fails AssertSameHousehold without a special case here.
//
// Delegates to authz.AssertSameHousehold (FR1.2's write invariant,
// invariant.go) rather than reimplementing per-reference resolution --
// this is the "every write RPC that would leave a live reference in
// place ... calls this once per reference it is about to write" call site
// AssertSameHousehold's own doc comment names.
func (s *LeafLabAPIServer) validatePushRegions(ctx context.Context, writerHousehold int64, sensors []*configpb.SensorConfig) error {
	var refs []authz.LiveRef
	for i, sc := range sensors {
		if sc.RegionId == 0 {
			continue
		}
		refs = append(refs, authz.LiveRef{
			EntityRef: authz.EntityRef{Kind: authz.EntityRegion, ID: int64(sc.RegionId)},
			Field:     fmt.Sprintf("sensors[%d].region_id", i),
		})
	}
	if len(refs) == 0 {
		return nil
	}

	err := authz.AssertSameHousehold(ctx, s.authzSvc, writerHousehold, refs...)
	if err == nil {
		return nil
	}
	if _, ok := contract.FromError(err); ok {
		// AssertSameHousehold's own FailureInvalidArgument, already naming
		// the offending entry and field (FR1.3) -- pass through verbatim.
		return err
	}
	if errors.Is(err, authz.ErrNotFound) {
		return contract.InvalidArgument("device_config", "", "This references something that doesn't exist.")
	}
	s.logger.Error("region household validation failed", "error", err)
	return contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
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

	// FR1.3's caller-visible skip surface, independent of whether an
	// accepted config exists yet -- a board can have skipped apply-time
	// entries recorded against it regardless of Found below.
	skipRows, err := s.repo.GetRegionApplySkips(ctx, req.DeviceId)
	if err != nil {
		s.logger.Error("get region apply skips failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Could not look up this device's config right now. Please try again.")
	}
	skips := make([]*pb.RegionApplySkip, 0, len(skipRows))
	for _, row := range skipRows {
		skips = append(skips, &pb.RegionApplySkip{
			SensorId:   row.SensorID,
			Reason:     row.Reason,
			OccurredAt: contract.ToInstant(row.OccurredAt),
		})
	}

	if cfg == nil {
		return &pb.GetDeviceConfigResponse{Found: false, Skips: skips}, nil
	}
	return &pb.GetDeviceConfigResponse{Config: cfg, Found: true, Skips: skips}, nil
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

// configVersionNotFoundFailure is the one contract.NotFound value returned
// for a (device_id, version) pair that names no stored config version --
// whether because the version was never pushed or because the board
// itself doesn't exist. Board existence/scope is always checked first via
// authorizeBoardAccess (NFR2 -- a caller outside the board's household
// reach gets boardNotFoundFailure instead, never this one), so reaching
// this failure means the board is real and in scope but this exact
// version isn't -- FR34.1's "a rejected push is never indistinguishable
// from no push at all" is about state within an existing version, not
// about this case: a version that was never pushed at all is reported
// distinctly, as not_found, not as any of the three ConfigState values.
func configVersionNotFoundFailure() error {
	return contract.NotFound("device_config", "version", "No config version matches this device and version.")
}

// stateToProto translates config.State (leaflab/api/config.DeriveState's
// return value -- the one place these three states are computed) onto the
// wire pb.ConfigState enum used by GetConfigStatus, ListConfigHistory and
// GetConfigVersion.
func stateToProto(s config.State) pb.ConfigState {
	switch s {
	case config.StateAccepted:
		return pb.ConfigState_CONFIG_STATE_ACCEPTED
	case config.StatePending:
		return pb.ConfigState_CONFIG_STATE_PENDING
	case config.StateRejected:
		return pb.ConfigState_CONFIG_STATE_REJECTED
	default:
		return pb.ConfigState_CONFIG_STATE_UNSPECIFIED
	}
}

// provenanceToProto translates config.Provenance (leaflab/api/config's
// FR82.4 per-entry provenance, as stored in device_config_entry) onto the
// wire pb.EntryProvenance enum GetConfigVersion returns per entry.
func provenanceToProto(p string) pb.EntryProvenance {
	switch config.Provenance(p) {
	case config.ProvenanceAuthored:
		return pb.EntryProvenance_ENTRY_PROVENANCE_AUTHORED
	case config.ProvenanceMaterialised:
		return pb.EntryProvenance_ENTRY_PROVENANCE_MATERIALISED
	default:
		return pb.EntryProvenance_ENTRY_PROVENANCE_UNSPECIFIED
	}
}

// sensorTypeFromName is sensorTypeNameFromConfig's inverse: it converts a
// sensor_type.name value read back from device_config_entry (via
// GetConfigVersionEntries' join) to the proto SensorType enum value it
// mirrors. A name with no matching enum value -- including "", stored for
// a single-virtual chip's SENSOR_TYPE_UNKNOWN entry -- maps back to
// SENSOR_TYPE_UNKNOWN, matching sensorTypeNameFromConfig's own "" output
// for that case.
func sensorTypeFromName(name string) firmwarepb.SensorType {
	if name == "" {
		return firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN
	}
	if v, ok := firmwarepb.SensorType_value["SENSOR_TYPE_"+strings.ToUpper(name)]; ok {
		return firmwarepb.SensorType(v)
	}
	return firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN
}

// configEntryMuxPath decodes a device_config_entry.mux_path jsonb column
// (hwkey.MuxPath's canonical encoding, migration 028) into the wire
// []*pb.MuxHop shape GetConfigVersion's ConfigVersionEntry.mux_path
// returns. A decode failure is treated as an empty path (root bus) rather
// than failing the whole RPC -- mux_path is always this package's own
// canonical encoding on write (InsertDeviceConfigNextVersion), so this
// should never actually occur outside a hand-edited row.
func configEntryMuxPath(raw []byte) []*configpb.MuxHop {
	var path hwkey.MuxPath
	if err := path.UnmarshalJSON(raw); err != nil {
		return nil
	}
	hops := make([]*configpb.MuxHop, len(path))
	for i, h := range path {
		hops[i] = &configpb.MuxHop{MuxAddress: h.MuxAddress, MuxChannel: h.MuxChannel}
	}
	return hops
}

// GetConfigStatus reports one pushed config version's FR34.1 derived state
// -- accepted/pending/rejected, always exactly these three -- plus
// pushed-at, acked-at and the firmware's verbatim rejection reason
// (FR34.1) and FR34.2/FR59.2's persona-appropriate sentence. Household-
// scoped like GetDeviceConfig: a caller outside the board's household
// reach gets the identical NFR2 not-found boardNotFoundFailure a
// nonexistent device_id would, via authorizeBoardAccess.
func (s *LeafLabAPIServer) GetConfigStatus(ctx context.Context, req *pb.GetConfigStatusRequest) (*pb.GetConfigStatusResponse, error) {
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("device_config", "device_id", reason)
	}
	if err := s.authorizeBoardAccess(ctx, req.DeviceId); err != nil {
		return nil, err
	}

	row, err := s.repo.GetDeviceConfigVersion(ctx, req.DeviceId, int64(req.Version))
	if err != nil {
		s.logger.Error("get config version failed", "device_id", req.DeviceId, "version", req.Version, "error", err)
		return nil, contract.Internal("device_config", "", "Could not look up this config version right now. Please try again.")
	}
	if row == nil {
		return nil, configVersionNotFoundFailure()
	}

	state := config.DeriveState(row.Accepted, row.AckedAt, row.RejectionReason)
	return &pb.GetConfigStatusResponse{
		State:           stateToProto(state),
		PushedAt:        contract.ToInstant(row.PushedAt),
		AckedAt:         contract.ToInstant(timeOrZero(row.AckedAt)),
		RejectionReason: row.RejectionReason,
		Sentence:        state.Sentence(),
		ServerNow:       contract.Now(),
	}, nil
}

// timeOrZero returns *t, or the zero time.Time if t is nil --
// contract.ToInstant renders a zero time.Time as an unset/zero-valued
// Instant, matching the proto doc comment's "unset while state is
// CONFIG_STATE_PENDING" for acked_at.
func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// ListConfigHistory lists a board's full config push history, newest
// first, keyset paginated (FR61) -- including pending and rejected
// versions, each visibly marked with its ConfigState (FR35.1). Household-
// scoped like GetDeviceConfig/GetConfigStatus.
func (s *LeafLabAPIServer) ListConfigHistory(ctx context.Context, req *pb.ListConfigHistoryRequest) (*pb.ListConfigHistoryResponse, error) {
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("device_config", "device_id", reason)
	}
	if err := s.authorizeBoardAccess(ctx, req.DeviceId); err != nil {
		return nil, err
	}

	beforeVersion, hasBefore, err := contract.DecodeConfigHistoryCursor(req.GetPage().GetPageToken())
	if err != nil {
		return nil, contract.InvalidArgument("list_config_history_request", "page_token", "This page link is no longer valid. Start again from the first page.")
	}

	limit := contract.ClampPageSize(req.GetPage().GetPageSize())

	// Fetch one extra row to detect whether a next page exists without a
	// separate COUNT query, matching ListBoards' own convention.
	rows, err := s.repo.ListConfigHistory(ctx, req.DeviceId, beforeVersion, hasBefore, limit+1)
	if err != nil {
		s.logger.Error("list config history failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Could not list this device's config history right now. Please try again.")
	}

	var nextToken string
	if int32(len(rows)) > limit {
		rows = rows[:limit]
		nextToken = contract.EncodeConfigHistoryCursor(rows[len(rows)-1].Version)
	}

	entries := make([]*pb.ConfigHistoryEntry, 0, len(rows))
	for _, r := range rows {
		state := config.DeriveState(r.Accepted, r.AckedAt, r.RejectionReason)
		entries = append(entries, &pb.ConfigHistoryEntry{
			Version:         uint64(r.Version),
			State:           stateToProto(state),
			PushedAt:        contract.ToInstant(r.PushedAt),
			AckedAt:         contract.ToInstant(timeOrZero(r.AckedAt)),
			RejectionReason: r.RejectionReason,
			Sentence:        state.Sentence(),
		})
	}
	return &pb.ListConfigHistoryResponse{
		Entries:   entries,
		Page:      &pb.PageResponse{NextPageToken: nextToken},
		ServerNow: contract.Now(),
	}, nil
}

// GetConfigVersion fetches any single stored config version by
// (device_id, version), regardless of whether it was ever accepted
// (FR35.2), returning its stored payload plus each entry's FR82.4
// provenance. Household-scoped like GetDeviceConfig/GetConfigStatus.
func (s *LeafLabAPIServer) GetConfigVersion(ctx context.Context, req *pb.GetConfigVersionRequest) (*pb.GetConfigVersionResponse, error) {
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("device_config", "device_id", reason)
	}
	if err := s.authorizeBoardAccess(ctx, req.DeviceId); err != nil {
		return nil, err
	}

	row, err := s.repo.GetDeviceConfigVersion(ctx, req.DeviceId, int64(req.Version))
	if err != nil {
		s.logger.Error("get config version failed", "device_id", req.DeviceId, "version", req.Version, "error", err)
		return nil, contract.Internal("device_config", "", "Could not look up this config version right now. Please try again.")
	}
	if row == nil {
		return nil, configVersionNotFoundFailure()
	}

	var cfg configpb.DeviceConfig
	if err := protojson.Unmarshal(row.ConfigJSON, &cfg); err != nil {
		s.logger.Error("unmarshal stored config version failed", "device_id", req.DeviceId, "version", req.Version, "error", err)
		return nil, contract.Internal("device_config", "", "Could not read this config version right now. Please try again.")
	}
	// Version isn't stored in config_json (it's assigned atomically by
	// InsertDeviceConfigNextVersion after marshaling -- see PushDeviceConfig)
	// -- filled in here from the row this query resolved by, so a caller
	// never has to trust an unset/zero field on the returned payload.
	cfg.Version = uint64(row.Version)

	entryRows, err := s.repo.GetConfigVersionEntries(ctx, row.ConfigID)
	if err != nil {
		s.logger.Error("get config version entries failed", "device_id", req.DeviceId, "version", req.Version, "error", err)
		return nil, contract.Internal("device_config", "", "Could not read this config version's entries right now. Please try again.")
	}
	entries := make([]*pb.ConfigVersionEntry, 0, len(entryRows))
	for _, e := range entryRows {
		var i2cAddr uint32
		if e.I2CAddress != nil {
			i2cAddr = uint32(*e.I2CAddress)
		}
		entries = append(entries, &pb.ConfigVersionEntry{
			MuxPath:    configEntryMuxPath(e.MuxPath),
			I2CAddress: i2cAddr,
			SensorType: sensorTypeFromName(e.SensorTypeName),
			Provenance: provenanceToProto(e.Provenance),
		})
	}

	state := config.DeriveState(row.Accepted, row.AckedAt, row.RejectionReason)
	return &pb.GetConfigVersionResponse{
		Config:          &cfg,
		State:           stateToProto(state),
		PushedAt:        contract.ToInstant(row.PushedAt),
		AckedAt:         contract.ToInstant(timeOrZero(row.AckedAt)),
		RejectionReason: row.RejectionReason,
		Entries:         entries,
		ServerNow:       contract.Now(),
	}, nil
}
