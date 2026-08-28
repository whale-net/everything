package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/config"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
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
	InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte, entries []config.Entry, removed []config.RemovedEntry, entry audit.Entry, pushGroupID *int64, derivedFromVersion *int64) (int64, error)
	// PeekNextConfigVersion is FR38 dry run's read-only "what version would
	// this assign" -- see Repository.PeekNextConfigVersion.
	PeekNextConfigVersion(ctx context.Context, boardID int64) (int64, error)
	GetLatestAcceptedConfig(ctx context.Context, deviceID string) (*configpb.DeviceConfig, error)
	// GetConfigVersion is FR37's DiffConfigVersions RPC's exact-version
	// lookup -- see Repository.GetConfigVersion.
	GetConfigVersion(ctx context.Context, deviceID string, version uint64) (*configpb.DeviceConfig, error)
	// GetConfigVersionRow is FR40 RollbackDeviceConfig's source-version
	// load -- see Repository.GetConfigVersionRow.
	GetConfigVersionRow(ctx context.Context, deviceID string, version uint64) (ConfigVersionRow, bool, error)
	// LoadCatalog is FR39's chip/measurement-type catalog snapshot -- see
	// Repository.LoadCatalog.
	LoadCatalog(ctx context.Context) (*config.Catalog, error)
	// GetRegionApplySkips is FR1.3's caller-visible skip surface: the
	// audit.Entry rows leaflab/processor's ApplyConfigRegions wrote for
	// this board's skipped config entries (household drift or a stale
	// push), most recent first. See Repository.GetRegionApplySkips.
	GetRegionApplySkips(ctx context.Context, deviceID string) ([]RegionApplySkipRow, error)
	// ListBoards is household-scoped (FR5.1): scope's SQL fragment
	// (Scope.Filter) is applied inside the query itself, never as a
	// post-filter -- see Repository.ListBoards.
	ListBoards(ctx context.Context, afterBoardID int64, hasAfter bool, limit int32, scope authz.Scope) ([]BoardRow, error)
	Ping(ctx context.Context) error
	// FR16/FR16.3/FR16.4/FR17 sensor identity resolution -- see identity.go
	// and Repository's implementations.
	FindSensorIDByName(ctx context.Context, boardID int64, name string) (int64, bool, error)
	resolveSensorTypeID(ctx context.Context, typeName string) (int64, bool, error)
	LoadBoardSensorIdentities(ctx context.Context, boardID int64) ([]BoardSensorIdentity, error)
	RewireSensorHW(ctx context.Context, sensorID int64, hw *HardwareAddress) error
	// FR48.1 push_group bookkeeping -- see Repository.CreatePushGroup,
	// Repository.GetPushGroup and Repository.GetPushGroupBoards.
	CreatePushGroup(ctx context.Context, reason, actorSubject string) (int64, error)
	GetPushGroup(ctx context.Context, pushGroupID int64) (PushGroupRow, bool, error)
	GetPushGroupBoards(ctx context.Context, pushGroupID int64) ([]PushGroupBoardRow, error)
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
	// pollIntervalBounds is FR39's stated poll_interval_ms min/max, resolved
	// once at boot from configuration (leaflab/api/ENV.md) -- never the
	// zero value in production (main.go refuses to boot without it set),
	// matching config.PollIntervalBounds' own "never silently disabled"
	// doc comment.
	pollIntervalBounds config.PollIntervalBounds
}

// DefaultPollIntervalMsMin/DefaultPollIntervalMsMax are FR39's fallback
// poll_interval_ms bounds, used by main.go when
// LEAFLAB_API_POLL_INTERVAL_MS_MIN/_MAX are unset (see leaflab/api/ENV.md)
// and directly by tests that construct NewLeafLabAPIServer without
// exercising this specific check.
const (
	DefaultPollIntervalMsMin uint32 = 1000
	DefaultPollIntervalMsMax uint32 = 3_600_000
)

// defaultPollIntervalBounds is DefaultPollIntervalMsMin/Max bundled as the
// config.PollIntervalBounds NewLeafLabAPIServer takes directly.
var defaultPollIntervalBounds = config.PollIntervalBounds{MinMs: DefaultPollIntervalMsMin, MaxMs: DefaultPollIntervalMsMax}

func NewLeafLabAPIServer(repo deviceRepository, authzSvc authzResolver, publisher *rmq.Publisher, rmqConn *rmq.Connection, logger *slog.Logger, pollIntervalBounds config.PollIntervalBounds) *LeafLabAPIServer {
	return &LeafLabAPIServer{
		repo:               repo,
		authzSvc:           authzSvc,
		publisher:          publisher,
		rmqConn:            rmqConn,
		logger:             logger,
		pollIntervalBounds: pollIntervalBounds,
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
	// FR48: device_ids (a board set) supersedes the legacy singular
	// device_id whenever it's non-empty -- api.proto's own doc comment on
	// both fields. isBoardSet distinguishes "caller used the new field"
	// (which may still name exactly one board) from "caller used only the
	// legacy field" -- the household-spanning check below only makes sense
	// once a caller has explicitly named a set.
	isBoardSet := len(req.DeviceIds) > 0
	targetIDs := req.DeviceIds
	if !isBoardSet {
		targetIDs = []string{req.DeviceId}
	}
	for _, id := range targetIDs {
		if reason := validateDeviceID(id); reason != "" {
			return nil, contract.InvalidArgument("device_config", "device_id", reason)
		}
	}

	// FR82.1: scope is required, with no default and never inferred from
	// payload shape/size/caller/endpoint. Checked before any board
	// bookkeeping or write -- an omitted scope leaves nothing stored,
	// nothing published, and no version assigned for any board in the set.
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

	// FR48.2: a multi-board push (more than one board named in device_ids)
	// requires a stated, non-empty reason -- refused before any board is
	// touched, naming none of them.
	isMultiBoard := len(targetIDs) > 1
	reason := strings.TrimSpace(req.Reason)
	if isMultiBoard && reason == "" {
		return nil, contract.InvalidArgument(
			"device_config",
			"reason",
			"A multi-board push requires a stated reason.",
		)
	}

	// Resolve board identity and household once per target (self-
	// registering a never-seen board, exactly as the pre-FR48 single-board
	// path always has) -- reused both by the household-spanning check below
	// and by each board's own push, so a board set is never resolved
	// twice.
	targets := make([]boardTarget, len(targetIDs))
	for i, id := range targetIDs {
		boardID, err := s.repo.GetOrCreateBoard(ctx, id)
		if err != nil {
			s.logger.Error("board lookup failed", "device_id", id, "error", err)
			return nil, contract.Internal("board", "", "Could not process this request right now. Please try again.")
		}
		boardRes, err := s.authzSvc.Resolve(ctx, authz.EntityRef{Kind: authz.EntityBoard, ID: boardID})
		if err != nil {
			s.logger.Error("resolve board household failed", "device_id", id, "board_id", boardID, "error", err)
			return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
		}
		targets[i] = boardTarget{deviceID: id, boardID: boardID, household: boardRes}
	}

	// FR48/FR5/FR1.2: a board set spanning more than one household is
	// refused unless the caller's scope reaches every one of them -- never
	// silently narrowed to "just the boards you can reach". An unclaimed
	// board (FR1.1) never counts toward "spanning" on its own: it belongs
	// to no household yet, so it can never be the second household in a
	// span.
	if isBoardSet {
		if err := s.checkBoardSetHouseholdReach(ctx, targets); err != nil {
			return nil, err
		}
	}

	// FR38: dry_run selects a writer that cannot reach storage or the
	// publisher at all (noopConfigWriter has no *rmq.Publisher field to
	// call, and never calls InsertDeviceConfigNextVersion) -- structural,
	// not a boolean re-checked at each call site below. A real push shares
	// one pushGroupState across every board in the set, so the push_group
	// row (if any board's write actually reaches it) is created at most
	// once and lazily -- never for a dry run, and never for a push where
	// every board fails before reaching storage.
	var writer configWriter
	if req.DryRun {
		writer = &noopConfigWriter{repo: s.repo}
	} else {
		group := &pushGroupState{repo: s.repo, reason: reason, actorSubject: actingSubject(ctx)}
		writer = &liveConfigWriter{repo: s.repo, publisher: s.publisher, logger: s.logger, group: group, isMultiBoard: isMultiBoard, correlationID: CorrelationIDFromContext(ctx)}
	}

	// FR48.1: one result per targeted board, never collapsed to an
	// aggregate -- a board-set push (isBoardSet) captures each board's own
	// failure/refusal into its own BoardPushResult and continues with the
	// rest; the legacy single-board path instead returns that failure as
	// this call's own error, preserving its pre-FR48 behavior exactly.
	results := make([]*pb.BoardPushResult, len(targets))
	for i, t := range targets {
		result, err := s.pushOneBoard(ctx, t, req, writer)
		if err != nil {
			if !isBoardSet {
				return nil, err
			}
			failure, ok := contract.FromError(err)
			if !ok {
				s.logger.Error("push board failed with no Failure detail", "device_id", t.deviceID, "error", err)
				failure = &pb.Failure{Class: string(contract.FailureInternal), Reason: "Could not process this request right now. Please try again."}
			}
			result = &pb.BoardPushResult{DeviceId: t.deviceID, Success: false, Failure: failure}
		}
		results[i] = result
	}

	resp := &pb.PushDeviceConfigResponse{
		Results:      results,
		Reason:       reason,
		PushedAt:     contract.Now(),
		ActorSubject: actingSubject(ctx),
	}
	if live, ok := writer.(*liveConfigWriter); ok && live.group.created {
		resp.PushGroupId = live.group.id
	}
	return resp, nil
}

// boardTarget is one board a PushDeviceConfig call named, already resolved
// to its board_id and household (self-registering a never-seen board via
// GetOrCreateBoard, exactly as the pre-FR48 single-board path always has)
// -- built once per call, before any board's own validation/materialisation
// runs, so a board set is never resolved twice (once for the FR48
// household-spanning check, once for the push itself).
type boardTarget struct {
	deviceID  string
	boardID   int64
	household authz.Resolution
}

// checkBoardSetHouseholdReach is FR48/FR5/FR1.2's board-set guard: refused
// (before any board in targets is validated or written) when targets span
// more than one claimed household and the caller's own Scope does not
// reach every one of them. A board set that resolves to a single household
// (or to no claimed household at all -- every board unclaimed, FR1.1) never
// triggers this: "spanning" requires at least two distinct claimed
// households in the same request.
func (s *LeafLabAPIServer) checkBoardSetHouseholdReach(ctx context.Context, targets []boardTarget) error {
	households := make(map[int64]bool, len(targets))
	for _, t := range targets {
		if !t.household.Unclaimed {
			households[t.household.HouseholdID] = true
		}
	}
	if len(households) <= 1 {
		return nil
	}

	scope, err := s.scopeForCaller(ctx)
	if err != nil {
		s.logger.Error("resolve caller scope failed", "error", err)
		return contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	for _, t := range targets {
		if t.household.Unclaimed {
			continue
		}
		ref := authz.EntityRef{Kind: authz.EntityBoard, ID: t.boardID}
		if !scope.Permits(ref, t.household) {
			return contract.PermissionDenied(
				"device_config",
				"device_ids",
				"This board set spans households you don't have access to; a multi-board push must reach every household it targets.",
			)
		}
	}
	return nil
}

// configWriter is PushDeviceConfig's persistence/publish step, separated
// from pushOneBoard's validation/materialisation logic so FR38's "the
// dry-run path cannot reach the writer" is structural: PushDeviceConfig
// selects a *noopConfigWriter under dry_run and a *liveConfigWriter
// otherwise, and pushOneBoard itself never branches on dry_run when it
// comes time to write or publish -- only which configWriter it was handed
// differs. write returns the version this board's push assigned (real) or
// would assign (dry run, FR38), and populates cfgProto.Version with that
// same value so BoardPushResult.effective_config always carries it.
type configWriter interface {
	write(ctx context.Context, t boardTarget, cfgProto *configpb.DeviceConfig, entries []config.Entry, removedEntries []config.RemovedEntry) (version uint64, err error)
}

// liveConfigWriter is configWriter's real implementation: it stores the
// device_config row (and FR82.4/FR82.6's per-entry provenance/removal
// bookkeeping, via Repository.InsertDeviceConfigNextVersion) and publishes
// the resulting config over MQTT. group is shared across every board a
// single PushDeviceConfig call targets, so at most one push_group row is
// created per call (lazily, on this writer's first successful reach of
// storage) rather than one per board.
type liveConfigWriter struct {
	repo          deviceRepository
	publisher     *rmq.Publisher
	logger        *slog.Logger
	group         *pushGroupState
	isMultiBoard  bool
	correlationID string
}

func (w *liveConfigWriter) write(ctx context.Context, t boardTarget, cfgProto *configpb.DeviceConfig, entries []config.Entry, removedEntries []config.RemovedEntry) (uint64, error) {
	configJSON, err := protojson.Marshal(cfgProto)
	if err != nil {
		w.logger.Error("protojson marshal failed", "device_id", t.deviceID, "error", err)
		return 0, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}

	pushGroupID, err := w.group.ensure(ctx)
	if err != nil {
		w.logger.Error("create push group failed", "device_id", t.deviceID, "error", err)
		return 0, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}

	var targetHouseholdID *int64
	if !t.household.Unclaimed {
		hh := t.household.HouseholdID
		targetHouseholdID = &hh
	}

	// FR8.2/FR48.2: a multi-board push's audit entry states its reason and
	// names the FR48 action explicitly, rather than reusing this method's
	// generic per-RPC registration (auditRegistrations) -- a single-board
	// push (whether via the legacy device_id field or a one-board
	// device_ids set) keeps that generic "PushConfig" action unchanged.
	var entry audit.Entry
	if w.isMultiBoard {
		entry = audit.NewMultiBoardPushEntry(actingSubject(ctx), audit.ActorKindHuman, targetHouseholdID, nil, w.group.reason, w.correlationID)
	} else {
		reg := auditRegistrations[pushDeviceConfigFullMethod]
		entry = audit.Entry{
			ActorSubject:      actingSubject(ctx),
			ActorKind:         audit.ActorKindHuman,
			TargetHouseholdID: targetHouseholdID,
			Action:            reg.Action,
			EntityKind:        reg.EntityKind,
			CorrelationID:     w.correlationID,
		}
	}

	version, err := w.repo.InsertDeviceConfigNextVersion(ctx, t.boardID, configJSON, entries, removedEntries, entry, &pushGroupID, nil)
	if err != nil {
		w.logger.Error("record config push failed", "device_id", t.deviceID, "error", err)
		return 0, contract.Internal("device_config", "", "Could not record this config push right now. Please try again.")
	}

	cfgProto.Version = uint64(version)
	wire, err := proto.Marshal(cfgProto)
	if err != nil {
		w.logger.Error("proto marshal failed", "device_id", t.deviceID, "error", err)
		return 0, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}

	// MQTT '/' → AMQP '.'; device_id should not contain '/' but sanitize to be safe.
	routingKey := fmt.Sprintf("leaflab.%s.config", strings.ReplaceAll(t.deviceID, "/", "."))
	if err := w.publisher.Publish(ctx, mqttExchange, routingKey, wire); err != nil {
		// Row is in DB but publish failed — device never received the push.
		// The row stays accepted=FALSE, which is correct: no ack will arrive.
		w.logger.Error("publish config failed", "device_id", t.deviceID, "error", err)
		return 0, contract.Internal("device_config", "", "Config was recorded but could not be delivered to the device. Please try pushing again.")
	}

	w.logger.Info("device config pushed",
		"device_id", t.deviceID,
		"version", version,
		"sensors", len(cfgProto.Sensors))
	return uint64(version), nil
}

// pushGroupState lazily creates at most one FR48.1 push_group row, shared
// by every board a single PushDeviceConfig call targets -- created on
// first use (the first board whose push actually reaches storage), not up
// front, so a push where every board fails validation creates no orphaned
// push_group row, matching "a refused push writes nothing" for the group
// bookkeeping too.
type pushGroupState struct {
	repo         deviceRepository
	reason       string
	actorSubject string
	created      bool
	id           int64
}

func (g *pushGroupState) ensure(ctx context.Context) (int64, error) {
	if g.created {
		return g.id, nil
	}
	id, err := g.repo.CreatePushGroup(ctx, g.reason, g.actorSubject)
	if err != nil {
		return 0, err
	}
	g.id = id
	g.created = true
	return id, nil
}

// noopConfigWriter is configWriter's FR38 dry-run implementation. It holds
// no *rmq.Publisher and never calls InsertDeviceConfigNextVersion --
// structurally, not merely by choosing not to -- so a dry run cannot store
// a device_config row or publish anything no matter how pushOneBoard's
// logic evolves around it. It returns the version a real push would assign
// next (Repository.PeekNextConfigVersion, best-effort/non-atomic since
// nothing is being reserved) and stamps it onto cfgProto so
// BoardPushResult.effective_config carries it exactly as a real push's
// would.
type noopConfigWriter struct {
	repo deviceRepository
}

func (w *noopConfigWriter) write(ctx context.Context, t boardTarget, cfgProto *configpb.DeviceConfig, entries []config.Entry, removedEntries []config.RemovedEntry) (uint64, error) {
	next, err := w.repo.PeekNextConfigVersion(ctx, t.boardID)
	if err != nil {
		return 0, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	cfgProto.Version = uint64(next)
	return uint64(next), nil
}

// pushOneBoard runs t's board through the full FR82 materialisation and
// FR39 validation PushDeviceConfig has always run for a single board, then
// hands off to writer for persistence/publish (or, under FR38 dry_run,
// writer's no-op equivalent) -- identical logic either way, so a dry run
// covers exactly the same blast radius a real push would (FR38's own
// stated goal). Returns a non-nil error, never a BoardPushResult, on any
// failure or refusal -- PushDeviceConfig's caller decides whether that
// becomes this call's own RPC error (the legacy single-board path) or one
// board's own captured failure within a board-set push (FR48.1).
func (s *LeafLabAPIServer) pushOneBoard(ctx context.Context, t boardTarget, req *pb.PushDeviceConfigRequest, writer configWriter) (*pb.BoardPushResult, error) {
	deviceID := t.deviceID
	boardID := t.boardID

	writerHousehold := t.household.HouseholdID
	if t.household.Unclaimed {
		// FR1.1: this board has no household yet -- no region_id in the
		// payload can ever satisfy AssertSameHousehold against it (see
		// validatePushRegions).
		writerHousehold = 0
	}

	// FR1.3: reject this board's push -- no device_config row stored,
	// nothing published, naming the offending entry and field -- before
	// either of those writes. GetOrCreateBoard's self-registration upsert
	// (already run, building t) is unaffected either way: it is
	// pre-existing, idempotent board-identity bookkeeping this validation
	// itself depends on, not a config write.
	if err := s.validatePushRegions(ctx, writerHousehold, req.Sensors); err != nil {
		return nil, err
	}

	// FR17 pre-write identity check: refuses before anything is written or
	// published if any entry would establish a new sensor identity rather
	// than continue an existing one (or would require an unresolved swap,
	// FR16.4). Run identically under dry_run: FR38's preview must cover
	// the same blast radius the real push would, including a refusal this
	// check would produce.
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

	// FR39: resolve the catalog snapshot once, shared by both scope
	// branches below -- Validate never talks to the DB itself (config
	// package doc comment), so this is the one place that loads it for the
	// push path.
	catalog, err := s.repo.LoadCatalog(ctx)
	if err != nil {
		s.logger.Error("load catalog failed", "device_id", deviceID, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}

	// FR37/FR38: base is this board's prior accepted config, resolved once
	// and shared by every scope branch below -- the EDIT branch's own
	// materialisation base, the COMPLETE branch's FR38 removed-set diff
	// source, and the general diff every successful push returns
	// (BoardPushResult.diff). base stays nil (not an empty, non-nil slice)
	// when no accepted config exists at all -- config.Materialise's
	// documented signal for FR82.3's "no accepted config to edit from",
	// distinct from a genuinely-empty accepted config.
	baseCfg, err := s.repo.GetLatestAcceptedConfig(ctx, deviceID)
	if err != nil {
		s.logger.Error("get latest accepted config failed", "device_id", deviceID, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	var base []config.Entry
	if baseCfg != nil {
		base, err = s.resolveConfigEntries(ctx, baseCfg.Sensors)
		if err != nil {
			return nil, err
		}
	}

	var entries []config.Entry
	sensorsForStorage := req.Sensors
	var removedForResponse []*pb.RemovedEntry
	var removedEntries []config.RemovedEntry

	switch req.Scope {
	case pb.PushScope_PUSH_SCOPE_COMPLETE:
		// FR39: validate before any write -- every failure found together,
		// each naming its entry index and field. removes/base are always
		// empty/nil under scope=COMPLETE (there is no base to check a
		// remove against, and removes itself was already refused above if
		// non-empty).
		if validation := config.Validate(adds, nil, nil, catalog, s.pollIntervalBounds); !validation.OK() {
			return nil, validationFailureError(validation)
		}

		// FR82.2: the payload is the board's entire desired sensor set,
		// stored as submitted -- every entry is authored, and there is no
		// base to materialise against.
		entries = adds
		for i := range entries {
			entries[i].Provenance = config.ProvenanceAuthored
		}

	case pb.PushScope_PUSH_SCOPE_EDIT:
		removeKeys, err := s.resolveRemoveKeys(ctx, req.Removes)
		if err != nil {
			return nil, err
		}

		// FR39: validate before any write, once base and removes are both
		// resolved -- catches every add-side check (I2C range, catalog,
		// poll_interval_ms, within-payload collision) plus FR82.4's two
		// removal-validation cases (a remove matching nothing in base; a
		// remove naming an unaddressable entry), all together. Deliberately
		// skipped when baseCfg == nil: with no base at all, a remove key's
		// "matches nothing" failure would be misleading noise ahead of
		// FR82.3's own, more specific refusal below (via
		// config.Materialise's ErrNoAcceptedConfig).
		if baseCfg != nil {
			if validation := config.Validate(adds, removeKeys, base, catalog, s.pollIntervalBounds); !validation.OK() {
				return nil, validationFailureError(validation)
			}
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
				s.logger.Error("materialise edit push failed", "device_id", deviceID, "error", err)
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
			"device_id", deviceID,
			"authored", len(adds),
			"removed", len(result.Removed),
			"total", len(entries))
	}

	// FR37/FR38: the diff between this board's prior accepted config and
	// the effective resulting payload, computed the same way
	// DiffConfigVersions computes one -- populated for every successful
	// push, dry_run or real. DiffRemoved is reachable here from either
	// scope: an EDIT push's materialised result (an entry dropped by
	// `removes`) or a COMPLETE push that simply omitted it (config.Diff's
	// own doc comment) -- which is exactly FR38's "a COMPLETE push that
	// drops an entry by accident shows the drop before it lands".
	diffs := config.Diff(base, entries)
	pbDiffs := make([]*pb.EntryDiff, len(diffs))
	for i, d := range diffs {
		pbDiffs[i] = entryDiffToProto(d)
	}

	// FR38: under scope=COMPLETE (which has no `removes` list of its own),
	// the named removal set is exactly the diff's DiffRemoved entries --
	// "these N entries would stop being polled", computed against the
	// effective resulting payload. Each is a distinct entry from omission,
	// not a chip-key group, so it is stated back as a full key.
	if req.Scope == pb.PushScope_PUSH_SCOPE_COMPLETE {
		for _, d := range diffs {
			if d.Kind == config.DiffRemoved {
				removedForResponse = append(removedForResponse, &pb.RemovedEntry{
					MuxPath:    d.Base.GetMuxPath(),
					I2CAddress: d.Base.GetI2CAddress(),
					SensorType: d.Base.GetSensorType(),
					Form:       pb.RemoveForm_REMOVE_FORM_FULL_KEY,
				})
			}
		}
	}

	// FR38: the effective resulting config this push produces -- the
	// board's complete new desired sensor set, not the partial payload the
	// request submitted (meaningful under scope=EDIT). writer.write below
	// stamps the assigned (real) or would-be (dry run) version onto this
	// same message.
	cfgProto := &configpb.DeviceConfig{
		DeviceId: deviceID,
		Sensors:  sensorsForStorage,
	}

	version, err := writer.write(ctx, t, cfgProto, entries, removedEntries)
	if err != nil {
		return nil, err
	}

	return &pb.BoardPushResult{
		DeviceId:        deviceID,
		Success:         true,
		Version:         version,
		Removed:         removedForResponse,
		EffectiveConfig: cfgProto,
		Diff:            pbDiffs,
	}, nil
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

// DiffConfigVersions computes FR37's server-side, per-entry diff between
// any two sides of a board's config history -- two stored versions, or a
// stored version against an unpushed draft (api.proto's ConfigSide doc
// comment). Never mutates anything: a draft side is materialised in
// memory (materialiseDraft, FR82's own scope semantics) purely to diff
// it, but is never stored or published -- the same "resolve, don't write"
// posture GetDeviceConfig takes.
func (s *LeafLabAPIServer) DiffConfigVersions(ctx context.Context, req *pb.DiffConfigVersionsRequest) (*pb.DiffConfigVersionsResponse, error) {
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("device_config", "device_id", reason)
	}

	// FR4.1: same board-reach check GetDeviceConfig uses -- "doesn't exist"
	// and "exists, out of scope" collapse to the same refusal (NFR2).
	if err := s.authorizeBoardAccess(ctx, req.DeviceId); err != nil {
		return nil, err
	}

	// FR37/FR82.1: no default side, never inferred -- an unset oneof is
	// refused before either side is resolved, the same "no default, never
	// inferred" posture PushScope takes.
	if req.GetFrom().GetSide() == nil {
		return nil, contract.InvalidArgument("diff_config_versions", "from", "A from side (version or draft) is required; there is no default.")
	}
	if req.GetTo().GetSide() == nil {
		return nil, contract.InvalidArgument("diff_config_versions", "to", "A to side (version or draft) is required; there is no default.")
	}

	fromCfg, err := s.resolveConfigSide(ctx, req.DeviceId, "from", req.From)
	if err != nil {
		return nil, err
	}
	toCfg, err := s.resolveConfigSide(ctx, req.DeviceId, "to", req.To)
	if err != nil {
		return nil, err
	}

	// FR37: Diff runs over two complete payloads, never a partial EDIT
	// payload -- both fromCfg/toCfg are already complete here, whether
	// resolved from a stored version (every stored payload is complete,
	// FR82) or materialised from a draft (materialiseDraft applies the
	// same FR82 scope semantics a real push would).
	fromEntries, err := s.resolveConfigEntries(ctx, fromCfg.GetSensors())
	if err != nil {
		return nil, err
	}
	toEntries, err := s.resolveConfigEntries(ctx, toCfg.GetSensors())
	if err != nil {
		return nil, err
	}

	diffs := config.Diff(fromEntries, toEntries)
	pbDiffs := make([]*pb.EntryDiff, len(diffs))
	for i, d := range diffs {
		pbDiffs[i] = entryDiffToProto(d)
	}

	return &pb.DiffConfigVersionsResponse{
		Entries: pbDiffs,
		// Both raw complete payloads the diff was computed from (FR37) --
		// a draft side is included here already materialised, exactly as
		// it was diffed, not as originally submitted.
		From: fromCfg,
		To:   toCfg,
	}, nil
}

// resolveConfigSide resolves one side of a DiffConfigVersionsRequest
// (field names the side for error messages, "from" or "to"): a stored
// version is fetched as-is (contract.NotFound if the version doesn't
// exist); a draft is materialised in memory via materialiseDraft. Callers
// must already have refused a nil side (req.GetFrom/To().GetSide() ==
// nil) before calling this -- the default branch below is defensive only.
func (s *LeafLabAPIServer) resolveConfigSide(ctx context.Context, deviceID, field string, side *pb.ConfigSide) (*configpb.DeviceConfig, error) {
	switch v := side.GetSide().(type) {
	case *pb.ConfigSide_Version:
		cfg, err := s.repo.GetConfigVersion(ctx, deviceID, v.Version)
		if err != nil {
			s.logger.Error("get config version failed", "device_id", deviceID, "version", v.Version, "error", err)
			return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
		}
		if cfg == nil {
			return nil, contract.NotFound("device_config", field, fmt.Sprintf("Version %d does not exist for this device.", v.Version))
		}
		return cfg, nil
	case *pb.ConfigSide_Draft:
		return s.materialiseDraft(ctx, deviceID, v.Draft)
	default:
		return nil, contract.InvalidArgument("diff_config_versions", field, "A "+field+" side (version or draft) is required; there is no default.")
	}
}

// materialiseDraft resolves an unpushed ConfigDraft into a complete
// *configpb.DeviceConfig, applying the same FR82 scope semantics
// PushDeviceConfig itself uses -- COMPLETE: the draft's sensors list, as
// submitted; EDIT: materialised against the board's current accepted
// config, exactly as an EDIT push would be -- but this never stores or
// publishes anything (DiffConfigVersions' own doc comment). FR39
// validation is deliberately not run here: this is a hypothetical "what
// would this look like", not a write to gate.
func (s *LeafLabAPIServer) materialiseDraft(ctx context.Context, deviceID string, draft *pb.ConfigDraft) (*configpb.DeviceConfig, error) {
	if draft.GetScope() == pb.PushScope_PUSH_SCOPE_UNSPECIFIED {
		return nil, contract.InvalidArgument(
			"diff_config_versions",
			"draft.scope",
			"A draft must state scope=COMPLETE or scope=EDIT; there is no default.",
		)
	}
	if draft.GetScope() == pb.PushScope_PUSH_SCOPE_COMPLETE && len(draft.GetRemoves()) > 0 {
		return nil, contract.InvalidArgument(
			"diff_config_versions",
			"draft.removes",
			"removes is only used with scope=EDIT; a scope=COMPLETE draft removes an entry by omitting it from sensors instead.",
		)
	}

	adds, err := s.resolveConfigEntries(ctx, draft.GetSensors())
	if err != nil {
		return nil, err
	}

	sensors := draft.GetSensors()

	if draft.GetScope() == pb.PushScope_PUSH_SCOPE_EDIT {
		baseCfg, err := s.repo.GetLatestAcceptedConfig(ctx, deviceID)
		if err != nil {
			s.logger.Error("get latest accepted config for draft failed", "device_id", deviceID, "error", err)
			return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
		}
		// base stays nil (not an empty, non-nil slice) when baseCfg == nil
		// -- see PushDeviceConfig's identical EDIT handling.
		var base []config.Entry
		if baseCfg != nil {
			base, err = s.resolveConfigEntries(ctx, baseCfg.Sensors)
			if err != nil {
				return nil, err
			}
		}

		removeKeys, err := s.resolveRemoveKeys(ctx, draft.GetRemoves())
		if err != nil {
			return nil, err
		}

		result, err := config.Materialise(base, adds, removeKeys)
		if err != nil {
			switch {
			case errors.Is(err, config.ErrNoAcceptedConfig):
				return nil, contract.Refuse(
					"device_config",
					"draft.scope",
					"This board has no accepted config to complete your edit from; send a complete push.",
					"Push scope=COMPLETE with this device's entire desired sensor set.",
				)
			case errors.Is(err, config.ErrUnaddressableRemove):
				return nil, contract.Refuse(
					"device_config",
					"draft.removes",
					"This entry has no I2C address on record and cannot be removed by an edit push.",
					"Push scope=COMPLETE with this entry omitted from the sensors list.",
				)
			default:
				s.logger.Error("materialise draft failed", "device_id", deviceID, "error", err)
				return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
			}
		}

		sensors = make([]*configpb.SensorConfig, len(result.Entries))
		for i, e := range result.Entries {
			sensors[i] = e.Sensor
		}
	}

	return &configpb.DeviceConfig{DeviceId: deviceID, Sensors: sensors}, nil
}

// entryDiffToProto translates one config.EntryDiff onto the wire. Rather
// than reconstruct mux_path/i2c_address/sensor_type from the canonical
// hwkey.Key (which carries a resolved SensorTypeID, not the wire
// firmware.SensorType enum), this reads them straight off whichever raw
// *configpb.SensorConfig is present -- Target for everything but
// DiffRemoved, Base for DiffRemoved (EntryDiff's own doc comment: Target
// is nil exactly when Kind is DiffRemoved).
func entryDiffToProto(d config.EntryDiff) *pb.EntryDiff {
	sensor := d.Target
	if sensor == nil {
		sensor = d.Base
	}
	return &pb.EntryDiff{
		Kind:       diffKindToProto(d.Kind),
		MuxPath:    sensor.GetMuxPath(),
		I2CAddress: sensor.GetI2CAddress(),
		SensorType: sensor.GetSensorType(),
	}
}

func diffKindToProto(k config.DiffKind) pb.DiffKind {
	switch k {
	case config.DiffAdded:
		return pb.DiffKind_DIFF_KIND_ADDED
	case config.DiffRemoved:
		return pb.DiffKind_DIFF_KIND_REMOVED
	case config.DiffChanged:
		return pb.DiffKind_DIFF_KIND_CHANGED
	case config.DiffUnchanged:
		return pb.DiffKind_DIFF_KIND_UNCHANGED
	default:
		return pb.DiffKind_DIFF_KIND_UNSPECIFIED
	}
}

// GetPushGroupStatus reads back FR48.1's "the resulting group's ack state
// is readable as a group afterwards": one AckState per board a
// PushDeviceConfig call actually pushed under pushGroupID (migration 034's
// idx_device_config_push_group_id -- a board that never reached storage,
// e.g. a per-board failure within a board-set push, was never linked to
// this group and so is absent here, not reported as SILENT). Read-only;
// never mutates anything. Not household-scoped today -- the push_group_id
// itself is only ever learned from this same caller's own prior
// PushDeviceConfig response, not guessable/enumerable, so this does not
// yet need FR4/NFR2's per-entity authorization; broader access-control
// hardening for this RPC is tracked separately if that assumption changes.
func (s *LeafLabAPIServer) GetPushGroupStatus(ctx context.Context, req *pb.GetPushGroupStatusRequest) (*pb.GetPushGroupStatusResponse, error) {
	if req.PushGroupId == 0 {
		return nil, contract.InvalidArgument("push_group", "push_group_id", "A push_group_id is required.")
	}

	group, found, err := s.repo.GetPushGroup(ctx, req.PushGroupId)
	if err != nil {
		s.logger.Error("get push group failed", "push_group_id", req.PushGroupId, "error", err)
		return nil, contract.Internal("push_group", "", "Could not process this request right now. Please try again.")
	}
	if !found {
		return nil, contract.NotFound("push_group", "push_group_id", "No push group matches this id.")
	}

	boardRows, err := s.repo.GetPushGroupBoards(ctx, req.PushGroupId)
	if err != nil {
		s.logger.Error("get push group boards failed", "push_group_id", req.PushGroupId, "error", err)
		return nil, contract.Internal("push_group", "", "Could not process this request right now. Please try again.")
	}

	boards := make([]*pb.BoardAckStatus, len(boardRows))
	for i, row := range boardRows {
		boards[i] = &pb.BoardAckStatus{
			DeviceId: row.DeviceID,
			State:    ackStateFromRow(row),
		}
	}

	return &pb.GetPushGroupStatusResponse{
		Boards:       boards,
		Reason:       group.Reason,
		PushedAt:     contract.ToInstant(group.PushedAt),
		ActorSubject: group.ActorSubject,
	}, nil
}

// ackStateFromRow classifies one PushGroupBoardRow's AckState (FR48.1), as
// of the moment GetPushGroupStatus is called -- not a value fixed at push
// time: AckedAt == nil means no ack has arrived yet (SILENT); once it has,
// Accepted decides ACKED vs REJECTED. Mirrors device_config's own
// current-config convention (migration 007's doc comment).
func ackStateFromRow(row PushGroupBoardRow) pb.AckState {
	if row.AckedAt == nil {
		return pb.AckState_ACK_STATE_SILENT
	}
	if row.Accepted {
		return pb.AckState_ACK_STATE_ACKED
	}
	return pb.AckState_ACK_STATE_REJECTED
}

// RollbackDeviceConfig is FR40's "rollback writes forward" RPC: it never
// mutates the device_config row at to_version or any other existing row --
// it only ever loads that version's complete stored payload (FR82
// guarantees completeness regardless of which scope produced it) and
// inserts it again as a new, higher version, recording
// device_config.derived_from_version. Unlike PushDeviceConfigRequest there
// is no legacy singular device_id field (api.proto's own doc comment) --
// every named board's outcome is always captured into its own
// BoardRollbackResult, never surfaced as this call's own RPC error, the
// same way PushDeviceConfig's board-set path (FR48.1) does.
func (s *LeafLabAPIServer) RollbackDeviceConfig(ctx context.Context, req *pb.RollbackDeviceConfigRequest) (*pb.RollbackDeviceConfigResponse, error) {
	if len(req.DeviceIds) == 0 {
		return nil, contract.InvalidArgument("device_config", "device_ids", "At least one device is required.")
	}
	for _, id := range req.DeviceIds {
		if reason := validateDeviceID(id); reason != "" {
			return nil, contract.InvalidArgument("device_config", "device_id", reason)
		}
	}

	// A rollback always states why -- unlike PushDeviceConfigRequest.reason
	// (required only once device_ids names more than one board), this is
	// required regardless of board-set size (api.proto's own doc comment
	// on RollbackDeviceConfigRequest.reason).
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return nil, contract.InvalidArgument("device_config", "reason", "A rollback requires a stated reason.")
	}

	// Resolve board identity and household once per target, exactly as
	// PushDeviceConfig does -- reused both by the FR48/FR5 household-spanning
	// check below and by each board's own rollback.
	targets := make([]boardTarget, len(req.DeviceIds))
	for i, id := range req.DeviceIds {
		boardID, err := s.repo.GetOrCreateBoard(ctx, id)
		if err != nil {
			s.logger.Error("board lookup failed", "device_id", id, "error", err)
			return nil, contract.Internal("board", "", "Could not process this request right now. Please try again.")
		}
		boardRes, err := s.authzSvc.Resolve(ctx, authz.EntityRef{Kind: authz.EntityBoard, ID: boardID})
		if err != nil {
			s.logger.Error("resolve board household failed", "device_id", id, "board_id", boardID, "error", err)
			return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
		}
		targets[i] = boardTarget{deviceID: id, boardID: boardID, household: boardRes}
	}

	// FR48/FR5/FR1.2: a board set spanning more than one household is
	// refused unless the caller's scope reaches every one of them -- same
	// guard PushDeviceConfig's own board set applies, since
	// RollbackDeviceConfig "accepts the same board set as an FR48
	// multi-board push" (this task's own requirement text).
	if len(targets) > 1 {
		if err := s.checkBoardSetHouseholdReach(ctx, targets); err != nil {
			return nil, err
		}
	}

	// FR38-style dry_run: a noopRollbackWriter cannot reach storage or the
	// publisher at all -- structural, not a boolean re-checked at each call
	// site below -- so a preview covers exactly the same blast radius a
	// real rollback would without writing or publishing anything.
	var writer rollbackWriter
	if req.DryRun {
		writer = &noopRollbackWriter{repo: s.repo}
	} else {
		writer = &liveRollbackWriter{repo: s.repo, publisher: s.publisher, logger: s.logger, reason: reason, correlationID: CorrelationIDFromContext(ctx)}
	}

	// One result per targeted board, never collapsed to an aggregate --
	// mirrors PushDeviceConfig's own FR48.1 board-set path exactly, except
	// there is no legacy single-board fallback to preserve here (this RPC
	// never had one).
	results := make([]*pb.BoardRollbackResult, len(targets))
	for i, t := range targets {
		result, err := s.rollbackOneBoard(ctx, t, req.ToVersion, writer)
		if err != nil {
			failure, ok := contract.FromError(err)
			if !ok {
				s.logger.Error("rollback board failed with no Failure detail", "device_id", t.deviceID, "error", err)
				failure = &pb.Failure{Class: string(contract.FailureInternal), Reason: "Could not process this request right now. Please try again."}
			}
			result = &pb.BoardRollbackResult{DeviceId: t.deviceID, Success: false, Failure: failure}
		}
		results[i] = result
	}

	return &pb.RollbackDeviceConfigResponse{
		Results:      results,
		Reason:       reason,
		RolledBackAt: contract.Now(),
		ActorSubject: actingSubject(ctx),
	}, nil
}

// rollbackWriter is RollbackDeviceConfig's persistence/publish step,
// separated from rollbackOneBoard's load/diff logic exactly the way
// configWriter is separated from pushOneBoard -- so dry_run's "cannot
// reach the writer" is structural. write returns the version this board's
// rollback assigned (real) or would assign (dry run), and stamps it onto
// cfgProto.Version so BoardRollbackResult.effective_config always carries
// it.
type rollbackWriter interface {
	write(ctx context.Context, t boardTarget, configJSON []byte, cfgProto *configpb.DeviceConfig, entries []config.Entry, toVersion uint64) (version uint64, err error)
}

// liveRollbackWriter is rollbackWriter's real implementation: it stores
// the device_config row (with derived_from_version set to toVersion) and
// publishes the resulting config over MQTT, exactly as liveConfigWriter
// does for an ordinary push -- but passing configJSON through verbatim
// (see ConfigVersionRow.ConfigJSON's doc comment) rather than
// re-marshaling cfgProto, so the stored payload is byte-identical to
// toVersion's own (FR40's restore guarantee).
type liveRollbackWriter struct {
	repo          deviceRepository
	publisher     *rmq.Publisher
	logger        *slog.Logger
	reason        string
	correlationID string
}

func (w *liveRollbackWriter) write(ctx context.Context, t boardTarget, configJSON []byte, cfgProto *configpb.DeviceConfig, entries []config.Entry, toVersion uint64) (uint64, error) {
	var targetHouseholdID *int64
	if !t.household.Unclaimed {
		hh := t.household.HouseholdID
		targetHouseholdID = &hh
	}

	// FR8/FR40: the reason, the source version and the new version --
	// entry.EntityID is filled in with the new version by
	// InsertDeviceConfigNextVersion itself (matching PushConfig's own
	// convention); NewRollbackEntry records toVersion in Reason, the only
	// other free-text slot Entry carries.
	entry := audit.NewRollbackEntry(actingSubject(ctx), audit.ActorKindHuman, targetHouseholdID, toVersion, w.reason, w.correlationID)

	derivedFrom := int64(toVersion)
	version, err := w.repo.InsertDeviceConfigNextVersion(ctx, t.boardID, configJSON, entries, nil, entry, nil, &derivedFrom)
	if err != nil {
		w.logger.Error("record rollback failed", "device_id", t.deviceID, "to_version", toVersion, "error", err)
		return 0, contract.Internal("device_config", "", "Could not record this rollback right now. Please try again.")
	}

	cfgProto.Version = uint64(version)
	wire, err := proto.Marshal(cfgProto)
	if err != nil {
		w.logger.Error("proto marshal failed", "device_id", t.deviceID, "error", err)
		return 0, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}

	routingKey := fmt.Sprintf("leaflab.%s.config", strings.ReplaceAll(t.deviceID, "/", "."))
	if err := w.publisher.Publish(ctx, mqttExchange, routingKey, wire); err != nil {
		// Row is in DB but publish failed -- device never received the
		// rollback. The row stays accepted=FALSE, which is correct: no ack
		// will arrive.
		w.logger.Error("publish rollback config failed", "device_id", t.deviceID, "error", err)
		return 0, contract.Internal("device_config", "", "Rollback was recorded but could not be delivered to the device. Please try again.")
	}

	w.logger.Info("device config rolled back",
		"device_id", t.deviceID,
		"to_version", toVersion,
		"version", version,
		"sensors", len(cfgProto.Sensors))
	return uint64(version), nil
}

// noopRollbackWriter is rollbackWriter's dry-run implementation, mirroring
// noopConfigWriter: it holds no *rmq.Publisher and never calls
// InsertDeviceConfigNextVersion -- structurally, not merely by choosing not
// to -- so a dry run cannot store a device_config row or publish anything.
type noopRollbackWriter struct {
	repo deviceRepository
}

func (w *noopRollbackWriter) write(ctx context.Context, t boardTarget, configJSON []byte, cfgProto *configpb.DeviceConfig, entries []config.Entry, toVersion uint64) (uint64, error) {
	next, err := w.repo.PeekNextConfigVersion(ctx, t.boardID)
	if err != nil {
		return 0, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	cfgProto.Version = uint64(next)
	return uint64(next), nil
}

// rollbackOneBoard runs t's board through FR40's rollback logic: load
// to_version's complete stored payload verbatim (never re-validated or
// re-materialised -- it was already validated and accepted as a stored
// payload the moment it was originally pushed, per FR82's completeness
// guarantee), diff it against the board's current accepted config for the
// response (the same way pushOneBoard computes BoardPushResult.diff), then
// hand off to writer for persistence/publish (or, under dry_run, its
// no-op equivalent).
func (s *LeafLabAPIServer) rollbackOneBoard(ctx context.Context, t boardTarget, toVersion uint64, writer rollbackWriter) (*pb.BoardRollbackResult, error) {
	deviceID := t.deviceID

	row, found, err := s.repo.GetConfigVersionRow(ctx, deviceID, toVersion)
	if err != nil {
		s.logger.Error("get config version row failed", "device_id", deviceID, "to_version", toVersion, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	if !found {
		return nil, contract.NotFound(
			"device_config",
			"to_version",
			fmt.Sprintf("Version %d does not exist for this device.", toVersion),
		)
	}

	var sourceCfg configpb.DeviceConfig
	if err := protojson.Unmarshal(row.ConfigJSON, &sourceCfg); err != nil {
		s.logger.Error("unmarshal source config version failed", "device_id", deviceID, "to_version", toVersion, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}

	// FR40 re-pushes to_version's payload as scope=COMPLETE: every entry is
	// authored, exactly as an ordinary scope=COMPLETE push's own entries
	// are (see pushOneBoard's COMPLETE branch) -- there is no base to
	// materialise against, and no removes list.
	entries, err := s.resolveConfigEntries(ctx, sourceCfg.Sensors)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].Provenance = config.ProvenanceAuthored
	}

	// FR37/FR38-style diff: this board's prior accepted config vs. the
	// effective resulting payload, computed the same way pushOneBoard
	// computes BoardPushResult.diff/removed.
	baseCfg, err := s.repo.GetLatestAcceptedConfig(ctx, deviceID)
	if err != nil {
		s.logger.Error("get latest accepted config failed", "device_id", deviceID, "error", err)
		return nil, contract.Internal("device_config", "", "Could not process this request right now. Please try again.")
	}
	var base []config.Entry
	if baseCfg != nil {
		base, err = s.resolveConfigEntries(ctx, baseCfg.Sensors)
		if err != nil {
			return nil, err
		}
	}

	diffs := config.Diff(base, entries)
	pbDiffs := make([]*pb.EntryDiff, len(diffs))
	for i, d := range diffs {
		pbDiffs[i] = entryDiffToProto(d)
	}

	var removedForResponse []*pb.RemovedEntry
	for _, d := range diffs {
		if d.Kind == config.DiffRemoved {
			removedForResponse = append(removedForResponse, &pb.RemovedEntry{
				MuxPath:    d.Base.GetMuxPath(),
				I2CAddress: d.Base.GetI2CAddress(),
				SensorType: d.Base.GetSensorType(),
				Form:       pb.RemoveForm_REMOVE_FORM_FULL_KEY,
			})
		}
	}

	// FR40's restore guarantee: cfgProto.Sensors is to_version's own
	// sensors list, unchanged -- writer.write below stores row.ConfigJSON
	// verbatim (not a re-marshal of this message), so the stored payload
	// and this response field describe the identical result.
	cfgProto := &configpb.DeviceConfig{
		DeviceId: deviceID,
		Sensors:  sourceCfg.Sensors,
	}

	version, err := writer.write(ctx, t, row.ConfigJSON, cfgProto, entries, toVersion)
	if err != nil {
		return nil, err
	}

	return &pb.BoardRollbackResult{
		DeviceId:            deviceID,
		Success:             true,
		Version:             version,
		DerivedFromVersion:  toVersion,
		SourceNeverAccepted: !row.Accepted,
		Removed:             removedForResponse,
		EffectiveConfig:     cfgProto,
		Diff:                pbDiffs,
	}, nil
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
