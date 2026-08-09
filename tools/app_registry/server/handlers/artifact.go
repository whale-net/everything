package handlers

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/whale-net/everything/libs/go/semver"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var semverRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

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
			owner, err := s.resolveOwner(ctx, r, kind, req.OwnerFullName)
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

			artifact, alreadyRecorded, err := r.Artifacts().RecordArtifact(ctx, a, containedImagesFromPB(req.Contains))
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

// resolveOwner resolves owner_full_name to an app_id or chart_id, depending
// on kind, wrapping repository.ErrNotFound as ErrInvalidArgument — an
// unknown owner is a caller mistake, not a "row doesn't exist yet" case.
func (s *ArtifactServer) resolveOwner(ctx context.Context, r repository.Registry, kind repository.ArtifactKind, ownerFullName string) (string, error) {
	if kind == repository.ArtifactKindImage {
		app, err := r.Apps().GetAppByFullName(ctx, ownerFullName)
		if err != nil {
			return "", repository.ErrInvalidArgument
		}
		return app.AppID, nil
	}
	chart, err := r.Apps().GetChartByFullName(ctx, ownerFullName)
	if err != nil {
		return "", repository.ErrInvalidArgument
	}
	return chart.ChartID, nil
}

func (s *ArtifactServer) ListArtifacts(ctx context.Context, req *pb.ListArtifactsRequest) (*pb.ListArtifactsResponse, error) {
	if err := auth.RequireAuthenticated(ctx); err != nil {
		return nil, err
	}
	artifacts, err := s.repo.Artifacts().ListArtifacts(ctx, repository.ArtifactListFilter{
		OwnerFullName:  req.OwnerFullName,
		Kind:           artifactKindFromPB(req.Kind),
		PromotableOnly: req.PromotableOnly,
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

	domain, ownerID, err := s.resolveOwnerAndDomain(ctx, kind, req.OwnerFullName)
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
				alloc, aerr := r.Artifacts().AllocateVersion(ctx, kind, ownerID, req.Increment, req.ExplicitVersion)
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

// resolveOwnerAndDomain resolves owner_full_name to its owning row's id and
// domain, for AllocateVersion's storage key and adoption-stage gate
// respectively. Mirrors resolveOwner but also needs Domain, which
// resolveOwner's callers (RecordArtifact) don't.
func (s *ArtifactServer) resolveOwnerAndDomain(ctx context.Context, kind repository.ArtifactKind, ownerFullName string) (domain, ownerID string, err error) {
	if kind == repository.ArtifactKindImage {
		app, aerr := s.repo.Apps().GetAppByFullName(ctx, ownerFullName)
		if aerr != nil {
			return "", "", status.Errorf(codes.InvalidArgument, "unknown app %q", ownerFullName)
		}
		return app.Domain, app.AppID, nil
	}
	chart, cerr := s.repo.Apps().GetChartByFullName(ctx, ownerFullName)
	if cerr != nil {
		return "", "", status.Errorf(codes.InvalidArgument, "unknown chart %q", ownerFullName)
	}
	return chart.Domain, chart.ChartID, nil
}
