package handlers

import (
	"context"

	"github.com/whale-net/everything/manmanv2/models"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GameConfigVolumeHandler handles GameConfigVolume-related RPCs
type GameConfigVolumeHandler struct {
	repo repository.GameConfigVolumeRepository
}

func NewGameConfigVolumeHandler(repo repository.GameConfigVolumeRepository) *GameConfigVolumeHandler {
	return &GameConfigVolumeHandler{repo: repo}
}

func (h *GameConfigVolumeHandler) CreateGameConfigVolume(ctx context.Context, req *pb.CreateGameConfigVolumeRequest) (*pb.CreateGameConfigVolumeResponse, error) {
	volumeType := req.VolumeType
	if volumeType == "" {
		volumeType = "bind"
	}

	isEnabled := true
	if req.IsEnabled != nil {
		isEnabled = req.GetIsEnabled()
	}

	volume := &manman.GameConfigVolume{
		ConfigID:      req.ConfigId,
		Name:          req.Name,
		Description:   stringPtr(req.Description),
		ContainerPath: req.ContainerPath,
		HostSubpath:   stringPtr(req.HostSubpath),
		ReadOnly:      req.ReadOnly,
		VolumeType:    volumeType,
		IsEnabled:     isEnabled,
	}

	created, err := h.repo.Create(ctx, volume)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create game config volume: %v", err)
	}

	return &pb.CreateGameConfigVolumeResponse{
		Volume: gameConfigVolumeToProto(created),
	}, nil
}

func (h *GameConfigVolumeHandler) GetGameConfigVolume(ctx context.Context, req *pb.GetGameConfigVolumeRequest) (*pb.GetGameConfigVolumeResponse, error) {
	volume, err := h.repo.Get(ctx, req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "game config volume not found: %v", err)
	}

	return &pb.GetGameConfigVolumeResponse{
		Volume: gameConfigVolumeToProto(volume),
	}, nil
}

func (h *GameConfigVolumeHandler) ListGameConfigVolumes(ctx context.Context, req *pb.ListGameConfigVolumesRequest) (*pb.ListGameConfigVolumesResponse, error) {
	volumes, err := h.repo.ListByGameConfig(ctx, req.ConfigId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list game config volumes: %v", err)
	}

	pbVolumes := make([]*pb.GameConfigVolume, len(volumes))
	for i, v := range volumes {
		pbVolumes[i] = gameConfigVolumeToProto(v)
	}

	return &pb.ListGameConfigVolumesResponse{
		Volumes: pbVolumes,
	}, nil
}

func (h *GameConfigVolumeHandler) UpdateGameConfigVolume(ctx context.Context, req *pb.UpdateGameConfigVolumeRequest) (*pb.UpdateGameConfigVolumeResponse, error) {
	volume, err := h.repo.Get(ctx, req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "game config volume not found: %v", err)
	}

	volume.Name = req.Name
	volume.Description = stringPtr(req.Description)
	volume.ContainerPath = req.ContainerPath
	volume.HostSubpath = stringPtr(req.HostSubpath)
	volume.ReadOnly = req.ReadOnly
	if req.VolumeType != "" {
		volume.VolumeType = req.VolumeType
	}
	if req.IsEnabled != nil {
		volume.IsEnabled = req.GetIsEnabled()
	}

	if err := h.repo.Update(ctx, volume); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update game config volume: %v", err)
	}

	return &pb.UpdateGameConfigVolumeResponse{
		Volume: gameConfigVolumeToProto(volume),
	}, nil
}

func (h *GameConfigVolumeHandler) DeleteGameConfigVolume(ctx context.Context, req *pb.DeleteGameConfigVolumeRequest) (*pb.DeleteGameConfigVolumeResponse, error) {
	if err := h.repo.Delete(ctx, req.VolumeId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete game config volume: %v", err)
	}

	return &pb.DeleteGameConfigVolumeResponse{}, nil
}

func gameConfigVolumeToProto(v *manman.GameConfigVolume) *pb.GameConfigVolume {
	proto := &pb.GameConfigVolume{
		VolumeId:      v.VolumeID,
		ConfigId:      v.ConfigID,
		Name:          v.Name,
		ContainerPath: v.ContainerPath,
		ReadOnly:      v.ReadOnly,
		VolumeType:    v.VolumeType,
		IsEnabled:     v.IsEnabled,
	}

	if v.Description != nil {
		proto.Description = *v.Description
	}
	if v.HostSubpath != nil {
		proto.HostSubpath = *v.HostSubpath
	}

	return proto
}
