package workshop

import (
	"context"

	"github.com/whale-net/everything/manmanv2"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (h *WorkshopServiceHandler) CreateAddonPathPreset(ctx context.Context, req *pb.CreateAddonPathPresetRequest) (*pb.CreateAddonPathPresetResponse, error) {
	if req.GameId == 0 {
		return nil, status.Error(codes.InvalidArgument, "game_id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.InstallationPath == "" {
		return nil, status.Error(codes.InvalidArgument, "installation_path is required")
	}

	preset := &manman.GameAddonPathPreset{
		GameID:           req.GameId,
		Name:             req.Name,
		InstallationPath: req.InstallationPath,
	}

	if req.Description != "" {
		preset.Description = &req.Description
	}

	created, err := h.presetRepo.Create(ctx, preset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create preset: %v", err)
	}

	return &pb.CreateAddonPathPresetResponse{
		Preset: presetToProto(created),
	}, nil
}

func (h *WorkshopServiceHandler) GetAddonPathPreset(ctx context.Context, req *pb.GetAddonPathPresetRequest) (*pb.GetAddonPathPresetResponse, error) {
	if req.PresetId == 0 {
		return nil, status.Error(codes.InvalidArgument, "preset_id is required")
	}

	preset, err := h.presetRepo.Get(ctx, req.PresetId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "preset not found: %v", err)
	}

	return &pb.GetAddonPathPresetResponse{
		Preset: presetToProto(preset),
	}, nil
}

func (h *WorkshopServiceHandler) ListAddonPathPresets(ctx context.Context, req *pb.ListAddonPathPresetsRequest) (*pb.ListAddonPathPresetsResponse, error) {
	if req.GameId == 0 {
		return nil, status.Error(codes.InvalidArgument, "game_id is required")
	}

	presets, err := h.presetRepo.ListByGame(ctx, req.GameId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list presets: %v", err)
	}

	pbPresets := make([]*pb.GameAddonPathPreset, len(presets))
	for i, preset := range presets {
		pbPresets[i] = presetToProto(preset)
	}

	return &pb.ListAddonPathPresetsResponse{
		Presets: pbPresets,
	}, nil
}

func (h *WorkshopServiceHandler) UpdateAddonPathPreset(ctx context.Context, req *pb.UpdateAddonPathPresetRequest) (*pb.UpdateAddonPathPresetResponse, error) {
	if req.PresetId == 0 {
		return nil, status.Error(codes.InvalidArgument, "preset_id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.InstallationPath == "" {
		return nil, status.Error(codes.InvalidArgument, "installation_path is required")
	}

	existing, err := h.presetRepo.Get(ctx, req.PresetId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "preset not found: %v", err)
	}

	preset := &manman.GameAddonPathPreset{
		PresetID:         req.PresetId,
		GameID:           existing.GameID,
		Name:             req.Name,
		InstallationPath: req.InstallationPath,
	}

	if req.Description != "" {
		preset.Description = &req.Description
	}

	err = h.presetRepo.Update(ctx, preset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update preset: %v", err)
	}

	updated, err := h.presetRepo.Get(ctx, req.PresetId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get updated preset: %v", err)
	}

	return &pb.UpdateAddonPathPresetResponse{
		Preset: presetToProto(updated),
	}, nil
}

func (h *WorkshopServiceHandler) DeleteAddonPathPreset(ctx context.Context, req *pb.DeleteAddonPathPresetRequest) (*pb.DeleteAddonPathPresetResponse, error) {
	if req.PresetId == 0 {
		return nil, status.Error(codes.InvalidArgument, "preset_id is required")
	}

	err := h.presetRepo.Delete(ctx, req.PresetId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete preset: %v", err)
	}

	return &pb.DeleteAddonPathPresetResponse{}, nil
}

// presetToProto converts a GameAddonPathPreset model to protobuf
func presetToProto(preset *manman.GameAddonPathPreset) *pb.GameAddonPathPreset {
	pbPreset := &pb.GameAddonPathPreset{
		PresetId:         preset.PresetID,
		GameId:           preset.GameID,
		Name:             preset.Name,
		InstallationPath: preset.InstallationPath,
		CreatedAt:        preset.CreatedAt.Unix(),
	}

	if preset.Description != nil {
		pbPreset.Description = *preset.Description
	}

	return pbPreset
}
