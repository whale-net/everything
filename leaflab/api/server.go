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
	"github.com/whale-net/everything/leaflab/api/claim"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
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

	// Board claim (FR76, #1342) -- see claim.go. OpenClaimChallenge never
	// queries board (requirement 1); CompleteClaim is the one write among
	// these four, auditedWrite-backed like the households.go writes above.
	OpenClaimChallenge(ctx context.Context, principalSubject, deviceID string, cfg claim.Config) (ClaimChallengeRow, error)
	MarkClaimRound(ctx context.Context, handle string, cfg claim.Config) error
	GetClaimChallengeStatus(ctx context.Context, handle string, cfg claim.Config) (waiting bool, err error)
	CompleteClaim(ctx context.Context, principalSubject, handle string, cfg claim.Config, entry audit.Entry) (HouseholdRow, error)

	// Ownership closure, release and transfer (FR70.2-.4, FR77, #1343) --
	// see closure.go. PreviewClosure is read-only (a thin wrapper over
	// ComputeClosure); ReleaseBoard and TransferClosure are auditedWrite
	// writes, matching the households.go/claim.go entry-in/entry-mutated
	// convention above.
	PreviewClosure(ctx context.Context, boardID int64) (Closure, error)
	ReleaseBoard(ctx context.Context, boardID int64, principalSubject, reason string, entry audit.Entry) (releaseToken string, err error)
	TransferClosure(ctx context.Context, boardID, destinationHouseholdID int64, releaseToken, dischargedChallengeHandle, actorSubject, reason string, entry audit.Entry) (HouseholdRow, error)
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
	// claimConfig is FR76's A28 configuration (leaflab/api/claim.Config),
	// threaded through to every board-claim handler below rather than read
	// from the environment per-call.
	claimConfig claim.Config
	// limiter backs the board-claim handlers' NFR10 enforcement
	// (claim_open/claim_round buckets), keyed on a composite of principal
	// and device_id/challenge_handle -- see OpenClaimChallenge/MarkClaimRound
	// below for why this can't go through the generic per-method interceptor
	// (ratelimit_interceptor.go), which only ever derives a principal-only
	// key.
	limiter ratelimit.Limiter
}

func NewLeafLabAPIServer(repo deviceRepository, authzSvc authzResolver, publisher *rmq.Publisher, rmqConn *rmq.Connection, logger *slog.Logger, claimConfig claim.Config, limiter ratelimit.Limiter) *LeafLabAPIServer {
	return &LeafLabAPIServer{
		repo:        repo,
		authzSvc:    authzSvc,
		publisher:   publisher,
		rmqConn:     rmqConn,
		logger:      logger,
		claimConfig: claimConfig,
		limiter:     limiter,
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

// boardIDNotFoundFailure is boardNotFoundFailure's board_id-keyed sibling,
// for the closure RPCs (PreviewClosure, ReleaseBoard) which take board_id
// directly rather than device_id -- same NFR2-style masking, naming the
// field a caller of these RPCs actually supplied.
func boardIDNotFoundFailure() error {
	return contract.NotFound("board", "board_id", "No board matches this id.")
}

// authorizeBoardIDAccess is authorizeBoardAccess's board_id-keyed sibling:
// same one-query resolve-and-check shape (NFR2), used by PreviewClosure
// (and any future closure RPC gated the same way) instead of
// authorizeBoardAccess, which only accepts a device_id.
func (s *LeafLabAPIServer) authorizeBoardIDAccess(ctx context.Context, boardID int64) error {
	scope, err := s.scopeForCaller(ctx)
	if err != nil {
		s.logger.Error("resolve caller scope failed", "board_id", boardID, "error", err)
		return contract.Internal("board", "", "Could not process this request right now. Please try again.")
	}

	ref := authz.EntityRef{Kind: authz.EntityBoard, ID: boardID}
	res, err := s.authzSvc.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			return boardIDNotFoundFailure()
		}
		s.logger.Error("resolve board failed", "board_id", boardID, "error", err)
		return contract.Internal("board", "", "Could not process this request right now. Please try again.")
	}
	if !scope.Permits(ref, res) {
		return boardIDNotFoundFailure()
	}
	return nil
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

// --- Board claim (FR76) ---------------------------------------------------

// claimInstructions is OpenClaimChallengeResponse.instructions (requirement
// 8): names the identifier as what's printed on the board, never as
// "device id", and frames the round mechanic without disclosing any
// per-round outcome. rounds_required is carried as its own response field
// rather than interpolated here, so this string stays static regardless of
// A28's configured r.
const claimInstructions = "Find the ID printed on your board (not a technical \"device ID\") and keep this page open. " +
	"When you're ready, mark a round, then unplug and replug the board's power. Repeat for each round -- you have plenty of time to walk to the greenhouse and back."

// claimOpenRateLimitKey/claimRoundRateLimitKey build NFR10's composite keys
// for the claim_open/claim_round buckets (requirement 2: "keyed on the
// submitted device_id and on the calling principal"). Built from plain
// strings only -- no board query -- so Allow's oracle-safety guarantee
// (identical behavior whether or not the key resolves to anything) holds
// unchanged.
//
// claimRoundRateLimitKey uses challenge_handle rather than device_id:
// MarkClaimRoundRequest carries no device_id, and handle is already a 1:1,
// caller-held stand-in for (principal, device_id) established at
// OpenClaimChallenge -- resolving handle to device_id first would cost an
// extra query for no oracle-safety benefit, since handle itself is already
// unguessable and scoped to one principal/device pair. Flagged here as a
// deliberate substitution for requirement 2's literal wording.
func claimOpenRateLimitKey(principalSubject, deviceID string) ratelimit.Key {
	return ratelimit.Key("claim_open:" + principalSubject + ":" + deviceID)
}

func claimRoundRateLimitKey(principalSubject, challengeHandle string) ratelimit.Key {
	return ratelimit.Key("claim_round:" + principalSubject + ":" + challengeHandle)
}

func claimRateLimitedFailure(retryAfter time.Duration) error {
	return contract.RateLimitedWithRetry("claim_challenge", "", "Too many claim attempts. Try again shortly.", retryAfter)
}

// claimAuditEntry builds CompleteClaim's audit.Entry, matching
// householdAuditEntry's shape/precedent: actor is always the authenticated
// caller, Action/EntityKind come from auditRegistrations so the registered
// audit contract and what's actually written can't drift apart.
func claimAuditEntry(ctx context.Context) audit.Entry {
	reg := auditRegistrations[completeClaimFullMethod]
	return audit.Entry{
		ActorSubject:  actingSubject(ctx),
		ActorKind:     audit.ActorKindHuman,
		Action:        reg.Action,
		EntityKind:    reg.EntityKind,
		CorrelationID: CorrelationIDFromContext(ctx),
	}
}

// OpenClaimChallenge opens a possession challenge against device_id
// (requirement 1). validateDeviceID is the *only* check on device_id this
// handler performs before delegating to the repository -- syntactic shape,
// never existence -- and s.repo.OpenClaimChallenge itself never queries
// board (see claim.go's doc comment). Status, body shape and field set are
// therefore identical for a never-claimed, Unadopted, owned or nonexistent
// device_id; only the rate-limit/cooldown/concurrency refusals below differ
// the response, and those are keyed purely on (principal, device_id) as
// submitted, never on what device_id resolves to.
func (s *LeafLabAPIServer) OpenClaimChallenge(ctx context.Context, req *pb.OpenClaimChallengeRequest) (*pb.OpenClaimChallengeResponse, error) {
	if reason := validateDeviceID(req.GetDeviceId()); reason != "" {
		return nil, contract.InvalidArgument("claim_challenge", "device_id", reason)
	}

	actor := actingSubject(ctx)

	if allowed, retryAfter := s.limiter.Allow(ctx, claimOpenRateLimitKey(actor, req.GetDeviceId()), ratelimit.BucketClaimOpen); !allowed {
		return nil, claimRateLimitedFailure(retryAfter)
	}

	row, err := s.repo.OpenClaimChallenge(ctx, actor, req.GetDeviceId(), s.claimConfig)
	if err != nil {
		switch {
		case errors.Is(err, ErrClaimCooldownActive):
			return nil, contract.Refuse("claim_challenge", "device_id",
				"You recently tried to claim this board and need to wait before trying again.",
				"Ask a household member to invite you instead (FR75), or wait for the cooldown to end.")
		case errors.Is(err, ErrClaimTooManyOpenChallenges):
			return nil, contract.Refuse("claim_challenge", "",
				"You have too many claim attempts in progress.",
				"Finish or let an existing claim attempt expire before starting another.")
		}
		s.logger.Error("open claim challenge failed", "actor", actor, "error", err)
		return nil, contract.Internal("claim_challenge", "", "Could not start a claim attempt right now. Please try again.")
	}

	return &pb.OpenClaimChallengeResponse{
		ChallengeHandle: row.Handle,
		Instructions:    claimInstructions,
		RoundsRequired:  row.RoundsRequired,
		ExpiresAt:       contract.ToInstant(row.ExpiresAt),
		ServerNow:       contract.Now(),
	}, nil
}

// MarkClaimRound marks the start of the challenge's next discharge round
// (requirement 3). The response is a uniform acknowledgement regardless of
// internal state -- an invalid/expired/already-discharged/attempts-exhausted
// challenge all ack identically; only a challenge_handle naming no row at
// all (ErrClaimChallengeNotFound, a caller-error class distinct from FR76's
// device_id oracle -- see claim.go) is reported differently.
func (s *LeafLabAPIServer) MarkClaimRound(ctx context.Context, req *pb.MarkClaimRoundRequest) (*pb.MarkClaimRoundResponse, error) {
	if req.GetChallengeHandle() == "" {
		return nil, contract.InvalidArgument("claim_challenge", "challenge_handle", "A challenge handle is required.")
	}

	actor := actingSubject(ctx)

	if allowed, retryAfter := s.limiter.Allow(ctx, claimRoundRateLimitKey(actor, req.GetChallengeHandle()), ratelimit.BucketClaimRound); !allowed {
		return nil, claimRateLimitedFailure(retryAfter)
	}

	if err := s.repo.MarkClaimRound(ctx, req.GetChallengeHandle(), s.claimConfig); err != nil {
		if errors.Is(err, ErrClaimChallengeNotFound) {
			return nil, contract.NotFound("claim_challenge", "challenge_handle", "This claim attempt could not be found. Start a new one.")
		}
		s.logger.Error("mark claim round failed", "actor", actor, "error", err)
		return nil, contract.Internal("claim_challenge", "", "Could not record this step right now. Please try again.")
	}

	return &pb.MarkClaimRoundResponse{ServerNow: contract.Now()}, nil
}

// GetClaimChallengeStatus reports whether handle's challenge is still
// waiting or has ended (requirement 8's waiting/failed/expired presentation
// states collapse to this one ENDED value at the RPC layer -- the BFF is
// responsible for rendering "waiting" vs. an ended challenge's identical
// failed/expired wording).
func (s *LeafLabAPIServer) GetClaimChallengeStatus(ctx context.Context, req *pb.GetClaimChallengeStatusRequest) (*pb.GetClaimChallengeStatusResponse, error) {
	if req.GetChallengeHandle() == "" {
		return nil, contract.InvalidArgument("claim_challenge", "challenge_handle", "A challenge handle is required.")
	}

	waiting, err := s.repo.GetClaimChallengeStatus(ctx, req.GetChallengeHandle(), s.claimConfig)
	if err != nil {
		if errors.Is(err, ErrClaimChallengeNotFound) {
			return nil, contract.NotFound("claim_challenge", "challenge_handle", "This claim attempt could not be found. Start a new one.")
		}
		s.logger.Error("get claim challenge status failed", "error", err)
		return nil, contract.Internal("claim_challenge", "", "Could not check this claim attempt right now. Please try again.")
	}

	state := pb.ClaimChallengeState_CLAIM_CHALLENGE_STATE_ENDED
	if waiting {
		state = pb.ClaimChallengeState_CLAIM_CHALLENGE_STATE_WAITING
	}
	return &pb.GetClaimChallengeStatusResponse{State: state, ServerNow: contract.Now()}, nil
}

// CompleteClaim finalizes a challenge (requirement 6). ErrClaimNotDischarged
// covers every non-success case uniformly -- not found, wrong principal,
// still open, expired/exhausted, or discharged against a real household's
// board -- all rendered as the same refusal, worded per requirement 8: "we
// couldn't confirm you were at the board", with both fallbacks (FR75 invite,
// FR80 support reference) named as prominently as retry.
func (s *LeafLabAPIServer) CompleteClaim(ctx context.Context, req *pb.CompleteClaimRequest) (*pb.CompleteClaimResponse, error) {
	if req.GetChallengeHandle() == "" {
		return nil, contract.InvalidArgument("claim_challenge", "challenge_handle", "A challenge handle is required.")
	}

	actor := actingSubject(ctx)
	entry := claimAuditEntry(ctx)

	household, err := s.repo.CompleteClaim(ctx, actor, req.GetChallengeHandle(), s.claimConfig, entry)
	if err != nil {
		if errors.Is(err, ErrClaimNotDischarged) {
			return nil, contract.Refuse("claim_challenge", "challenge_handle",
				"We couldn't confirm you were at the board.",
				"Ask a household member to invite you (FR75), or contact support with a reference (FR80).")
		}
		s.logger.Error("complete claim failed", "actor", actor, "error", err)
		return nil, contract.Internal("claim_challenge", "", "Could not complete this claim right now. Please try again.")
	}

	return &pb.CompleteClaimResponse{Household: toHouseholdProto(household), ServerNow: contract.Now()}, nil
}

// --- Ownership closure, release, and transfer (FR70.2-.4, FR77) -----------

// toClosureProto mirrors leaflab/api.Closure onto the wire -- see that
// type's doc comment (closure.go) for what each field means.
func toClosureProto(c Closure) *pb.OwnershipClosure {
	return &pb.OwnershipClosure{
		BoardId:           c.BoardID,
		SensorIds:         c.SensorIDs,
		RegionIds:         c.RegionIDs,
		SubtreeRootIds:    c.SubtreeRootIDs,
		EntangledBoardIds: c.EntangledBoardIDs,
		PlantIds:          c.PlantIDs,
	}
}

// PreviewClosure returns a board's ownership closure without moving
// anything (FR70) -- the read-only preview a caller uses before deciding
// whether to adopt or transfer. Gated by authorizeBoardIDAccess: the
// caller must currently be a member of the board's owning household (or
// hold a wider future grant/elevation Scope) -- same NFR2-style
// not-found-masking as GetDeviceConfig, so "no such board" and "not yours"
// are indistinguishable.
func (s *LeafLabAPIServer) PreviewClosure(ctx context.Context, req *pb.PreviewClosureRequest) (*pb.PreviewClosureResponse, error) {
	if req.GetBoardId() <= 0 {
		return nil, contract.InvalidArgument("board", "board_id", "A board id is required.")
	}
	if err := s.authorizeBoardIDAccess(ctx, req.GetBoardId()); err != nil {
		return nil, err
	}

	closure, err := s.repo.PreviewClosure(ctx, req.GetBoardId())
	if err != nil {
		if errors.Is(err, ErrBoardNotFound) {
			return nil, boardIDNotFoundFailure()
		}
		s.logger.Error("preview closure failed", "board_id", req.GetBoardId(), "error", err)
		return nil, contract.Internal("board", "", "Could not preview this board's closure right now. Please try again.")
	}
	return &pb.PreviewClosureResponse{Closure: toClosureProto(closure), ServerNow: contract.Now()}, nil
}

// releaseBoardAuditEntry builds ReleaseBoard's audit.Entry, matching
// householdAuditEntry/claimAuditEntry's shape/precedent: Action/EntityKind
// come from auditRegistrations so the registered audit contract and what's
// actually written can't drift apart. EntityID/TargetHouseholdID/Reason are
// filled in by Repository.ReleaseBoard once the board's household is known
// (closure.go), matching households.go's entry-mutated-inside-repo
// convention.
func releaseBoardAuditEntry(ctx context.Context) audit.Entry {
	reg := auditRegistrations[releaseBoardFullMethod]
	return audit.Entry{
		ActorSubject:  actingSubject(ctx),
		ActorKind:     audit.ActorKindHuman,
		Action:        reg.Action,
		EntityKind:    reg.EntityKind,
		CorrelationID: CorrelationIDFromContext(ctx),
	}
}

// ReleaseBoard is FR77(a)'s evidence path: a current member of the board's
// owning household releases it, producing an opaque release token
// TransferClosure later presents as evidence that the losing household
// consented. Authorization is the membership check itself
// (Repository.ReleaseBoard's ErrClosureNotHouseholdMember) rather than
// authorizeBoardIDAccess/Scope: TransferClosure's caller may legitimately
// be a member of the *gaining* household presenting a token issued by
// someone else entirely, so board-Scope gating would be wrong here --
// ReleaseBoard itself, not a separate authorization layer, is where "must
// be a member of the losing household" is enforced.
func (s *LeafLabAPIServer) ReleaseBoard(ctx context.Context, req *pb.ReleaseBoardRequest) (*pb.ReleaseBoardResponse, error) {
	if req.GetBoardId() <= 0 {
		return nil, contract.InvalidArgument("board", "board_id", "A board id is required.")
	}

	actor := actingSubject(ctx)
	entry := releaseBoardAuditEntry(ctx)

	token, err := s.repo.ReleaseBoard(ctx, req.GetBoardId(), actor, req.GetReason(), entry)
	if err != nil {
		switch {
		case errors.Is(err, ErrBoardNotFound):
			return nil, boardIDNotFoundFailure()
		case errors.Is(err, ErrClosureNotRealHousehold):
			return nil, contract.Refuse("board", "board_id",
				"This board isn't currently owned by a household.",
				"Adopt this board first (FR76), then release it.")
		case errors.Is(err, ErrClosureNotHouseholdMember):
			return nil, contract.PermissionDenied("board", "board_id",
				"Only a current member of this board's household can release it.")
		}
		s.logger.Error("release board failed", "board_id", req.GetBoardId(), "actor", actor, "error", err)
		return nil, contract.Internal("board", "", "Could not release this board right now. Please try again.")
	}

	return &pb.ReleaseBoardResponse{ReleaseToken: token, ServerNow: contract.Now()}, nil
}

// TransferClosure moves a board's ownership closure to
// destination_household_id (FR77), gated on evidence: exactly one of
// TransferClosureRequest.evidence's branches must be set, checked here
// before any repository call so a caller supplying neither never reaches
// the DB. "An admin assertion alone is never sufficient" -- the
// admin_evidence branch additionally requires a non-empty reason
// (FR77(b)'s "elevated, reasoned admin action"); the release_token branch
// defaults an empty reason to a fixed, honest description of what
// happened, since NewTransferEntry's audit row cannot go unreasoned
// (FR77's transfer-must-carry-a-reason).
func (s *LeafLabAPIServer) TransferClosure(ctx context.Context, req *pb.TransferClosureRequest) (*pb.TransferClosureResponse, error) {
	if req.GetBoardId() <= 0 {
		return nil, contract.InvalidArgument("board", "board_id", "A board id is required.")
	}
	if req.GetDestinationHouseholdId() <= 0 {
		return nil, contract.InvalidArgument("transfer_closure_request", "destination_household_id", "A destination household id is required.")
	}

	releaseToken := req.GetReleaseToken()
	adminEvidence := req.GetAdminEvidence()
	if releaseToken == "" && adminEvidence == nil {
		return nil, contract.Refuse("board", "",
			"This transfer needs evidence that the losing household consented.",
			"Ask a member of the current household to release the board (FR77), or have an admin present a discharged possession challenge.")
	}

	var dischargedHandle string
	reason := req.GetReason()
	if adminEvidence != nil {
		dischargedHandle = adminEvidence.GetDischargedChallengeHandle()
		if reason == "" {
			return nil, contract.InvalidArgument("transfer_closure_request", "reason", "A reason is required for an admin-evidence transfer.")
		}
	} else if reason == "" {
		reason = "Released by a member of the losing household."
	}

	actor := actingSubject(ctx)
	boardIDStr := fmt.Sprintf("%d", req.GetBoardId())
	destHouseholdID := req.GetDestinationHouseholdId()
	entry := audit.NewTransferEntry(actor, audit.ActorKindHuman, &destHouseholdID, &boardIDStr, reason, CorrelationIDFromContext(ctx))

	household, err := s.repo.TransferClosure(ctx, req.GetBoardId(), destHouseholdID, releaseToken, dischargedHandle, actor, reason, entry)
	if err != nil {
		var entangled *ErrEntangledClosure
		switch {
		case errors.Is(err, ErrBoardNotFound):
			return nil, boardIDNotFoundFailure()
		case errors.Is(err, ErrClosureNoEvidence):
			return nil, contract.Refuse("board", "",
				"This transfer needs evidence that the losing household consented.",
				"Ask a member of the current household to release the board (FR77), or have an admin present a discharged possession challenge.")
		case errors.Is(err, ErrClosureNotRealHousehold):
			return nil, contract.Refuse("board", "board_id",
				"This board has no losing household to transfer from.",
				"Use board adoption (FR76) instead if this board is unowned.")
		case errors.Is(err, ErrClosureSameHousehold):
			return nil, contract.Refuse("transfer_closure_request", "destination_household_id",
				"This board already belongs to that household.",
				"Choose a different destination household.")
		case errors.Is(err, ErrHouseholdNotFound):
			return nil, householdNotFoundFailure()
		case errors.Is(err, ErrClosureDestinationUnadopted):
			return nil, contract.Refuse("transfer_closure_request", "destination_household_id",
				"Boards cannot be transferred into the Unadopted household.",
				"Choose a real destination household.")
		case errors.Is(err, ErrClosureInvalidReleaseToken):
			return nil, contract.Refuse("transfer_closure_request", "release_token",
				"This release token is invalid, expired or already used.",
				"Ask a member of the losing household to release the board again (FR77).")
		case errors.Is(err, ErrClosureChallengeNotDischarged):
			return nil, contract.Refuse("transfer_closure_request", "admin_evidence",
				"This possession challenge is not discharged against this board.",
				"Complete a possession challenge (FR76) before presenting it as transfer evidence.")
		case errors.Is(err, ErrClosureAdminReasonRequired):
			return nil, contract.InvalidArgument("transfer_closure_request", "reason", "A reason is required for an admin-evidence transfer.")
		case errors.As(err, &entangled):
			return nil, contract.Refuse("board", "board_id",
				fmt.Sprintf("This board shares a subtree with board(s) %v that belong to a different household.", entangled.ForeignBoardIDs),
				"Separate the entangled boards first (FR51, FR54, FR74), then try the transfer again.")
		}
		s.logger.Error("transfer closure failed", "board_id", req.GetBoardId(), "actor", actor, "error", err)
		return nil, contract.Internal("board", "", "Could not complete this transfer right now. Please try again.")
	}

	return &pb.TransferClosureResponse{Household: toHouseholdProto(household), ServerNow: contract.Now()}, nil
}
