package main

// FR9 owner-readable activity -- implementation for #1348.
//
// ListHouseholdActivity merges two independently-queried sources into one
// list, most recent first (activity_repository.go's ListAuditActivity and
// ListClaimAttemptActivity): audit_log rows for every ordinary write, and
// FR76.7's claim-attempt detection, which CompleteClaim never writes an
// audit row for when the attempt is against a real household's board (see
// claim.go's CompleteClaim doc comment -- it only ever succeeds, and so
// only ever audits, against a never-claimed or Unadopted board). "One
// list, one voice" (FR9): both sources render through the exact same
// leaflab/api/activity.Render seam and land in the exact same []ActivityEntry,
// with no field or section distinguishing which source (or which actor
// kind) produced a given entry.
//
// Merging two keyset sources into one paginated list is the one piece of
// genuine complexity here. ListAuditActivity is properly keyset-paginated
// in SQL; ListClaimAttemptActivity is fetched in full every call (its doc
// comment explains why that's safe at this system's scale) and filtered/
// merged against the audit page in Go. mergeActivitySources' doc comment
// below has the correctness argument for why fetching
// limit+len(claimCandidates)+1 audit rows is always enough to determine
// both the correct top `limit` merged entries and whether a further page
// exists, no matter how the two sources interleave.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/whale-net/everything/leaflab/api/activity"
	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// adminActorActions names every action whose acting principal is always an
// administrator (FR10, FR12 activation), never a household member --
// actorLabelForAudit uses this to decide "an administrator" vs. the
// you/another-household-member phrasing every other action gets. A plain
// set, not a per-Action bool on some larger table, because it is consulted
// nowhere else: leaflab/api/audit_registry.go's auditRegistrations already
// separates admin RPCs from member RPCs by file (server.go's "Admin"
// section) and this is the one other place that distinction matters.
var adminActorActions = map[string]bool{
	audit.ActionElevate:                    true,
	"RenewElevation":                       true,
	"EndElevation":                         true,
	"ResolveToHousehold":                   true,
	activity.SupportReferenceResolveAction: true,
}

// isBeforeCursor reports whether the row at (occurredAt, tag) sorts after
// the cursor position (afterOccurredAt, afterTag) in
// ListHouseholdActivity's descending (most-recent-first) order -- i.e.
// whether it still needs to be returned on this or a later page. Mirrors
// activity_repository.go's ListAuditActivity SQL row-comparison exactly,
// so the same predicate governs both the audit query (in SQL) and the
// claim-candidate filter below (in Go).
func isBeforeCursor(occurredAt time.Time, tag string, afterOccurredAt time.Time, afterTag string) bool {
	if !occurredAt.Equal(afterOccurredAt) {
		return occurredAt.Before(afterOccurredAt)
	}
	return tag < afterTag
}

// activityCandidate is one not-yet-rendered row from either source, kept
// in this common shape so mergeActivitySources can sort both sources
// together on (OccurredAt, Tag) alone before either is ever rendered into
// a proto message -- rendering (which, for a ClaimBoard row, costs an
// extra board lookup) only ever happens for rows that survive the merge
// and the page-size cutoff.
type activityCandidate struct {
	Tag        string
	OccurredAt time.Time
	Audit      *AuditActivityRow
	Claim      *ClaimAttemptActivityRow
}

// moreRecentThan reports whether c sorts before other in
// ListHouseholdActivity's descending order.
func (c activityCandidate) moreRecentThan(other activityCandidate) bool {
	if !c.OccurredAt.Equal(other.OccurredAt) {
		return c.OccurredAt.After(other.OccurredAt)
	}
	return c.Tag > other.Tag
}

// mergeActivitySources combines auditRows (already keyset-filtered and
// capped by the caller) with claimRows (unfiltered -- filtered here
// against the cursor) into one descending-order, page-sized slice, plus
// whether a further page exists.
//
// Correctness argument for "auditRows must be fetched with limit +
// len(claimCandidates) + 1": after filtering, at most len(claimCandidates)
// claim rows can appear in the merged order at all, so at most that many
// of the eventual top `limit` merged entries can be claim rows -- the rest
// (at least limit - len(claimCandidates), and in the worst case all of
// limit) must come from auditRows. Fetching limit+len(claimCandidates)+1
// audit rows therefore always supplies enough audit candidates to fill
// whatever share of the top `limit` merged slots audit rows end up
// occupying, with one row to spare to prove a further page exists,
// regardless of how the two sources actually interleave. This holds
// however few rows the database actually has too: a shorter auditRows
// slice than requested is exactly "the database had fewer", which is the
// correct input to the same hasMore computation below.
func mergeActivitySources(auditRows []AuditActivityRow, claimCandidates []ClaimAttemptActivityRow, limit int32) (page []activityCandidate, hasMore bool) {
	candidates := make([]activityCandidate, 0, len(auditRows)+len(claimCandidates))
	for i := range auditRows {
		candidates = append(candidates, activityCandidate{Tag: auditRows[i].Tag, OccurredAt: auditRows[i].OccurredAt, Audit: &auditRows[i]})
	}
	for i := range claimCandidates {
		candidates = append(candidates, activityCandidate{Tag: claimCandidates[i].Tag, OccurredAt: claimCandidates[i].OccurredAt, Claim: &claimCandidates[i]})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].moreRecentThan(candidates[j])
	})

	if int32(len(candidates)) > limit {
		return candidates[:limit], true
	}
	return candidates, false
}

// householdActivityFailure is the one contract.Internal value every
// ListHouseholdActivity failure past authorization uses -- nothing about
// this RPC's internal errors (a merge source failing, a renderer having no
// registration) is persona-appropriate to surface more specifically than
// "could not list this right now" (FR59.2).
func householdActivityFailure() error {
	return contract.Internal("household_activity", "", "Could not list this household's activity right now. Please try again.")
}

// authorizeHouseholdActivityAccess checks the caller's Scope permits
// householdID (an ordinary current member, same predicate
// authorizeHouseholdAccess uses for GetHousehold/ListHouseholdMembers), or
// -- FR9's explicit extension over that check -- that they hold an active
// admin elevation against exactly this household (FR10.3). Mirrors
// authorizeBoardAccess's member-then-elevation fallback shape. Returns the
// same not-found failure whichever of "doesn't exist" / "not a member,
// never elevated" applies, so response shape carries no oracle (NFR2).
func (s *LeafLabAPIServer) authorizeHouseholdActivityAccess(ctx context.Context, householdID int64) error {
	scope, err := s.scopeForCaller(ctx)
	if err != nil {
		s.logger.Error("list household activity: resolve caller scope failed", "household_id", householdID, "error", err)
		return householdActivityFailure()
	}
	ref := authz.EntityRef{Kind: authz.EntityHousehold, ID: householdID}
	res := authz.Resolution{HouseholdID: householdID}
	if scope.Permits(ref, res) {
		return nil
	}

	elevatedScope, err := s.elevatedBoardScope(ctx, householdID)
	if err != nil {
		s.logger.Error("list household activity: check admin elevation failed", "household_id", householdID, "error", err)
		return householdActivityFailure()
	}
	if elevatedScope != nil && elevatedScope.Permits(ref, res) {
		return nil
	}
	return householdNotFoundFailure()
}

// publicEntityKind translates an audit.Entry.EntityKind (internal, matches
// a table name for several actions -- household_membership, device_config,
// admin_resolution) into ActivityEntry.entity_kind's public vocabulary
// (api.proto: "never a raw table name"). Kinds already legitimate product
// vocabulary in their own right (board, household, support_reference) pass
// through unchanged.
func publicEntityKind(entityKind string) string {
	switch entityKind {
	case "household_membership":
		return "membership"
	case "device_config":
		return "board_configuration"
	case "admin_resolution":
		return "admin_lookup"
	default:
		return entityKind
	}
}

// actorLabelForAudit resolves an audit_log row's raw actor_subject into
// the "you" / "another household member" / "an administrator" phrasing
// every Registry Template in leaflab/api/activity expects in
// RenderInput.ActorLabel -- the one comparison against the calling
// principal's own subject, made once here rather than re-derived per
// Template.
func actorLabelForAudit(action, actorSubject, callerSubject string) string {
	if adminActorActions[action] {
		return "an administrator"
	}
	return activity.PersonLabel(actorSubject == callerSubject, "another household member")
}

// entityLabelForAudit resolves the handful of audit actions whose
// Template consults RenderInput.EntityLabel. Every other action's
// Template ignores EntityLabel entirely, so this returns "" for them --
// harmless, since nothing reads it.
func (s *LeafLabAPIServer) entityLabelForAudit(ctx context.Context, row AuditActivityRow, callerSubject string) string {
	if row.EntityID == nil {
		return ""
	}
	switch {
	case row.Action == "InviteMember" && row.EntityKind == "household_membership":
		return activity.PersonLabel(*row.EntityID == callerSubject, "a new member")
	case row.Action == "RemoveMember" && row.EntityKind == "household_membership":
		return activity.PersonLabel(*row.EntityID == callerSubject, "a household member")
	case row.Action == "ClaimBoard" && row.EntityKind == "board":
		return s.boardLabelForEntityID(ctx, *row.EntityID)
	default:
		return ""
	}
}

// boardLabelForEntityID resolves a ClaimBoard audit row's numeric
// entity_id (a board_id, per repository.go's CompleteClaim) to its
// device_id -- the same "board display name falls back to device_id"
// convention repository.go's scanAdminBoardHealth already uses, kept
// consistent here rather than inventing a second fallback rule. Falls back
// to a generic label rather than failing the whole request: a rendering
// gap here is far less severe than the caller losing their entire activity
// page over one malformed/unresolvable row.
func (s *LeafLabAPIServer) boardLabelForEntityID(ctx context.Context, entityID string) string {
	boardID, err := strconv.ParseInt(entityID, 10, 64)
	if err != nil {
		return "a board"
	}
	board, err := s.repo.GetBoardByID(ctx, boardID)
	if err != nil {
		s.logger.Error("list household activity: board label lookup failed", "board_id", boardID, "error", err)
		return "a board"
	}
	return board.DeviceID
}

// renderAuditActivityEntry turns one AuditActivityRow into an
// ActivityEntry (FR9, FR59.2, FR64). err is non-nil only when
// leaflab/api/activity.Render finds no registered Template for this row's
// (action, entity_kind, actor_kind) -- a missing-registration bug, never a
// data problem, which is why it fails the whole request (householdActivityFailure)
// rather than being swallowed per-row.
func (s *LeafLabAPIServer) renderAuditActivityEntry(ctx context.Context, row AuditActivityRow, callerSubject string) (*pb.ActivityEntry, error) {
	in := activity.RenderInput{
		ActorLabel:  actorLabelForAudit(row.Action, row.ActorSubject, callerSubject),
		EntityLabel: s.entityLabelForAudit(ctx, row, callerSubject),
	}
	sentence, ok := activity.Render(row.Action, row.EntityKind, row.ActorKind, in)
	if !ok {
		return nil, fmt.Errorf("no activity renderer registered for action=%q entity_kind=%q actor_kind=%q", row.Action, row.EntityKind, row.ActorKind)
	}
	return &pb.ActivityEntry{
		Sentence:   sentence,
		EntityKind: publicEntityKind(row.EntityKind),
		OccurredAt: contract.ToInstant(row.OccurredAt),
	}, nil
}

// renderClaimAttemptEntry turns one ClaimAttemptActivityRow into an
// ActivityEntry (FR76.7). Never sets RenderInput.ActorLabel -- A29: the
// attempting principal belongs to another household and is never
// identified, enforced by leaflab/api/activity's Template for this Key
// simply never referencing it.
func renderClaimAttemptEntry(row ClaimAttemptActivityRow) (*pb.ActivityEntry, error) {
	in := activity.RenderInput{EntityLabel: row.DeviceID, Outcome: row.Outcome}
	sentence, ok := activity.Render(activity.ClaimAttemptAction, activity.ClaimAttemptEntityKind, audit.ActorKindHuman, in)
	if !ok {
		return nil, errors.New("no activity renderer registered for the ClaimAttempt/board synthetic entry")
	}
	return &pb.ActivityEntry{
		Sentence:   sentence,
		EntityKind: activity.ClaimAttemptEntityKind,
		OccurredAt: contract.ToInstant(row.OccurredAt),
	}, nil
}

// ListHouseholdActivity lists household_id's activity, most recent first,
// keyset-paginated (FR9, FR61) -- see this file's doc comment for the
// two-source merge and mergeActivitySources for the pagination
// correctness argument.
func (s *LeafLabAPIServer) ListHouseholdActivity(ctx context.Context, req *pb.ListHouseholdActivityRequest) (*pb.ListHouseholdActivityResponse, error) {
	householdID := req.GetHouseholdId()
	if householdID <= 0 {
		return nil, contract.InvalidArgument("list_household_activity_request", "household_id", "A household id is required.")
	}
	if err := s.authorizeHouseholdActivityAccess(ctx, householdID); err != nil {
		return nil, err
	}

	afterOccurredAt, afterTag, hasAfter, err := contract.DecodeActivityCursor(req.GetPage().GetPageToken())
	if err != nil {
		return nil, contract.InvalidArgument("list_household_activity_request", "page_token", "This page link is no longer valid. Start again from the first page.")
	}
	limit := contract.ClampPageSize(req.GetPage().GetPageSize())

	claimRows, err := s.repo.ListClaimAttemptActivity(ctx, householdID)
	if err != nil {
		s.logger.Error("list household activity: claim attempts failed", "household_id", householdID, "error", err)
		return nil, householdActivityFailure()
	}
	claimCandidates := make([]ClaimAttemptActivityRow, 0, len(claimRows))
	for _, row := range claimRows {
		if !hasAfter || isBeforeCursor(row.OccurredAt, row.Tag, afterOccurredAt, afterTag) {
			claimCandidates = append(claimCandidates, row)
		}
	}

	auditLimit := limit + int32(len(claimCandidates)) + 1
	auditRows, err := s.repo.ListAuditActivity(ctx, householdID, afterOccurredAt, afterTag, hasAfter, auditLimit)
	if err != nil {
		s.logger.Error("list household activity: audit rows failed", "household_id", householdID, "error", err)
		return nil, householdActivityFailure()
	}

	page, hasMore := mergeActivitySources(auditRows, claimCandidates, limit)

	callerSubject := actingSubject(ctx)
	entries := make([]*pb.ActivityEntry, 0, len(page))
	for _, c := range page {
		var entry *pb.ActivityEntry
		var err error
		if c.Audit != nil {
			entry, err = s.renderAuditActivityEntry(ctx, *c.Audit, callerSubject)
		} else {
			entry, err = renderClaimAttemptEntry(*c.Claim)
		}
		if err != nil {
			s.logger.Error("list household activity: render failed", "household_id", householdID, "error", err)
			return nil, householdActivityFailure()
		}
		entries = append(entries, entry)
	}

	var nextToken string
	if hasMore {
		last := page[len(page)-1]
		nextToken = contract.EncodeActivityCursor(last.OccurredAt, last.Tag)
	}

	return &pb.ListHouseholdActivityResponse{
		Entries:   entries,
		Page:      &pb.PageResponse{NextPageToken: nextToken},
		ServerNow: contract.Now(),
	}, nil
}
