package handlers

import (
	"context"

	"github.com/whale-net/everything/manmanv2"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GameConfigHandler handles GameConfig-related RPCs
type GameConfigHandler struct {
	repo repository.GameConfigRepository
}

func NewGameConfigHandler(repo repository.GameConfigRepository) *GameConfigHandler {
	return &GameConfigHandler{repo: repo}
}

func (h *GameConfigHandler) ListGameConfigs(ctx context.Context, req *pb.ListGameConfigsRequest) (*pb.ListGameConfigsResponse, error) {
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

	var gameID *int64
	if req.GameId > 0 {
		gameID = &req.GameId
	}

	configs, err := h.repo.List(ctx, gameID, pageSize+1, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list game configs: %v", err)
	}

	var nextPageToken string
	if len(configs) > pageSize {
		configs = configs[:pageSize]
		nextPageToken = encodePageToken(offset + pageSize)
	}

	pbConfigs := make([]*pb.GameConfig, len(configs))
	for i, c := range configs {
		pbConfigs[i] = gameConfigToProto(c)
	}

	return &pb.ListGameConfigsResponse{
		Configs:       pbConfigs,
		NextPageToken: nextPageToken,
	}, nil
}

func (h *GameConfigHandler) GetGameConfig(ctx context.Context, req *pb.GetGameConfigRequest) (*pb.GetGameConfigResponse, error) {
	config, err := h.repo.Get(ctx, req.ConfigId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "game config not found: %v", err)
	}

	return &pb.GetGameConfigResponse{
		Config: gameConfigToProto(config),
	}, nil
}

func (h *GameConfigHandler) CreateGameConfig(ctx context.Context, req *pb.CreateGameConfigRequest) (*pb.CreateGameConfigResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.Image == "" {
		return nil, status.Error(codes.InvalidArgument, "image is required")
	}

	config := &manman.GameConfig{
		GameID:       req.GameId,
		Name:         req.Name,
		Image:        req.Image,
		ArgsTemplate: stringPtr(req.ArgsTemplate),
		EnvTemplate:  mapToJSONB(req.EnvTemplate),
		Entrypoint:   stringArrayToJSONB(req.Entrypoint),
		Command:      stringArrayToJSONB(req.Command),
	}

	config, err := h.repo.Create(ctx, config)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create game config: %v", err)
	}

	return &pb.CreateGameConfigResponse{
		Config: gameConfigToProto(config),
	}, nil
}

func (h *GameConfigHandler) UpdateGameConfig(ctx context.Context, req *pb.UpdateGameConfigRequest) (*pb.UpdateGameConfigResponse, error) {
	config, err := h.repo.Get(ctx, req.ConfigId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "game config not found: %v", err)
	}

	// Apply field paths
	if len(req.UpdatePaths) == 0 {
		// Update all provided fields
		if req.Name != "" {
			config.Name = req.Name
		}
		if req.Image != "" {
			config.Image = req.Image
		}
		if req.ArgsTemplate != "" {
			config.ArgsTemplate = stringPtr(req.ArgsTemplate)
		}
		if req.EnvTemplate != nil {
			config.EnvTemplate = mapToJSONB(req.EnvTemplate)
		}
		if req.Entrypoint != nil {
			config.Entrypoint = stringArrayToJSONB(req.Entrypoint)
		}
		if req.Command != nil {
			config.Command = stringArrayToJSONB(req.Command)
		}
	} else {
		// Update only specified fields
		for _, path := range req.UpdatePaths {
			switch path {
			case "name":
				config.Name = req.Name
			case "image":
				config.Image = req.Image
			case "args_template":
				config.ArgsTemplate = stringPtr(req.ArgsTemplate)
			case "env_template":
				config.EnvTemplate = mapToJSONB(req.EnvTemplate)
			case "entrypoint":
				config.Entrypoint = stringArrayToJSONB(req.Entrypoint)
			case "command":
				config.Command = stringArrayToJSONB(req.Command)
			}
		}
	}

	if err := h.repo.Update(ctx, config); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update game config: %v", err)
	}

	return &pb.UpdateGameConfigResponse{
		Config: gameConfigToProto(config),
	}, nil
}

func (h *GameConfigHandler) DeleteGameConfig(ctx context.Context, req *pb.DeleteGameConfigRequest) (*pb.DeleteGameConfigResponse, error) {
	if err := h.repo.Delete(ctx, req.ConfigId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete game config: %v", err)
	}

	return &pb.DeleteGameConfigResponse{}, nil
}

func gameConfigToProto(c *manman.GameConfig) *pb.GameConfig {
	pbConfig := &pb.GameConfig{
		ConfigId:    c.ConfigID,
		GameId:      c.GameID,
		Name:        c.Name,
		Image:       c.Image,
		EnvTemplate: jsonbToMap(c.EnvTemplate),
		Entrypoint:  jsonbToStringArray(c.Entrypoint),
		Command:     jsonbToStringArray(c.Command),
	}

	if c.ArgsTemplate != nil {
		pbConfig.ArgsTemplate = *c.ArgsTemplate
	}

	return pbConfig
}
