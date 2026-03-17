package workshop

import (
	"context"

	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateAddon creates a new workshop addon in the library
func (h *WorkshopServiceHandler) CreateAddon(ctx context.Context, req *pb.CreateAddonRequest) (*pb.CreateAddonResponse, error) {
	if req.GameId == 0 {
		return nil, status.Error(codes.InvalidArgument, "game_id is required")
	}
	if req.WorkshopId == "" {
		return nil, status.Error(codes.InvalidArgument, "workshop_id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	platformType := req.PlatformType
	if platformType == "" {
		platformType = manman.PlatformTypeSteamWorkshop
	}

	if req.PresetId == 0 && req.InstallationPath == "" {
		return nil, status.Error(codes.InvalidArgument,
			"addon must have either preset_id or installation_path set. "+
				"Use preset_id to reference a path preset, or provide a custom installation_path.")
	}

	addon := &manman.WorkshopAddon{
		GameID:       req.GameId,
		WorkshopID:   req.WorkshopId,
		PlatformType: platformType,
		Name:         req.Name,
		IsCollection: req.IsCollection,
	}

	if req.Description != "" {
		addon.Description = &req.Description
	}
	if req.FileSizeBytes > 0 {
		addon.FileSizeBytes = &req.FileSizeBytes
	}
	addon.PresetID = req.PresetId
	if req.InstallationPath != "" {
		addon.InstallationPath = &req.InstallationPath
	}
	if req.Metadata != "" {
		metadata := make(map[string]interface{})
		addon.Metadata = metadata
	}

	addon, err := h.addonRepo.Create(ctx, addon)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create addon: %v", err)
	}

	return &pb.CreateAddonResponse{
		Addon: addonToProto(addon),
	}, nil
}

// GetAddon retrieves a workshop addon by ID
func (h *WorkshopServiceHandler) GetAddon(ctx context.Context, req *pb.GetAddonRequest) (*pb.GetAddonResponse, error) {
	if req.AddonId == 0 {
		return nil, status.Error(codes.InvalidArgument, "addon_id is required")
	}

	addon, err := h.addonRepo.Get(ctx, req.AddonId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "addon not found: %v", err)
	}

	return &pb.GetAddonResponse{
		Addon: addonToProto(addon),
	}, nil
}

// ListAddons lists workshop addons with optional filtering
func (h *WorkshopServiceHandler) ListAddons(ctx context.Context, req *pb.ListAddonsRequest) (*pb.ListAddonsResponse, error) {
	limit := int(req.Limit)
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	offset := int(req.Offset)
	if offset < 0 {
		offset = 0
	}

	var gameID *int64
	if req.GameId > 0 {
		gameID = &req.GameId
	}

	addons, err := h.addonRepo.List(ctx, gameID, req.IncludeDeprecated, limit, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list addons: %v", err)
	}

	pbAddons := make([]*pb.WorkshopAddon, len(addons))
	for i, addon := range addons {
		pbAddons[i] = addonToProto(addon)
	}

	return &pb.ListAddonsResponse{
		Addons:     pbAddons,
		TotalCount: int32(len(addons)),
	}, nil
}

// UpdateAddon updates an existing workshop addon
func (h *WorkshopServiceHandler) UpdateAddon(ctx context.Context, req *pb.UpdateAddonRequest) (*pb.UpdateAddonResponse, error) {
	if req.AddonId == 0 {
		return nil, status.Error(codes.InvalidArgument, "addon_id is required")
	}

	addon, err := h.addonRepo.Get(ctx, req.AddonId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "addon not found: %v", err)
	}

	if len(req.UpdatePaths) == 0 {
		if req.Name != "" {
			addon.Name = req.Name
		}
		if req.Description != "" {
			addon.Description = &req.Description
		}
		if req.FileSizeBytes > 0 {
			addon.FileSizeBytes = &req.FileSizeBytes
		}
		addon.PresetID = req.PresetId
		if req.InstallationPath != "" {
			addon.InstallationPath = &req.InstallationPath
		}
		if req.Metadata != "" {
			metadata := make(map[string]interface{})
			addon.Metadata = metadata
		}
		addon.IsDeprecated = req.IsDeprecated
	} else {
		for _, path := range req.UpdatePaths {
			switch path {
			case "name":
				addon.Name = req.Name
			case "description":
				addon.Description = &req.Description
			case "file_size_bytes":
				addon.FileSizeBytes = &req.FileSizeBytes
			case "preset_id":
				addon.PresetID = req.PresetId
			case "installation_path":
				if req.InstallationPath != "" {
					addon.InstallationPath = &req.InstallationPath
				} else {
					addon.InstallationPath = nil
				}
			case "is_deprecated":
				addon.IsDeprecated = req.IsDeprecated
			case "metadata":
				metadata := make(map[string]interface{})
				addon.Metadata = metadata
			}
		}
	}

	hasPresetID := addon.PresetID != 0
	hasInstallPath := addon.InstallationPath != nil && *addon.InstallationPath != ""

	if !hasPresetID && !hasInstallPath {
		return nil, status.Error(codes.FailedPrecondition,
			"addon must have either preset_id or installation_path set after update. "+
				"Cannot clear both values as the addon would not be installable.")
	}

	if err := h.addonRepo.Update(ctx, addon); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update addon: %v", err)
	}

	return &pb.UpdateAddonResponse{
		Addon: addonToProto(addon),
	}, nil
}

// DeleteAddon deletes a workshop addon
func (h *WorkshopServiceHandler) DeleteAddon(ctx context.Context, req *pb.DeleteAddonRequest) (*pb.DeleteAddonResponse, error) {
	if req.AddonId == 0 {
		return nil, status.Error(codes.InvalidArgument, "addon_id is required")
	}

	if err := h.addonRepo.Delete(ctx, req.AddonId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete addon: %v", err)
	}

	return &pb.DeleteAddonResponse{}, nil
}

// FetchAddonMetadata fetches metadata from Steam Workshop API without creating a database record
func (h *WorkshopServiceHandler) FetchAddonMetadata(ctx context.Context, req *pb.FetchAddonMetadataRequest) (*pb.FetchAddonMetadataResponse, error) {
	if req.GameId == 0 {
		return nil, status.Error(codes.InvalidArgument, "game_id is required")
	}
	if req.WorkshopId == "" {
		return nil, status.Error(codes.InvalidArgument, "workshop_id is required")
	}

	platformType := req.PlatformType
	if platformType == "" {
		platformType = manman.PlatformTypeSteamWorkshop
	}

	if platformType != manman.PlatformTypeSteamWorkshop {
		return nil, status.Errorf(codes.InvalidArgument, "unsupported platform_type: %s", platformType)
	}

	metadata, err := h.workshopManager.FetchMetadata(ctx, req.GameId, req.WorkshopId)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "failed to fetch addon metadata from Steam Workshop: %v", err)
	}

	return &pb.FetchAddonMetadataResponse{
		Addon: addonToProto(metadata),
	}, nil
}

// addonToProto converts a WorkshopAddon model to protobuf.
// steam_app_id is read from metadata when available (legacy path for single-addon fetches).
func addonToProto(addon *manman.WorkshopAddon) *pb.WorkshopAddon {
	pbAddon := &pb.WorkshopAddon{
		AddonId:      addon.AddonID,
		GameId:       addon.GameID,
		WorkshopId:   addon.WorkshopID,
		PlatformType: addon.PlatformType,
		Name:         addon.Name,
		IsCollection: addon.IsCollection,
		IsDeprecated: addon.IsDeprecated,
		CreatedAt:    addon.CreatedAt.Unix(),
		UpdatedAt:    addon.UpdatedAt.Unix(),
	}

	if addon.Description != nil {
		pbAddon.Description = *addon.Description
	}
	if addon.FileSizeBytes != nil {
		pbAddon.FileSizeBytes = *addon.FileSizeBytes
	}
	if addon.InstallationPath != nil {
		pbAddon.InstallationPath = *addon.InstallationPath
	}
	pbAddon.PresetId = addon.PresetID
	if addon.LastUpdated != nil {
		pbAddon.LastUpdated = addon.LastUpdated.Unix()
	}
	if addon.Metadata != nil {
		if appID, ok := addon.Metadata["steam_app_id"].(string); ok {
			pbAddon.SteamAppId = appID
		}
	}

	return pbAddon
}

// addonWithGameToProto converts a WorkshopAddonWithGame (joined) to protobuf,
// using the game's steam_app_id directly from the JOIN result.
func addonWithGameToProto(addon *manman.WorkshopAddonWithGame) *pb.WorkshopAddon {
	pb := addonToProto(&addon.WorkshopAddon)
	if addon.SteamAppID != nil {
		pb.SteamAppId = *addon.SteamAppID
	}
	return pb
}
