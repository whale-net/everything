package handlers

import (
	"context"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EnvironmentServer implements pb.EnvironmentRegistryServer. Role: admin
// only, per ARCHITECTURE.md "Authorization". The `environment` table doesn't
// exist until migration 002 (AR-3), so every RPC here is Unimplemented.
type EnvironmentServer struct {
	pb.UnimplementedEnvironmentRegistryServer
}

// NewEnvironmentServer constructs an EnvironmentServer.
func NewEnvironmentServer() *EnvironmentServer {
	return &EnvironmentServer{}
}

func (s *EnvironmentServer) UpsertEnvironment(ctx context.Context, req *pb.UpsertEnvironmentRequest) (*pb.UpsertEnvironmentResponse, error) {
	return nil, status.Error(codes.Unimplemented, "UpsertEnvironment not implemented")
}

func (s *EnvironmentServer) GetEnvironment(ctx context.Context, req *pb.GetEnvironmentRequest) (*pb.GetEnvironmentResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetEnvironment not implemented")
}

func (s *EnvironmentServer) ListEnvironments(ctx context.Context, req *pb.ListEnvironmentsRequest) (*pb.ListEnvironmentsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListEnvironments not implemented")
}

func (s *EnvironmentServer) ArchiveEnvironment(ctx context.Context, req *pb.ArchiveEnvironmentRequest) (*pb.ArchiveEnvironmentResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ArchiveEnvironment not implemented")
}
