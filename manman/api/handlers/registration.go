package handlers

import (
	"context"
	"time"

	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/api/repository"
	pb "github.com/whale-net/everything/manman/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type RegistrationHandler struct {
	serverRepo repository.ServerRepository
	// capabilityRepo will be added in Phase 5
}

func NewRegistrationHandler(serverRepo repository.ServerRepository) *RegistrationHandler {
	return &RegistrationHandler{
		serverRepo: serverRepo,
	}
}

func (h *RegistrationHandler) RegisterServer(ctx context.Context, req *pb.RegisterServerRequest) (*pb.RegisterServerResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	// TODO: Check if server with this name exists (requires GetByName method in repository)
	// For now, just create a new server
	server, err := h.serverRepo.Create(ctx, req.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create server: %v", err)
	}

	now := time.Now()
	server.Status = manman.ServerStatusOnline
	server.LastSeen = &now

	if err := h.serverRepo.Update(ctx, server); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update server status: %v", err)
	}

	// TODO: Store capabilities using capabilityRepo (Phase 5)

	return &pb.RegisterServerResponse{
		ServerId: server.ServerID,
		Server:   serverToProto(server),
	}, nil
}

func (h *RegistrationHandler) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	server, err := h.serverRepo.Get(ctx, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "server not found")
	}

	now := time.Now()
	server.Status = manman.ServerStatusOnline
	server.LastSeen = &now

	if err := h.serverRepo.Update(ctx, server); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update server: %v", err)
	}

	// TODO: Update capabilities using capabilityRepo (Phase 5)

	return &pb.HeartbeatResponse{
		Acknowledged:         true,
		NextHeartbeatSeconds: 30, // 30 second interval
	}, nil
}
