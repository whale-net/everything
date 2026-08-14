package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/semver"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var semverRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// artifactLog is this file's logger name, matching app.go's appLog pattern
// -- AdoptArtifact (AR-7e, issue #558) is the one write RPC in this file
// whose audit trail is a log line rather than a stored column (its `reason`
// is validated as required but not persisted -- see
// repository.ArtifactRepository.AdoptArtifact's doc comment and
// AppServer.SetAppStatus's identically-shaped `reason` parameter), so this
// line is load-bearing, not incidental.
var artifactLog = logging.Get("app-registry-handlers")

// ArtifactServer implements pb.ArtifactRegistryServer, backed by
// repository.Registry.
type ArtifactServer struct {
	pb.UnimplementedArtifactRegistryServer
	repo repository.Registry
}

// NewArtifactServer constructs an ArtifactServer over repo.
func NewArtifactServer(repo repository.Registry) *ArtifactServer {
	return &ArtifactServer{repo: repo}
}

func (s *ArtifactServer) RecordBuild(ctx context.Context, req *pb.RecordBuildRequest) (*pb.RecordBuildResponse, error) {
	if err := auth.Require(ctx, auth.RoleBuilder); err != nil {
		return nil, err
	}
	if req.WorkflowRunId == "" {
		return nil, status.Error(codes.InvalidArgument, "workflow_run_id is required")
	}
	if req.GitSha == "" {
		return nil, status.Error(codes.InvalidArgument, "git_sha is required")
	}
	if req.IdempotencyKey == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}

	attempt := req.WorkflowAttempt
	if attempt == 0 {
		attempt = 1
	}

	resp, _, err := runIdempotent(ctx, s.repo, req.IdempotencyKey, "RecordBuild",
		func() proto.Message { return &pb.RecordBuildResponse{} },
		func(ctx context.Context, r repository.Registry) (proto.Message, error) {
			var startedAt *time.Time
			if req.StartedAt != 0 {
				t := unixToTime(req.StartedAt)
				startedAt = &t
			}
			build, alreadyRecorded, err := r.Builds().RecordBuild(ctx, repository.Build{
				GitSHA:          req.GitSha,
				GitRef:          req.GitRef,
				WorkflowRunID:   req.WorkflowRunId,
				WorkflowAttempt: attempt,
				Actor:           req.Actor,
				StartedAt:       startedAt,
			})
			if err != nil {
				return nil, err
			}
			return &pb.RecordBuildResponse{Build: buildToPB(*build), AlreadyRecorded: alreadyRecorded}, nil
		},
	)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return resp.(*pb.RecordBuildResponse), nil
}

func (s *ArtifactServer) RecordArtifact(ctx context.Context, req *pb.RecordArtifactRequest) (*pb.RecordArtifactResponse, error) {
	if err := auth.Require(ctx, auth.RoleBuilder); err != nil {
		return nil, err
	}
	if req.BuildId == "" {
		return nil, status.Error(codes.InvalidArgument, "build_id is required")
	}
	kind := artifactKindFromPB(req.Kind)
	if kind == "" {
		return nil, status.Error(codes.InvalidArgument, "kind is required")
	}
	if req.OwnerFullName == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_full_name is required")
	}
	if req.Digest == "" || !strings.HasPrefix(req.Digest, "sha256:") {
		return nil, status.Error(codes.InvalidArgument, `digest is required and must be "sha256:..."`)
	}
	if req.Version != "" && !semverRe.MatchString(req.Version) {
		return nil, status.Errorf(codes.InvalidArgument, "version %q must match v<major>.<minor>.<patch>", req.Version)
	}
	if kind == repository.ArtifactKindImage && len(req.Contains) > 0 {
		return nil, status.Error(codes.InvalidArgument, "contains is only valid for kind == CHART")
	}
	if req.IdempotencyKey == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}

	resp, _, err := runIdempotent(ctx, s.repo, req.IdempotencyKey, "RecordArtifact",
		func() proto.Message { return &pb.RecordArtifactResponse{} },
		func(ctx context.Context, r repository.Registry) (proto.Message, error) {
			owner, domain, err := s.resolveOwner(ctx, r, kind, req.OwnerFullName)
			if err != nil {
				return nil, err
			}
			// AR-7b: RecordArtifact's direct-create fallback (no prior
			// BeginPublish) is only legal while this owner's domain is
			// still at adoption stage "observe" -- see
			// repository.ArtifactRepository.RecordArtifact's doc comment
			// and ARCHITECTURE.md "Backward compatibility during rollout".
			stage, err := r.DomainAdoption().GetStage(ctx, domain)
			if err != nil {
				return nil, err
			}

			a := repository.Artifact{
				Kind:        kind,
				Repository:  req.Repository,
				Version:     req.Version,
				Digest:      req.Digest,
				BuildID:     req.BuildId,
				PublishedAt: unixToTime(req.PublishedAt),
			}
			if kind == repository.ArtifactKindImage {
				a.AppID = owner
			} else {
				a.ChartID = owner
			}

			artifact, alreadyRecorded, err := r.Artifacts().RecordArtifact(ctx, a, containedImagesFromPB(req.Contains), stage)
			if err != nil {
				return nil, err
			}
			return &pb.RecordArtifactResponse{Artifact: artifactToPB(*artifact), AlreadyRecorded: alreadyRecorded}, nil
		},
	)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return resp.(*pb.RecordArtifactResponse), nil
}

// resolveOwner resolves owner_full_name to an app_id or chart_id (and its
// domain, needed by RecordArtifact's AR-7b adoption-stage check), depending
// on kind, wrapping repository.ErrNotFound as ErrOwnerNotReconciled (which
// itself wraps ErrInvalidArgument — an unknown owner is a caller mistake,
// not a "row doesn't exist yet" case). The wrapped message names the owner
// and hints at the most common cause (see issue #548): a release running
// ahead of ReconcileApps, which runs on push to main via ci.yml (see
// .github/actions/app-registry-reconcile). mapRepoErr (errors.go) attaches a
// structured apierrors.ReasonOwnerNotReconciled detail for this specific
// sentinel (issue #547) so a caller can classify the failure without
// parsing this message.
func (s *ArtifactServer) resolveOwner(ctx context.Context, r repository.Registry, kind repository.ArtifactKind, ownerFullName string) (ownerID, domain string, err error) {
	if kind == repository.ArtifactKindImage {
		app, aerr := r.Apps().GetAppByFullName(ctx, ownerFullName)
		if aerr != nil {
			return "", "", fmt.Errorf("%w: app %q not found -- has it been reconciled? (ReconcileApps runs on push to main via ci.yml)", repository.ErrOwnerNotReconciled, ownerFullName)
		}
		// AR-7c (issue #558): recording against an ARCHIVED owner is
		// rejected, same rule AssertApps enforces for identity assertion --
		// see repository.ErrOwnerArchived's doc comment. Before AR-7c this
		// succeeded silently, which was not intended.
		if app.Status == repository.StatusArchived {
			return "", "", fmt.Errorf("%w: app %q", repository.ErrOwnerArchived, ownerFullName)
		}
		return app.AppID, app.Domain, nil
	}
	chart, cerr := r.Apps().GetChartByFullName(ctx, ownerFullName)
	if cerr != nil {
		return "", "", fmt.Errorf("%w: chart %q not found -- has it been reconciled? (ReconcileApps runs on push to main via ci.yml)", repository.ErrOwnerNotReconciled, ownerFullName)
	}
	if chart.Status == repository.StatusArchived {
		return "", "", fmt.Errorf("%w: chart %q", repository.ErrOwnerArchived, ownerFullName)
	}
	return chart.ChartID, chart.Domain, nil
}

func (s *ArtifactServer) ListArtifacts(ctx context.Context, req *pb.ListArtifactsRequest) (*pb.ListArtifactsResponse, error) {
	if err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	artifacts, err := s.repo.Artifacts().ListArtifacts(ctx, repository.ArtifactListFilter{
		OwnerFullName:  req.OwnerFullName,
		Kind:           artifactKindFromPB(req.Kind),
		PromotableOnly: req.PromotableOnly,
		Provenance:     artifactProvenanceFromPB(req.Provenance),
	})
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.ListArtifactsResponse{
		Artifacts: artifactsToPB(artifacts),
		Page:      &pb.PageResponse{TotalSize: int32(len(artifacts))},
	}, nil
}

func (s *ArtifactServer) GetArtifact(ctx context.Context, req *pb.GetArtifactRequest) (*pb.GetArtifactResponse, error) {
	if err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	lookup, err := artifactLookupFromGetRequest(req)
	if err != nil {
		return nil, err
	}
	artifact, err := s.repo.Artifacts().GetArtifact(ctx, lookup)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	resp := &pb.GetArtifactResponse{Artifact: artifactToPB(*artifact)}
	if build, err := s.repo.Builds().GetBuild(ctx, artifact.BuildID); err == nil {
		resp.Build = buildToPB(*build)
	}
	return resp, nil
}

func artifactLookupFromGetRequest(req *pb.GetArtifactRequest) (repository.ArtifactLookup, error) {
	switch {
	case req.ArtifactId != "":
		return repository.ArtifactLookup{ArtifactID: req.ArtifactId}, nil
	case req.Digest != "":
		return repository.ArtifactLookup{Digest: req.Digest}, nil
	case req.OwnerFullName != "":
		return repository.ArtifactLookup{
			OwnerFullName: req.OwnerFullName,
			Kind:          artifactKindFromPB(req.Kind),
			Version:       req.Version,
		}, nil
	default:
		return repository.ArtifactLookup{}, errMissingArtifactLookup
	}
}

var errMissingArtifactLookup = status.Error(codes.InvalidArgument, "artifact_id, digest, or owner_full_name+kind+version is required")

func (s *ArtifactServer) ResolveArtifact(ctx context.Context, req *pb.ResolveArtifactRequest) (*pb.ResolveArtifactResponse, error) {
	if err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	var lookup repository.ArtifactLookup
	switch {
	case req.ArtifactId != "":
		lookup = repository.ArtifactLookup{ArtifactID: req.ArtifactId}
	case req.Digest != "":
		lookup = repository.ArtifactLookup{Digest: req.Digest}
	default:
		return nil, status.Error(codes.InvalidArgument, "artifact_id or digest is required")
	}

	artifact, images, builds, err := s.repo.Artifacts().ResolveArtifact(ctx, lookup)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.ResolveArtifactResponse{
		Artifact: artifactToPB(*artifact),
		Images:   artifactsToPB(images),
		Builds:   buildsToPB(builds),
	}, nil
}

// ListArtifactPins is ResolveArtifact's reverse walk: given an image
// artifact, returns the chart artifacts that pin it -- see
// ARCHITECTURE.md "ListArtifactPins" for the not-found-vs-empty
// distinction (FR3.2/FR3.3).
func (s *ArtifactServer) ListArtifactPins(ctx context.Context, req *pb.ListArtifactPinsRequest) (*pb.ListArtifactPinsResponse, error) {
	if err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	var lookup repository.ArtifactLookup
	switch {
	case req.ArtifactId != "":
		lookup = repository.ArtifactLookup{ArtifactID: req.ArtifactId}
	case req.Digest != "":
		lookup = repository.ArtifactLookup{Digest: req.Digest}
	default:
		return nil, status.Error(codes.InvalidArgument, "artifact_id or digest is required")
	}

	chartArtifacts, err := s.repo.Artifacts().ListArtifactPins(ctx, lookup)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.ListArtifactPinsResponse{ChartArtifacts: artifactsToPB(chartArtifacts)}, nil
}

// maxAllocateVersionAttempts bounds the retry loop below. A collision means
// a concurrent caller's INSERT into version_allocation won the race for the
// version this attempt computed (see postgres/errors.go's ErrAlreadyExists
// doc comment) — retrying recomputes "next" against the now-committed
// state, so it converges quickly under realistic contention; this is a
// backstop against a pathological hot loop, not an expected steady state.
const maxAllocateVersionAttempts = 5

// AllocateVersion implements phase AR-5's version allocation RPC. AR-5a
// ships this fully working and tested but wired to nothing: no
// domain_adoption row is ever set to 'allocate' by this change, and
// tools/release_helper_go/cmd/plan.go's git-tag path (autoIncrementVersion)
// is untouched — see PLAN.md's AR-5 status for what remains before any
// domain can actually be cut over.
func (s *ArtifactServer) AllocateVersion(ctx context.Context, req *pb.AllocateVersionRequest) (*pb.AllocateVersionResponse, error) {
	if err := auth.Require(ctx, auth.RoleBuilder); err != nil {
		return nil, err
	}
	kind := artifactKindFromPB(req.Kind)
	if kind == "" {
		return nil, status.Error(codes.InvalidArgument, "kind is required")
	}
	if req.OwnerFullName == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_full_name is required")
	}
	if req.IdempotencyKey == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}
	if req.ExplicitVersion != "" {
		// AR-5 addendum item 3: reject a prerelease or build-metadata
		// version explicitly, rather than half-accepting one and sorting it
		// wrongly (see libs/go/semver.ParseRelease).
		if _, err := semver.ParseRelease(req.ExplicitVersion); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "explicit_version: %v", err)
		}
	} else if req.Increment != "major" && req.Increment != "minor" && req.Increment != "patch" {
		return nil, status.Errorf(codes.InvalidArgument, `increment must be "major", "minor", or "patch" (got %q); or set explicit_version`, req.Increment)
	}

	domain, ownerID, repo, err := s.resolveOwnerAndDomain(ctx, kind, req.OwnerFullName)
	if err != nil {
		return nil, err
	}

	// Per-domain adoption gate — ARCHITECTURE.md "Resolved questions" #3 and
	// PLAN.md's AR-5 scope: AllocateVersion serves only domains explicitly
	// cut over to stage "allocate", so a misconfigured caller fails loudly
	// rather than silently allocating from the wrong source of truth. No
	// domain is at "allocate" as of AR-5a.
	stage, err := s.repo.DomainAdoption().GetStage(ctx, domain)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	if stage != repository.DomainAdoptionStageAllocate {
		return nil, status.Errorf(codes.FailedPrecondition,
			"domain %q is at adoption stage %q; AllocateVersion only serves domains at stage \"allocate\"", domain, stage)
	}

	var resp *pb.AllocateVersionResponse
	var replayed bool
	for attempt := 0; attempt < maxAllocateVersionAttempts; attempt++ {
		out, wasReplayed, ferr := runIdempotent(ctx, s.repo, req.IdempotencyKey, "AllocateVersion",
			func() proto.Message { return &pb.AllocateVersionResponse{} },
			func(ctx context.Context, r repository.Registry) (proto.Message, error) {
				alloc, aerr := r.Artifacts().AllocateVersion(ctx, kind, ownerID, repo, req.Increment, req.ExplicitVersion)
				if aerr != nil {
					return nil, aerr
				}
				return &pb.AllocateVersionResponse{Version: alloc.Version, PreviousVersion: alloc.PreviousVersion}, nil
			},
		)
		if ferr == nil {
			resp = out.(*pb.AllocateVersionResponse)
			replayed = wasReplayed
			err = nil
			break
		}
		err = ferr
		// An explicit_version collision is a real "fails if taken" per
		// api_messages_artifact.proto's doc comment — not a race worth
		// retrying. Only an auto-increment collision (someone else's
		// concurrent allocation took the version THIS attempt computed) is
		// retried, in a fresh transaction — the aborted one carries no
		// partial state to resume from (see postgres/errors.go).
		if req.ExplicitVersion == "" && errors.Is(ferr, repository.ErrAlreadyExists) {
			continue
		}
		break
	}
	if err != nil {
		return nil, mapRepoErr(err)
	}
	if replayed {
		resp.AlreadyAllocated = true
	}
	return resp, nil
}

// resolveOwnerAndDomain resolves owner_full_name to its owning row's id,
// domain, and stored repository (image_repository/chart_repository), for
// AllocateVersion's storage key + adoption-stage gate and, as of AR-7b,
// BeginPublish's repositoryHint on its ∅ -> publishing branch. Mirrors
// resolveOwner but also needs Domain/repository, which resolveOwner's
// caller (RecordArtifact) doesn't.
func (s *ArtifactServer) resolveOwnerAndDomain(ctx context.Context, kind repository.ArtifactKind, ownerFullName string) (domain, ownerID, repo string, err error) {
	if kind == repository.ArtifactKindImage {
		app, aerr := s.repo.Apps().GetAppByFullName(ctx, ownerFullName)
		if aerr != nil {
			return "", "", "", status.Errorf(codes.InvalidArgument, "app %q not found -- has it been reconciled? (ReconcileApps runs on push to main via ci.yml)", ownerFullName)
		}
		// AR-7c (issue #558): same ARCHIVED rejection resolveOwner enforces
		// for RecordArtifact -- see repository.ErrOwnerArchived's doc
		// comment. Applied here too so BeginPublish/AllocateVersion can't
		// start a publish against an owner a human has archived.
		if app.Status == repository.StatusArchived {
			return "", "", "", mapRepoErr(fmt.Errorf("%w: app %q", repository.ErrOwnerArchived, ownerFullName))
		}
		return app.Domain, app.AppID, app.ImageRepository, nil
	}
	chart, cerr := s.repo.Apps().GetChartByFullName(ctx, ownerFullName)
	if cerr != nil {
		return "", "", "", status.Errorf(codes.InvalidArgument, "chart %q not found -- has it been reconciled? (ReconcileApps runs on push to main via ci.yml)", ownerFullName)
	}
	if chart.Status == repository.StatusArchived {
		return "", "", "", mapRepoErr(fmt.Errorf("%w: chart %q", repository.ErrOwnerArchived, ownerFullName))
	}
	return chart.Domain, chart.ChartID, chart.ChartRepository, nil
}

// BeginPublish implements the ∅|allocated|failed -> publishing transition
// (AR-7b, issue #558): called immediately before a release run pushes an
// image or chart. See ArtifactState's doc comment for the full lifecycle
// and ARCHITECTURE.md "Artifact lifecycle: allocated -> publishing ->
// published".
func (s *ArtifactServer) BeginPublish(ctx context.Context, req *pb.BeginPublishRequest) (*pb.BeginPublishResponse, error) {
	if err := auth.Require(ctx, auth.RoleBuilder); err != nil {
		return nil, err
	}
	kind := artifactKindFromPB(req.Kind)
	if kind == "" {
		return nil, status.Error(codes.InvalidArgument, "kind is required")
	}
	if req.OwnerFullName == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_full_name is required")
	}
	if req.Version == "" || !semverRe.MatchString(req.Version) {
		return nil, status.Errorf(codes.InvalidArgument, "version %q must match v<major>.<minor>.<patch>", req.Version)
	}
	if req.BuildId == "" {
		return nil, status.Error(codes.InvalidArgument, "build_id is required")
	}
	if req.IdempotencyKey == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}

	_, ownerID, repo, err := s.resolveOwnerAndDomain(ctx, kind, req.OwnerFullName)
	if err != nil {
		return nil, err
	}
	// The caller's own repository wins over the owner row's when supplied.
	// Required for charts, which have no usable stored repository at all --
	// see BeginPublishRequest.repository's doc comment. Images normally omit
	// it and fall back to the app's stored image_repository, which is the
	// same value RecordArtifact would go on to use.
	if req.Repository != "" {
		repo = req.Repository
	}

	artifact, err := s.beginPublishOne(ctx, req.IdempotencyKey, kind, ownerID, req.Version, req.BuildId, repo)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.BeginPublishResponse{Artifact: artifact}, nil
}

// beginPublishOne is the shared ∅|allocated|failed|publishing -> publishing
// transition both BeginPublish and BeginPublishBatch (AR-7d, issue #558)
// drive. Kept as one code path so both RPCs enforce the identical
// legal-transition table, including the publishing -> publishing heartbeat
// (see repository.ArtifactRepository.BeginPublish's doc comment).
//
// Callers MUST use distinct idempotency keys for the batch call and any
// later individual call against the same target -- see
// BeginPublishBatchRequest.idempotency_key_prefix's doc comment for why:
// unlike every other write RPC here, this one is deliberately called more
// than once against the same target within one run, and each call must
// actually re-execute (to refresh state_changed_at, or to revive a row the
// reaper expired), not replay a cached response from runIdempotent.
func (s *ArtifactServer) beginPublishOne(ctx context.Context, idempotencyKey string, kind repository.ArtifactKind, ownerID, version, buildID, repositoryHint string) (*pb.Artifact, error) {
	resp, _, err := runIdempotent(ctx, s.repo, idempotencyKey, "BeginPublish",
		func() proto.Message { return &pb.BeginPublishResponse{} },
		func(ctx context.Context, r repository.Registry) (proto.Message, error) {
			// versionSource is only consulted by the ∅ -> publishing branch
			// (no prior AllocateVersion call for this version) -- an
			// existing allocated/failed row keeps whatever it was already
			// given. "tag" is correct there: the caller is the pre-cutover
			// release path (ARCHITECTURE.md "Backward compatibility during
			// rollout") recording intent for a version the git-tag path
			// chose, not one the registry allocated.
			artifact, aerr := r.Artifacts().BeginPublish(ctx, kind, ownerID, version, buildID, repositoryHint, repository.VersionSourceTag)
			if aerr != nil {
				return nil, aerr
			}
			return &pb.BeginPublishResponse{Artifact: artifactToPB(*artifact)}, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return resp.(*pb.BeginPublishResponse).Artifact, nil
}

// BeginPublishBatch implements the bulk, up-front form of BeginPublish
// (AR-7d, issue #558): the release plan step calls this ONCE, before the
// release matrix fans out, so every intended target already has a
// "publishing" row -- carrying build_id -- before that target's own matrix
// leg (if it ever starts) runs. This is what closes the gap AR-7b's own
// scope note left open: a matrix leg that never starts at all previously
// left no row of any kind, so GetReleaseRun could not distinguish "never
// started" from "not part of this run". See ARCHITECTURE.md "The run log"
// -> "As built (AR-7d)".
//
// Each target is processed independently, mirroring AR-7a's ReconcileApps
// partial-apply precedent: one bad target (unknown owner, malformed
// version, a real state-machine conflict) is reported in its own result and
// does not block the rest. The RPC itself only fails on a malformed
// request (missing build_id/idempotency_key_prefix/targets).
//
// This is a plan-time declaration, not the whole story: release.yml's
// per-leg BeginPublish call still runs immediately before that leg's own
// push (see BeginPublish's own comment and repository.ArtifactRepository.
// BeginPublish's doc comment on the publishing -> publishing heartbeat) --
// it re-arms the row's staleness clock right before the work that clock is
// supposed to bound, and revives a row the stale-row reaper already
// expired to "failed" while this leg was still queued. Declaring intent up
// front and keeping the clock accurate per-leg are two different jobs; this
// RPC only does the first one.
func (s *ArtifactServer) BeginPublishBatch(ctx context.Context, req *pb.BeginPublishBatchRequest) (*pb.BeginPublishBatchResponse, error) {
	if err := auth.Require(ctx, auth.RoleBuilder); err != nil {
		return nil, err
	}
	if req.BuildId == "" {
		return nil, status.Error(codes.InvalidArgument, "build_id is required")
	}
	if req.IdempotencyKeyPrefix == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key_prefix is required")
	}
	if len(req.Targets) == 0 {
		return nil, status.Error(codes.InvalidArgument, "targets is required")
	}

	results := make([]*pb.BeginPublishBatchResult, 0, len(req.Targets))
	for _, t := range req.Targets {
		result := &pb.BeginPublishBatchResult{Kind: t.Kind, OwnerFullName: t.OwnerFullName, Version: t.Version}

		kind := artifactKindFromPB(t.Kind)
		switch {
		case kind == "":
			result.Error = "kind is required"
		case t.OwnerFullName == "":
			result.Error = "owner_full_name is required"
		case t.Version == "" || !semverRe.MatchString(t.Version):
			result.Error = fmt.Sprintf("version %q must match v<major>.<minor>.<patch>", t.Version)
		}
		if result.Error != "" {
			results = append(results, result)
			continue
		}

		_, ownerID, repo, rerr := s.resolveOwnerAndDomain(ctx, kind, t.OwnerFullName)
		if rerr != nil {
			result.Error = rerr.Error()
			results = append(results, result)
			continue
		}

		// "-intent" suffix: deliberately DIFFERENT from the key the same
		// target's per-leg BeginPublish call uses
		// ("<prefix>-<owner>-<kind>", no suffix) -- see
		// BeginPublishBatchRequest.idempotency_key_prefix's doc comment.
		// The two calls must NOT share a key, or the per-leg call would
		// replay this one's cached response via runIdempotent instead of
		// actually re-executing -- and re-execution is the whole point of
		// the per-leg call (AR-7d's heartbeat/revive).
		idemKey := fmt.Sprintf("%s-%s-%s-intent", req.IdempotencyKeyPrefix, t.OwnerFullName, string(kind))
		artifact, berr := s.beginPublishOne(ctx, idemKey, kind, ownerID, t.Version, req.BuildId, repo)
		if berr != nil {
			result.Error = mapRepoErr(berr).Error()
		} else {
			result.Artifact = artifact
		}
		results = append(results, result)
	}

	return &pb.BeginPublishBatchResponse{Results: results}, nil
}

// FailPublish implements the publishing -> failed transition (AR-7b, issue
// #558): called on release.yml's error path immediately after a
// BeginPublish for the same target.
func (s *ArtifactServer) FailPublish(ctx context.Context, req *pb.FailPublishRequest) (*pb.FailPublishResponse, error) {
	if err := auth.Require(ctx, auth.RoleBuilder); err != nil {
		return nil, err
	}
	kind := artifactKindFromPB(req.Kind)
	if kind == "" {
		return nil, status.Error(codes.InvalidArgument, "kind is required")
	}
	if req.OwnerFullName == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_full_name is required")
	}
	if req.Version == "" || !semverRe.MatchString(req.Version) {
		return nil, status.Errorf(codes.InvalidArgument, "version %q must match v<major>.<minor>.<patch>", req.Version)
	}
	if req.Reason == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}
	if req.IdempotencyKey == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}

	_, ownerID, _, err := s.resolveOwnerAndDomain(ctx, kind, req.OwnerFullName)
	if err != nil {
		return nil, err
	}

	resp, _, err := runIdempotent(ctx, s.repo, req.IdempotencyKey, "FailPublish",
		func() proto.Message { return &pb.FailPublishResponse{} },
		func(ctx context.Context, r repository.Registry) (proto.Message, error) {
			artifact, aerr := r.Artifacts().FailPublish(ctx, kind, ownerID, req.Version, req.Reason)
			if aerr != nil {
				return nil, aerr
			}
			return &pb.FailPublishResponse{Artifact: artifactToPB(*artifact)}, nil
		},
	)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return resp.(*pb.FailPublishResponse), nil
}

// GetReleaseRun implements the run log CI resumes against (AR-7d, issue
// #558): the build aggregate for (workflow_run_id[, workflow_attempt]) plus
// every artifact row hanging off it, in whatever state each is in. "What is
// incomplete?" = artifacts here with State != ARTIFACT_STATE_PUBLISHED --
// left to the caller (the CLI's --incomplete filter) rather than a second
// field on the response, since it is exactly a filter over Artifacts, not
// separately-derived data. See ARCHITECTURE.md "The run log".
//
// A read, not a write: no idempotency key, any authenticated principal may
// call it (matches ListArtifacts/GetArtifact above), same as every other
// query RPC in this service.
func (s *ArtifactServer) GetReleaseRun(ctx context.Context, req *pb.GetReleaseRunRequest) (*pb.GetReleaseRunResponse, error) {
	if err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if req.WorkflowRunId == "" {
		return nil, status.Error(codes.InvalidArgument, "workflow_run_id is required")
	}

	build, err := s.repo.Builds().GetBuildByWorkflowRun(ctx, req.WorkflowRunId, req.WorkflowAttempt)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	artifacts, err := s.repo.Artifacts().ListArtifacts(ctx, repository.ArtifactListFilter{BuildID: build.BuildID})
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.GetReleaseRunResponse{
		Build:     buildToPB(*build),
		Artifacts: artifactsToPB(artifacts),
	}, nil
}

// ListBuilds browses the `build` table (migration 001, no schema change) --
// see ARCHITECTURE.md "ListBuilds" for the pagination contract. Additive to
// GetReleaseRun/GetBuildByWorkflowRun above and the CLI's `builds status`
// (FR2.5) -- neither is changed by this RPC.
func (s *ArtifactServer) ListBuilds(ctx context.Context, req *pb.ListBuildsRequest) (*pb.ListBuildsResponse, error) {
	if err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	since := time.Time{}
	if req.Since != 0 {
		since = time.Unix(req.Since, 0)
	}
	builds, nextToken, err := s.repo.Builds().ListBuilds(ctx, since, req.GetPage().GetPageSize(), req.GetPage().GetPageToken())
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.ListBuildsResponse{
		Builds: buildsToPB(builds),
		// total_size is a PAGE-LOCAL count (len(builds) <= page_size), not
		// the true total row count across all pages -- same tradeoff as
		// ListReconcileRuns's identical note (see ARCHITECTURE.md's
		// pagination note this issue adds).
		Page: &pb.PageResponse{NextPageToken: nextToken, TotalSize: int32(len(builds))},
	}, nil
}

// AdoptArtifact implements AR-7e's (issue #558) admin-only adoption /
// disaster-recovery path: records a pre-existing GHCR image or chart as
// published with provenance ADOPTED and a required reason, for when there is
// no CI run to resume. See ARCHITECTURE.md "Adoption and disaster recovery"
// and repository.ArtifactRepository.AdoptArtifact's doc comment for the full
// state-collision contract.
//
// Role: admin ONLY -- deliberately NOT auth.RoleBuilder, unlike every other
// write RPC in this file. This is the security-critical line of the whole
// phase: the builder credential is what every CI job holds, and CI must
// never be able to assert an artifact into existence that it did not
// observe being published -- see ARCHITECTURE.md "Authorization" and
// server/handlers/authz_test.go's TestAdoptArtifact_Authorization.
func (s *ArtifactServer) AdoptArtifact(ctx context.Context, req *pb.AdoptArtifactRequest) (*pb.AdoptArtifactResponse, error) {
	if err := auth.Require(ctx, auth.RoleAdmin); err != nil {
		return nil, err
	}
	kind := artifactKindFromPB(req.Kind)
	if kind == "" {
		return nil, status.Error(codes.InvalidArgument, "kind is required")
	}
	if req.OwnerFullName == "" {
		return nil, status.Error(codes.InvalidArgument, "owner_full_name is required")
	}
	if req.Version == "" || !semverRe.MatchString(req.Version) {
		return nil, status.Errorf(codes.InvalidArgument, "version %q must match v<major>.<minor>.<patch>", req.Version)
	}
	if req.Digest == "" || !strings.HasPrefix(req.Digest, "sha256:") {
		return nil, status.Error(codes.InvalidArgument, `digest is required and must be "sha256:..."`)
	}
	if kind == repository.ArtifactKindImage && len(req.Contains) > 0 {
		return nil, status.Error(codes.InvalidArgument, "contains is only valid for kind == CHART")
	}
	// Required -- the audit trail for a deliberately rare, human-triggered
	// operation. Not persisted to a column (no schema change this phase --
	// see repository.ArtifactRepository.AdoptArtifact's doc comment); logged
	// below instead, same shape as AppServer.SetAppStatus's reason.
	if req.Reason == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}
	if req.IdempotencyKey == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}

	_, ownerID, _, err := s.resolveOwnerAndDomain(ctx, kind, req.OwnerFullName)
	if err != nil {
		return nil, err
	}

	// actor authors the synthetic `build` row AdoptArtifact may need to
	// create (see repository.ArtifactRepository.AdoptArtifact's doc
	// comment) -- the calling admin's own identity, not a service account,
	// since this credential is human-operated per DEPLOY.md §4's "Where
	// each secret goes" table.
	actor := "admin"
	if claims, ok := grpcauth.ClaimsFromContext(ctx); ok && claims.Subject != "" {
		actor = claims.Subject
	}

	resp, _, err := runIdempotent(ctx, s.repo, req.IdempotencyKey, "AdoptArtifact",
		func() proto.Message { return &pb.AdoptArtifactResponse{} },
		func(ctx context.Context, r repository.Registry) (proto.Message, error) {
			a := repository.Artifact{
				Kind:        kind,
				Repository:  req.Repository,
				Version:     req.Version,
				Digest:      req.Digest,
				PublishedAt: unixToTime(req.PublishedAt),
			}
			if kind == repository.ArtifactKindImage {
				a.AppID = ownerID
			} else {
				a.ChartID = ownerID
			}
			artifact, alreadyRecorded, aerr := r.Artifacts().AdoptArtifact(ctx, a, containedImagesFromPB(req.Contains), req.Reason, actor)
			if aerr != nil {
				return nil, aerr
			}
			return &pb.AdoptArtifactResponse{Artifact: artifactToPB(*artifact), AlreadyRecorded: alreadyRecorded}, nil
		},
	)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	out := resp.(*pb.AdoptArtifactResponse)

	// The audit trail: a required reason, an identified actor, and exactly
	// what was asserted -- structured so "why did we adopt this?" is a log
	// query, not archaeology. Info, not Debug: every call here is a
	// deliberate, rare, human-triggered operation (ARCHITECTURE.md "Adoption
	// and disaster recovery"), worth a durable record even on the happy
	// path -- unlike ReconcileApps's Warn-on-anomaly-only logging above.
	artifactLog.Info("artifact adopted",
		slog.String("kind", string(kind)),
		slog.String("owner_full_name", req.OwnerFullName),
		slog.String("version", req.Version),
		slog.String("digest", req.Digest),
		slog.String("reason", req.Reason),
		slog.String("actor", actor),
		slog.Bool("already_recorded", out.AlreadyRecorded),
	)
	return out, nil
}
