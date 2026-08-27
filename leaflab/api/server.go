package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
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
	InsertDeviceConfigNextVersion(ctx context.Context, boardID int64, configJSON []byte, entry audit.Entry) (int64, error)
	GetLatestAcceptedConfig(ctx context.Context, deviceID string) (*configpb.DeviceConfig, error)
	// ListBoards is household-scoped (FR5.1): scope's SQL fragment
	// (Scope.Filter) is applied inside the query itself, never as a
	// post-filter -- see Repository.ListBoards.
	ListBoards(ctx context.Context, afterBoardID int64, hasAfter bool, limit int32, scope authz.Scope) ([]BoardRow, error)
	Ping(ctx context.Context) error

	// InsertHouseholdGrant, RevokeHouseholdGrant, ListActiveHouseholdGrants
	// and RecordRead back the three FR7 RPCs below -- see
	// Repository's household grants section for each method's contract.
	InsertHouseholdGrant(ctx context.Context, householdID int64, granteeSubject string, grantedBySubject string, expiresAt time.Time, reason *string, entry audit.Entry) (int64, error)
	RevokeHouseholdGrant(ctx context.Context, grantID int64, entry audit.Entry) error
	ListActiveHouseholdGrants(ctx context.Context, householdID int64, afterGrantID int64, hasAfter bool, limit int32) ([]HouseholdGrantRow, error)
	RecordRead(ctx context.Context, entry audit.Entry) error
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
	// RoleForPrincipalInHousehold and ResolveGrantRole back FR7's three
	// grant RPCs -- see authz.PGResolver's doc comments on each.
	RoleForPrincipalInHousehold(ctx context.Context, principalSubject string, householdID int64) (authz.PrincipalRole, error)
	ResolveGrantRole(ctx context.Context, grantID int64, principalSubject string) (authz.GrantResolution, error)
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

// ── Household grants (FR7) ───────────────────────────────────────────────

// householdNotFoundFailure is the one contract.NotFound value returned for
// a household_id that doesn't exist and for one that exists but the caller
// has no reach over at all (neither a current member nor an active
// grantee) -- the same NFR2 collapse boardNotFoundFailure performs for
// boards, applied to household_id, which is just as caller-suppliable and
// enumerable (BIGSERIAL) as device_id.
func householdNotFoundFailure() error {
	return contract.NotFound("household", "household_id", "No household matches this id.")
}

// grantNotFoundFailure is householdNotFoundFailure's counterpart for
// grant_id (RevokeHouseholdAccess): returned both when grant_id names no
// row and when it names a real row belonging to a household the caller has
// no reach over (authz.ErrGrantNotFound / authz.PrincipalRole == RoleNone
// via ResolveGrantRole -- see its doc comment for why both collapse to one
// query and one response here).
func grantNotFoundFailure() error {
	return contract.NotFound("household_grant", "grant_id", "No grant matches this id.")
}

// grantExcludedFailure is FR7's refuse-and-name-the-alternative response
// (FR59.3) for the one exclusion these RPCs enforce today
// (CapabilityGrantAccess -- see authz.MemberOrGrantee): a grantee's
// household reach is real and acknowledged, but this specific action is
// refused with the alternative FR7 gives them -- ask a member -- rather
// than the caller-has-no-reach-at-all NotFound above.
func grantExcludedFailure() error {
	return contract.Refuse("household_grant", "", "A granted helper cannot grant further access to a household.", "Ask a household member to grant access instead.")
}

// GrantHouseholdAccess grants grantee_subject time-boxed write access to
// household_id (FR7). CapabilityGrantAccess is one of FR7's three
// exclusions -- a grantee may not call this -- enforced here via
// authz.MemberOrGrantee, the one place any of the three exclusions is
// checked (capability.go's grantExcludedCapabilities). The caller is
// always a member by the time this succeeds, so the audit entry's
// actor_kind is always audit.ActorKindMember (FR7's "actor_kind
// distinguishing grantee from member").
func (s *LeafLabAPIServer) GrantHouseholdAccess(ctx context.Context, req *pb.GrantHouseholdAccessRequest) (*pb.GrantHouseholdAccessResponse, error) {
	if req.GranteeSubject == "" {
		return nil, contract.InvalidArgument("household_grant", "grantee_subject", "A grantee is required.")
	}
	expiresAt := contract.FromInstant(req.ExpiresAt)
	if !expiresAt.After(time.Now()) {
		return nil, contract.InvalidArgument("household_grant", "expires_at", "The expiry must be a future time.")
	}

	subject := actingSubject(ctx)
	role, err := s.authzSvc.RoleForPrincipalInHousehold(ctx, subject, req.HouseholdId)
	if err != nil {
		s.logger.Error("resolve caller role failed", "household_id", req.HouseholdId, "error", err)
		return nil, contract.Internal("household_grant", "", "Could not process this request right now. Please try again.")
	}
	if role == authz.RoleNone {
		return nil, householdNotFoundFailure()
	}

	scope := authz.MemberOrGrantee(authz.NewHouseholdScope(req.HouseholdId), role == authz.RoleGrantee, authz.CapabilityGrantAccess)
	ref := authz.EntityRef{Kind: authz.EntityHousehold, ID: req.HouseholdId}
	res := authz.Resolution{HouseholdID: req.HouseholdId}
	if !scope.Permits(ref, res) {
		return nil, grantExcludedFailure()
	}

	var reason *string
	if req.Reason != "" {
		reason = &req.Reason
	}

	reg := auditRegistrations[grantHouseholdAccessFullMethod]
	entry := audit.Entry{
		ActorSubject:      subject,
		ActorKind:         audit.ActorKindMember,
		TargetHouseholdID: &req.HouseholdId,
		Action:            reg.Action,
		EntityKind:        reg.EntityKind,
		Reason:            reason,
		CorrelationID:     CorrelationIDFromContext(ctx),
	}
	grantID, err := s.repo.InsertHouseholdGrant(ctx, req.HouseholdId, req.GranteeSubject, subject, expiresAt, reason, entry)
	if err != nil {
		s.logger.Error("insert household grant failed", "household_id", req.HouseholdId, "error", err)
		return nil, contract.Internal("household_grant", "", "Could not record this grant right now. Please try again.")
	}

	return &pb.GrantHouseholdAccessResponse{GrantId: grantID}, nil
}

// RevokeHouseholdAccess revokes a household grant in one action (FR7).
// Unlike GrantHouseholdAccess, RevokeHouseholdAccess is not one of FR7's
// three named exclusions -- both a member and a grantee may call it
// (authz.CapabilityOrdinary), so the audit entry's actor_kind reflects
// whichever role the caller actually resolved to.
func (s *LeafLabAPIServer) RevokeHouseholdAccess(ctx context.Context, req *pb.RevokeHouseholdAccessRequest) (*pb.RevokeHouseholdAccessResponse, error) {
	subject := actingSubject(ctx)
	grantRes, err := s.authzSvc.ResolveGrantRole(ctx, req.GrantId, subject)
	if err != nil {
		if errors.Is(err, authz.ErrGrantNotFound) {
			return nil, grantNotFoundFailure()
		}
		s.logger.Error("resolve grant failed", "grant_id", req.GrantId, "error", err)
		return nil, contract.Internal("household_grant", "", "Could not process this request right now. Please try again.")
	}
	if grantRes.Role == authz.RoleNone {
		return nil, grantNotFoundFailure()
	}

	scope := authz.MemberOrGrantee(authz.NewHouseholdScope(grantRes.HouseholdID), grantRes.Role == authz.RoleGrantee, authz.CapabilityOrdinary)
	ref := authz.EntityRef{Kind: authz.EntityHousehold, ID: grantRes.HouseholdID}
	res := authz.Resolution{HouseholdID: grantRes.HouseholdID}
	if !scope.Permits(ref, res) {
		// CapabilityOrdinary is never one of grantExcludedCapabilities, so
		// this branch is unreachable today -- kept for the same reason
		// authorizeBoardAccess's analogous check is kept: a Scope must
		// always be consulted, never assumed, even where the answer is
		// currently always "permitted".
		return nil, grantExcludedFailure()
	}

	actorKind := audit.ActorKindMember
	if grantRes.Role == authz.RoleGrantee {
		actorKind = audit.ActorKindGrantee
	}
	reg := auditRegistrations[revokeHouseholdAccessFullMethod]
	grantIDStr := strconv.FormatInt(req.GrantId, 10)
	entry := audit.Entry{
		ActorSubject:      subject,
		ActorKind:         actorKind,
		TargetHouseholdID: &grantRes.HouseholdID,
		Action:            reg.Action,
		EntityKind:        reg.EntityKind,
		EntityID:          &grantIDStr,
		CorrelationID:     CorrelationIDFromContext(ctx),
	}
	if err := s.repo.RevokeHouseholdGrant(ctx, req.GrantId, entry); err != nil {
		if errors.Is(err, authz.ErrGrantNotFound) {
			return nil, grantNotFoundFailure()
		}
		if errors.Is(err, ErrGrantAlreadyRevoked) {
			return nil, contract.InvalidArgument("household_grant", "grant_id", "This grant has already been revoked.")
		}
		s.logger.Error("revoke household grant failed", "grant_id", req.GrantId, "error", err)
		return nil, contract.Internal("household_grant", "", "Could not revoke this grant right now. Please try again.")
	}

	return &pb.RevokeHouseholdAccessResponse{}, nil
}

// ListHouseholdGrants lists household_id's currently active grants (FR7):
// a member (or grantee, via authz.MemberOrGrantee's CapabilityOrdinary --
// not one of the three exclusions) sees grantee identity and expiry for
// every active grant without an admin. Revoked and expired grants are
// never returned --
// expiry is evaluated against NOW() in the repository query, at request
// time, with no background job (FR7).
//
// FR8.1: a read performed under a granted (non-member) identity produces
// an audit record; a member's read does not. This is the one read path in
// this service that is sometimes audited -- the audit write happens
// outside any transaction (Repository.RecordRead) since this RPC performs
// no accompanying DB write of its own.
func (s *LeafLabAPIServer) ListHouseholdGrants(ctx context.Context, req *pb.ListHouseholdGrantsRequest) (*pb.ListHouseholdGrantsResponse, error) {
	subject := actingSubject(ctx)
	role, err := s.authzSvc.RoleForPrincipalInHousehold(ctx, subject, req.HouseholdId)
	if err != nil {
		s.logger.Error("resolve caller role failed", "household_id", req.HouseholdId, "error", err)
		return nil, contract.Internal("household_grant", "", "Could not process this request right now. Please try again.")
	}
	if role == authz.RoleNone {
		return nil, householdNotFoundFailure()
	}

	// CapabilityOrdinary: listing grants is not one of the three
	// exclusions, so both a member and a grantee may call this. Routed
	// through MemberOrGrantee like every other "member capability" call
	// site, even though CapabilityOrdinary always permits -- so a grep for
	// MemberOrGrantee finds this call site too, and so this handler never
	// falls back to a bare membership check if a future FR8.1-adjacent
	// requirement narrows what a grantee may list.
	scope := authz.MemberOrGrantee(authz.NewHouseholdScope(req.HouseholdId), role == authz.RoleGrantee, authz.CapabilityOrdinary)
	ref := authz.EntityRef{Kind: authz.EntityHousehold, ID: req.HouseholdId}
	res := authz.Resolution{HouseholdID: req.HouseholdId}
	if !scope.Permits(ref, res) {
		return nil, grantExcludedFailure()
	}

	afterGrantID, hasAfter, err := contract.DecodeGrantCursor(req.GetPage().GetPageToken())
	if err != nil {
		return nil, contract.InvalidArgument("list_household_grants_request", "page_token", "This page link is no longer valid. Start again from the first page.")
	}
	limit := contract.ClampPageSize(req.GetPage().GetPageSize())

	rows, err := s.repo.ListActiveHouseholdGrants(ctx, req.HouseholdId, afterGrantID, hasAfter, limit+1)
	if err != nil {
		s.logger.Error("list household grants failed", "household_id", req.HouseholdId, "error", err)
		return nil, contract.Internal("household_grant", "", "Could not list grants right now. Please try again.")
	}

	var nextToken string
	if int32(len(rows)) > limit {
		rows = rows[:limit]
		nextToken = contract.EncodeGrantCursor(rows[len(rows)-1].GrantID)
	}

	if role == authz.RoleGrantee {
		if err := s.repo.RecordRead(ctx, audit.Entry{
			ActorSubject:      subject,
			ActorKind:         audit.ActorKindGrantee,
			TargetHouseholdID: &req.HouseholdId,
			Action:            "ListHouseholdGrants",
			EntityKind:        "household_grant",
			CorrelationID:     CorrelationIDFromContext(ctx),
		}); err != nil {
			s.logger.Error("record granted-read audit entry failed", "household_id", req.HouseholdId, "error", err)
			return nil, contract.Internal("household_grant", "", "Could not process this request right now. Please try again.")
		}
	}

	grants := make([]*pb.HouseholdGrantInfo, 0, len(rows))
	for _, r := range rows {
		grants = append(grants, &pb.HouseholdGrantInfo{
			GrantId:          r.GrantID,
			GranteeSubject:   r.GranteeSubject,
			GrantedBySubject: r.GrantedBySubject,
			GrantedAt:        contract.ToInstant(r.GrantedAt),
			ExpiresAt:        contract.ToInstant(r.ExpiresAt),
		})
	}
	return &pb.ListHouseholdGrantsResponse{
		Grants:    grants,
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
