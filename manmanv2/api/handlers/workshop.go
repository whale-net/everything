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
	sgcRepo          repository.ServerGameConfigRepository
	presetRepo       repository.AddonPathPresetRepository
	workshopManager  workshop.WorkshopManagerInterface
}

// NewWorkshopServiceHandler creates a new WorkshopServiceHandler
func NewWorkshopServiceHandler(
	addonRepo repository.WorkshopAddonRepository,
	installationRepo repository.WorkshopInstallationRepository,
	libraryRepo repository.WorkshopLibraryRepository,
	sgcRepo repository.ServerGameConfigRepository,
	presetRepo repository.AddonPathPresetRepository,
	workshopManager *workshop.WorkshopManager,
) *WorkshopServiceHandler {
	return &WorkshopServiceHandler{
		addonRepo:        addonRepo,
		installationRepo: installationRepo,
		libraryRepo:      libraryRepo,
		sgcRepo:          sgcRepo,
		presetRepo:       presetRepo,
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

	// VALIDATION: Addon must have a way to determine installation path
	// Either preset_id OR installation_path must be provided
	hasPresetID := req.PresetId != 0
	hasInstallPath := req.InstallationPath != ""

	if !hasPresetID && !hasInstallPath {
		return nil, status.Error(codes.InvalidArgument,
			"addon must have either preset_id or installation_path set. "+
			"Use preset_id to reference a path preset, or provide a custom installation_path.")
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
	if req.PresetId != 0 {
		addon.PresetID = &req.PresetId
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
		if req.PresetId != 0 {
			addon.PresetID = &req.PresetId
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
			case "preset_id":
				if req.PresetId != 0 {
					addon.PresetID = &req.PresetId
				} else {
					addon.PresetID = nil
				}
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

	// VALIDATION: After update, addon must still have a way to determine installation path
	hasPresetID := addon.PresetID != nil && *addon.PresetID != 0
	hasInstallPath := addon.InstallationPath != nil && *addon.InstallationPath != ""

	if !hasPresetID && !hasInstallPath {
		return nil, status.Error(codes.FailedPrecondition,
			"addon must have either preset_id or installation_path set after update. "+
			"Cannot clear both values as the addon would not be installable.")
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

// FetchAddonMetadata fetches metadata from Steam Workshop API without creating a database record
func (h *WorkshopServiceHandler) FetchAddonMetadata(ctx context.Context, req *pb.FetchAddonMetadataRequest) (*pb.FetchAddonMetadataResponse, error) {
	// Validate required fields
	if req.GameId == 0 {
		return nil, status.Error(codes.InvalidArgument, "game_id is required")
	}
	if req.WorkshopId == "" {
		return nil, status.Error(codes.InvalidArgument, "workshop_id is required")
	}

	// Set default platform type if not provided
	platformType := req.PlatformType
	if platformType == "" {
		platformType = manman.PlatformTypeSteamWorkshop
	}

	// Only support Steam Workshop for now
	if platformType != manman.PlatformTypeSteamWorkshop {
		return nil, status.Errorf(codes.InvalidArgument, "unsupported platform_type: %s", platformType)
	}

	// Fetch metadata from Steam Workshop API using the workshop manager's steam client
	// We use FetchAndCreateAddon but don't persist - just get the metadata
	metadata, err := h.workshopManager.FetchMetadata(ctx, req.GameId, req.WorkshopId)
	if err != nil {
		// Handle API failures gracefully with appropriate error codes
		return nil, status.Errorf(codes.Unavailable, "failed to fetch addon metadata from Steam Workshop: %v", err)
	}

	// Convert to protobuf and return without creating database record
	return &pb.FetchAddonMetadataResponse{
		Addon: addonToProto(metadata),
	}, nil
}

// ============================================================================
// Installation Management RPCs
// ============================================================================

// InstallAddon installs a workshop addon to a ServerGameConfig
func (h *WorkshopServiceHandler) InstallAddon(ctx context.Context, req *pb.InstallAddonRequest) (*pb.InstallAddonResponse, error) {
	if req.SgcId == 0 {
		return nil, status.Error(codes.InvalidArgument, "sgc_id is required")
	}
	if req.AddonId == 0 {
		return nil, status.Error(codes.InvalidArgument, "addon_id is required")
	}

	installation, err := h.workshopManager.InstallAddon(ctx, req.SgcId, req.AddonId, req.ForceReinstall, req.SkipDispatch)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to install addon: %v", err)
	}

	return &pb.InstallAddonResponse{
		Installation: installationToProto(installation),
	}, nil
}

// GetInstallation retrieves a workshop installation by ID
func (h *WorkshopServiceHandler) GetInstallation(ctx context.Context, req *pb.GetInstallationRequest) (*pb.GetInstallationResponse, error) {
	if req.InstallationId == 0 {
		return nil, status.Error(codes.InvalidArgument, "installation_id is required")
	}

	installation, err := h.installationRepo.Get(ctx, req.InstallationId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "installation not found: %v", err)
	}

	return &pb.GetInstallationResponse{
		Installation: installationToProto(installation),
	}, nil
}

// ListInstallations lists workshop installations with optional filtering
func (h *WorkshopServiceHandler) ListInstallations(ctx context.Context, req *pb.ListInstallationsRequest) (*pb.ListInstallationsResponse, error) {
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

	var installations []*manman.WorkshopInstallation
	var err error

	// Apply filters based on request
	if req.SgcId > 0 && req.AddonId > 0 {
		// Filter by both SGC and addon
		installation, err := h.installationRepo.GetBySGCAndAddon(ctx, req.SgcId, req.AddonId)
		if err != nil {
			// Not found is not an error for list operations
			installations = []*manman.WorkshopInstallation{}
		} else {
			installations = []*manman.WorkshopInstallation{installation}
		}
	} else if req.SgcId > 0 {
		// Filter by SGC only
		installations, err = h.installationRepo.ListBySGC(ctx, req.SgcId, limit, offset)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to list installations: %v", err)
		}
	} else if req.AddonId > 0 {
		// Filter by addon only
		installations, err = h.installationRepo.ListByAddon(ctx, req.AddonId, limit, offset)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to list installations: %v", err)
		}
	} else {
		// No filters - list all (with pagination)
		installations, err = h.installationRepo.List(ctx, limit, offset)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to list installations: %v", err)
		}
	}

	// Convert to protobuf
	pbInstallations := make([]*pb.WorkshopInstallation, len(installations))
	for i, installation := range installations {
		pbInstallations[i] = installationToProto(installation)
	}

	return &pb.ListInstallationsResponse{
		Installations: pbInstallations,
		TotalCount:    int32(len(installations)),
	}, nil
}

// RemoveInstallation removes an installed addon from a ServerGameConfig
func (h *WorkshopServiceHandler) RemoveInstallation(ctx context.Context, req *pb.RemoveInstallationRequest) (*pb.RemoveInstallationResponse, error) {
	if req.InstallationId == 0 {
		return nil, status.Error(codes.InvalidArgument, "installation_id is required")
	}

	if err := h.workshopManager.RemoveInstallation(ctx, req.InstallationId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to remove installation: %v", err)
	}

	return &pb.RemoveInstallationResponse{}, nil
}

// ============================================================================
// Library Management RPCs
// ============================================================================

// CreateLibrary creates a new workshop library
func (h *WorkshopServiceHandler) CreateLibrary(ctx context.Context, req *pb.CreateLibraryRequest) (*pb.CreateLibraryResponse, error) {
	if req.GameId == 0 {
		return nil, status.Error(codes.InvalidArgument, "game_id is required")
	}
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	library := &manman.WorkshopLibrary{
		GameID: req.GameId,
		Name:   req.Name,
	}

	if req.Description != "" {
		library.Description = &req.Description
	}
	if req.PresetId != 0 {
		library.PresetID = &req.PresetId
	}

	library, err := h.libraryRepo.Create(ctx, library)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create library: %v", err)
	}

	return &pb.CreateLibraryResponse{
		Library: libraryToProto(library),
	}, nil
}

// GetLibrary retrieves a workshop library by ID
func (h *WorkshopServiceHandler) GetLibrary(ctx context.Context, req *pb.GetLibraryRequest) (*pb.GetLibraryResponse, error) {
	if req.LibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "library_id is required")
	}

	library, err := h.libraryRepo.Get(ctx, req.LibraryId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "library not found: %v", err)
	}

	return &pb.GetLibraryResponse{
		Library: libraryToProto(library),
	}, nil
}

// ListLibraries lists workshop libraries with optional filtering
func (h *WorkshopServiceHandler) ListLibraries(ctx context.Context, req *pb.ListLibrariesRequest) (*pb.ListLibrariesResponse, error) {
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

	libraries, err := h.libraryRepo.List(ctx, gameID, limit, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list libraries: %v", err)
	}

	// Convert to protobuf
	pbLibraries := make([]*pb.WorkshopLibrary, len(libraries))
	for i, library := range libraries {
		pbLibraries[i] = libraryToProto(library)
	}

	return &pb.ListLibrariesResponse{
		Libraries:  pbLibraries,
		TotalCount: int32(len(libraries)),
	}, nil
}

// UpdateLibrary updates an existing workshop library
func (h *WorkshopServiceHandler) UpdateLibrary(ctx context.Context, req *pb.UpdateLibraryRequest) (*pb.UpdateLibraryResponse, error) {
	if req.LibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "library_id is required")
	}

	// Get existing library
	library, err := h.libraryRepo.Get(ctx, req.LibraryId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "library not found: %v", err)
	}

	// Apply field updates
	if len(req.UpdatePaths) == 0 {
		// Update all provided fields
		if req.Name != "" {
			library.Name = req.Name
		}
		if req.Description != "" {
			library.Description = &req.Description
		}
		if req.PresetId != 0 {
			library.PresetID = &req.PresetId
		}
	} else {
		// Update only specified fields
		for _, path := range req.UpdatePaths {
			switch path {
			case "name":
				library.Name = req.Name
			case "description":
				library.Description = &req.Description
			case "preset_id":
				if req.PresetId != 0 {
					library.PresetID = &req.PresetId
				}
			}
		}
	}

	// Update in database
	if err := h.libraryRepo.Update(ctx, library); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update library: %v", err)
	}

	return &pb.UpdateLibraryResponse{
		Library: libraryToProto(library),
	}, nil
}

// DeleteLibrary deletes a workshop library
func (h *WorkshopServiceHandler) DeleteLibrary(ctx context.Context, req *pb.DeleteLibraryRequest) (*pb.DeleteLibraryResponse, error) {
	if req.LibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "library_id is required")
	}

	if err := h.libraryRepo.Delete(ctx, req.LibraryId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete library: %v", err)
	}

	return &pb.DeleteLibraryResponse{}, nil
}

// AddAddonToLibrary adds an addon to a library
func (h *WorkshopServiceHandler) AddAddonToLibrary(ctx context.Context, req *pb.AddAddonToLibraryRequest) (*pb.AddAddonToLibraryResponse, error) {
	if req.LibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "library_id is required")
	}
	if req.AddonId == 0 {
		return nil, status.Error(codes.InvalidArgument, "addon_id is required")
	}

	displayOrder := int(req.DisplayOrder)
	if displayOrder < 0 {
		displayOrder = 0
	}

	if err := h.libraryRepo.AddAddon(ctx, req.LibraryId, req.AddonId, displayOrder); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to add addon to library: %v", err)
	}

	return &pb.AddAddonToLibraryResponse{}, nil
}

// RemoveAddonFromLibrary removes an addon from a library
func (h *WorkshopServiceHandler) RemoveAddonFromLibrary(ctx context.Context, req *pb.RemoveAddonFromLibraryRequest) (*pb.RemoveAddonFromLibraryResponse, error) {
	if req.LibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "library_id is required")
	}
	if req.AddonId == 0 {
		return nil, status.Error(codes.InvalidArgument, "addon_id is required")
	}

	if err := h.libraryRepo.RemoveAddon(ctx, req.LibraryId, req.AddonId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to remove addon from library: %v", err)
	}

	return &pb.RemoveAddonFromLibraryResponse{}, nil
}

// AddLibraryReference adds a reference from one library to another with circular reference detection
func (h *WorkshopServiceHandler) AddLibraryReference(ctx context.Context, req *pb.AddLibraryReferenceRequest) (*pb.AddLibraryReferenceResponse, error) {
	if req.ParentLibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "parent_library_id is required")
	}
	if req.ChildLibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "child_library_id is required")
	}
	if req.ParentLibraryId == req.ChildLibraryId {
		return nil, status.Error(codes.InvalidArgument, "cannot reference library to itself")
	}

	// Check for circular reference before adding
	hasCircular, err := h.libraryRepo.DetectCircularReference(ctx, req.ParentLibraryId, req.ChildLibraryId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check for circular reference: %v", err)
	}
	if hasCircular {
		return nil, status.Error(codes.InvalidArgument, "adding this reference would create a circular dependency")
	}

	if err := h.libraryRepo.AddReference(ctx, req.ParentLibraryId, req.ChildLibraryId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to add library reference: %v", err)
	}

	return &pb.AddLibraryReferenceResponse{}, nil
}

// RemoveLibraryReference removes a reference from one library to another
func (h *WorkshopServiceHandler) RemoveLibraryReference(ctx context.Context, req *pb.RemoveLibraryReferenceRequest) (*pb.RemoveLibraryReferenceResponse, error) {
	if req.ParentLibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "parent_library_id is required")
	}
	if req.ChildLibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "child_library_id is required")
	}

	if err := h.libraryRepo.RemoveReference(ctx, req.ParentLibraryId, req.ChildLibraryId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to remove library reference: %v", err)
	}

	return &pb.RemoveLibraryReferenceResponse{}, nil
}

// GetLibraryAddons returns all addons in a library
func (h *WorkshopServiceHandler) GetLibraryAddons(ctx context.Context, req *pb.GetLibraryAddonsRequest) (*pb.GetLibraryAddonsResponse, error) {
	if req.LibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "library_id is required")
	}

	addons, err := h.libraryRepo.ListAddons(ctx, req.LibraryId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get library addons: %v", err)
	}

	pbAddons := make([]*pb.WorkshopAddon, len(addons))
	for i, addon := range addons {
		pbAddons[i] = addonToProto(addon)
	}

	return &pb.GetLibraryAddonsResponse{
		Addons: pbAddons,
	}, nil
}

// GetChildLibraries returns all child libraries of a library
func (h *WorkshopServiceHandler) GetChildLibraries(ctx context.Context, req *pb.GetChildLibrariesRequest) (*pb.GetChildLibrariesResponse, error) {
	if req.LibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "library_id is required")
	}

	libraries, err := h.libraryRepo.ListReferences(ctx, req.LibraryId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get child libraries: %v", err)
	}

	pbLibraries := make([]*pb.WorkshopLibrary, len(libraries))
	for i, lib := range libraries {
		pbLibraries[i] = libraryToProto(lib)
	}

	return &pb.GetChildLibrariesResponse{
		Libraries: pbLibraries,
	}, nil
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

// ============================================================================
// SGC-Library Management RPCs
// ============================================================================

// AddLibraryToSGC attaches a workshop library to a ServerGameConfig
func (h *WorkshopServiceHandler) AddLibraryToSGC(ctx context.Context, req *pb.AddLibraryToSGCRequest) (*pb.AddLibraryToSGCResponse, error) {
	if req.SgcId == 0 {
		return nil, status.Error(codes.InvalidArgument, "sgc_id is required")
	}
	if req.LibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "library_id is required")
	}

	var presetID, volumeID *int64
	var installationPathOverride *string

	if req.PresetId != 0 {
		presetID = &req.PresetId
	}
	if req.VolumeId != 0 {
		volumeID = &req.VolumeId
	}
	if req.InstallationPathOverride != "" {
		installationPathOverride = &req.InstallationPathOverride
	}

	if err := h.sgcRepo.AddLibrary(ctx, req.SgcId, req.LibraryId, presetID, volumeID, installationPathOverride); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to add library to SGC: %v", err)
	}

	return &pb.AddLibraryToSGCResponse{}, nil
}

// RemoveLibraryFromSGC detaches a workshop library from a ServerGameConfig
func (h *WorkshopServiceHandler) RemoveLibraryFromSGC(ctx context.Context, req *pb.RemoveLibraryFromSGCRequest) (*pb.RemoveLibraryFromSGCResponse, error) {
	if req.SgcId == 0 {
		return nil, status.Error(codes.InvalidArgument, "sgc_id is required")
	}
	if req.LibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "library_id is required")
	}

	if err := h.sgcRepo.RemoveLibrary(ctx, req.SgcId, req.LibraryId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to remove library from SGC: %v", err)
	}

	return &pb.RemoveLibraryFromSGCResponse{}, nil
}

// ListSGCLibraries lists all libraries attached to a ServerGameConfig
func (h *WorkshopServiceHandler) ListSGCLibraries(ctx context.Context, req *pb.ListSGCLibrariesRequest) (*pb.ListSGCLibrariesResponse, error) {
	if req.SgcId == 0 {
		return nil, status.Error(codes.InvalidArgument, "sgc_id is required")
	}

	libraries, err := h.sgcRepo.ListLibraries(ctx, req.SgcId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list SGC libraries: %v", err)
	}

	pbLibraries := make([]*pb.WorkshopLibrary, len(libraries))
	for i, lib := range libraries {
		pbLibraries[i] = libraryToProto(lib)
	}

	return &pb.ListSGCLibrariesResponse{
		Libraries: pbLibraries,
	}, nil
}

// GetSGCLibraryAttachments gets attachment details for all libraries on an SGC
func (h *WorkshopServiceHandler) GetSGCLibraryAttachments(ctx context.Context, req *pb.GetSGCLibraryAttachmentsRequest) (*pb.GetSGCLibraryAttachmentsResponse, error) {
	if req.SgcId == 0 {
		return nil, status.Error(codes.InvalidArgument, "sgc_id is required")
	}

	attachments, err := h.sgcRepo.GetSGCLibraryAttachments(ctx, req.SgcId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get SGC library attachments: %v", err)
	}

	pbAttachments := make([]*pb.SGCWorkshopLibrary, len(attachments))
	for i, a := range attachments {
		attachment := &pb.SGCWorkshopLibrary{
			SgcId:     a.SGCID,
			LibraryId: a.LibraryID,
			CreatedAt: a.CreatedAt.Unix(),
		}

		if a.PresetID != nil {
			attachment.PresetId = *a.PresetID
		}
		if a.VolumeID != nil {
			attachment.VolumeId = *a.VolumeID
		}
		if a.InstallationPathOverride != nil {
			attachment.InstallationPathOverride = *a.InstallationPathOverride
		}

		pbAttachments[i] = attachment
	}

	return &pb.GetSGCLibraryAttachmentsResponse{
		Attachments: pbAttachments,
	}, nil
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
	if library.PresetID != nil {
		pbLibrary.PresetId = *library.PresetID
	}

	return pbLibrary
}

// ============================================================================
// Path Preset Management RPCs
// ============================================================================

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

	// Get existing preset to preserve game_id
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

	// Refetch to get updated data
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
