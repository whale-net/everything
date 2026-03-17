package handlers

import (
	"context"

	"github.com/whale-net/everything/manmanv2/models"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ServerGameConfigHandler handles ServerGameConfig-related RPCs
type ServerGameConfigHandler struct {
	repo     repository.ServerGameConfigRepository
	portRepo repository.ServerPortRepository
}

func NewServerGameConfigHandler(repo repository.ServerGameConfigRepository, portRepo repository.ServerPortRepository) *ServerGameConfigHandler {
	return &ServerGameConfigHandler{
		repo:     repo,
		portRepo: portRepo,
	}
}

func (h *ServerGameConfigHandler) ListServerGameConfigs(ctx context.Context, req *pb.ListServerGameConfigsRequest) (*pb.ListServerGameConfigsResponse, error) {
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

	var serverID *int64
	if req.ServerId > 0 {
		serverID = &req.ServerId
	}

	configs, err := h.repo.List(ctx, serverID, pageSize+1, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list server game configs: %v", err)
	}

	var nextPageToken string
	if len(configs) > pageSize {
		configs = configs[:pageSize]
		nextPageToken = encodePageToken(offset + pageSize)
	}

	pbConfigs := make([]*pb.ServerGameConfig, len(configs))
	for i, c := range configs {
		pbConfigs[i] = serverGameConfigToProto(c)
	}

	return &pb.ListServerGameConfigsResponse{
		Configs:       pbConfigs,
		NextPageToken: nextPageToken,
	}, nil
}

func (h *ServerGameConfigHandler) GetServerGameConfig(ctx context.Context, req *pb.GetServerGameConfigRequest) (*pb.GetServerGameConfigResponse, error) {
	config, err := h.repo.Get(ctx, req.ServerGameConfigId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "server game config not found: %v", err)
	}

	return &pb.GetServerGameConfigResponse{
		Config: serverGameConfigToProto(config),
	}, nil
}

func (h *ServerGameConfigHandler) DeployGameConfig(ctx context.Context, req *pb.DeployGameConfigRequest) (*pb.DeployGameConfigResponse, error) {
	// Port availability is checked when starting a session, not at deployment time.
	// This allows multiple SGCs to define the same ports, with actual allocation
	// and conflict detection happening only when sessions start.

	// Create the ServerGameConfig
	sgc := &manman.ServerGameConfig{
		ServerID:     req.ServerId,
		GameConfigID: req.GameConfigId,
		Status:       manman.SGCStatusInactive,
		PortBindings: portBindingsToJSONB(req.PortBindings),
	}

	sgc, err := h.repo.Create(ctx, sgc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to deploy game config: %v", err)
	}

	// Port allocation is now handled at session start time (not deployment time)
	// This allows multiple SGCs to share port definitions, with actual allocation
	// happening only when a session starts.

	return &pb.DeployGameConfigResponse{
		Config: serverGameConfigToProto(sgc),
	}, nil
}

func (h *ServerGameConfigHandler) UpdateServerGameConfig(ctx context.Context, req *pb.UpdateServerGameConfigRequest) (*pb.UpdateServerGameConfigResponse, error) {
	sgc, err := h.repo.Get(ctx, req.ServerGameConfigId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "server game config not found: %v", err)
	}

	// Apply field paths
	if len(req.UpdatePaths) == 0 {
		// Update all provided fields
		if req.PortBindings != nil {
			sgc.PortBindings = portBindingsToJSONB(req.PortBindings)
		}
		if req.Status != "" {
			sgc.Status = req.Status
		}
	} else {
		// Update only specified fields
		for _, path := range req.UpdatePaths {
			switch path {
			case "port_bindings":
				sgc.PortBindings = portBindingsToJSONB(req.PortBindings)
			case "status":
				sgc.Status = req.Status
			}
		}
	}

	if err := h.repo.Update(ctx, sgc); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update server game config: %v", err)
	}

	return &pb.UpdateServerGameConfigResponse{
		Config: serverGameConfigToProto(sgc),
	}, nil
}

func (h *ServerGameConfigHandler) DeleteServerGameConfig(ctx context.Context, req *pb.DeleteServerGameConfigRequest) (*pb.DeleteServerGameConfigResponse, error) {
	// Port deallocation is handled automatically via database CASCADE when sessions are deleted

	// Delete the ServerGameConfig
	if err := h.repo.Delete(ctx, req.ServerGameConfigId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete server game config: %v", err)
	}

	return &pb.DeleteServerGameConfigResponse{}, nil
}

func serverGameConfigToProto(sgc *manman.ServerGameConfig) *pb.ServerGameConfig {
	return &pb.ServerGameConfig{
		ServerGameConfigId: sgc.SGCID,
		ServerId:           sgc.ServerID,
		GameConfigId:       sgc.GameConfigID,
		PortBindings:       jsonbToPortBindings(sgc.PortBindings),
		Status:             sgc.Status,
	}
}
