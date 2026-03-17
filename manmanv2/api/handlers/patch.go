package handlers

import (
	"context"

	"github.com/whale-net/everything/manmanv2"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ConfigurationPatchHandler handles ConfigurationPatch-related RPCs
type ConfigurationPatchHandler struct {
	repo repository.ConfigurationPatchRepository
}

func NewConfigurationPatchHandler(repo repository.ConfigurationPatchRepository) *ConfigurationPatchHandler {
	return &ConfigurationPatchHandler{repo: repo}
}

func (h *ConfigurationPatchHandler) CreateConfigurationPatch(ctx context.Context, req *pb.CreateConfigurationPatchRequest) (*pb.CreateConfigurationPatchResponse, error) {
	patch := &manman.ConfigurationPatch{
		StrategyID:   req.StrategyId,
		PatchLevel:   req.PatchLevel,
		EntityID:     req.EntityId,
		PatchContent: stringPtr(req.PatchContent),
		PatchFormat:  req.PatchFormat,
		PatchOrder:   int(req.PatchOrder),
	}

	patch, err := h.repo.Create(ctx, patch)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create configuration patch: %v", err)
	}

	return &pb.CreateConfigurationPatchResponse{
		Patch: patchToProto(patch),
	}, nil
}

func (h *ConfigurationPatchHandler) UpdateConfigurationPatch(ctx context.Context, req *pb.UpdateConfigurationPatchRequest) (*pb.UpdateConfigurationPatchResponse, error) {
	patch, err := h.repo.Get(ctx, req.PatchId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "configuration patch not found: %v", err)
	}

	patch.PatchContent = stringPtr(req.PatchContent)
	patch.PatchFormat = req.PatchFormat
	patch.PatchOrder = int(req.PatchOrder)

	if err := h.repo.Update(ctx, patch); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update configuration patch: %v", err)
	}

	return &pb.UpdateConfigurationPatchResponse{
		Patch: patchToProto(patch),
	}, nil
}

func (h *ConfigurationPatchHandler) DeleteConfigurationPatch(ctx context.Context, req *pb.DeleteConfigurationPatchRequest) (*pb.DeleteConfigurationPatchResponse, error) {
	if err := h.repo.Delete(ctx, req.PatchId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete configuration patch: %v", err)
	}

	return &pb.DeleteConfigurationPatchResponse{}, nil
}

func (h *ConfigurationPatchHandler) ListConfigurationPatches(ctx context.Context, req *pb.ListConfigurationPatchesRequest) (*pb.ListConfigurationPatchesResponse, error) {
	var strategyID *int64
	var patchLevel *string
	var entityID *int64

	if req.StrategyId != nil {
		strategyID = req.StrategyId
	}
	if req.PatchLevel != nil {
		patchLevel = req.PatchLevel
	}
	if req.EntityId != nil {
		entityID = req.EntityId
	}

	patches, err := h.repo.List(ctx, strategyID, patchLevel, entityID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list configuration patches: %v", err)
	}

	pbPatches := make([]*pb.ConfigurationPatch, len(patches))
	for i, p := range patches {
		pbPatches[i] = patchToProto(p)
	}

	return &pb.ListConfigurationPatchesResponse{
		Patches: pbPatches,
	}, nil
}

func patchToProto(p *manman.ConfigurationPatch) *pb.ConfigurationPatch {
	proto := &pb.ConfigurationPatch{
		PatchId:     p.PatchID,
		StrategyId:  p.StrategyID,
		PatchLevel:  p.PatchLevel,
		EntityId:    p.EntityID,
		PatchFormat: p.PatchFormat,
		PatchOrder:  int32(p.PatchOrder),
	}

	if p.PatchContent != nil {
		proto.PatchContent = *p.PatchContent
	}
	if p.VolumeID != nil {
		proto.VolumeId = *p.VolumeID
	}
	if p.PathOverride != nil {
		proto.PathOverride = *p.PathOverride
	}

	return proto
}
