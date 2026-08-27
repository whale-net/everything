package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// This file implements FR80's owner-initiated support reference: a
// household member produces, lists and revokes a short-lived, opaque,
// revocable code that resolves -- for an admin, in FR10.2's standing lane
// -- to that household. CreateSupportReference/RevokeSupportReference/
// ListSupportReferences below are the owner-facing RPCs; resolveSupportReference
// is the admin-side resolution ResolveToHousehold's support_reference
// query term calls into (server.go).

// supportReferenceCodeAlphabet is Crockford's Base32 (32 symbols): the
// usual base32 alphabet minus I/L/O/U, which Crockford excludes because
// they're easy to misread or mis-transcribe when read aloud or written
// down -- FR80's "short enough to read over a phone."
const supportReferenceCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// supportReferenceCodeLength is the number of symbols generateSupportReferenceCode
// returns. 32 symbols is exactly one byte's worth of unbiased entropy per
// random draw (256 / 32 = 8, so `b % 32` on a crypto/rand byte introduces
// no modulo bias). At 10 symbols this is 50 bits of entropy
// (32^10 ≈ 1.13 * 10^15 combinations): against BucketSupportReferenceResolve's
// default 10 attempts/minute and DefaultSupportReferenceTTL's default
// 15-minute lifetime, a guesser gets at most ~150 attempts before a code
// expires, for a worst-case single-code guess probability on the order of
// 1.3 * 10^-13 -- state this arithmetic in the PR alongside whatever the
// deployed environment's actual bucket/TTL configuration is. FR80: "the
// limiter, not the length, is the primary control" -- length exists so the
// limiter has enough keyspace to actually matter, not as the primary
// defense by itself.
const supportReferenceCodeLength = 10

// generateSupportReferenceCode returns a fresh opaque, high-entropy code
// (FR80) using crypto/rand -- never math/rand, which is not
// cryptographically secure and must not back a credential of any kind.
func generateSupportReferenceCode() (string, error) {
	raw := make([]byte, supportReferenceCodeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate support reference code: %w", err)
	}
	code := make([]byte, supportReferenceCodeLength)
	for i, b := range raw {
		code[i] = supportReferenceCodeAlphabet[int(b)%len(supportReferenceCodeAlphabet)]
	}
	return string(code), nil
}

// hashSupportReferenceCode returns the SHA-256 hex digest stored as
// support_reference.code_hash -- never the plaintext (FR80, migration
// 032's code_hash column). A plain fast hash (not a slow/salted password
// hash) is appropriate here: the code's own entropy
// (supportReferenceCodeLength) plus BucketSupportReferenceResolve's
// per-admin rate limit (NFR10) are what bound *online* guessing, which is
// the attack this code is designed against; a leaked code_hash column is a
// different threat model this migration's column doc comment does not
// claim to defend against.
func hashSupportReferenceCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// supportReferenceResolveAction names the audit.Entry.Action FR10.2's
// successful support-reference resolution writes (server.go's
// resolveSupportReference) -- distinct from "ResolveToHousehold" (the
// FR10.4 call-granularity entry every standing-lane query writes,
// regardless of outcome or query kind) so FR9's owner-facing activity list
// can filter on "my support reference was used" specifically, by action
// name, rather than parsing entity_kind/entity_id.
const supportReferenceResolveAction = "SupportReferenceResolve"

// supportReferenceHouseholdNotFoundFailure is returned for a household_id
// the caller does not currently belong to, and for a household_id that
// names no row at all -- the same NotFound shape both ways (mirrors
// boardNotFoundFailure's doc comment in server.go) so a caller cannot use
// response shape to learn whether a household they don't belong to
// exists.
func supportReferenceHouseholdNotFoundFailure() error {
	return contract.NotFound("household", "household_id", "No household matches this id.")
}

// supportReferenceNotFoundFailure is returned when
// (household_id, support_reference_id) names no currently-unrevoked row --
// covering "doesn't exist", "belongs to a different household" and
// "already revoked" alike (see repository.go's ErrSupportReferenceNotFound
// doc comment), so a caller cannot distinguish the three by response
// shape.
func supportReferenceNotFoundFailure() error {
	return contract.NotFound("support_reference", "support_reference_id", "No support reference matches this id.")
}

// CreateSupportReference mints a new short-lived, opaque, revocable code
// for the caller's household (FR80). Takes no problem description and
// discloses nothing about the household in its response -- only the code
// and its expiry, and the code appears here and nowhere else, ever again:
// it is never logged, never re-derivable from the stored code_hash, and
// never returned by ListSupportReferences.
func (s *LeafLabAPIServer) CreateSupportReference(ctx context.Context, req *pb.CreateSupportReferenceRequest) (*pb.CreateSupportReferenceResponse, error) {
	householdID := req.GetHouseholdId()
	if householdID <= 0 {
		return nil, contract.InvalidArgument("create_support_reference_request", "household_id", "A household id is required.")
	}

	scope, err := s.scopeForCaller(ctx)
	if err != nil {
		s.logger.Error("create support reference: resolve caller scope failed", "household_id", householdID, "error", err)
		return nil, contract.Internal("support_reference", "", "Could not process this request right now. Please try again.")
	}
	if !scopePermitsHousehold(scope, householdID) {
		return nil, supportReferenceHouseholdNotFoundFailure()
	}

	code, err := generateSupportReferenceCode()
	if err != nil {
		s.logger.Error("create support reference: generate code failed", "household_id", householdID, "error", err)
		return nil, contract.Internal("support_reference", "", "Could not create a support reference right now. Please try again.")
	}

	expiresAt := time.Now().Add(s.supportReferenceTTL)
	subject := actingSubject(ctx)
	reg := auditRegistrations[createSupportReferenceFullMethod]
	entry := audit.Entry{
		ActorSubject:      subject,
		ActorKind:         audit.ActorKindHuman,
		TargetHouseholdID: &householdID,
		Action:            reg.Action,
		EntityKind:        reg.EntityKind,
		CorrelationID:     CorrelationIDFromContext(ctx),
	}
	if _, err := s.repo.CreateSupportReference(ctx, householdID, hashSupportReferenceCode(code), subject, expiresAt, entry); err != nil {
		s.logger.Error("create support reference failed", "household_id", householdID, "error", err)
		return nil, contract.Internal("support_reference", "", "Could not create a support reference right now. Please try again.")
	}

	return &pb.CreateSupportReferenceResponse{Code: code, ExpiresAt: contract.ToInstant(expiresAt)}, nil
}

// RevokeSupportReference immediately revokes an existing reference (FR80):
// one call, immediate effect.
func (s *LeafLabAPIServer) RevokeSupportReference(ctx context.Context, req *pb.RevokeSupportReferenceRequest) (*pb.RevokeSupportReferenceResponse, error) {
	householdID := req.GetHouseholdId()
	if householdID <= 0 {
		return nil, contract.InvalidArgument("revoke_support_reference_request", "household_id", "A household id is required.")
	}
	referenceID := req.GetSupportReferenceId()
	if referenceID <= 0 {
		return nil, contract.InvalidArgument("revoke_support_reference_request", "support_reference_id", "A support reference id is required.")
	}

	scope, err := s.scopeForCaller(ctx)
	if err != nil {
		s.logger.Error("revoke support reference: resolve caller scope failed", "household_id", householdID, "error", err)
		return nil, contract.Internal("support_reference", "", "Could not process this request right now. Please try again.")
	}
	if !scopePermitsHousehold(scope, householdID) {
		return nil, supportReferenceHouseholdNotFoundFailure()
	}

	entityID := strconv.FormatInt(referenceID, 10)
	reg := auditRegistrations[revokeSupportReferenceFullMethod]
	entry := audit.Entry{
		ActorSubject:      actingSubject(ctx),
		ActorKind:         audit.ActorKindHuman,
		TargetHouseholdID: &householdID,
		Action:            reg.Action,
		EntityKind:        reg.EntityKind,
		EntityID:          &entityID,
		CorrelationID:     CorrelationIDFromContext(ctx),
	}
	if err := s.repo.RevokeSupportReference(ctx, householdID, referenceID, entry); err != nil {
		if errors.Is(err, ErrSupportReferenceNotFound) {
			return nil, supportReferenceNotFoundFailure()
		}
		s.logger.Error("revoke support reference failed", "household_id", householdID, "support_reference_id", referenceID, "error", err)
		return nil, contract.Internal("support_reference", "", "Could not revoke this support reference right now. Please try again.")
	}
	return &pb.RevokeSupportReferenceResponse{}, nil
}

// ListSupportReferences lists a household's support references (FR80),
// never the plaintext code -- only per-reference metadata:
// creation/expiry/revocation and use tracking.
func (s *LeafLabAPIServer) ListSupportReferences(ctx context.Context, req *pb.ListSupportReferencesRequest) (*pb.ListSupportReferencesResponse, error) {
	householdID := req.GetHouseholdId()
	if householdID <= 0 {
		return nil, contract.InvalidArgument("list_support_references_request", "household_id", "A household id is required.")
	}

	scope, err := s.scopeForCaller(ctx)
	if err != nil {
		s.logger.Error("list support references: resolve caller scope failed", "household_id", householdID, "error", err)
		return nil, contract.Internal("support_reference", "", "Could not list support references right now. Please try again.")
	}
	if !scopePermitsHousehold(scope, householdID) {
		return nil, supportReferenceHouseholdNotFoundFailure()
	}

	afterID, hasAfter, err := contract.DecodeSupportReferenceCursor(req.GetPage().GetPageToken())
	if err != nil {
		return nil, contract.InvalidArgument("list_support_references_request", "page_token", "This page link is no longer valid. Start again from the first page.")
	}
	limit := contract.ClampPageSize(req.GetPage().GetPageSize())

	rows, err := s.repo.ListSupportReferences(ctx, householdID, afterID, hasAfter, limit+1)
	if err != nil {
		s.logger.Error("list support references failed", "household_id", householdID, "error", err)
		return nil, contract.Internal("support_reference", "", "Could not list support references right now. Please try again.")
	}

	var nextToken string
	if int32(len(rows)) > limit {
		rows = rows[:limit]
		nextToken = contract.EncodeSupportReferenceCursor(rows[len(rows)-1].SupportReferenceID)
	}

	refs := make([]*pb.SupportReferenceInfo, 0, len(rows))
	for _, r := range rows {
		info := &pb.SupportReferenceInfo{
			SupportReferenceId: r.SupportReferenceID,
			CreatedAt:          contract.ToInstant(r.CreatedAt),
			ExpiresAt:          contract.ToInstant(r.ExpiresAt),
			Revoked:            r.RevokedAt != nil,
			ResolveCount:       r.ResolveCount,
		}
		if r.RevokedAt != nil {
			info.RevokedAt = contract.ToInstant(*r.RevokedAt)
		}
		if r.LastResolvedAt != nil {
			info.LastResolvedAt = contract.ToInstant(*r.LastResolvedAt)
		}
		refs = append(refs, info)
	}
	return &pb.ListSupportReferencesResponse{References: refs, Page: &pb.PageResponse{NextPageToken: nextToken}, ServerNow: contract.Now()}, nil
}

// resolveSupportReference is FR10.2's admin-side resolution of a support
// reference (FR80): a single LookupSupportReferenceByHash query classifies
// the code as unknown, expired, revoked or valid off one round trip
// (NFR2) -- unknown/expired/revoked all take the identical "no match" path
// back to ResolveToHousehold (nil rows, no error -- exactly what a
// person_identifier or partial_device_id query that finds nothing already
// returns), so none of the three failure classes is distinguishable from
// the others, or from an ordinary no-match resolve, by status, body or
// timing.
//
// Only a valid resolve does the extra work of incrementing
// resolve_count/last_resolved_at and writing a household-attributed audit
// row (FR80's "existence and use are visible to the owner in FR9's
// activity list") -- that asymmetry is deliberate and outside NFR2's
// scope, which requires only the *failure* outcomes to be indistinguishable
// from each other, not from a genuine success (the admin needs to know
// resolution succeeded; that is this RPC's entire purpose).
func (s *LeafLabAPIServer) resolveSupportReference(ctx context.Context, code string) ([]AdminBoardHealthRow, error) {
	lookup, found, err := s.repo.LookupSupportReferenceByHash(ctx, hashSupportReferenceCode(code))
	if err != nil {
		return nil, err
	}
	if !found || lookup.RevokedAt != nil || !lookup.ExpiresAt.After(time.Now()) {
		return nil, nil
	}

	householdID := lookup.HouseholdID
	entityID := strconv.FormatInt(lookup.SupportReferenceID, 10)
	entry := audit.Entry{
		ActorSubject:      actingSubject(ctx),
		ActorKind:         audit.ActorKindHuman,
		TargetHouseholdID: &householdID,
		Action:            supportReferenceResolveAction,
		EntityKind:        "support_reference",
		EntityID:          &entityID,
		CorrelationID:     CorrelationIDFromContext(ctx),
	}
	if err := s.repo.RecordSupportReferenceResolve(ctx, lookup.SupportReferenceID, entry); err != nil {
		return nil, err
	}

	return s.repo.AdminBoardHealthByHousehold(ctx, householdID)
}
