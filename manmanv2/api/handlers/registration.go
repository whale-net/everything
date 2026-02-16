package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/manmanv2"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type RegistrationHandler struct {
	serverRepo       repository.ServerRepository
	capabilityRepo   repository.ServerCapabilityRepository
}

func NewRegistrationHandler(serverRepo repository.ServerRepository, capRepo repository.ServerCapabilityRepository) *RegistrationHandler {
	return &RegistrationHandler{
		serverRepo:       serverRepo,
		capabilityRepo:   capRepo,
	}
}

func (h *RegistrationHandler) RegisterServer(ctx context.Context, req *pb.RegisterServerRequest) (*pb.RegisterServerResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	// Check if server with this name exists
	server, err := h.serverRepo.GetByName(ctx, req.Name)
	if err != nil {
		// Only create if server doesn't exist (not found error)
		if errors.Is(err, pgx.ErrNoRows) {
			server, err = h.serverRepo.Create(ctx, req.Name)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to create server: %v", err)
			}
		} else {
			// Database error or other issue - don't swallow it
			return nil, status.Errorf(codes.Internal, "failed to query server: %v", err)
		}
	}
	// If server exists, proceed with idempotent registration
	// This allows the same server to re-register (e.g., after restart)

	// Update server status and environment
	now := time.Now()
	server.Status = manman.ServerStatusOnline
	server.LastSeen = &now

	// Update environment if provided
	if req.Environment != "" {
		server.Environment = &req.Environment
	}

	if err := h.serverRepo.Update(ctx, server); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update server status: %v", err)
	}

	// Store capabilities
	if req.Capabilities != nil {
		capability := &manman.ServerCapability{
			ServerID:               server.ServerID,
			TotalMemoryMB:          req.Capabilities.TotalMemoryMb,
			AvailableMemoryMB:      req.Capabilities.AvailableMemoryMb,
			CPUCores:               req.Capabilities.CpuCores,
			AvailableCPUMillicores: req.Capabilities.AvailableCpuMillicores,
			DockerVersion:          req.Capabilities.DockerVersion,
		}

		if err := h.capabilityRepo.Insert(ctx, capability); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to store capabilities: %v", err)
		}
	}

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

	// Update capabilities
	if req.Capabilities != nil {
		capability := &manman.ServerCapability{
			ServerID:               req.ServerId,
			TotalMemoryMB:          req.Capabilities.TotalMemoryMb,
			AvailableMemoryMB:      req.Capabilities.AvailableMemoryMb,
			CPUCores:               req.Capabilities.CpuCores,
			AvailableCPUMillicores: req.Capabilities.AvailableCpuMillicores,
			DockerVersion:          req.Capabilities.DockerVersion,
		}

		if err := h.capabilityRepo.Insert(ctx, capability); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to update capabilities: %v", err)
		}
	}

	return &pb.HeartbeatResponse{
		Acknowledged:         true,
		NextHeartbeatSeconds: 30, // 30 second interval
	}, nil
}
