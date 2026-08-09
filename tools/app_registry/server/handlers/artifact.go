package handlers

import (
	"context"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ArtifactServer implements pb.ArtifactRegistryServer. See AppServer for the
// AR-1 skeleton convention — no business logic, codes.Unimplemented only.
type ArtifactServer struct {
	pb.UnimplementedArtifactRegistryServer
}

// NewArtifactServer constructs an ArtifactServer.
func NewArtifactServer() *ArtifactServer {
	return &ArtifactServer{}
}

func (s *ArtifactServer) RecordBuild(ctx context.Context, req *pb.RecordBuildRequest) (*pb.RecordBuildResponse, error) {
	return nil, status.Error(codes.Unimplemented, "RecordBuild not implemented")
}

func (s *ArtifactServer) RecordArtifact(ctx context.Context, req *pb.RecordArtifactRequest) (*pb.RecordArtifactResponse, error) {
	return nil, status.Error(codes.Unimplemented, "RecordArtifact not implemented")
}

func (s *ArtifactServer) ListArtifacts(ctx context.Context, req *pb.ListArtifactsRequest) (*pb.ListArtifactsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListArtifacts not implemented")
}

func (s *ArtifactServer) GetArtifact(ctx context.Context, req *pb.GetArtifactRequest) (*pb.GetArtifactResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetArtifact not implemented")
}

func (s *ArtifactServer) ResolveArtifact(ctx context.Context, req *pb.ResolveArtifactRequest) (*pb.ResolveArtifactResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ResolveArtifact not implemented")
}

func (s *ArtifactServer) AllocateVersion(ctx context.Context, req *pb.AllocateVersionRequest) (*pb.AllocateVersionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "AllocateVersion not implemented")
}
