package main

import (
	"context"
	"fmt"
	"time"

	"github.com/whale-net/everything/leaflab/api/apierrors"
	"github.com/whale-net/everything/leaflab/api/pagetoken"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ── FR10: Admin elevation ─────────────────────────────────────────────────

// EnterElevation starts a time-boxed elevation window against a named
// target household with a stated reason (FR10, A22). Admin-only.
func (s *LeafLabAPIServer) EnterElevation(ctx context.Context, req *pb.EnterElevationRequest) (*pb.ElevationState, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	if !IsAdmin(ctx) {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_PRECONDITION, "elevation", "", apierrors.AdminRoleRequired)
		return nil, apierrors.StatusWithDetail(codes.PermissionDenied, "admin role required", detail)
	}
	if req.Reason == "" {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT, "elevation", "reason", apierrors.ReasonRequired)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "reason is required", detail)
	}
	if req.TargetHouseholdId == 0 {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT, "elevation", "target_household_id", apierrors.InvalidDeviceID)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "target_household_id is required", detail)
	}

	exists, err := s.repo.HouseholdExists(ctx, req.TargetHouseholdId)
	if err != nil {
		s.logger.Error("check household exists failed", "target_household_id", req.TargetHouseholdId, "subject", subject, "correlation_id", corrID, "error", err)
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "household", "", apierrors.InternalError)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}
	if !exists {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_PRECONDITION, "household", "target_household_id", apierrors.HouseholdNotFound)
		return nil, apierrors.StatusWithDetail(codes.NotFound, "target household not found", detail)
	}

	elevationID, expiresAt, err := s.repo.InsertElevation(ctx, subject, req.TargetHouseholdId, req.Reason, s.adminConfig.ElevationDuration, nil)
	if err != nil {
		s.logger.Error("enter elevation failed", "target_household_id", req.TargetHouseholdId, "subject", subject, "correlation_id", corrID, "error", err)
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "elevation", "", apierrors.InternalError)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	s.logger.Info("elevation entered",
		"elevation_id", elevationID,
		"target_household_id", req.TargetHouseholdId,
		"subject", subject,
		"correlation_id", corrID)

	return elevationStateResponse(req.TargetHouseholdId, req.Reason, expiresAt), nil
}

// RenewElevation extends the caller's active elevation against the same
// target household by re-stating a reason (FR10). Fails if there is no
// active elevation to renew, or if the reason is not freshly stated.
func (s *LeafLabAPIServer) RenewElevation(ctx context.Context, req *pb.RenewElevationRequest) (*pb.ElevationState, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	if !IsAdmin(ctx) {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_PRECONDITION, "elevation", "", apierrors.AdminRoleRequired)
		return nil, apierrors.StatusWithDetail(codes.PermissionDenied, "admin role required", detail)
	}
	if req.Reason == "" {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT, "elevation", "reason", apierrors.ReasonRequired)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "reason is required", detail)
	}

	active, err := s.repo.GetActiveElevation(ctx, subject, req.TargetHouseholdId)
	if err != nil {
		s.logger.Error("get active elevation failed", "target_household_id", req.TargetHouseholdId, "subject", subject, "correlation_id", corrID, "error", err)
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "elevation", "", apierrors.InternalError)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}
	if active == nil {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_PRECONDITION, "elevation", "", apierrors.NoActiveElevation)
		return nil, apierrors.StatusWithDetail(codes.FailedPrecondition, "no active elevation to renew", detail)
	}
	if req.Reason == active.Reason {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT, "elevation", "reason", apierrors.ReasonNotRestated)
		return nil, apierrors.StatusWithDetail(codes.InvalidArgument, "renewal reason must be restated, not reused", detail)
	}

	elevationID, expiresAt, err := s.repo.InsertElevation(ctx, subject, req.TargetHouseholdId, req.Reason, s.adminConfig.ElevationDuration, &active.ElevationID)
	if err != nil {
		s.logger.Error("renew elevation failed", "target_household_id", req.TargetHouseholdId, "subject", subject, "correlation_id", corrID, "error", err)
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "elevation", "", apierrors.InternalError)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	s.logger.Info("elevation renewed",
		"elevation_id", elevationID,
		"renewed_from", active.ElevationID,
		"target_household_id", req.TargetHouseholdId,
		"subject", subject,
		"correlation_id", corrID)

	return elevationStateResponse(req.TargetHouseholdId, req.Reason, expiresAt), nil
}

// GetElevationState reads the caller's current elevation state against a
// target household, including remaining time (FR10, A22). A caller with no
// active elevation (including any non-admin) simply sees active=false.
func (s *LeafLabAPIServer) GetElevationState(ctx context.Context, req *pb.GetElevationStateRequest) (*pb.ElevationState, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	active, err := s.repo.GetActiveElevation(ctx, subject, req.TargetHouseholdId)
	if err != nil {
		s.logger.Error("get elevation state failed", "target_household_id", req.TargetHouseholdId, "subject", subject, "correlation_id", corrID, "error", err)
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "elevation", "", apierrors.InternalError)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}
	if active == nil {
		return &pb.ElevationState{Active: false, TargetHouseholdId: req.TargetHouseholdId}, nil
	}
	return elevationStateResponse(req.TargetHouseholdId, active.Reason, active.ExpiresAt), nil
}

// elevationStateResponse builds an ElevationState reflecting an active
// window expiring at expiresAt (A22: remaining time is readable while
// elevated).
func elevationStateResponse(targetHouseholdID int64, reason string, expiresAt time.Time) *pb.ElevationState {
	remaining := int64(time.Until(expiresAt).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	return &pb.ElevationState{
		Active:            true,
		TargetHouseholdId: targetHouseholdID,
		Reason:            reason,
		RemainingSeconds:  remaining,
		ExpiresAt:         expiresAt.Unix(),
	}
}

// ── FR10.2: Standing lane — resolve ────────────────────────────────────────

// resolveNotFoundErr is the single failure Resolve returns for every
// unresolvable target — unknown/expired/revoked support reference, unknown
// person, unmatched or ambiguous device-id prefix. NFR2 requires these be
// indistinguishable in status, body and timing; using one constructor at
// exactly one call site is what keeps that true as the RPC evolves.
func resolveNotFoundErr() error {
	detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_PRECONDITION, "resolve", "", apierrors.ResolveNotFound)
	return apierrors.StatusWithDetail(codes.NotFound, "no matching household", detail)
}

// Resolve is the admin standing lane (FR10.2): without elevation, resolve a
// person, a support reference (FR80), or a partial device identifier to the
// owning household, and return the FR79 health fields for its boards and
// nothing else. Admin-only.
func (s *LeafLabAPIServer) Resolve(ctx context.Context, req *pb.ResolveRequest) (*pb.ResolveResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	if !IsAdmin(ctx) {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_PRECONDITION, "resolve", "", apierrors.AdminRoleRequired)
		return nil, apierrors.StatusWithDetail(codes.PermissionDenied, "admin role required", detail)
	}

	var householdID int64
	var action string

	switch target := req.Target.(type) {
	case *pb.ResolveRequest_Person:
		if target.Person == "" {
			return nil, status.Error(codes.InvalidArgument, "person is required")
		}
		hh, err := s.repo.GetPrincipalHousehold(ctx, target.Person)
		if err != nil {
			s.logger.Error("resolve person failed", "subject", subject, "correlation_id", corrID, "error", err)
			detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "resolve", "", apierrors.InternalError)
			return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
		}
		if hh == 0 {
			return nil, resolveNotFoundErr()
		}
		householdID, action = hh, "resolve_person"

	case *pb.ResolveRequest_SupportReference:
		if target.SupportReference == "" {
			return nil, status.Error(codes.InvalidArgument, "support_reference is required")
		}
		// NFR10/FR80: support-reference resolution is rate-limited per admin
		// principal, ahead of the (deliberately timing-flat) lookup below.
		if !s.limiter.Allow(ratelimit.ForPrincipal(subject), "support-reference") {
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}
		hash := hashSupportCode(target.SupportReference)
		hh, found, err := s.repo.ResolveSupportReference(ctx, hash)
		if err != nil {
			s.logger.Error("resolve support reference failed", "subject", subject, "correlation_id", corrID, "error", err)
			detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "resolve", "", apierrors.InternalError)
			return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
		}
		if !found {
			return nil, resolveNotFoundErr()
		}
		householdID, action = hh, "resolve_support_reference"

	case *pb.ResolveRequest_DeviceIdPrefix:
		if target.DeviceIdPrefix == "" {
			return nil, status.Error(codes.InvalidArgument, "device_id_prefix is required")
		}
		hh, found, err := s.repo.ResolveDeviceIDPrefixHousehold(ctx, target.DeviceIdPrefix)
		if err != nil {
			s.logger.Error("resolve device_id_prefix failed", "subject", subject, "correlation_id", corrID, "error", err)
			detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "resolve", "", apierrors.InternalError)
			return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
		}
		if !found {
			return nil, resolveNotFoundErr()
		}
		householdID, action = hh, "resolve_device_prefix"

	default:
		return nil, status.Error(codes.InvalidArgument, "one of person, support_reference, device_id_prefix is required")
	}

	rows, err := s.repo.QueryBoardHealth(ctx, boardHealthFilters{HouseholdID: householdID, Limit: MaxPageSize})
	if err != nil {
		s.logger.Error("resolve board health failed", "household_id", householdID, "subject", subject, "correlation_id", corrID, "error", err)
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "resolve", "", apierrors.InternalError)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	now := time.Now()
	boards := make([]*pb.BoardHealth, 0, len(rows))
	for _, row := range rows {
		boards = append(boards, toPBBoardHealth(toBoardHealth(row, s.adminConfig.Staleness, now)))
	}

	// Standing-lane reads are audited at query granularity (FR10.4), not per
	// row: one audit record for the household this resolve touched.
	if err := s.repo.RecordAudit(ctx, subject, householdID, action, "household", householdID, ""); err != nil {
		s.logger.Error("record audit failed", "action", action, "household_id", householdID, "subject", subject, "correlation_id", corrID, "error", err)
	}

	s.logger.Info("resolved",
		"action", action,
		"household_id", householdID,
		"board_count", len(boards),
		"subject", subject,
		"correlation_id", corrID)

	return &pb.ResolveResponse{HouseholdId: householdID, Boards: boards}, nil
}

// ── FR79: Fleet health listing ─────────────────────────────────────────────

// ListFleetHealth returns the FR79 fleet health listing (standing lane,
// admin-only): per board, last-seen age against the A23 staleness
// threshold, active accepted config version, outstanding push duration, and
// sensor count. No computed health score or severity ranking. Retired
// boards are excluded (FR22.4).
func (s *LeafLabAPIServer) ListFleetHealth(ctx context.Context, req *pb.ListFleetHealthRequest) (*pb.ListFleetHealthResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	if !IsAdmin(ctx) {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_PRECONDITION, "fleet_health", "", apierrors.AdminRoleRequired)
		return nil, apierrors.StatusWithDetail(codes.PermissionDenied, "admin role required", detail)
	}

	var decodedToken *pagetoken.Token
	if req.Page != nil && req.Page.PageToken != "" {
		var err error
		decodedToken, err = pagetoken.Decode(req.Page.PageToken)
		if err != nil {
			detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INVALID_ARGUMENT, "page", "page_token", apierrors.InvalidPageToken)
			return nil, apierrors.StatusWithDetail(codes.InvalidArgument, err.Error(), detail)
		}
	}

	pageSize := int32(DefaultPageSize)
	if req.Page != nil && req.Page.PageSize > 0 {
		pageSize = req.Page.PageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	var afterBoardID int64
	if decodedToken != nil {
		afterBoardID = decodedToken.LastBoardID
	}

	// Over-fetch by one raw row beyond pageSize to detect a next page, and
	// keep the cursor anchored to raw rows examined — unhealthy_only is
	// applied after fetch (it depends on parsing each board's accepted
	// config, which cannot be pushed into SQL), so a page may legitimately
	// return fewer than pageSize items even when more data exists; callers
	// must follow next_page_token regardless of item count.
	rawRows, err := s.repo.QueryBoardHealth(ctx, boardHealthFilters{
		DevicePrefix: req.DeviceIdPrefix,
		HouseholdID:  req.HouseholdId,
		RegionID:     req.RegionId,
		AfterBoardID: afterBoardID,
		Limit:        pageSize + 1,
	})
	if err != nil {
		s.logger.Error("list fleet health failed", "subject", subject, "correlation_id", corrID, "error", err)
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "fleet_health", "", apierrors.InternalError)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	var nextPageToken string
	if int32(len(rawRows)) > pageSize {
		cursorBoardID := rawRows[pageSize-1].BoardID
		rawRows = rawRows[:pageSize]
		encoded, err := pagetoken.Encode(&pagetoken.Token{LastBoardID: cursorBoardID})
		if err != nil {
			s.logger.Error("failed to encode next page token", "error", err)
		} else {
			nextPageToken = encoded
		}
	}

	now := time.Now()
	boards := make([]*pb.BoardHealth, 0, len(rawRows))
	householdsSeen := make(map[int64]bool)
	for _, row := range rawRows {
		health := toBoardHealth(row, s.adminConfig.Staleness, now)
		if req.UnhealthyOnly && health.Reporting {
			continue
		}
		boards = append(boards, toPBBoardHealth(health))
		if health.HouseholdID != nil {
			householdsSeen[*health.HouseholdID] = true
		}
	}

	totalSize, err := s.repo.CountBoardHealth(ctx, req.DeviceIdPrefix, req.HouseholdId, req.RegionId)
	if err != nil {
		s.logger.Error("count fleet health failed", "subject", subject, "correlation_id", corrID, "error", err)
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "fleet_health", "", apierrors.InternalError)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// Standing-lane reads are audited at query granularity (FR10.4): one
	// audit record per distinct household actually returned, not one per
	// board row.
	reason := fmt.Sprintf("device_id_prefix=%q household_id=%d region_id=%d unhealthy_only=%t",
		req.DeviceIdPrefix, req.HouseholdId, req.RegionId, req.UnhealthyOnly)
	for hh := range householdsSeen {
		if err := s.repo.RecordAudit(ctx, subject, hh, "list_fleet_health", "household", hh, reason); err != nil {
			s.logger.Error("record audit failed", "household_id", hh, "subject", subject, "correlation_id", corrID, "error", err)
		}
	}

	s.logger.Info("fleet health listed",
		"board_count", len(boards),
		"subject", subject,
		"correlation_id", corrID)

	return &pb.ListFleetHealthResponse{
		Boards: boards,
		Page: &pb.PageResponse{
			NextPageToken: nextPageToken,
			TotalSize:     totalSize,
		},
	}, nil
}

// toPBBoardHealth converts the internal derivation result to the wire
// message. household_id is intentionally not part of BoardHealth (FR79
// lists boards, not households) — it is used internally for audit only.
func toPBBoardHealth(h BoardHealthResult) *pb.BoardHealth {
	return &pb.BoardHealth{
		DeviceId:               h.DeviceID,
		BoardId:                h.BoardID,
		LastSeenAgeSeconds:     h.LastSeenAgeSeconds,
		Reporting:              h.Reporting,
		ActiveConfigVersion:    h.ActiveConfigVersion,
		PushOutstanding:        h.PushOutstanding,
		PushOutstandingSeconds: h.PushOutstandingSeconds,
		SensorCount:            h.SensorCount,
	}
}

// ── FR80: Support references ──────────────────────────────────────────────

// CreateSupportReference produces a short-lived, opaque, revocable support
// reference for the caller's household (FR80). Member-only (the caller's
// own household, derived from identity per FR5 — no household_id param).
func (s *LeafLabAPIServer) CreateSupportReference(ctx context.Context, _ *pb.CreateSupportReferenceRequest) (*pb.CreateSupportReferenceResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	householdID, err := s.repo.GetPrincipalHousehold(ctx, subject)
	if err != nil {
		s.logger.Error("get principal household failed", "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "get household: %v", err)
	}
	if householdID == 0 {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_PRECONDITION, "support_reference", "", apierrors.NoHousehold)
		return nil, apierrors.StatusWithDetail(codes.PermissionDenied, "principal has no household", detail)
	}

	code, err := generateSupportCode()
	if err != nil {
		s.logger.Error("generate support code failed", "household_id", householdID, "subject", subject, "correlation_id", corrID, "error", err)
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "support_reference", "", apierrors.InternalError)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	id, expiresAt, err := s.repo.InsertSupportReference(ctx, householdID, hashSupportCode(code), subject, s.adminConfig.SupportReferenceDuration)
	if err != nil {
		s.logger.Error("create support reference failed", "household_id", householdID, "subject", subject, "correlation_id", corrID, "error", err)
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "support_reference", "", apierrors.InternalError)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}

	// FR9: the reference's existence is visible in the household's own
	// activity list, without disclosing the code itself.
	if err := s.repo.RecordAudit(ctx, subject, householdID, "create_support_reference", "support_reference", id, ""); err != nil {
		s.logger.Error("record audit failed", "household_id", householdID, "subject", subject, "correlation_id", corrID, "error", err)
	}

	s.logger.Info("support reference created",
		"support_reference_id", id,
		"household_id", householdID,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.CreateSupportReferenceResponse{Reference: code, ExpiresAt: expiresAt.Unix()}, nil
}

// RevokeSupportReference revokes a support reference before it expires
// (FR80). Member-only, scoped to the caller's own household.
func (s *LeafLabAPIServer) RevokeSupportReference(ctx context.Context, req *pb.RevokeSupportReferenceRequest) (*pb.RevokeSupportReferenceResponse, error) {
	if err := requireAuthentication(ctx); err != nil {
		return nil, err
	}
	subject, corrID := getSubjectAndCorrelationID(ctx)

	if req.Reference == "" {
		return nil, status.Error(codes.InvalidArgument, "reference is required")
	}

	householdID, err := s.repo.GetPrincipalHousehold(ctx, subject)
	if err != nil {
		s.logger.Error("get principal household failed", "subject", subject, "correlation_id", corrID, "error", err)
		return nil, status.Errorf(codes.Internal, "get household: %v", err)
	}
	if householdID == 0 {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_PRECONDITION, "support_reference", "", apierrors.NoHousehold)
		return nil, apierrors.StatusWithDetail(codes.PermissionDenied, "principal has no household", detail)
	}

	revoked, err := s.repo.RevokeSupportReferenceByCode(ctx, householdID, hashSupportCode(req.Reference))
	if err != nil {
		s.logger.Error("revoke support reference failed", "household_id", householdID, "subject", subject, "correlation_id", corrID, "error", err)
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_INTERNAL, "support_reference", "", apierrors.InternalError)
		return nil, apierrors.StatusWithDetail(codes.Internal, err.Error(), detail)
	}
	if !revoked {
		detail := apierrors.NewErrorDetail(pb.FailureClass_FAILURE_CLASS_PRECONDITION, "support_reference", "reference", apierrors.SupportReferenceNotFound)
		return nil, apierrors.StatusWithDetail(codes.NotFound, "no matching active support reference", detail)
	}

	if err := s.repo.RecordAudit(ctx, subject, householdID, "revoke_support_reference", "support_reference", 0, ""); err != nil {
		s.logger.Error("record audit failed", "household_id", householdID, "subject", subject, "correlation_id", corrID, "error", err)
	}

	s.logger.Info("support reference revoked",
		"household_id", householdID,
		"subject", subject,
		"correlation_id", corrID)

	return &pb.RevokeSupportReferenceResponse{}, nil
}
