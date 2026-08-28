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
	"github.com/whale-net/everything/leaflab/api/health"
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

	// -- Admin (FR10, FR12 activation) -- see leaflab/api/repository.go's
	// "Admin" section for each method's doc comment.
	AdminBoardHealthByPerson(ctx context.Context, personIdentifier string) ([]AdminBoardHealthRow, error)
	AdminBoardHealthByPartialDeviceID(ctx context.Context, partial string) ([]AdminBoardHealthRow, error)
	// ListFleetHealth backs the RPC of the same name (FR79) -- see
	// Repository.ListFleetHealth's doc comment. Not household-scoped via
	// authz.Scope, same rationale as AdminBoardHealthByPerson/
	// AdminBoardHealthByPartialDeviceID above: this is the standing admin
	// lane's own dedicated projection.
	ListFleetHealth(ctx context.Context, afterBoardID int64, devicePrefix string, householdID int64, regionID int64, limit int32) ([]FleetBoardHealthRow, error)
	RecordAuditEntry(ctx context.Context, entry audit.Entry) error
	HouseholdExists(ctx context.Context, householdID int64) (bool, error)
	OpenElevation(ctx context.Context, adminSubject string, targetHouseholdID int64, reason string, expiresAt time.Time, entry audit.Entry) error
	RenewElevation(ctx context.Context, adminSubject string, targetHouseholdID int64, reason string, newExpiresAt time.Time, entry audit.Entry) error
	EndElevation(ctx context.Context, adminSubject string, targetHouseholdID int64, entry audit.Entry) error
	ActiveElevation(ctx context.Context, adminSubject string, targetHouseholdID int64) (time.Time, error)

	// GetBoardReportingHealth backs FR42's GetResendAvailability/
	// ResendDeviceConfig: the same reportingStateFor input ListFleetHealth's
	// rows carry, fetched for one board by device_id -- see
	// Repository.GetBoardReportingHealth's doc comment.
	GetBoardReportingHealth(ctx context.Context, deviceID string) (FleetBoardHealthRow, bool, error)
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

// DefaultElevationDuration is FR10.1's 60-minute elevation window, used
// unless a ServerOption overrides it (see WithElevationDuration). main.go
// wires LEAFLAB_ADMIN_ELEVATION_MINUTES to that option, so the window is
// configurable in the sense FR10.1 asks for -- documented here and at the
// env var's read site in main.go, since leaflab/api has no ENV.md of its
// own yet (unlike migrate/processor/ui).
const DefaultElevationDuration = 60 * time.Minute

// configPublisher is the subset of *rmq.Publisher's methods
// LeafLabAPIServer's RPCs depend on for publishing DeviceConfig payloads to
// MQTT (PushDeviceConfig's live push, ResendDeviceConfig's FR42.1
// retained re-publish). Narrowed to an interface -- like deviceRepository
// and authzResolver above -- so a future test can substitute an in-memory
// fake and assert exactly what was published (exchange, routing key, wire
// bytes, retain flag) without a live RabbitMQ connection; see
// push_device_config_scope_integration_test.go's doc comment for why no
// such fake exists yet. *rmq.Publisher satisfies this with no changes on
// the production path (NewLeafLabAPIServer is still called with
// *rmq.Publisher in main.go); passing a literal nil (every existing test
// call site) is valid for either the concrete type or this interface.
type configPublisher interface {
	Publish(ctx context.Context, exchange, routingKey string, body interface{}) error
	// PublishRetained is Publish with the MQTT retain flag set -- FR42.1's
	// "publish to the retained topic" (see libs/go/rmq's PublishRetained
	// doc comment for why AMQP needs an explicit header for this, unlike a
	// native MQTT publish).
	PublishRetained(ctx context.Context, exchange, routingKey string, body interface{}) error
}

type LeafLabAPIServer struct {
	pb.UnimplementedLeafLabAPIServer
	repo      deviceRepository
	authzSvc  authzResolver
	publisher configPublisher
	// rmqConn is the underlying RabbitMQ/MQTT connection GetHealth probes
	// (FR63.1). Held separately from publisher because rmq.Publisher does
	// not expose its connection's liveness. May be nil in tests that don't
	// exercise GetHealth -- see GetHealth's nil handling.
	rmqConn *rmq.Connection
	logger  *slog.Logger
	// elevationDuration is FR10.1's elevation window, applied by Elevate
	// and RenewElevation. Defaults to DefaultElevationDuration; see
	// WithElevationDuration.
	elevationDuration time.Duration
}

// ServerOption configures optional LeafLabAPIServer behavior beyond
// NewLeafLabAPIServer's required dependencies -- added as a variadic
// trailing parameter specifically so every existing call site (main.go,
// and every test file building a server directly) keeps compiling
// unchanged when a new option is introduced.
type ServerOption func(*LeafLabAPIServer)

// WithElevationDuration overrides DefaultElevationDuration -- FR10.1's
// "60 minutes, configurable". See main.go's LEAFLAB_ADMIN_ELEVATION_MINUTES
// for the production wiring.
func WithElevationDuration(d time.Duration) ServerOption {
	return func(s *LeafLabAPIServer) { s.elevationDuration = d }
}

func NewLeafLabAPIServer(repo deviceRepository, authzSvc authzResolver, publisher configPublisher, rmqConn *rmq.Connection, logger *slog.Logger, opts ...ServerOption) *LeafLabAPIServer {
	s := &LeafLabAPIServer{
		repo:              repo,
		authzSvc:          authzSvc,
		publisher:         publisher,
		rmqConn:           rmqConn,
		logger:            logger,
		elevationDuration: DefaultElevationDuration,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
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
// A caller's membership Scope not permitting the board is not the final
// word (FR10.3): "config payloads" is the one non-standing admin read this
// codebase has today, so before refusing, this function gives
// elevatedBoardScope one more chance -- it performs the ActiveElevation
// check ElevatedScope's own doc comment requires precede its construction,
// scoped to this board's *resolved* household, never a request-supplied
// one, so an elevation against household A still cannot open household B.
// This deliberately does not gate on isAdminEligible first: an
// admin_elevation row can only exist for a subject Elevate's own
// requireAdminEligible gate already let through, so checking for the row
// directly (exactly as scopeForCaller checks household_membership
// directly, with no separate "is this person a member" flag) is enough --
// it keeps every entity-access handler routing reach entirely through
// Scope, with "is admin" never itself a branch outside leaflab/api/authz/
// (see this package's Validation criterion). A non-elevated caller (admin
// or not), or an elevation against a different household, still gets the
// exact same boardNotFoundFailure as a nonexistent device_id (NFR2) -- the
// extra check never introduces a new response shape.
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
	if scope.Permits(ref, res) {
		return nil
	}

	if !res.Unclaimed {
		elevatedScope, err := s.elevatedBoardScope(ctx, res.HouseholdID)
		if err != nil {
			s.logger.Error("check admin elevation failed", "device_id", deviceID, "error", err)
			return contract.Internal("board", "", "Could not process this request right now. Please try again.")
		}
		if elevatedScope != nil && elevatedScope.Permits(ref, res) {
			return nil
		}
	}

	return boardNotFoundFailure()
}

// elevatedBoardScope returns an authz.ElevatedScope for targetHouseholdID
// if ctx's caller holds an unexpired admin_elevation row against that
// exact household (FR10.1, FR10.3), or (nil, nil) if they do not -- the
// "no active elevation" case is a normal, expected outcome here (a caller
// who never elevated, or elevated against a different household), not an
// error, so callers must check for a nil Scope rather than treating nil as
// a failure. A genuine repository error is still returned as an error.
// Deliberately does not check isAdminEligible: an admin_elevation row can
// only exist for a subject that already passed Elevate's own
// requireAdminEligible gate, so re-checking the role here would be
// redundant, and skipping it keeps entity-access call sites like
// authorizeBoardAccess free of any "is admin" branch (see that function's
// doc comment). This is the one call site (besides Elevate/RenewElevation/
// EndElevation/GetElevationStatus's own direct repo.ActiveElevation calls)
// that turns "an unexpired elevation exists for this specific household"
// into the Scope ElevatedScope's doc comment requires be verified first.
func (s *LeafLabAPIServer) elevatedBoardScope(ctx context.Context, targetHouseholdID int64) (authz.Scope, error) {
	_, err := s.repo.ActiveElevation(ctx, actingSubject(ctx), targetHouseholdID)
	if err != nil {
		if errors.Is(err, ErrNoActiveElevation) {
			return nil, nil
		}
		return nil, err
	}
	scope := authz.NewElevatedScope(targetHouseholdID)
	return scope, nil
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

// ── FR42 -- the one safe button ──────────────────────────────────────────────
//
// A re-send re-publishes the board's currently accepted config unchanged: it
// cannot alter what the device runs, never opens an editor, and pressing it
// twice is harmless (no new device_config row, no new version). Both RPCs
// below share resendAvailability so GetResendAvailability's answer and
// ResendDeviceConfig's own refusal, when unavailable, are computed by the
// exact same logic -- never two independently-maintained copies of "can this
// be attempted right now" that could drift out of agreement.

// resendAvailability computes FR42.2's up-front answer for deviceID's
// re-send: RESEND_AVAILABILITY_REASON_NOT_REPORTING when the board is stale
// per the shared A23 threshold -- reportingStateFor, the exact function
// FR79's ListFleetHealth uses (leaflab/api/health.Threshold and
// leaflab/api/health.IsStale have exactly one call site in this package;
// see TestReportingStateFor_IsTheOnlyA23CallSiteInLeafLabAPI); or
// RESEND_AVAILABILITY_REASON_NOTHING_TO_RESEND when no configuration has
// ever been accepted, or the most recent push was rejected (checked as two
// independent conditions, per FR42.2's own wording: an older accepted
// version can still exist even when the *latest* push was rejected, and
// that case is still "nothing to resend" -- it is not "resend the stale
// older version instead"). RESEND_AVAILABILITY_REASON_AVAILABLE otherwise,
// with an empty sentence/alternative (GetResendAvailabilityResponse's own
// doc comment: both are empty exactly when available is true).
func (s *LeafLabAPIServer) resendAvailability(ctx context.Context, deviceID string) (pb.ResendAvailabilityReason, string, string, error) {
	boardHealth, ok, err := s.repo.GetBoardReportingHealth(ctx, deviceID)
	if err != nil {
		s.logger.Error("get board reporting health failed", "device_id", deviceID, "error", err)
		return pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_UNSPECIFIED, "", "", contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	if !ok {
		// authorizeBoardAccess already refused a nonexistent/out-of-scope
		// device_id before either RPC below calls this -- reaching here
		// with ok == false would mean the board vanished between that
		// check and this query, not a caller mistake.
		return pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_UNSPECIFIED, "", "", boardNotFoundFailure()
	}

	if reportingStateFor(boardHealth, time.Now()) == pb.ReportingState_REPORTING_STATE_NOT_REPORTING {
		return pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_NOT_REPORTING,
			"This board isn't currently reporting.",
			"Check that the board is online, then try again.",
			nil
	}

	acceptedCfg, err := s.repo.GetLatestAcceptedConfig(ctx, deviceID)
	if err != nil {
		s.logger.Error("get latest accepted config for resend availability failed", "device_id", deviceID, "error", err)
		return pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_UNSPECIFIED, "", "", contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	if acceptedCfg == nil {
		return pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_NOTHING_TO_RESEND,
			"No configuration has ever been accepted for this board.",
			"Push a configuration.",
			nil
	}

	latest, err := s.repo.ListConfigHistory(ctx, deviceID, 0, false, 1)
	if err != nil {
		s.logger.Error("list config history for resend availability failed", "device_id", deviceID, "error", err)
		return pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_UNSPECIFIED, "", "", contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	if len(latest) > 0 && config.DeriveState(latest[0].Accepted, latest[0].AckedAt, latest[0].RejectionReason) == config.StateRejected {
		return pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_NOTHING_TO_RESEND,
			"The most recent push to this board was rejected; there is nothing newly accepted to re-send.",
			"Push a configuration.",
			nil
	}

	return pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_AVAILABLE, "", "", nil
}

// GetResendAvailability answers FR42.2's "stated up front" question: whether
// ResendDeviceConfig can be attempted right now, and if not, why -- so a
// caller renders the button disabled with the reason inline (FR59.3) rather
// than enabling it and letting the press fail. Household-scoped like
// GetDeviceConfig/GetConfigStatus (NFR2: a non-member gets the same
// boardNotFoundFailure as a nonexistent device_id).
func (s *LeafLabAPIServer) GetResendAvailability(ctx context.Context, req *pb.GetResendAvailabilityRequest) (*pb.GetResendAvailabilityResponse, error) {
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("device_config", "device_id", reason)
	}
	if err := s.authorizeBoardAccess(ctx, req.DeviceId); err != nil {
		return nil, err
	}

	availReason, sentence, alternative, err := s.resendAvailability(ctx, req.DeviceId)
	if err != nil {
		return nil, err
	}

	return &pb.GetResendAvailabilityResponse{
		Available:   availReason == pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_AVAILABLE,
		Reason:      availReason,
		Sentence:    sentence,
		Alternative: alternative,
		ServerNow:   contract.Now(),
	}, nil
}

// ResendDeviceConfig re-publishes deviceID's currently accepted config,
// byte-identical, to the retained MQTT topic (FR42.1). It never alters what
// the device runs, never assigns a new version, and never inserts a
// device_config row -- pressing it twice republishes the identical payload
// twice and changes nothing server-side, so the device applying the same
// payload twice is a no-op by construction. Refused (FR59.3, via
// resendAvailability's same reasons) when resendAvailability answers
// anything other than AVAILABLE. Household-scoped and audited (FR8.2) even
// though it writes no device_config row -- FR8.2 names this exact case.
func (s *LeafLabAPIServer) ResendDeviceConfig(ctx context.Context, req *pb.ResendDeviceConfigRequest) (*pb.ResendDeviceConfigResponse, error) {
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("device_config", "device_id", reason)
	}
	if err := s.authorizeBoardAccess(ctx, req.DeviceId); err != nil {
		return nil, err
	}

	availReason, sentence, alternative, err := s.resendAvailability(ctx, req.DeviceId)
	if err != nil {
		return nil, err
	}
	if availReason != pb.ResendAvailabilityReason_RESEND_AVAILABILITY_REASON_AVAILABLE {
		return nil, contract.Refuse("device_config", "device_id", sentence, alternative)
	}

	// resendAvailability's AVAILABLE answer already proved a non-nil
	// accepted config exists (it returns NOTHING_TO_RESEND otherwise) --
	// fetched again here rather than threaded through, since
	// resendAvailability's return shape is shared with GetResendAvailability
	// and carries no payload.
	cfg, err := s.repo.GetLatestAcceptedConfig(ctx, req.DeviceId)
	if err != nil {
		s.logger.Error("get latest accepted config for resend failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	if cfg == nil {
		// Can't happen after an AVAILABLE answer above; guarded rather than
		// dereferenced blindly so a future refactor that reorders these
		// calls fails loudly instead of publishing a version-0 payload.
		s.logger.Error("resend found no accepted config despite AVAILABLE answer", "device_id", req.DeviceId)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}

	wire, err := proto.Marshal(cfg)
	if err != nil {
		s.logger.Error("proto marshal failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}

	// Same exchange/routing-key convention as PushDeviceConfig's live push
	// (see mqttExchange's doc comment there) -- PublishRetained, not
	// Publish, is what makes this FR42.1's "retained topic" re-send rather
	// than a second live push (see libs/go/rmq's PublishRetained doc
	// comment for why AMQP needs an explicit retain header).
	routingKey := fmt.Sprintf("leaflab.%s.config", strings.ReplaceAll(req.DeviceId, "/", "."))
	if err := s.publisher.PublishRetained(ctx, mqttExchange, routingKey, wire); err != nil {
		s.logger.Error("resend publish failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Could not re-send this configuration right now. Please try again.")
	}

	// FR8.2: audited despite writing no device_config row -- EntityID stays
	// nil (no new entity was created; see
	// TestAuditor_RecordsReSendWithNoDeviceConfigRow's doc comment).
	// TargetHouseholdID mirrors PushDeviceConfig's own resolution: nil for
	// an unclaimed board (FR1.1), the resolved household id otherwise.
	_, boardRes, err := s.authzSvc.ResolveBoardByDeviceID(ctx, req.DeviceId)
	if err != nil {
		s.logger.Error("resolve board for resend audit failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	var targetHouseholdID *int64
	if !boardRes.Unclaimed {
		targetHouseholdID = &boardRes.HouseholdID
	}
	reg := auditRegistrations[resendDeviceConfigFullMethod]
	entry := audit.Entry{
		ActorSubject:      actingSubject(ctx),
		ActorKind:         audit.ActorKindHuman,
		TargetHouseholdID: targetHouseholdID,
		Action:            reg.Action,
		EntityKind:        reg.EntityKind,
		CorrelationID:     CorrelationIDFromContext(ctx),
	}
	if err := s.repo.RecordAuditEntry(ctx, entry); err != nil {
		s.logger.Error("record resend audit failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}

	s.logger.Info("device config re-sent", "device_id", req.DeviceId, "version", cfg.Version)

	return &pb.ResendDeviceConfigResponse{Version: cfg.Version}, nil
}

// ── Admin (FR10, FR12 activation) ────────────────────────────────────────────
//
// leaflab-admin (auth.go's RoleAdmin) is eligibility only -- requireAdminEligible
// is the gate every RPC below applies before doing anything else. Eligibility
// alone confers nothing past that gate: ResolveToHousehold's standing lane is
// resolution-only (AdminScope permits no entity and is never consulted here --
// see its doc comment), and every other admin reach requires an elevation row
// this file writes/reads directly against admin_elevation, never a bare
// isAdminEligible check standing in for it. Entity-access handlers outside
// this section (GetDeviceConfig today; authorizeBoardAccess) reach that same
// admin_elevation row through elevatedBoardScope, which checks for the row
// directly rather than isAdminEligible first (see its doc comment) -- so
// Validation's "no handler branches on 'is admin' outside
// leaflab/api/authz/" still holds: isAdminEligible itself is called only
// here, gating the five RPCs that *are* the admin surface, never by an
// entity-access handler deciding whether to widen a Scope. Nor does an
// elevation confer FR75's membership-change capability: no RPC here
// writes household_membership, and ElevatedScope (authz/scope.go) behaves
// exactly like HouseholdScope, which was never itself a path to changing
// membership either.

// requireAdminEligible refuses ctx's caller unless their Claims carry the
// leaflab-admin realm role (FR12 activation). Every RPC in this section
// gates on this first -- eligibility is a precondition for both the
// standing lane and elevation, never proof of either by itself (see
// auth.go's isAdminEligible/RoleAdmin doc comments).
func (s *LeafLabAPIServer) requireAdminEligible(ctx context.Context) error {
	if !isAdminEligible(ctx) {
		return contract.PermissionDenied("admin", "", "This action requires the leaflab-admin role.")
	}
	return nil
}

// ResolveToHousehold is the standing (non-elevated) admin lane (FR10.2):
// across all households, resolve a person, a support reference, or a
// partial device identifier to the owning household(s) and FR79's health
// fields for the boards found -- and nothing else. It does not route
// through authz.Scope at all (see authz.AdminScope's doc comment) --
// there is no wider entity to check against, only this dedicated
// projection.
//
// Audited at query granularity (FR10.4): exactly one audit row per call,
// carrying the query term, regardless of how many boards match -- never
// one row per returned board.
func (s *LeafLabAPIServer) ResolveToHousehold(ctx context.Context, req *pb.ResolveToHouseholdRequest) (*pb.ResolveToHouseholdResponse, error) {
	if err := s.requireAdminEligible(ctx); err != nil {
		return nil, err
	}

	var rows []AdminBoardHealthRow
	var queryTerm string
	var err error
	switch q := req.GetQuery().(type) {
	case *pb.ResolveToHouseholdRequest_PersonIdentifier:
		if q.PersonIdentifier == "" {
			return nil, contract.InvalidArgument("resolve_to_household_request", "person_identifier", "A person identifier is required.")
		}
		queryTerm = "person_identifier=" + q.PersonIdentifier
		rows, err = s.repo.AdminBoardHealthByPerson(ctx, q.PersonIdentifier)
	case *pb.ResolveToHouseholdRequest_SupportReference:
		if q.SupportReference == "" {
			return nil, contract.InvalidArgument("resolve_to_household_request", "support_reference", "A support reference is required.")
		}
		queryTerm = "support_reference=" + q.SupportReference
		// FR80 (support references) has not landed yet -- resolves to no
		// boards until it does, per the Scaffold's doc comment on this
		// RPC. Still audited below, at the same query granularity as
		// every other standing-lane query.
	case *pb.ResolveToHouseholdRequest_PartialDeviceId:
		if q.PartialDeviceId == "" {
			return nil, contract.InvalidArgument("resolve_to_household_request", "partial_device_id", "A partial device id is required.")
		}
		queryTerm = "partial_device_id=" + q.PartialDeviceId
		rows, err = s.repo.AdminBoardHealthByPartialDeviceID(ctx, q.PartialDeviceId)
	default:
		return nil, contract.InvalidArgument("resolve_to_household_request", "query", "Provide a person identifier, a support reference, or a partial device id.")
	}
	if err != nil {
		s.logger.Error("resolve to household failed", "error", err)
		return nil, contract.Internal("resolve_to_household_request", "", "Could not resolve this query right now. Please try again.")
	}

	reg := auditRegistrations[resolveToHouseholdFullMethod]
	auditEntry := audit.Entry{
		ActorSubject:  actingSubject(ctx),
		ActorKind:     audit.ActorKindHuman,
		Action:        reg.Action,
		EntityKind:    reg.EntityKind,
		EntityID:      &queryTerm,
		CorrelationID: CorrelationIDFromContext(ctx),
	}
	if err := s.repo.RecordAuditEntry(ctx, auditEntry); err != nil {
		s.logger.Error("resolve to household: audit record failed", "error", err)
		return nil, contract.Internal("resolve_to_household_request", "", "Could not process this request right now. Please try again.")
	}

	boards := make([]*pb.AdminBoardHealth, 0, len(rows))
	for _, r := range rows {
		boards = append(boards, &pb.AdminBoardHealth{
			DeviceId:         r.DeviceID,
			BoardDisplayName: r.BoardDisplayName,
			HouseholdId:      r.HouseholdID,
			HouseholdName:    r.HouseholdName,
			LastSeenAt:       contract.ToInstant(r.LastSeenAt),
			ActiveVersion:    r.ActiveVersion,
			OutstandingPush:  r.OutstandingPush,
			SensorCount:      r.SensorCount,
		})
	}
	return &pb.ResolveToHouseholdResponse{Boards: boards, ServerNow: contract.Now()}, nil
}

// listFleetHealthDBBatch is how many candidate rows ListFleetHealth pulls
// from the repository per repository round-trip while narrowing by
// reporting_state -- see this function's doc comment for why
// reporting_state is not itself a SQL predicate. Deliberately larger than
// contract.PageCap so a single round-trip usually satisfies a full page
// even when a fraction of the batch is filtered out.
const listFleetHealthDBBatch = 200

// listFleetHealthMaxSteps bounds the repository round-trips a single
// ListFleetHealth call makes while accumulating reporting_state matches
// (see this function's doc comment) -- bounded work per call, per FR61,
// even against a fleet that is almost entirely the state the caller is
// filtering out.
const listFleetHealthMaxSteps = 10

// ListFleetHealth is FR79's admin fleet health listing. Like
// ResolveToHousehold, eligibility alone (requireAdminEligible) is
// sufficient reach -- this RPC does not route through authz.Scope; it is
// the standing admin lane's own dedicated projection (FR10.2's field set,
// extended per FR79), reachable across every household with no elevation
// required, and narrowed -- never widened -- by this request's filters
// (an elevated admin reaches the same data by simply setting
// household_id to the one household they elevated against).
//
// device_id_prefix, household_id and region_id are applied as SQL
// predicates (repository.go's fleetBoardHealthQuery). reporting_state is
// not: it depends on leaflab/api/health.Threshold, which needs each
// board's accepted config (already fetched by the same query) and A23's
// "global, not per-household" arithmetic -- computed here, once, via
// reportingStateFor, the same function every other A23 consumer calls,
// never re-derived per RPC. A retired board is never classified
// REPORTING_STATE_NOT_REPORTING regardless of staleness (FR22.4/FR79:
// "excluded from its offline counts" -- a caller tallying NOT_REPORTING
// across this response never double-counts a retired board because it can
// never appear in that tally).
//
// Because reporting_state is filtered after the fact, this handler steps
// through the keyset up to listFleetHealthMaxSteps repository round-trips,
// accumulating matches, so a caller filtering unhealthy_only against a
// mostly-healthy fleet still gets a full page rather than a near-empty one
// after a single narrow SQL fetch. next_page_token is always derived from
// the board_id of the last row actually *returned* (mirroring ListBoards'
// convention exactly) when a next page is known to exist, or from the
// last row *scanned* when the step budget was exhausted without either
// filling the page or reaching the end of the fleet -- either way,
// resuming from it can never skip an unscanned board and never re-returns
// an already-returned one.
func (s *LeafLabAPIServer) ListFleetHealth(ctx context.Context, req *pb.ListFleetHealthRequest) (*pb.ListFleetHealthResponse, error) {
	if err := s.requireAdminEligible(ctx); err != nil {
		return nil, err
	}

	afterBoardID, hasAfter, err := contract.DecodeBoardCursor(req.GetPage().GetPageToken())
	if err != nil {
		return nil, contract.InvalidArgument("list_fleet_health_request", "page_token", "This page link is no longer valid. Start again from the first page.")
	}
	if !hasAfter {
		afterBoardID = 0
	}

	limit := contract.ClampPageSize(req.GetPage().GetPageSize())
	wantState := req.GetReportingState()
	now := time.Now()

	// matchedBoardIDs runs parallel to matched: matchedBoardIDs[i] is the
	// board_id matched[i] was built from, so nextToken (below) can be
	// derived from the last *returned* row's board_id, per ListBoards'
	// convention, rather than from an excess (limit+1'th) match that gets
	// trimmed off.
	var matched []*pb.FleetBoardHealth
	var matchedBoardIDs []int64
	cursor := afterBoardID
	exhausted := false

	for step := 0; step < listFleetHealthMaxSteps; step++ {
		rows, err := s.repo.ListFleetHealth(ctx, cursor, req.GetDeviceIdPrefix(), req.GetHouseholdId(), req.GetRegionId(), listFleetHealthDBBatch)
		if err != nil {
			s.logger.Error("list fleet health failed", "error", err)
			return nil, contract.Internal("board", "", "Could not list fleet health right now. Please try again.")
		}
		if len(rows) < listFleetHealthDBBatch {
			exhausted = true
		}

		for _, r := range rows {
			cursor = r.BoardID

			state := reportingStateFor(r, now)
			if wantState != pb.ReportingState_REPORTING_STATE_UNSPECIFIED && state != wantState {
				continue
			}
			board := &pb.FleetBoardHealth{
				DeviceId:         r.DeviceID,
				BoardDisplayName: r.BoardDisplayName,
				HouseholdId:      r.HouseholdID,
				HouseholdName:    r.HouseholdName,
				LastSeenAt:       contract.ToInstant(r.LastSeenAt),
				ReportingState:   state,
				ActiveVersion:    r.ActiveVersion,
				OutstandingPush:  r.OutstandingPush,
				SensorCount:      r.SensorCount,
				Retired:          r.Retired,
			}
			// OutstandingPushSince is meaningful only when OutstandingPush
			// is true (FleetBoardHealth's proto doc comment) -- left unset
			// otherwise, the same convention GetElevationStatusResponse
			// uses for expires_at, rather than converting a zero
			// time.Time into a misleadingly-real-looking Instant.
			if r.OutstandingPush {
				board.OutstandingPushSince = contract.ToInstant(r.OutstandingPushSince)
			}
			matched = append(matched, board)
			matchedBoardIDs = append(matchedBoardIDs, r.BoardID)
			if int32(len(matched)) > limit {
				break
			}
		}

		if int32(len(matched)) > limit || exhausted {
			break
		}
	}

	var nextToken string
	if int32(len(matched)) > limit {
		matched = matched[:limit]
		nextToken = contract.EncodeBoardCursor(matchedBoardIDs[limit-1])
	} else if !exhausted {
		// The step budget ran out before either filling the page or
		// reaching the end of the fleet -- more boards remain unscanned
		// beyond cursor, so a next page still exists even though this
		// call is returning fewer than limit matches.
		nextToken = contract.EncodeBoardCursor(cursor)
	}

	return &pb.ListFleetHealthResponse{
		Boards:    matched,
		Page:      &pb.PageResponse{NextPageToken: nextToken},
		ServerNow: contract.ToInstant(now),
	}, nil
}

// reportingStateFor computes r's A23 reporting_state as of now:
// REPORTING_STATE_REPORTING for a retired board unconditionally (FR22.4 --
// never counted "not reporting"), otherwise leaflab/api/health.IsStale
// against r's own longest configured poll interval, decoded from its
// accepted config. A board with no accepted config, or an accepted config
// with no sensors, is treated as a longest-configured-interval of 0, which
// leaflab/api/health.Threshold floors to StalenessFloor -- the same
// outcome a freshly-registered, never-configured board should have.
func reportingStateFor(r FleetBoardHealthRow, now time.Time) pb.ReportingState {
	if r.Retired {
		return pb.ReportingState_REPORTING_STATE_REPORTING
	}

	var longest time.Duration
	if len(r.AcceptedConfigJSON) > 0 {
		var cfg configpb.DeviceConfig
		if err := protojson.Unmarshal(r.AcceptedConfigJSON, &cfg); err == nil {
			for _, sensor := range cfg.GetSensors() {
				if interval := health.EffectivePollInterval(sensor.GetPollIntervalMs()); interval > longest {
					longest = interval
				}
			}
		}
	}

	if health.IsStale(r.LastSeenAt, now, longest) {
		return pb.ReportingState_REPORTING_STATE_NOT_REPORTING
	}
	return pb.ReportingState_REPORTING_STATE_REPORTING
}

// Elevate opens a fresh, time-boxed elevation against target_household_id
// (FR10.1). Requires a non-empty reason and a target household that
// exists; writes the admin_elevation row and its audit record in one
// transaction (repository.go's OpenElevation/auditedWrite).
func (s *LeafLabAPIServer) Elevate(ctx context.Context, req *pb.ElevateRequest) (*pb.ElevateResponse, error) {
	if err := s.requireAdminEligible(ctx); err != nil {
		return nil, err
	}
	if req.GetReason() == "" {
		return nil, contract.InvalidArgument("elevate_request", "reason", "A reason is required to elevate.")
	}
	targetHouseholdID := req.GetTargetHouseholdId()
	if targetHouseholdID <= 0 {
		return nil, contract.InvalidArgument("elevate_request", "target_household_id", "A target household is required.")
	}

	exists, err := s.repo.HouseholdExists(ctx, targetHouseholdID)
	if err != nil {
		s.logger.Error("elevate: household existence check failed", "target_household_id", targetHouseholdID, "error", err)
		return nil, contract.Internal("elevate_request", "", "Could not process this request right now. Please try again.")
	}
	if !exists {
		return nil, contract.NotFound("household", "target_household_id", "No household matches this id.")
	}

	subject := actingSubject(ctx)
	reason := req.GetReason()
	expiresAt := time.Now().Add(s.elevationDuration)
	entry := audit.NewElevationEntry(subject, audit.ActorKindHuman, &targetHouseholdID, reason, CorrelationIDFromContext(ctx))
	if err := s.repo.OpenElevation(ctx, subject, targetHouseholdID, reason, expiresAt, entry); err != nil {
		s.logger.Error("elevate failed", "target_household_id", targetHouseholdID, "error", err)
		return nil, contract.Internal("elevate_request", "", "Could not open elevation right now. Please try again.")
	}
	return &pb.ElevateResponse{ExpiresAt: contract.ToInstant(expiresAt)}, nil
}

// RenewElevation extends the caller's currently-open elevation against
// target_household_id (FR10.1's "renewable by re-stating a reason").
// reason must be restated -- an empty reason, or no elevation currently
// open for target_household_id, is refused; renewal never opens a new
// elevation.
func (s *LeafLabAPIServer) RenewElevation(ctx context.Context, req *pb.RenewElevationRequest) (*pb.RenewElevationResponse, error) {
	if err := s.requireAdminEligible(ctx); err != nil {
		return nil, err
	}
	if req.GetReason() == "" {
		return nil, contract.InvalidArgument("renew_elevation_request", "reason", "A restated reason is required to renew.")
	}
	targetHouseholdID := req.GetTargetHouseholdId()
	if targetHouseholdID <= 0 {
		return nil, contract.InvalidArgument("renew_elevation_request", "target_household_id", "A target household is required.")
	}

	subject := actingSubject(ctx)
	reason := req.GetReason()
	expiresAt := time.Now().Add(s.elevationDuration)
	reg := auditRegistrations[renewElevationFullMethod]
	entry := audit.Entry{
		ActorSubject:      subject,
		ActorKind:         audit.ActorKindHuman,
		TargetHouseholdID: &targetHouseholdID,
		Action:            reg.Action,
		EntityKind:        reg.EntityKind,
		Reason:            &reason,
		CorrelationID:     CorrelationIDFromContext(ctx),
	}
	if err := s.repo.RenewElevation(ctx, subject, targetHouseholdID, reason, expiresAt, entry); err != nil {
		if errors.Is(err, ErrNoActiveElevation) {
			return nil, contract.Refuse("admin_elevation", "target_household_id", "No elevation is currently open for this household.", "Call Elevate to open an elevation for this household first.")
		}
		s.logger.Error("renew elevation failed", "target_household_id", targetHouseholdID, "error", err)
		return nil, contract.Internal("renew_elevation_request", "", "Could not renew elevation right now. Please try again.")
	}
	return &pb.RenewElevationResponse{ExpiresAt: contract.ToInstant(expiresAt)}, nil
}

// EndElevation ends every currently-open elevation the caller holds
// against target_household_id before its natural expiry.
func (s *LeafLabAPIServer) EndElevation(ctx context.Context, req *pb.EndElevationRequest) (*pb.EndElevationResponse, error) {
	if err := s.requireAdminEligible(ctx); err != nil {
		return nil, err
	}
	targetHouseholdID := req.GetTargetHouseholdId()
	if targetHouseholdID <= 0 {
		return nil, contract.InvalidArgument("end_elevation_request", "target_household_id", "A target household is required.")
	}

	subject := actingSubject(ctx)
	reg := auditRegistrations[endElevationFullMethod]
	entry := audit.Entry{
		ActorSubject:      subject,
		ActorKind:         audit.ActorKindHuman,
		TargetHouseholdID: &targetHouseholdID,
		Action:            reg.Action,
		EntityKind:        reg.EntityKind,
		CorrelationID:     CorrelationIDFromContext(ctx),
	}
	if err := s.repo.EndElevation(ctx, subject, targetHouseholdID, entry); err != nil {
		if errors.Is(err, ErrNoActiveElevation) {
			return nil, contract.NotFound("admin_elevation", "target_household_id", "No elevation is currently open for this household.")
		}
		s.logger.Error("end elevation failed", "target_household_id", targetHouseholdID, "error", err)
		return nil, contract.Internal("end_elevation_request", "", "Could not end elevation right now. Please try again.")
	}
	return &pb.EndElevationResponse{}, nil
}

// GetElevationStatus reports whether the caller currently holds an
// unexpired elevation against target_household_id, and its remaining time
// (A22) -- readable throughout the elevation, not only at grant time. A
// caller renders remaining time as expires_at - server_now, never against
// its own clock alone (FR64).
func (s *LeafLabAPIServer) GetElevationStatus(ctx context.Context, req *pb.GetElevationStatusRequest) (*pb.GetElevationStatusResponse, error) {
	if err := s.requireAdminEligible(ctx); err != nil {
		return nil, err
	}
	targetHouseholdID := req.GetTargetHouseholdId()
	if targetHouseholdID <= 0 {
		return nil, contract.InvalidArgument("get_elevation_status_request", "target_household_id", "A target household is required.")
	}

	expiresAt, err := s.repo.ActiveElevation(ctx, actingSubject(ctx), targetHouseholdID)
	if err != nil {
		if errors.Is(err, ErrNoActiveElevation) {
			return &pb.GetElevationStatusResponse{Elevated: false, ServerNow: contract.Now()}, nil
		}
		s.logger.Error("get elevation status failed", "target_household_id", targetHouseholdID, "error", err)
		return nil, contract.Internal("get_elevation_status_request", "", "Could not check elevation status right now. Please try again.")
	}
	return &pb.GetElevationStatusResponse{Elevated: true, ExpiresAt: contract.ToInstant(expiresAt), ServerNow: contract.Now()}, nil
}
