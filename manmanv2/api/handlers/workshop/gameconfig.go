package workshop

import (
	"context"
	"log/slog"

	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AddLibraryToGameConfig attaches a workshop library to a GameConfig (M6
// #2365, FR8) -- every deployment of the config inherits it, with no
// per-deployment attach step. Idempotent on (config_id, library_id): a
// re-attach just updates the override columns rather than erroring.
func (h *WorkshopServiceHandler) AddLibraryToGameConfig(ctx context.Context, req *pb.AddLibraryToGameConfigRequest) (*pb.AddLibraryToGameConfigResponse, error) {
	if req.ConfigId == 0 {
		return nil, status.Error(codes.InvalidArgument, "config_id is required")
	}
	if req.LibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "library_id is required")
	}

	gc, err := h.gameConfigRepo.Get(ctx, req.ConfigId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "game config %d not found: %v", req.ConfigId, err)
	}

	library, err := h.libraryRepo.Get(ctx, req.LibraryId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "library %d not found: %v", req.LibraryId, err)
	}
	if library.GameID != 0 && library.GameID != gc.GameID {
		return nil, status.Errorf(codes.InvalidArgument, "library %d belongs to game %d, not game config %d's game %d", req.LibraryId, library.GameID, req.ConfigId, gc.GameID)
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

	if err := h.gcLibraryRepo.AddLibrary(ctx, req.ConfigId, req.LibraryId, presetID, volumeID, installationPathOverride); err != nil {
		slog.Warn("failed to add workshop library to GameConfig", "config_id", req.ConfigId, "library_id", req.LibraryId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to add library to game config: %v", err)
	}

	slog.Info("workshop library added to GameConfig", "config_id", req.ConfigId, "library_id", req.LibraryId)

	return &pb.AddLibraryToGameConfigResponse{}, nil
}

// RemoveLibraryFromGameConfig detaches a workshop library from a GameConfig
// (M6 #2365, FR9). Because every deployment resolves its library set from
// the GameConfig, removing the GC-level attachment removes it from every
// deployment with no per-deployment bookkeeping -- there is no fan-out here
// on purpose. Removing a library that isn't attached is an idempotent
// success, not an error.
func (h *WorkshopServiceHandler) RemoveLibraryFromGameConfig(ctx context.Context, req *pb.RemoveLibraryFromGameConfigRequest) (*pb.RemoveLibraryFromGameConfigResponse, error) {
	if req.ConfigId == 0 {
		return nil, status.Error(codes.InvalidArgument, "config_id is required")
	}
	if req.LibraryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "library_id is required")
	}

	if err := h.gcLibraryRepo.RemoveLibrary(ctx, req.ConfigId, req.LibraryId); err != nil {
		slog.Warn("failed to remove workshop library from GameConfig", "config_id", req.ConfigId, "library_id", req.LibraryId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to remove library from game config: %v", err)
	}

	slog.Info("workshop library removed from GameConfig", "config_id", req.ConfigId, "library_id", req.LibraryId)

	return &pb.RemoveLibraryFromGameConfigResponse{}, nil
}

// ListGameConfigLibraries lists all libraries attached to a GameConfig.
func (h *WorkshopServiceHandler) ListGameConfigLibraries(ctx context.Context, req *pb.ListGameConfigLibrariesRequest) (*pb.ListGameConfigLibrariesResponse, error) {
	if req.ConfigId == 0 {
		return nil, status.Error(codes.InvalidArgument, "config_id is required")
	}

	libraries, err := h.gcLibraryRepo.ListLibraries(ctx, req.ConfigId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list game config libraries: %v", err)
	}

	pbLibraries := make([]*pb.WorkshopLibrary, len(libraries))
	for i, lib := range libraries {
		pbLibraries[i] = libraryToProto(lib)
	}

	return &pb.ListGameConfigLibrariesResponse{
		Libraries: pbLibraries,
	}, nil
}

// GetGameConfigLibraryAttachments gets attachment details (including
// overrides) for all libraries on a GameConfig.
func (h *WorkshopServiceHandler) GetGameConfigLibraryAttachments(ctx context.Context, req *pb.GetGameConfigLibraryAttachmentsRequest) (*pb.GetGameConfigLibraryAttachmentsResponse, error) {
	if req.ConfigId == 0 {
		return nil, status.Error(codes.InvalidArgument, "config_id is required")
	}

	attachments, err := h.gcLibraryRepo.ListAttachments(ctx, req.ConfigId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get game config library attachments: %v", err)
	}

	pbAttachments := make([]*pb.GameConfigWorkshopLibrary, len(attachments))
	for i, a := range attachments {
		attachment := &pb.GameConfigWorkshopLibrary{
			ConfigId:  a.ConfigID,
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

	return &pb.GetGameConfigLibraryAttachmentsResponse{
		Attachments: pbAttachments,
	}, nil
}

// ListLibraryMigrationConflicts lists every unresolved SGC->GC backfill
// conflict (FR12) for the resolution UI.
func (h *WorkshopServiceHandler) ListLibraryMigrationConflicts(ctx context.Context, req *pb.ListLibraryMigrationConflictsRequest) (*pb.ListLibraryMigrationConflictsResponse, error) {
	conflicts, err := h.gcLibraryRepo.ListUnresolvedConflicts(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list library migration conflicts: %v", err)
	}

	pbConflicts := make([]*pb.WorkshopLibraryMigrationConflict, len(conflicts))
	for i, c := range conflicts {
		pbConflicts[i] = conflictToProto(c)
	}

	return &pb.ListLibraryMigrationConflictsResponse{
		Conflicts: pbConflicts,
	}, nil
}

// ResolveLibraryMigrationConflict applies a union or override resolution to
// an SGC->GC backfill conflict (FR12). Union and override are the only
// resolution shapes M6 supports -- per-item manual merge is explicitly out
// of scope. Resolving an already-resolved conflict returns
// FailedPrecondition; an override without (or with an invalid)
// keep_library_id returns InvalidArgument.
func (h *WorkshopServiceHandler) ResolveLibraryMigrationConflict(ctx context.Context, req *pb.ResolveLibraryMigrationConflictRequest) (*pb.ResolveLibraryMigrationConflictResponse, error) {
	if req.ConflictId == 0 {
		return nil, status.Error(codes.InvalidArgument, "conflict_id is required")
	}
	if req.Resolution != "union" && req.Resolution != "override" {
		return nil, status.Errorf(codes.InvalidArgument, "resolution must be \"union\" or \"override\", got %q", req.Resolution)
	}

	var keepLibraryID *int64
	if req.Resolution == "override" {
		if req.KeepLibraryId == 0 {
			return nil, status.Error(codes.InvalidArgument, "keep_library_id is required for an override resolution")
		}

		candidates, err := h.gcLibraryRepo.ListConflictCandidates(ctx, req.ConflictId)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to look up conflict candidates: %v", err)
		}

		found := false
		for _, cand := range candidates {
			if cand.LibraryID == req.KeepLibraryId {
				found = true
				break
			}
		}
		if !found {
			return nil, status.Errorf(codes.InvalidArgument, "keep_library_id %d is not among conflict %d's candidates", req.KeepLibraryId, req.ConflictId)
		}

		keepLibraryID = &req.KeepLibraryId
	}

	if err := h.gcLibraryRepo.ResolveConflict(ctx, req.ConflictId, req.Resolution, keepLibraryID); err != nil {
		// The repository's guarded UPDATE (resolved_at IS NULL) is the only
		// source of "not found or already resolved" -- everything else is a
		// genuine internal failure.
		slog.Warn("failed to resolve library migration conflict", "conflict_id", req.ConflictId, "resolution", req.Resolution, "error", err)
		return nil, status.Errorf(codes.FailedPrecondition, "failed to resolve conflict %d: %v", req.ConflictId, err)
	}

	slog.Info("library migration conflict resolved", "conflict_id", req.ConflictId, "resolution", req.Resolution)

	return &pb.ResolveLibraryMigrationConflictResponse{}, nil
}

func conflictToProto(c *manman.WorkshopLibraryMigrationConflict) *pb.WorkshopLibraryMigrationConflict {
	candidates := make([]*pb.WorkshopLibraryMigrationConflictCandidate, len(c.Candidates))
	for i, cand := range c.Candidates {
		candidates[i] = &pb.WorkshopLibraryMigrationConflictCandidate{
			LibraryId: cand.LibraryID,
			SgcId:     cand.SGCID,
		}
	}

	return &pb.WorkshopLibraryMigrationConflict{
		ConflictId: c.ConflictID,
		ConfigId:   c.ConfigID,
		DetectedAt: c.DetectedAt.Unix(),
		Candidates: candidates,
	}
}
