package handlers

import (
	"context"

	"github.com/whale-net/everything/manmanv2/models"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ConfigurationStrategyHandler handles ConfigurationStrategy-related RPCs
type ConfigurationStrategyHandler struct {
	repo repository.ConfigurationStrategyRepository
}

func NewConfigurationStrategyHandler(repo repository.ConfigurationStrategyRepository) *ConfigurationStrategyHandler {
	return &ConfigurationStrategyHandler{repo: repo}
}

func (h *ConfigurationStrategyHandler) CreateConfigurationStrategy(ctx context.Context, req *pb.CreateConfigurationStrategyRequest) (*pb.CreateConfigurationStrategyResponse, error) {
	strategy := &manman.ConfigurationStrategy{
		GameID:        req.GameId,
		Name:          req.Name,
		Description:   stringPtr(req.Description),
		StrategyType:  req.StrategyType,
		TargetPath:    stringPtr(req.TargetPath),
		BaseTemplate:  stringPtr(req.BaseTemplate),
		RenderOptions: mapToJSONB(req.RenderOptions),
		ApplyOrder:    int(req.ApplyOrder),
	}

	strategy, err := h.repo.Create(ctx, strategy)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create configuration strategy: %v", err)
	}

	return &pb.CreateConfigurationStrategyResponse{
		Strategy: strategyToProto(strategy),
	}, nil
}

func (h *ConfigurationStrategyHandler) ListConfigurationStrategies(ctx context.Context, req *pb.ListConfigurationStrategiesRequest) (*pb.ListConfigurationStrategiesResponse, error) {
	strategies, err := h.repo.ListByGame(ctx, req.GameId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list configuration strategies: %v", err)
	}

	pbStrategies := make([]*pb.ConfigurationStrategy, len(strategies))
	for i, s := range strategies {
		pbStrategies[i] = strategyToProto(s)
	}

	return &pb.ListConfigurationStrategiesResponse{
		Strategies: pbStrategies,
	}, nil
}

func (h *ConfigurationStrategyHandler) UpdateConfigurationStrategy(ctx context.Context, req *pb.UpdateConfigurationStrategyRequest) (*pb.UpdateConfigurationStrategyResponse, error) {
	strategy, err := h.repo.Get(ctx, req.StrategyId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "configuration strategy not found: %v", err)
	}

	// Apply field paths
	if len(req.UpdatePaths) == 0 {
		if req.Name != "" {
			strategy.Name = req.Name
		}
		if req.Description != "" {
			strategy.Description = stringPtr(req.Description)
		}
		if req.StrategyType != "" {
			strategy.StrategyType = req.StrategyType
		}
		if req.TargetPath != "" {
			strategy.TargetPath = stringPtr(req.TargetPath)
		}
		if req.BaseTemplate != "" {
			strategy.BaseTemplate = stringPtr(req.BaseTemplate)
		}
		if req.RenderOptions != nil {
			strategy.RenderOptions = mapToJSONB(req.RenderOptions)
		}
		if req.ApplyOrder != 0 {
			strategy.ApplyOrder = int(req.ApplyOrder)
		}
	} else {
		for _, path := range req.UpdatePaths {
			switch path {
			case "name":
				strategy.Name = req.Name
			case "description":
				strategy.Description = stringPtr(req.Description)
			case "strategy_type":
				strategy.StrategyType = req.StrategyType
			case "target_path":
				strategy.TargetPath = stringPtr(req.TargetPath)
			case "base_template":
				strategy.BaseTemplate = stringPtr(req.BaseTemplate)
			case "render_options":
				strategy.RenderOptions = mapToJSONB(req.RenderOptions)
			case "apply_order":
				strategy.ApplyOrder = int(req.ApplyOrder)
			}
		}
	}

	if err := h.repo.Update(ctx, strategy); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update configuration strategy: %v", err)
	}

	return &pb.UpdateConfigurationStrategyResponse{
		Strategy: strategyToProto(strategy),
	}, nil
}

func (h *ConfigurationStrategyHandler) DeleteConfigurationStrategy(ctx context.Context, req *pb.DeleteConfigurationStrategyRequest) (*pb.DeleteConfigurationStrategyResponse, error) {
	if err := h.repo.Delete(ctx, req.StrategyId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete configuration strategy: %v", err)
	}

	return &pb.DeleteConfigurationStrategyResponse{}, nil
}

func (h *ConfigurationStrategyHandler) GetSessionConfiguration(ctx context.Context, req *pb.GetSessionConfigurationRequest, fullRepo *repository.Repository) (*pb.GetSessionConfigurationResponse, error) {
	// Get session to find game/config IDs
	session, err := fullRepo.Sessions.Get(ctx, req.SessionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
	}

	// Get SGC to find game_config_id and server_id
	sgc, err := fullRepo.ServerGameConfigs.Get(ctx, session.SGCID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "server game config not found: %v", err)
	}

	// Get game config to find game_id
	gc, err := fullRepo.GameConfigs.Get(ctx, sgc.GameConfigID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "game config not found: %v", err)
	}

	// Fetch all strategies for this game
	strategies, err := h.repo.ListByGame(ctx, gc.GameID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch strategies: %v", err)
	}

	// Render each strategy
	var renderedConfigs []*pb.RenderedConfiguration
	for _, strategy := range strategies {
		// Skip volume strategies - host-manager handles those separately
		if strategy.StrategyType == manman.StrategyTypeVolume {
			continue
		}

		rendered := &pb.RenderedConfiguration{
			StrategyName:    strategy.Name,
			StrategyType:    strategy.StrategyType,
			RenderedContent: "",
			BaseContent:     "",
		}

		if strategy.TargetPath != nil {
			rendered.TargetPath = *strategy.TargetPath
		}

		// Set base content (may be empty for merge mode)
		if strategy.BaseTemplate != nil {
			rendered.BaseContent = *strategy.BaseTemplate
		}

		// Cascade patches: GameConfig → ServerGameConfig
		// 1. Get all game_config level patches (ordered by patch_order ASC, patch_id ASC)
		gcPatches, err := fullRepo.ConfigurationPatches.ListByStrategyAndEntity(ctx, strategy.StrategyID, "game_config", gc.ConfigID)
		if err != nil {
			gcPatches = nil
		}

		// 2. Get all server_game_config level patches (override game_config)
		sgcPatches, err := fullRepo.ConfigurationPatches.ListByStrategyAndEntity(ctx, strategy.StrategyID, "server_game_config", sgc.SGCID)
		if err != nil {
			sgcPatches = nil
		}

		// Concatenate all patches in cascade order: GC patches first, then SGC patches
		// Each group is already ordered by patch_order; SGC patches override GC patches
		allPatches := append(gcPatches, sgcPatches...)
		patchContent := joinPatchContents(allPatches)

		// Set rendered content to the cascaded patches
		// Host-manager will merge this with existing file if base is empty (merge mode)
		rendered.RenderedContent = patchContent

		renderedConfigs = append(renderedConfigs, rendered)
	}

	return &pb.GetSessionConfigurationResponse{
		Configurations:     renderedConfigs,
		GameId:             gc.GameID,
		GameConfigId:       gc.ConfigID,
		ServerGameConfigId: sgc.SGCID,
	}, nil
}

func (h *ConfigurationStrategyHandler) PreviewConfiguration(ctx context.Context, req *pb.PreviewConfigurationRequest, fullRepo *repository.Repository) (*pb.PreviewConfigurationResponse, error) {
	// Get session configuration (same as GetSessionConfiguration for now)
	sessionResp, err := h.GetSessionConfiguration(ctx, &pb.GetSessionConfigurationRequest{
		SessionId: req.SessionId,
	}, fullRepo)
	if err != nil {
		return nil, err
	}

	// TODO: Apply parameter overrides from req.ParameterOverrides

	return &pb.PreviewConfigurationResponse{
		Configurations: sessionResp.Configurations,
	}, nil
}

func strategyToProto(s *manman.ConfigurationStrategy) *pb.ConfigurationStrategy {
	proto := &pb.ConfigurationStrategy{
		StrategyId:    s.StrategyID,
		GameId:        s.GameID,
		Name:          s.Name,
		StrategyType:  s.StrategyType,
		RenderOptions: jsonbToMap(s.RenderOptions),
		ApplyOrder:    int32(s.ApplyOrder),
	}

	if s.Description != nil {
		proto.Description = *s.Description
	}
	if s.TargetPath != nil {
		proto.TargetPath = *s.TargetPath
	}
	if s.BaseTemplate != nil {
		proto.BaseTemplate = *s.BaseTemplate
	}

	return proto
}

// joinPatchContents concatenates non-nil patch contents with newline separators.
func joinPatchContents(patches []*manman.ConfigurationPatch) string {
	var parts []string
	for _, p := range patches {
		if p.PatchContent != nil && *p.PatchContent != "" {
			parts = append(parts, *p.PatchContent)
		}
	}
	result := ""
	for i, part := range parts {
		if i > 0 {
			result += "\n"
		}
		result += part
	}
	return result
}
