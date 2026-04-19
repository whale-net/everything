package handlers

import (
	"context"

	"github.com/whale-net/everything/manmanv2/models"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RestartScheduleHandler handles RestartSchedule CRUD RPCs
type RestartScheduleHandler struct {
	repo repository.RestartScheduleRepository
}

func NewRestartScheduleHandler(repo repository.RestartScheduleRepository) *RestartScheduleHandler {
	return &RestartScheduleHandler{repo: repo}
}

func (h *RestartScheduleHandler) CreateRestartSchedule(ctx context.Context, req *pb.CreateRestartScheduleRequest) (*pb.CreateRestartScheduleResponse, error) {
	if req.ServerGameConfigId == 0 {
		return nil, status.Error(codes.InvalidArgument, "server_game_config_id is required")
	}
	if req.CadenceMinutes <= 0 {
		return nil, status.Error(codes.InvalidArgument, "cadence_minutes must be > 0")
	}

	s := &manman.RestartSchedule{
		SGCID:          req.ServerGameConfigId,
		CadenceMinutes: int(req.CadenceMinutes),
		Enabled:        req.Enabled,
	}
	s, err := h.repo.Create(ctx, s)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create restart schedule: %v", err)
	}
	return &pb.CreateRestartScheduleResponse{Schedule: restartScheduleToProto(s)}, nil
}

func (h *RestartScheduleHandler) GetRestartSchedule(ctx context.Context, req *pb.GetRestartScheduleRequest) (*pb.GetRestartScheduleResponse, error) {
	s, err := h.repo.Get(ctx, req.RestartScheduleId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "restart schedule not found: %v", err)
	}
	return &pb.GetRestartScheduleResponse{Schedule: restartScheduleToProto(s)}, nil
}

func (h *RestartScheduleHandler) ListRestartSchedules(ctx context.Context, req *pb.ListRestartSchedulesRequest) (*pb.ListRestartSchedulesResponse, error) {
	schedules, err := h.repo.ListBySGC(ctx, req.ServerGameConfigId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list restart schedules: %v", err)
	}
	pbSchedules := make([]*pb.RestartSchedule, len(schedules))
	for i, s := range schedules {
		pbSchedules[i] = restartScheduleToProto(s)
	}
	return &pb.ListRestartSchedulesResponse{Schedules: pbSchedules}, nil
}

func (h *RestartScheduleHandler) UpdateRestartSchedule(ctx context.Context, req *pb.UpdateRestartScheduleRequest) (*pb.UpdateRestartScheduleResponse, error) {
	if req.CadenceMinutes <= 0 {
		return nil, status.Error(codes.InvalidArgument, "cadence_minutes must be > 0")
	}
	s, err := h.repo.Get(ctx, req.RestartScheduleId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "restart schedule not found: %v", err)
	}
	s.CadenceMinutes = int(req.CadenceMinutes)
	s.Enabled = req.Enabled
	if err := h.repo.Update(ctx, s); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update restart schedule: %v", err)
	}
	return &pb.UpdateRestartScheduleResponse{Schedule: restartScheduleToProto(s)}, nil
}

func (h *RestartScheduleHandler) DeleteRestartSchedule(ctx context.Context, req *pb.DeleteRestartScheduleRequest) (*pb.DeleteRestartScheduleResponse, error) {
	if err := h.repo.Delete(ctx, req.RestartScheduleId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete restart schedule: %v", err)
	}
	return &pb.DeleteRestartScheduleResponse{}, nil
}

func restartScheduleToProto(s *manman.RestartSchedule) *pb.RestartSchedule {
	p := &pb.RestartSchedule{
		RestartScheduleId:  s.RestartScheduleID,
		ServerGameConfigId: s.SGCID,
		CadenceMinutes:     int32(s.CadenceMinutes),
		Enabled:            s.Enabled,
		CreatedAt:          s.CreatedAt.Unix(),
		UpdatedAt:          s.UpdatedAt.Unix(),
	}
	if s.LastRestartAt != nil {
		p.LastRestartAt = s.LastRestartAt.Unix()
	}
	return p
}
