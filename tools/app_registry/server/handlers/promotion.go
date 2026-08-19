package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/whale-net/everything/libs/go/grpcauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// PromotionServer implements pb.PromotionRegistryServer, backed by
// repository.Registry. See ARCHITECTURE.md "Promotability" and
// "Authorization" for the rules enforced here: Promote/Rollback require
// auth.RequirePromoter for the target environment; the read RPCs require
// only auth.RequireAuthenticated.
type PromotionServer struct {
	pb.UnimplementedPromotionRegistryServer
	repo repository.Registry
}

// NewPromotionServer constructs a PromotionServer over repo.
func NewPromotionServer(repo repository.Registry) *PromotionServer {
	return &PromotionServer{repo: repo}
}

// Promote resolves the target artifact, enforces promotability (rejecting
// NOT_PROMOTABLE outright, requiring allow_override for VIA_CHART), then
// performs the SCD2 close-and-open write inside one transaction alongside
// the promotion_event row -- see repository.PromotionRepository.Promote and
// AGENTS.md "SCD2".
func (s *PromotionServer) Promote(ctx context.Context, req *pb.PromoteRequest) (*pb.PromoteResponse, error) {
	if req.EnvironmentKey == "" {
		return nil, status.Error(codes.InvalidArgument, "environment_key is required")
	}
	if err := auth.RequirePromoter(ctx, req.EnvironmentKey); err != nil {
		return nil, err
	}

	env, err := s.repo.Environments().Get(ctx, req.EnvironmentKey)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	if env.Archived {
		return nil, status.Errorf(codes.FailedPrecondition, "environment %q is archived", env.Key)
	}
	// ARCHITECTURE.md "Authorization": "reason is required on promotions to
	// any environment above rank 0" (dev is rank 0 by convention).
	if req.Reason == "" && env.Rank > 0 {
		return nil, status.Errorf(codes.InvalidArgument, "reason is required to promote to %q (rank %d)", env.Key, env.Rank)
	}

	lookup, err := promoteArtifactLookup(req)
	if err != nil {
		return nil, err
	}
	artifact, err := s.repo.Artifacts().GetArtifact(ctx, lookup)
	if err != nil {
		return nil, mapRepoErr(err)
	}

	candidate, err := s.buildCandidatePromotion(ctx, *env, *artifact, req.AllowOverride)
	if err != nil {
		return nil, err
	}

	if req.DryRun {
		resp := &pb.PromoteResponse{DryRun: true, Promotion: promotionToPB(candidate)}
		if current, cerr := s.repo.Promotions().GetCurrent(ctx, env.EnvironmentID, candidate.TargetKey); cerr == nil {
			resp.Superseded = promotionToPB(*current)
			resp.AlreadyPromoted = current.ArtifactID == artifact.ArtifactID && current.IsOverride == candidate.IsOverride
		} else if !errors.Is(cerr, repository.ErrNotFound) {
			return nil, mapRepoErr(cerr)
		}
		return resp, nil
	}

	if req.IdempotencyKey == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}

	resp, _, err := runIdempotent(ctx, s.repo, req.IdempotencyKey, "Promote",
		func() proto.Message { return &pb.PromoteResponse{} },
		func(ctx context.Context, r repository.Registry) (proto.Message, error) {
			// Short-circuit an already-current artifact rather than writing
			// an identical, redundant SCD2 row -- the whole point of a
			// distinct AlreadyPromoted flag on the response.
			if existing, gerr := r.Promotions().GetCurrent(ctx, env.EnvironmentID, candidate.TargetKey); gerr == nil {
				if existing.ArtifactID == artifact.ArtifactID && existing.IsOverride == candidate.IsOverride {
					return &pb.PromoteResponse{Promotion: promotionToPB(*existing), AlreadyPromoted: true}, nil
				}
			} else if !errors.Is(gerr, repository.ErrNotFound) {
				return nil, gerr
			}

			current, superseded, perr := r.Promotions().Promote(ctx, candidate)
			if perr != nil {
				return nil, perr
			}
			action := repository.PromotionActionPromote
			if candidate.IsOverride {
				action = repository.PromotionActionOverride
			}
			event, eerr := r.Promotions().RecordEvent(ctx, repository.PromotionEvent{
				PromotionID: current.PromotionID,
				Action:      action,
				Actor:       actorFromCtx(ctx),
				Reason:      req.Reason,
			})
			if eerr != nil {
				return nil, eerr
			}
			if werr := s.enqueueWriteback(ctx, r, *env, *current, current.PromotionID, event.EventID); werr != nil {
				return nil, werr
			}
			out := &pb.PromoteResponse{Promotion: promotionToPB(*current), Event: promotionEventToPB(*event)}
			if superseded != nil {
				out.Superseded = promotionToPB(*superseded)
			}
			return out, nil
		},
	)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return resp.(*pb.PromoteResponse), nil
}

// buildCandidatePromotion resolves artifact's promotability against
// ARCHITECTURE.md "Promotability" and, if legal, builds the (not yet
// written) repository.Promotion the caller is requesting. Every field is
// populated here -- not left for a later join -- so both dry_run responses
// and the fake repository (which does no SQL joins) see a complete row.
func (s *PromotionServer) buildCandidatePromotion(ctx context.Context, env repository.Environment, artifact repository.Artifact, allowOverride bool) (repository.Promotion, error) {
	isOverride := false
	switch artifact.Promotability {
	case repository.PromotabilityNotPromotable:
		return repository.Promotion{}, status.Errorf(codes.FailedPrecondition, "artifact %s is not promotable", artifact.ArtifactID)
	case repository.PromotabilityViaChart:
		if !allowOverride {
			return repository.Promotion{}, status.Errorf(codes.FailedPrecondition,
				"artifact %s belongs to a chart-deployed app; promoting it directly bypasses the chart -- pass allow_override to acknowledge", artifact.ArtifactID)
		}
		isOverride = true
	case repository.PromotabilityPromotable:
		// Legal regardless of allow_override.
	default:
		return repository.Promotion{}, status.Errorf(codes.FailedPrecondition, "artifact %s has unknown promotability", artifact.ArtifactID)
	}

	ownerFullName, err := s.ownerFullName(ctx, artifact)
	if err != nil {
		return repository.Promotion{}, mapRepoErr(err)
	}

	return repository.Promotion{
		EnvironmentID:  env.EnvironmentID,
		EnvironmentKey: env.Key,
		TargetKey:      repository.TargetKey(artifact.Kind, ownerFullName),
		Kind:           artifact.Kind,
		AppID:          artifact.AppID,
		ChartID:        artifact.ChartID,
		ArtifactID:     artifact.ArtifactID,
		Repository:     artifact.Repository,
		Version:        artifact.Version,
		Digest:         artifact.Digest,
		State:          repository.PromotionStateActive,
		IsOverride:     isOverride,
		ValidFrom:      time.Now().UTC(),
	}, nil
}

func (s *PromotionServer) ownerFullName(ctx context.Context, a repository.Artifact) (string, error) {
	if a.Kind != repository.ArtifactKindChart {
		app, err := s.repo.Apps().GetAppByID(ctx, a.AppID)
		if err != nil {
			return "", err
		}
		return app.FullName(), nil
	}
	chart, err := s.repo.Apps().GetChartByID(ctx, a.ChartID)
	if err != nil {
		return "", err
	}
	return chart.FullName(), nil
}

func promoteArtifactLookup(req *pb.PromoteRequest) (repository.ArtifactLookup, error) {
	switch {
	case req.ArtifactId != "":
		return repository.ArtifactLookup{ArtifactID: req.ArtifactId}, nil
	case req.Digest != "":
		return repository.ArtifactLookup{Digest: req.Digest}, nil
	case req.OwnerFullName != "":
		kind := artifactKindFromPB(req.Kind)
		if kind == "" {
			return repository.ArtifactLookup{}, status.Error(codes.InvalidArgument, "kind is required when identifying by owner_full_name")
		}
		if req.Version == "" {
			return repository.ArtifactLookup{}, status.Error(codes.InvalidArgument, "version is required when identifying by owner_full_name")
		}
		return repository.ArtifactLookup{OwnerFullName: req.OwnerFullName, Kind: kind, Version: req.Version}, nil
	default:
		return repository.ArtifactLookup{}, status.Error(codes.InvalidArgument, "artifact_id, digest, or owner_full_name+kind+version is required")
	}
}

// Rollback is sugar over Promote: it re-promotes whatever GetPrevious
// reports as the most recently superseded row for this target -- see
// api_messages_promotion.proto's doc comment on RollbackRequest.
func (s *PromotionServer) Rollback(ctx context.Context, req *pb.RollbackRequest) (*pb.RollbackResponse, error) {
	if req.EnvironmentKey == "" {
		return nil, status.Error(codes.InvalidArgument, "environment_key is required")
	}
	if err := auth.RequirePromoter(ctx, req.EnvironmentKey); err != nil {
		return nil, err
	}
	kind := artifactKindFromPB(req.Kind)
	if kind == "" {
		return nil, status.Error(codes.InvalidArgument, "kind is required")
	}
	if req.OwnerFullName == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_full_name is required")
	}

	env, err := s.repo.Environments().Get(ctx, req.EnvironmentKey)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	if req.Reason == "" && env.Rank > 0 {
		return nil, status.Errorf(codes.InvalidArgument, "reason is required to roll back %q (rank %d)", env.Key, env.Rank)
	}

	targetKey := repository.TargetKey(kind, req.OwnerFullName)
	previous, err := s.repo.Promotions().GetPrevious(ctx, env.EnvironmentID, targetKey)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Errorf(codes.FailedPrecondition, "no prior promotion of %s in %q to roll back to", req.OwnerFullName, env.Key)
		}
		return nil, mapRepoErr(err)
	}

	candidate := *previous
	candidate.PromotionID = ""
	candidate.State = repository.PromotionStateActive
	candidate.ValidFrom = time.Now().UTC()
	candidate.ValidTo = nil

	if req.DryRun {
		resp := &pb.RollbackResponse{DryRun: true, Promotion: promotionToPB(candidate)}
		if current, cerr := s.repo.Promotions().GetCurrent(ctx, env.EnvironmentID, targetKey); cerr == nil {
			resp.Superseded = promotionToPB(*current)
		} else if !errors.Is(cerr, repository.ErrNotFound) {
			return nil, mapRepoErr(cerr)
		}
		return resp, nil
	}

	if req.IdempotencyKey == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}

	resp, _, err := runIdempotent(ctx, s.repo, req.IdempotencyKey, "Rollback",
		func() proto.Message { return &pb.RollbackResponse{} },
		func(ctx context.Context, r repository.Registry) (proto.Message, error) {
			current, superseded, perr := r.Promotions().Promote(ctx, candidate)
			if perr != nil {
				return nil, perr
			}
			event, eerr := r.Promotions().RecordEvent(ctx, repository.PromotionEvent{
				PromotionID: current.PromotionID,
				Action:      repository.PromotionActionRollback,
				Actor:       actorFromCtx(ctx),
				Reason:      req.Reason,
			})
			if eerr != nil {
				return nil, eerr
			}
			if werr := s.enqueueWriteback(ctx, r, *env, *current, current.PromotionID, event.EventID); werr != nil {
				return nil, werr
			}
			out := &pb.RollbackResponse{Promotion: promotionToPB(*current), Event: promotionEventToPB(*event)}
			if superseded != nil {
				out.Superseded = promotionToPB(*superseded)
			}
			return out, nil
		},
	)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return resp.(*pb.RollbackResponse), nil
}

// GetEnvironmentState is the deploy-tooling read path: current state, or
// state at a past instant via the SCD2 window (StateAt). For chart
// promotions it also reports drift -- an image belonging to the chart that
// was separately promoted with allow_override, so the chart's pin no longer
// matches what's actually live. See ARCHITECTURE.md "Promotability".
func (s *PromotionServer) GetEnvironmentState(ctx context.Context, req *pb.GetEnvironmentStateRequest) (*pb.GetEnvironmentStateResponse, error) {
	if req.EnvironmentKey == "" {
		return nil, status.Error(codes.InvalidArgument, "environment_key is required")
	}

	env, err := s.repo.Environments().Get(ctx, req.EnvironmentKey)
	if err != nil {
		return nil, mapRepoErr(err)
	}

	var at *time.Time
	if req.At != 0 {
		t := unixToTime(req.At)
		at = &t
	}

	promotions, err := s.repo.Promotions().StateAt(ctx, env.EnvironmentID, at)
	if err != nil {
		return nil, mapRepoErr(err)
	}

	entries := make([]*pb.EnvironmentStateEntry, 0, len(promotions))
	// hashed mirrors entries (same filter applied), kept as repository types
	// so stateHash needs no proto conversion -- see stateHash's doc comment.
	hashed := make([]repository.Promotion, 0, len(promotions))
	type chartPin struct {
		digest string
		entry  *pb.EnvironmentStateEntry
	}
	chartPinByAppID := map[string]chartPin{}

	for _, p := range promotions {
		if req.Domain != "" {
			domain, derr := s.ownerDomain(ctx, p)
			if derr != nil || domain != req.Domain {
				continue
			}
		}
		artifact, aerr := s.repo.Artifacts().GetArtifact(ctx, repository.ArtifactLookup{ArtifactID: p.ArtifactID})
		if aerr != nil {
			return nil, mapRepoErr(aerr)
		}
		entry := &pb.EnvironmentStateEntry{Promotion: promotionToPB(p), Artifact: artifactToPB(*artifact)}
		if p.Kind == repository.ArtifactKindChart {
			for _, link := range artifact.Contains {
				entry.Images = append(entry.Images, artifactLinkToPB(link))
				chartPinByAppID[link.AppID] = chartPin{digest: link.Digest, entry: entry}
			}
		}
		entries = append(entries, entry)
		hashed = append(hashed, p)
	}

	for _, p := range promotions {
		if p.Kind != repository.ArtifactKindImage || !p.IsOverride {
			continue
		}
		pin, ok := chartPinByAppID[p.AppID]
		if !ok || pin.digest == p.Digest {
			continue
		}
		appFullName := ""
		if app, aerr := s.repo.Apps().GetAppByID(ctx, p.AppID); aerr == nil {
			appFullName = app.FullName()
		}
		pin.entry.Drift = append(pin.entry.Drift, &pb.DriftEntry{
			AppId:             p.AppID,
			AppFullName:       appFullName,
			ChartPinnedDigest: pin.digest,
			PromotedDigest:    p.Digest,
			PromotionId:       p.PromotionID,
		})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Promotion.PromotionId < entries[j].Promotion.PromotionId })

	asOf := req.At
	if asOf == 0 {
		asOf = time.Now().Unix()
	}

	return &pb.GetEnvironmentStateResponse{
		Environment: environmentToPB(*env),
		Entries:     entries,
		AsOf:        asOf,
		StateHash:   stateHash(hashed),
	}, nil
}

// enqueueWriteback writes one writeback_outbox row inside the caller's
// transaction (r is the Registry WithTx handed Promote/Rollback's fn — see
// AGENTS.md "SCD2" and ARCHITECTURE.md "Writeback: outbox -> Temporal"),
// extending AR-3c's existing SCD2 close-and-open transaction rather than
// opening a second one. This is the load-bearing property AR-4b exists to
// deliver: if the surrounding transaction rolls back, this row is rolled
// back with it, so a promotion can never exist without a corresponding
// writeback intent (or vice versa).
//
// state_hash is computed here, inside the same transaction, by re-reading
// StateAt(environmentID, nil) -- so its value is guaranteed consistent with
// what a GetEnvironmentState call for env.Key would return immediately
// after this transaction commits. This is what lets the Writeback
// activity's Publish step (AR-4b, tools/app_registry/worker) detect a
// no-op without a second registry round trip.
//
// current is the just-written promotion (its own PromotionID/EventID are
// passed separately since callers already have both close at hand) -- it's
// what ownerDomain needs to resolve the row's Domain, the denormalized
// column that lets the worker render/publish per (domain, environment)
// without a join -- see 015_writeback_outbox_domain.up.sql and
// tools/app_registry/architecture/12-writeback-outbox-temporal.md.
func (s *PromotionServer) enqueueWriteback(ctx context.Context, r repository.Registry, env repository.Environment, current repository.Promotion, promotionID, eventID string) error {
	domain, err := s.ownerDomain(ctx, current)
	if err != nil {
		return fmt.Errorf("enqueue writeback for promotion %s: resolve domain: %w", promotionID, err)
	}
	promotions, err := r.Promotions().StateAt(ctx, env.EnvironmentID, nil)
	if err != nil {
		return fmt.Errorf("enqueue writeback for promotion %s: read state for hash: %w", promotionID, err)
	}
	if _, err := r.Writeback().Enqueue(ctx, repository.WritebackOutbox{
		PromotionID:    promotionID,
		EnvironmentID:  env.EnvironmentID,
		EnvironmentKey: env.Key,
		Domain:         domain,
		EventID:        eventID,
		StateHash:      stateHash(promotions),
	}); err != nil {
		return fmt.Errorf("enqueue writeback for promotion %s: %w", promotionID, err)
	}
	return nil
}

// ownerDomain resolves p's owning app's/chart's domain. Only
// ArtifactKindChart is chart-owned -- every other kind (image, binary,
// firmware) is app-owned, matching ownerFullName's own `!= Chart` grouping
// above rather than a `== Image` check, which would wrongly try (and fail)
// a chart lookup for a binary/firmware promotion's empty ChartID.
func (s *PromotionServer) ownerDomain(ctx context.Context, p repository.Promotion) (string, error) {
	// Same non-CHART-owner rule as convert.go's artifactToPB/promotionToPB
	// (#780): IMAGE, BINARY, and FIRMWARE promotions all key off AppID, not
	// just IMAGE. Using Kind == Image here left BINARY promotions looking up
	// GetChartByID(ctx, "") and failing whenever a domain filter was applied.
	if p.Kind == repository.ArtifactKindChart {
		chart, err := s.repo.Apps().GetChartByID(ctx, p.ChartID)
		if err != nil {
			return "", err
		}
		return chart.Domain, nil
	}
	app, err := s.repo.Apps().GetAppByID(ctx, p.AppID)
	if err != nil {
		return "", err
	}
	return app.Domain, nil
}

// stateHash is a stable content hash of promotions' promoted-artifact
// identity, used by the writeback worker (AR-4b) to skip no-op commits --
// see GetEnvironmentStateResponse's doc comment and enqueueWriteback, which
// computes the same hash inside the promotion transaction so the value a
// worker sees on the outbox row is guaranteed consistent with a subsequent
// GetEnvironmentState call. Operates on repository.Promotion (not the pb
// entries) so enqueueWriteback never has to build proto messages just to
// hash them; Digest is already populated on Promotion by the artifact join
// done in StateAt (see promotionSelectBase in
// server/repository/postgres/promotion.go), so this is exactly the same
// data GetEnvironmentState's entries carry.
//
// promotions need not be pre-sorted -- stateHash sorts a local copy by
// promotion id so the hash is independent of map/query iteration order (see
// AGENTS.md/PLAN.md's "Workflow determinism" hazard -- this hash ends up
// inside a Temporal-visible value, so it must never depend on
// nondeterministic ordering).
func stateHash(promotions []repository.Promotion) string {
	sorted := append([]repository.Promotion(nil), promotions...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PromotionID < sorted[j].PromotionID })
	h := sha256.New()
	for _, p := range sorted {
		fmt.Fprintf(h, "%s|%s|%s|%v\n", p.PromotionID, p.Digest, p.State, p.IsOverride)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *PromotionServer) ListPromotions(ctx context.Context, req *pb.ListPromotionsRequest) (*pb.ListPromotionsResponse, error) {
	promotions, nextToken, err := s.repo.Promotions().ListPromotions(ctx, repository.PromotionListFilter{
		EnvironmentKey: req.EnvironmentKey,
		OwnerFullName:  req.OwnerFullName,
		IncludeHistory: req.IncludeHistory,
	}, req.GetPage().GetPageSize(), req.GetPage().GetPageToken())
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.ListPromotionsResponse{
		Promotions: promotionsToPB(promotions),
		// total_size is a PAGE-LOCAL count (len(promotions) <= page_size),
		// not the true total row count across all pages -- same tradeoff as
		// ListReconcileRuns/ListBuilds (see ARCHITECTURE.md's pagination
		// note).
		Page: &pb.PageResponse{NextPageToken: nextToken, TotalSize: int32(len(promotions))},
	}, nil
}

func (s *PromotionServer) ListPromotionEvents(ctx context.Context, req *pb.ListPromotionEventsRequest) (*pb.ListPromotionEventsResponse, error) {
	var since time.Time
	if req.Since != 0 {
		since = unixToTime(req.Since)
	}
	events, nextToken, err := s.repo.Promotions().ListEvents(ctx, repository.PromotionEventListFilter{
		PromotionID:    req.PromotionId,
		EnvironmentKey: req.EnvironmentKey,
		OwnerFullName:  req.OwnerFullName,
		Actor:          req.Actor,
		Since:          since,
	}, req.GetPage().GetPageSize(), req.GetPage().GetPageToken())
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.ListPromotionEventsResponse{
		Events: promotionEventsToPB(events),
		// total_size is a PAGE-LOCAL count (len(events) <= page_size), not
		// the true total row count across all pages -- same tradeoff as
		// ListReconcileRuns/ListBuilds (see ARCHITECTURE.md's pagination
		// note).
		Page: &pb.PageResponse{NextPageToken: nextToken, TotalSize: int32(len(events))},
	}, nil
}

// actorFromCtx reads the authenticated principal recorded on
// promotion_event.actor. Claims are guaranteed present here -- every caller
// of this helper runs after auth.RequirePromoter has already succeeded.
func actorFromCtx(ctx context.Context) string {
	if claims, ok := grpcauth.ClaimsFromContext(ctx); ok {
		return claims.Subject
	}
	return ""
}
