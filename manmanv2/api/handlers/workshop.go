package handlers

import (
	"context"

	"github.com/whale-net/everything/manmanv2"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/workshop"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// WorkshopServiceHandler handles workshop addon management RPCs
type WorkshopServiceHandler struct {
	pb.UnimplementedWorkshopServiceServer
	addonRepo        repository.WorkshopAddonRepository
	installationRepo repository.WorkshopInstallationRepository
	libraryRepo      repository.WorkshopLibraryRepository
	workshopManager  *workshop.WorkshopManager
}

// NewWorkshopServiceHandler creates a new WorkshopServiceHandler
func NewWorkshopServiceHandler(
	addonRepo repository.WorkshopAddonRepository,
	installationRepo repository.WorkshopInstallationRepository,
	libraryRepo repository.WorkshopLibraryRepository,
	workshopManager *workshop.WorkshopManager,
) *WorkshopServiceHandler {
	return &WorkshopServiceHandler{
		addonRepo:        addonRepo,
		installationRepo: installationRepo,
		libraryRepo:      libraryRepo,
		workshopManager:  workshopManager,
	}
}

// ============================================================================
// Addon Library Management RPCs
// ============================================================================

// CreateAddon creates a new workshop addon in the library
func (h *WorkshopServiceHandler) CreateAddon(ctx context.Context, req *pb.CreateAddonRequest) (*pb.CreateAddonResponse, error) {
	// Validate required fields
	if req.GameId == 0 {
		return nil, status.Error(codes.InvalidArgument, "game_id is required")
	}
	if req.WorkshopId == "" {
		return nil, status.Error(codes.InvalidArgument, "workshop_id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	// Set default platform type if not provided
	platformType := req.PlatformType
	if platformType == "" {
		platformType = manman.PlatformTypeSteamWorkshop
	}

	// Build addon model
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
	if req.InstallationPath != "" {
		addon.InstallationPath = &req.InstallationPath
	}
	if req.Metadata != "" {
		// Parse metadata JSON string into map
		metadata := make(map[string]interface{})
		// For now, store as-is; proper JSON parsing would be done here
		addon.Metadata = metadata
	}

	// Create addon in database
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
	// Set default pagination values
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

	// Apply game filter if provided
	var gameID *int64
	if req.GameId > 0 {
		gameID = &req.GameId
	}

	// List addons from repository
	addons, err := h.addonRepo.List(ctx, gameID, req.IncludeDeprecated, limit, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list addons: %v", err)
	}

	// Convert to protobuf
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

	// Get existing addon
	addon, err := h.addonRepo.Get(ctx, req.AddonId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "addon not found: %v", err)
	}

	// Apply field updates
	if len(req.UpdatePaths) == 0 {
		// Update all provided fields
		if req.Name != "" {
			addon.Name = req.Name
		}
		if req.Description != "" {
			addon.Description = &req.Description
		}
		if req.FileSizeBytes > 0 {
			addon.FileSizeBytes = &req.FileSizeBytes
		}
		if req.InstallationPath != "" {
			addon.InstallationPath = &req.InstallationPath
		}
		if req.Metadata != "" {
			// Parse metadata JSON string
			metadata := make(map[string]interface{})
			addon.Metadata = metadata
		}
		addon.IsDeprecated = req.IsDeprecated
	} else {
		// Update only specified fields
		for _, path := range req.UpdatePaths {
			switch path {
			case "name":
				addon.Name = req.Name
			case "description":
				addon.Description = &req.Description
			case "file_size_bytes":
				addon.FileSizeBytes = &req.FileSizeBytes
			case "installation_path":
				addon.InstallationPath = &req.InstallationPath
			case "is_deprecated":
				addon.IsDeprecated = req.IsDeprecated
			case "metadata":
				metadata := make(map[string]interface{})
				addon.Metadata = metadata
			}
		}
	}

	// Update in database
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

// ============================================================================
// Conversion Helpers
// ============================================================================

// addonToProto converts a WorkshopAddon model to protobuf
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
	if addon.LastUpdated != nil {
		pbAddon.LastUpdated = addon.LastUpdated.Unix()
	}
	if addon.Metadata != nil {
		// Convert metadata map to JSON string
		// For now, leave empty; proper JSON marshaling would be done here
		pbAddon.Metadata = ""
	}

	return pbAddon
}

// installationToProto converts a WorkshopInstallation model to protobuf
func installationToProto(installation *manman.WorkshopInstallation) *pb.WorkshopInstallation {
	pbInstallation := &pb.WorkshopInstallation{
		InstallationId:   installation.InstallationID,
		SgcId:            installation.SGCID,
		AddonId:          installation.AddonID,
		Status:           installation.Status,
		InstallationPath: installation.InstallationPath,
		ProgressPercent:  int32(installation.ProgressPercent),
		CreatedAt:        installation.CreatedAt.Unix(),
		UpdatedAt:        installation.UpdatedAt.Unix(),
	}

	if installation.ErrorMessage != nil {
		pbInstallation.ErrorMessage = *installation.ErrorMessage
	}
	if installation.DownloadStartedAt != nil {
		pbInstallation.DownloadStartedAt = installation.DownloadStartedAt.Unix()
	}
	if installation.DownloadCompletedAt != nil {
		pbInstallation.DownloadCompletedAt = installation.DownloadCompletedAt.Unix()
	}

	return pbInstallation
}

// libraryToProto converts a WorkshopLibrary model to protobuf
func libraryToProto(library *manman.WorkshopLibrary) *pb.WorkshopLibrary {
	pbLibrary := &pb.WorkshopLibrary{
		LibraryId: library.LibraryID,
		GameId:    library.GameID,
		Name:      library.Name,
		CreatedAt: library.CreatedAt.Unix(),
		UpdatedAt: library.UpdatedAt.Unix(),
	}

	if library.Description != nil {
		pbLibrary.Description = *library.Description
	}

	return pbLibrary
}
