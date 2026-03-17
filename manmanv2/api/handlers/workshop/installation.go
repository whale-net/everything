package workshop

import (
	"context"

	"github.com/whale-net/everything/manmanv2"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// InstallAddon installs a workshop addon to a ServerGameConfig
func (h *WorkshopServiceHandler) InstallAddon(ctx context.Context, req *pb.InstallAddonRequest) (*pb.InstallAddonResponse, error) {
	if req.SgcId == 0 {
		return nil, status.Error(codes.InvalidArgument, "sgc_id is required")
	}
	if req.AddonId == 0 {
		return nil, status.Error(codes.InvalidArgument, "addon_id is required")
	}

	installation, err := h.workshopManager.InstallAddon(ctx, req.SgcId, req.AddonId, req.ForceReinstall, req.SkipDispatch, req.InstallationPathOverride, req.PresetIdOverride, req.VolumeIdOverride)
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

	if req.SgcId > 0 && req.AddonId > 0 {
		installation, err := h.installationRepo.GetBySGCAndAddon(ctx, req.SgcId, req.AddonId)
		if err != nil {
			installations = []*manman.WorkshopInstallation{}
		} else {
			installations = []*manman.WorkshopInstallation{installation}
		}
	} else if req.SgcId > 0 {
		installations, err = h.installationRepo.ListBySGC(ctx, req.SgcId, limit, offset)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to list installations: %v", err)
		}
	} else if req.AddonId > 0 {
		installations, err = h.installationRepo.ListByAddon(ctx, req.AddonId, limit, offset)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to list installations: %v", err)
		}
	} else {
		installations, err = h.installationRepo.List(ctx, limit, offset)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to list installations: %v", err)
		}
	}

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

// ResetInstallation resets an installation status to pending for re-download
func (h *WorkshopServiceHandler) ResetInstallation(ctx context.Context, req *pb.ResetInstallationRequest) (*pb.ResetInstallationResponse, error) {
	if req.InstallationId == 0 {
		return nil, status.Error(codes.InvalidArgument, "installation_id is required")
	}

	installation, err := h.workshopManager.ResetInstallation(ctx, req.InstallationId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to reset installation: %v", err)
	}

	return &pb.ResetInstallationResponse{
		Installation: installationToProto(installation),
	}, nil
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
