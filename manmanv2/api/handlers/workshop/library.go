package workshop

import (
	"context"

	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

	libraries, err := h.libraryRepo.List(ctx, gameID, limit, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list libraries: %v", err)
	}

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

	library, err := h.libraryRepo.Get(ctx, req.LibraryId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "library not found: %v", err)
	}

	if len(req.UpdatePaths) == 0 {
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

	addon, err := h.addonRepo.Get(ctx, req.AddonId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "addon %d not found: %v", req.AddonId, err)
	}
	addonHasPath := addon.PresetID != 0 || (addon.InstallationPath != nil && *addon.InstallationPath != "")

	if !addonHasPath {
		library, err := h.libraryRepo.Get(ctx, req.LibraryId)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "library %d not found: %v", req.LibraryId, err)
		}
		if library.PresetID == nil {
			return nil, status.Errorf(codes.FailedPrecondition,
				"addon %d (%s) has no installation_path or preset_id, and library %d (%s) has no default preset_id. "+
					"Set a path on the addon, or set a default preset on the library before adding this addon.",
				addon.AddonID, addon.Name, library.LibraryID, library.Name)
		}
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
		pbAddons[i] = addonWithGameToProto(addon)
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
