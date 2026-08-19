package handlers

import (
	"context"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EnvironmentServer implements pb.EnvironmentRegistryServer, backed by
// repository.Registry. Role: admin for every RPC's write path, per
// ARCHITECTURE.md "Authorization" -- EnvironmentRegistry has no
// builder/promoter carve-out, unlike AppRegistry/ArtifactRegistry. Reads
// require only that the caller is authenticated, same as every other
// service's reads. PromotionRegistry (AR-3c) is the only other planned
// consumer of the environment table and stays Unimplemented until then.
type EnvironmentServer struct {
	pb.UnimplementedEnvironmentRegistryServer
	repo repository.Registry
}

// NewEnvironmentServer constructs an EnvironmentServer over repo.
func NewEnvironmentServer(repo repository.Registry) *EnvironmentServer {
	return &EnvironmentServer{repo: repo}
}

// UpsertEnvironment creates or updates an environment by its immutable key.
// Neither this nor ArchiveEnvironment take an idempotency_key (see
// api_messages_environment.proto) -- same shape as AppRegistry.SetAppStatus,
// the other admin-only write in this codebase, so there is no runIdempotent
// path here.
func (s *EnvironmentServer) UpsertEnvironment(ctx context.Context, req *pb.UpsertEnvironmentRequest) (*pb.UpsertEnvironmentResponse, error) {
	if err := auth.Require(ctx, auth.RoleAdmin); err != nil {
		return nil, err
	}
	if req.Key == "" {
		return nil, status.Error(codes.InvalidArgument, "key is required")
	}

	env, created, err := s.repo.Environments().Upsert(ctx, repository.Environment{
		Key:               req.Key,
		DisplayName:       req.DisplayName,
		Rank:              req.Rank,
		RequiresApproval:  req.RequiresApproval,
		GitopsPath:        req.GitopsPath,
		AllowedPrincipals: req.AllowedPrincipals,
	})
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.UpsertEnvironmentResponse{Environment: environmentToPB(*env), Created: created}, nil
}

func (s *EnvironmentServer) GetEnvironment(ctx context.Context, req *pb.GetEnvironmentRequest) (*pb.GetEnvironmentResponse, error) {
	if req.Key == "" {
		return nil, status.Error(codes.InvalidArgument, "key is required")
	}

	env, err := s.repo.Environments().Get(ctx, req.Key)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.GetEnvironmentResponse{Environment: environmentToPB(*env)}, nil
}

// ListEnvironments returns environments ordered by rank ascending, per
// ListEnvironmentsResponse's doc comment.
func (s *EnvironmentServer) ListEnvironments(ctx context.Context, req *pb.ListEnvironmentsRequest) (*pb.ListEnvironmentsResponse, error) {
	envs, err := s.repo.Environments().List(ctx, req.IncludeArchived)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.ListEnvironmentsResponse{Environments: environmentsToPB(envs)}, nil
}

// ArchiveEnvironment is the only lifecycle transition on an environment row.
// Unlike App/Chart, there is no automatic un-archive path -- if this proves
// wrong in practice, UpsertEnvironment can be extended to accept it later.
func (s *EnvironmentServer) ArchiveEnvironment(ctx context.Context, req *pb.ArchiveEnvironmentRequest) (*pb.ArchiveEnvironmentResponse, error) {
	if err := auth.Require(ctx, auth.RoleAdmin); err != nil {
		return nil, err
	}
	if req.Key == "" {
		return nil, status.Error(codes.InvalidArgument, "key is required")
	}
	if req.Reason == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}

	env, err := s.repo.Environments().Archive(ctx, req.Key, req.Reason)
	if err != nil {
		return nil, mapRepoErr(err)
	}
	return &pb.ArchiveEnvironmentResponse{Environment: environmentToPB(*env)}, nil
}
