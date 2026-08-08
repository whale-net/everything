package handlers

import (
	"context"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PromotionServer implements pb.PromotionRegistryServer. Real writes land in
// AR-3; the underlying `promotion`/`promotion_event` tables don't exist until
// migration 002, so every RPC here is a hard Unimplemented in AR-1.
type PromotionServer struct {
	pb.UnimplementedPromotionRegistryServer
}

// NewPromotionServer constructs a PromotionServer.
func NewPromotionServer() *PromotionServer {
	return &PromotionServer{}
}

func (s *PromotionServer) Promote(ctx context.Context, req *pb.PromoteRequest) (*pb.PromoteResponse, error) {
	return nil, status.Error(codes.Unimplemented, "Promote not implemented")
}

func (s *PromotionServer) Rollback(ctx context.Context, req *pb.RollbackRequest) (*pb.RollbackResponse, error) {
	return nil, status.Error(codes.Unimplemented, "Rollback not implemented")
}

func (s *PromotionServer) GetEnvironmentState(ctx context.Context, req *pb.GetEnvironmentStateRequest) (*pb.GetEnvironmentStateResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetEnvironmentState not implemented")
}

func (s *PromotionServer) ListPromotions(ctx context.Context, req *pb.ListPromotionsRequest) (*pb.ListPromotionsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListPromotions not implemented")
}

func (s *PromotionServer) ListPromotionEvents(ctx context.Context, req *pb.ListPromotionEventsRequest) (*pb.ListPromotionEventsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListPromotionEvents not implemented")
}
