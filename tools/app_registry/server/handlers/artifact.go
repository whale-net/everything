package handlers

import (
	"context"
	"regexp"
	"strings"
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var semverRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// ArtifactServer implements pb.ArtifactRegistryServer, backed by
// repository.Registry. AllocateVersion stays codes.Unimplemented — that's
// AR-5.
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

func (s *ArtifactServer) AllocateVersion(ctx context.Context, req *pb.AllocateVersionRequest) (*pb.AllocateVersionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "AllocateVersion not implemented")
}
