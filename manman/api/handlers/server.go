package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/api/repository"
	pb "github.com/whale-net/everything/manman/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ServerHandler struct {
	repo repository.ServerRepository
}

func NewServerHandler(repo repository.ServerRepository) *ServerHandler {
	return &ServerHandler{repo: repo}
}

func (h *ServerHandler) ListServers(ctx context.Context, req *pb.ListServersRequest) (*pb.ListServersResponse, error) {
	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}

	offset := 0
	if req.PageToken != "" {
		var err error
		offset, err = decodePageToken(req.PageToken)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid page token: %v", err)
		}
	}

	servers, err := h.repo.List(ctx, pageSize+1, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list servers: %v", err)
	}

	var nextPageToken string
	if len(servers) > pageSize {
		servers = servers[:pageSize]
		nextPageToken = encodePageToken(offset + pageSize)
	}

	pbServers := make([]*pb.Server, len(servers))
	for i, s := range servers {
		pbServers[i] = serverToProto(s)
	}

	return &pb.ListServersResponse{
		Servers:       pbServers,
		NextPageToken: nextPageToken,
	}, nil
}

func (h *ServerHandler) GetServer(ctx context.Context, req *pb.GetServerRequest) (*pb.GetServerResponse, error) {
	server, err := h.repo.Get(ctx, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "server not found: %v", err)
	}

	return &pb.GetServerResponse{
		Server: serverToProto(server),
	}, nil
}

func (h *ServerHandler) CreateServer(ctx context.Context, req *pb.CreateServerRequest) (*pb.CreateServerResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	server, err := h.repo.Create(ctx, req.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create server: %v", err)
	}

	return &pb.CreateServerResponse{
		Server: serverToProto(server),
	}, nil
}

func (h *ServerHandler) UpdateServer(ctx context.Context, req *pb.UpdateServerRequest) (*pb.UpdateServerResponse, error) {
	server, err := h.repo.Get(ctx, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "server not found: %v", err)
	}

	// Apply field paths
	if len(req.UpdatePaths) == 0 {
		// Update all provided fields
		if req.Name != "" {
			server.Name = req.Name
		}
		if req.Status != "" {
			server.Status = req.Status
		}
		// Only update is_default if explicitly provided
		if req.IsDefault {
			server.IsDefault = req.IsDefault
		}
	} else {
		// Update only specified fields
		for _, path := range req.UpdatePaths {
			switch path {
			case "name":
				server.Name = req.Name
			case "status":
				server.Status = req.Status
			case "is_default":
				server.IsDefault = req.IsDefault
			}
		}
	}

	if err := h.repo.Update(ctx, server); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update server: %v", err)
	}

	return &pb.UpdateServerResponse{
		Server: serverToProto(server),
	}, nil
}

func (h *ServerHandler) DeleteServer(ctx context.Context, req *pb.DeleteServerRequest) (*pb.DeleteServerResponse, error) {
	if err := h.repo.Delete(ctx, req.ServerId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete server: %v", err)
	}

	return &pb.DeleteServerResponse{}, nil
}

func serverToProto(s *manman.Server) *pb.Server {
	pbServer := &pb.Server{
		ServerId:  s.ServerID,
		Name:      s.Name,
		Status:    s.Status,
		IsDefault: s.IsDefault,
	}

	if s.LastSeen != nil {
		pbServer.LastSeen = s.LastSeen.Unix()
	}

	if s.Environment != nil {
		pbServer.Environment = *s.Environment
	}

	return pbServer
}

// Pagination token helpers
func encodePageToken(offset int) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", offset)))
}

func decodePageToken(token string) (int, error) {
	data, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(string(data))
}
