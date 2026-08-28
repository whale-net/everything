package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	"github.com/whale-net/everything/leaflab/api/health"
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
//
// Despite the name (kept to minimize diff churn from #1337's scaffold),
// this interface now also carries the households.go methods #1341's six
// RPCs depend on -- it is the one repository seam LeafLabAPIServer holds,
// not "device rows only".
type deviceRepository interface {
	GetOrCreateBoard(ctx context.Context, deviceID string) (int64, error)
	InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte, entry audit.Entry) (int64, error)
	GetLatestAcceptedConfig(ctx context.Context, deviceID string) (*configpb.DeviceConfig, error)
	// ListBoards is household-scoped (FR5.1): scope's SQL fragment
	// (Scope.Filter) is applied inside the query itself, never as a
	// post-filter -- see Repository.ListBoards.
	ListBoards(ctx context.Context, afterBoardID int64, hasAfter bool, limit int32, scope authz.Scope) ([]BoardRow, error)
	Ping(ctx context.Context) error

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

	// Households and membership (FR75, FR7, #1341) -- see households.go.
	GetHouseholdByID(ctx context.Context, householdID int64) (HouseholdRow, error)
	ListHouseholdMembers(ctx context.Context, householdID int64, afterMembershipID int64, hasAfter bool, limit int32) ([]HouseholdMembershipRow, error)
	// IsCurrentHouseholdMember is FR7's member-only authorization primitive
	// -- never authz.Scope, which will eventually include grants (FR7) and
	// elevation (FR10) as well; see requireHouseholdMember.
	IsCurrentHouseholdMember(ctx context.Context, householdID int64, principalSubject string) (bool, error)
	CreateHousehold(ctx context.Context, principalSubject, name string, entry audit.Entry) (HouseholdRow, error)
	InviteMember(ctx context.Context, householdID int64, principalSubject string, entry audit.Entry) (HouseholdMembershipRow, error)
	RemoveMember(ctx context.Context, householdID int64, principalSubject string, entry audit.Entry) error
	RenameHousehold(ctx context.Context, householdID int64, name string, entry audit.Entry) (HouseholdRow, error)
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
}

// DefaultElevationDuration is FR10.1's 60-minute elevation window, used
// unless a ServerOption overrides it (see WithElevationDuration). main.go
// wires LEAFLAB_ADMIN_ELEVATION_MINUTES to that option, so the window is
// configurable in the sense FR10.1 asks for -- documented here and at the
// env var's read site in main.go, since leaflab/api has no ENV.md of its
// own yet (unlike migrate/processor/ui).
const DefaultElevationDuration = 60 * time.Minute

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

func NewLeafLabAPIServer(repo deviceRepository, authzSvc authzResolver, publisher *rmq.Publisher, rmqConn *rmq.Connection, logger *slog.Logger, opts ...ServerOption) *LeafLabAPIServer {
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
	//
	// TargetHouseholdID is left nil: PushDeviceConfig doesn't yet resolve a
	// board to its owning household (that's FR1.1/NFR2 scoping, #1339's
	// job) -- wiring a value in ahead of that scoping risks a wrong
	// household silently shipping, which is worse than the field staying
	// nil until the real resolution lands. Action/EntityKind come from
	// auditRegistrations (audit_registry.go) rather than being repeated as
	// literals here, so the two can't drift out of agreement.
	reg := auditRegistrations[pushDeviceConfigFullMethod]
	entry := audit.Entry{
		ActorSubject:  actingSubject(ctx),
		ActorKind:     audit.ActorKindHuman,
		Action:        reg.Action,
		EntityKind:    reg.EntityKind,
		CorrelationID: CorrelationIDFromContext(ctx),
	}
	version, err := s.repo.InsertDeviceConfigNextVersion(ctx, boardID, configJSON, entry)
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


// --- Households and membership (FR75, FR7) --------------------------------

// householdNotFoundFailure is the one contract.NotFound value returned for
// a household that doesn't exist and for a household that exists but falls
// outside the caller's Scope -- same NFR2-style masking as
// boardNotFoundFailure above, applied to households: a stranger asking
// about a real household_id they don't belong to gets an identical
// response to a made-up id.
func householdNotFoundFailure() error {
	return contract.NotFound("household", "household_id", "No household matches this id.")
}

// authorizeHouseholdAccess checks the caller's Scope permits householdID,
// for the two read RPCs (GetHousehold, ListHouseholdMembers). Unlike
// authorizeBoardAccess, this needs no authzSvc.Resolve round trip: a
// household_id in the request *is* the Resolution's HouseholdID already --
// there is no separate table lookup that maps one to the other, the way
// device_id maps to a board's household_id. Scope (not
// IsCurrentHouseholdMember) is deliberately the check here: FR7 defines
// household reads as an ordinary "member capability" (member-or-grantee),
// not one of FR7's three member-only exclusions -- see
// requireHouseholdMember below for the write-side check that must NOT use
// Scope.
func (s *LeafLabAPIServer) authorizeHouseholdAccess(ctx context.Context, householdID int64) error {
	scope, err := s.scopeForCaller(ctx)
	if err != nil {
		s.logger.Error("resolve caller scope failed", "household_id", householdID, "error", err)
		return contract.Internal("household", "", "Could not process this request right now. Please try again.")
	}
	ref := authz.EntityRef{Kind: authz.EntityHousehold, ID: householdID}
	res := authz.Resolution{HouseholdID: householdID}
	if !scope.Permits(ref, res) {
		return householdNotFoundFailure()
	}
	return nil
}

// requireHouseholdMember is FR7's member-only authorization check for
// InviteMember/RemoveMember/RenameHousehold: "membership change is one of
// the three exclusions" -- a grantee or an elevated admin must be refused
// here even though either may hold a Scope that otherwise permits reading
// or writing within this household. This is why the check below queries
// household_membership directly (Repository.IsCurrentHouseholdMember)
// rather than consulting authz.Scope: Scope will eventually be widened by
// a grant (FR7) or elevation (FR10) Scope implementation composed into the
// caller's UnionScope, at which point a Scope-based check here would
// silently start permitting exactly what FR7 forbids.
func (s *LeafLabAPIServer) requireHouseholdMember(ctx context.Context, householdID int64, actorSubject string) error {
	isMember, err := s.repo.IsCurrentHouseholdMember(ctx, householdID, actorSubject)
	if err != nil {
		s.logger.Error("check household membership failed", "household_id", householdID, "error", err)
		return contract.Internal("household", "", "Could not process this request right now. Please try again.")
	}
	if !isMember {
		return contract.PermissionDenied("household", "", "Only a current member of this household can make this change.")
	}
	return nil
}

// householdAuditEntry builds the audit.Entry common to every household
// write RPC below: actor is always the authenticated caller (never a
// request field -- FR8 names the acting principal, not a caller-asserted
// one), Action/EntityKind come from auditRegistrations so the RPC's
// registered audit contract and what actually gets written can't drift
// apart (see audit_registry.go).
func householdAuditEntry(ctx context.Context, fullMethod string) audit.Entry {
	reg := auditRegistrations[fullMethod]
	return audit.Entry{
		ActorSubject:  actingSubject(ctx),
		ActorKind:     audit.ActorKindHuman,
		Action:        reg.Action,
		EntityKind:    reg.EntityKind,
		CorrelationID: CorrelationIDFromContext(ctx),
	}
}

func toHouseholdProto(h HouseholdRow) *pb.Household {
	return &pb.Household{HouseholdId: h.HouseholdID, Name: h.Name}
}

func toHouseholdMemberProto(m HouseholdMembershipRow) *pb.HouseholdMember {
	return &pb.HouseholdMember{
		PrincipalSubject: m.PrincipalSubject,
		JoinedAt:         contract.ToInstant(m.ValidFrom),
	}
}

// CreateHousehold gives the calling principal a new household with them as
// its sole initial member (FR75, FR76). Reachable only for a principal with
// no current household -- Repository.CreateHousehold refuses
// (ErrPrincipalAlreadyHasHousehold) a principal who already has one, per
// api.proto's CreateHousehold doc comment; that refusal uses FR59.3's
// refuse-and-name-the-alternative shape rather than an ordinary error, so a
// caller who mistakenly retries this RPC is told what to do instead.
func (s *LeafLabAPIServer) CreateHousehold(ctx context.Context, req *pb.CreateHouseholdRequest) (*pb.CreateHouseholdResponse, error) {
	actor := actingSubject(ctx)
	entry := householdAuditEntry(ctx, createHouseholdFullMethod)

	household, err := s.repo.CreateHousehold(ctx, actor, req.GetName(), entry)
	if err != nil {
		if errors.Is(err, ErrPrincipalAlreadyHasHousehold) {
			return nil, contract.Refuse("household", "", "You already belong to a household.",
				"Use your existing household instead of creating a new one.")
		}
		s.logger.Error("create household failed", "actor", actor, "error", err)
		return nil, contract.Internal("household", "", "Could not create a household right now. Please try again.")
	}

	return &pb.CreateHouseholdResponse{Household: toHouseholdProto(household)}, nil
}

// GetHousehold returns a household's id and display name, scoped to the
// caller's reach (authorizeHouseholdAccess).
func (s *LeafLabAPIServer) GetHousehold(ctx context.Context, req *pb.GetHouseholdRequest) (*pb.GetHouseholdResponse, error) {
	if err := s.authorizeHouseholdAccess(ctx, req.GetHouseholdId()); err != nil {
		return nil, err
	}

	household, err := s.repo.GetHouseholdByID(ctx, req.GetHouseholdId())
	if err != nil {
		if errors.Is(err, ErrHouseholdNotFound) {
			// Scope already passed (authorizeHouseholdAccess above) --
			// reaching a real not-found here would mean the caller's own
			// Scope names a household that no longer resolves, which
			// should not happen in practice. Same masked failure regardless.
			return nil, householdNotFoundFailure()
		}
		s.logger.Error("get household failed", "household_id", req.GetHouseholdId(), "error", err)
		return nil, contract.Internal("household", "", "Could not look up this household right now. Please try again.")
	}
	return &pb.GetHouseholdResponse{Household: toHouseholdProto(household)}, nil
}

// ListHouseholdMembers lists a household's current members, keyset
// paginated (FR61), scoped to the caller's reach (authorizeHouseholdAccess).
func (s *LeafLabAPIServer) ListHouseholdMembers(ctx context.Context, req *pb.ListHouseholdMembersRequest) (*pb.ListHouseholdMembersResponse, error) {
	if err := s.authorizeHouseholdAccess(ctx, req.GetHouseholdId()); err != nil {
		return nil, err
	}

	afterMembershipID, hasAfter, err := contract.DecodeHouseholdMemberCursor(req.GetPage().GetPageToken())
	if err != nil {
		return nil, contract.InvalidArgument("list_household_members_request", "page_token", "This page link is no longer valid. Start again from the first page.")
	}

	limit := contract.ClampPageSize(req.GetPage().GetPageSize())

	rows, err := s.repo.ListHouseholdMembers(ctx, req.GetHouseholdId(), afterMembershipID, hasAfter, limit+1)
	if err != nil {
		s.logger.Error("list household members failed", "household_id", req.GetHouseholdId(), "error", err)
		return nil, contract.Internal("household", "", "Could not list this household's members right now. Please try again.")
	}

	var nextToken string
	if int32(len(rows)) > limit {
		rows = rows[:limit]
		nextToken = contract.EncodeHouseholdMemberCursor(rows[len(rows)-1].HouseholdMembershipID)
	}

	members := make([]*pb.HouseholdMember, 0, len(rows))
	for _, r := range rows {
		members = append(members, toHouseholdMemberProto(r))
	}
	return &pb.ListHouseholdMembersResponse{
		Members:   members,
		Page:      &pb.PageResponse{NextPageToken: nextToken},
		ServerNow: contract.Now(),
	}, nil
}

// InviteMember adds another principal to a household (FR75). Member-only
// (FR7): requireHouseholdMember refuses a grantee or an elevated admin even
// though either may otherwise read/write within the household.
func (s *LeafLabAPIServer) InviteMember(ctx context.Context, req *pb.InviteMemberRequest) (*pb.InviteMemberResponse, error) {
	actor := actingSubject(ctx)
	if err := s.requireHouseholdMember(ctx, req.GetHouseholdId(), actor); err != nil {
		return nil, err
	}

	entry := householdAuditEntry(ctx, inviteMemberFullMethod)
	member, err := s.repo.InviteMember(ctx, req.GetHouseholdId(), req.GetPrincipalSubject(), entry)
	if err != nil {
		if errors.Is(err, ErrHouseholdAlreadyMember) {
			return nil, contract.Refuse("household_member", "principal_subject", "This principal is already a member of this household.",
				"No action needed -- they can already access this household.")
		}
		s.logger.Error("invite member failed", "household_id", req.GetHouseholdId(), "actor", actor, "error", err)
		return nil, contract.Internal("household_member", "", "Could not add this member right now. Please try again.")
	}

	return &pb.InviteMemberResponse{Member: toHouseholdMemberProto(member)}, nil
}

// RemoveMember removes a member from a household (FR75). A household never
// reaches zero members: removing the last remaining member is refused with
// FR59.3's refuse-and-name-the-alternative shape instead of succeeding.
// Member-only (FR7), same exclusion as InviteMember.
func (s *LeafLabAPIServer) RemoveMember(ctx context.Context, req *pb.RemoveMemberRequest) (*pb.RemoveMemberResponse, error) {
	actor := actingSubject(ctx)
	if err := s.requireHouseholdMember(ctx, req.GetHouseholdId(), actor); err != nil {
		return nil, err
	}

	entry := householdAuditEntry(ctx, removeMemberFullMethod)
	err := s.repo.RemoveMember(ctx, req.GetHouseholdId(), req.GetPrincipalSubject(), entry)
	if err != nil {
		if errors.Is(err, ErrHouseholdLastMember) {
			return nil, contract.Refuse("household_member", "principal_subject", "This is the last member of this household.",
				"Invite someone else first, or release this household's boards instead.")
		}
		if errors.Is(err, ErrHouseholdNotMember) {
			return nil, contract.NotFound("household_member", "principal_subject", "This principal is not a member of this household.")
		}
		s.logger.Error("remove member failed", "household_id", req.GetHouseholdId(), "actor", actor, "error", err)
		return nil, contract.Internal("household_member", "", "Could not remove this member right now. Please try again.")
	}

	return &pb.RemoveMemberResponse{}, nil
}

// RenameHousehold changes a household's display name. Member-only (FR7),
// same exclusion as InviteMember/RemoveMember.
func (s *LeafLabAPIServer) RenameHousehold(ctx context.Context, req *pb.RenameHouseholdRequest) (*pb.RenameHouseholdResponse, error) {
	actor := actingSubject(ctx)
	if err := s.requireHouseholdMember(ctx, req.GetHouseholdId(), actor); err != nil {
		return nil, err
	}

	entry := householdAuditEntry(ctx, renameHouseholdFullMethod)
	household, err := s.repo.RenameHousehold(ctx, req.GetHouseholdId(), req.GetName(), entry)
	if err != nil {
		if errors.Is(err, ErrHouseholdNotFound) {
			return nil, householdNotFoundFailure()
		}
		s.logger.Error("rename household failed", "household_id", req.GetHouseholdId(), "actor", actor, "error", err)
		return nil, contract.Internal("household", "", "Could not rename this household right now. Please try again.")
	}

	return &pb.RenameHouseholdResponse{Household: toHouseholdProto(household)}, nil
}
