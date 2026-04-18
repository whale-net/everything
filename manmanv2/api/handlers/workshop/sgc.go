package workshop

import (
	"context"

	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
