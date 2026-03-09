package handlers

import (
	"bytes"
	"context"
	"fmt"
	"text/template"
	"time"

	s3lib "github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manmanv2"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BackupConfigHandler handles BackupConfig CRUD and TriggerBackup
type BackupConfigHandler struct {
	backupConfigRepo repository.BackupConfigRepository
	backupRepo       repository.BackupRepository
	sgcRepo          repository.ServerGameConfigRepository
	sessionRepo      repository.SessionRepository
	volumeRepo       repository.GameConfigVolumeRepository
	serverRepo       repository.ServerRepository
	actionRepo       *postgres.ActionRepository
	commandPublisher *CommandPublisher
	s3Client         *s3lib.Client
}

func NewBackupConfigHandler(
	backupConfigRepo repository.BackupConfigRepository,
	backupRepo repository.BackupRepository,
	sgcRepo repository.ServerGameConfigRepository,
	sessionRepo repository.SessionRepository,
	volumeRepo repository.GameConfigVolumeRepository,
	serverRepo repository.ServerRepository,
	actionRepo *postgres.ActionRepository,
	commandPublisher *CommandPublisher,
	s3Client *s3lib.Client,
) *BackupConfigHandler {
	return &BackupConfigHandler{
		backupConfigRepo: backupConfigRepo,
		backupRepo:       backupRepo,
		sgcRepo:          sgcRepo,
		sessionRepo:      sessionRepo,
		volumeRepo:       volumeRepo,
		serverRepo:       serverRepo,
		actionRepo:       actionRepo,
		commandPublisher: commandPublisher,
		s3Client:         s3Client,
	}
}

func (h *BackupConfigHandler) CreateBackupConfig(ctx context.Context, req *pb.CreateBackupConfigRequest) (*pb.CreateBackupConfigResponse, error) {
	if req.VolumeId == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume_id is required")
	}
	if req.CadenceMinutes <= 0 {
		return nil, status.Error(codes.InvalidArgument, "cadence_minutes must be > 0")
	}
	if req.BackupPath == "" {
		return nil, status.Error(codes.InvalidArgument, "backup_path is required")
	}

	cfg := &manman.BackupConfig{
		VolumeID:       req.VolumeId,
		CadenceMinutes: int(req.CadenceMinutes),
		BackupPath:     req.BackupPath,
		Enabled:        req.Enabled,
	}
	cfg, err := h.backupConfigRepo.Create(ctx, cfg)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create backup config: %v", err)
	}
	return &pb.CreateBackupConfigResponse{Config: backupConfigToProto(cfg)}, nil
}

func (h *BackupConfigHandler) GetBackupConfig(ctx context.Context, req *pb.GetBackupConfigRequest) (*pb.GetBackupConfigResponse, error) {
	cfg, err := h.backupConfigRepo.Get(ctx, req.BackupConfigId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "backup config not found: %v", err)
	}
	return &pb.GetBackupConfigResponse{Config: backupConfigToProto(cfg)}, nil
}

func (h *BackupConfigHandler) ListBackupConfigs(ctx context.Context, req *pb.ListBackupConfigsRequest) (*pb.ListBackupConfigsResponse, error) {
	cfgs, err := h.backupConfigRepo.List(ctx, req.VolumeId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list backup configs: %v", err)
	}
	pbCfgs := make([]*pb.BackupConfig, len(cfgs))
	for i, c := range cfgs {
		pbCfgs[i] = backupConfigToProto(c)
	}
	return &pb.ListBackupConfigsResponse{Configs: pbCfgs}, nil
}

func (h *BackupConfigHandler) UpdateBackupConfig(ctx context.Context, req *pb.UpdateBackupConfigRequest) (*pb.UpdateBackupConfigResponse, error) {
	cfg, err := h.backupConfigRepo.Get(ctx, req.BackupConfigId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "backup config not found: %v", err)
	}
	if req.CadenceMinutes > 0 {
		cfg.CadenceMinutes = int(req.CadenceMinutes)
	}
	if req.BackupPath != "" {
		cfg.BackupPath = req.BackupPath
	}
	cfg.Enabled = req.Enabled
	if err := h.backupConfigRepo.Update(ctx, cfg); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update backup config: %v", err)
	}
	return &pb.UpdateBackupConfigResponse{Config: backupConfigToProto(cfg)}, nil
}

func (h *BackupConfigHandler) DeleteBackupConfig(ctx context.Context, req *pb.DeleteBackupConfigRequest) (*pb.DeleteBackupConfigResponse, error) {
	if err := h.backupConfigRepo.Delete(ctx, req.BackupConfigId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete backup config: %v", err)
	}
	return &pb.DeleteBackupConfigResponse{}, nil
}

func (h *BackupConfigHandler) AddBackupConfigAction(ctx context.Context, req *pb.AddBackupConfigActionRequest) (*pb.AddBackupConfigActionResponse, error) {
	if err := h.backupConfigRepo.AddAction(ctx, req.BackupConfigId, req.ActionId, int(req.DisplayOrder)); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to add action: %v", err)
	}
	return &pb.AddBackupConfigActionResponse{}, nil
}

func (h *BackupConfigHandler) RemoveBackupConfigAction(ctx context.Context, req *pb.RemoveBackupConfigActionRequest) (*pb.RemoveBackupConfigActionResponse, error) {
	if err := h.backupConfigRepo.RemoveAction(ctx, req.BackupConfigId, req.ActionId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to remove action: %v", err)
	}
	return &pb.RemoveBackupConfigActionResponse{}, nil
}

func (h *BackupConfigHandler) ListBackupConfigActions(ctx context.Context, req *pb.ListBackupConfigActionsRequest) (*pb.ListBackupConfigActionsResponse, error) {
	actions, err := h.backupConfigRepo.ListActions(ctx, req.BackupConfigId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list actions: %v", err)
	}
	ids := make([]int64, len(actions))
	orders := make([]int32, len(actions))
	for i, a := range actions {
		ids[i] = a.ActionID
		orders[i] = int32(a.DisplayOrder)
	}
	return &pb.ListBackupConfigActionsResponse{ActionIds: ids, DisplayOrders: orders}, nil
}

// TriggerBackup creates a pending Backup record and dispatches a BackupCommand to the host-manager.
func (h *BackupConfigHandler) TriggerBackup(ctx context.Context, req *pb.TriggerBackupRequest) (*pb.TriggerBackupResponse, error) {
	// Fetch backup config
	cfg, err := h.backupConfigRepo.Get(ctx, req.BackupConfigId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "backup config not found: %v", err)
	}

	// Fetch volume for host path info
	volume, err := h.volumeRepo.Get(ctx, cfg.VolumeID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get volume: %v", err)
	}

	// Fetch SGC to get server_id
	sgc, err := h.sgcRepo.Get(ctx, req.ServerGameConfigId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "SGC not found: %v", err)
	}

	// Find the active session for this SGC (needed for backup record)
	sessions, err := h.sessionRepo.List(ctx, &sgc.SGCID, 1, 0)
	if err != nil || len(sessions) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "no session found for SGC")
	}
	sessionID := sessions[0].SessionID

	// Create pending Backup record to get backup_id
	now := time.Now()
	backup := &manman.Backup{
		SessionID:          sessionID,
		ServerGameConfigID: sgc.SGCID,
		BackupConfigID:     &cfg.BackupConfigID,
		VolumeID:           &volume.VolumeID,
		Status:             manman.BackupStatusPending,
		CreatedAt:          now,
	}
	backup, err = h.backupRepo.Create(ctx, backup)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create backup record: %v", err)
	}

	// Build pre-action commands (render templates with no inputs)
	actions, err := h.backupConfigRepo.ListActions(ctx, cfg.BackupConfigID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list pre-backup actions: %v", err)
	}

	preActionCommands := make([]string, 0, len(actions))
	for _, a := range actions {
		def, _, err := h.actionRepo.Get(ctx, a.ActionID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get action %d: %v", a.ActionID, err)
		}
		rendered, err := renderActionTemplate(def.CommandTemplate, nil)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to render action %d template: %v", a.ActionID, err)
		}
		preActionCommands = append(preActionCommands, rendered)
	}

	// Fetch server to get server_id for routing
	server, err := h.serverRepo.Get(ctx, sgc.ServerID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get server: %v", err)
	}

	s3Key := fmt.Sprintf("backups/%d/%d/%d.tar.gz", sgc.SGCID, cfg.BackupConfigID, backup.BackupID)

	presignedURL, err := h.s3Client.PresignPutURL(ctx, s3Key, 1*time.Hour)
	if err != nil {
		_ = h.backupRepo.UpdateStatus(ctx, backup.BackupID, manman.BackupStatusFailed, nil, nil, strPtr(err.Error()))
		return nil, status.Errorf(codes.Internal, "failed to generate presigned URL: %v", err)
	}

	cmd := &hostrmq.BackupCommand{
		BackupID:          backup.BackupID,
		SGCID:             sgc.SGCID,
		VolumeHostPath:    buildVolumeHostPath(volume.HostSubpath),
		BackupPath:        cfg.BackupPath,
		S3Key:             s3Key,
		PresignedURL:      presignedURL,
		PreActionCommands: preActionCommands,
		CreatedAt:         time.Now(),
	}

	if err := h.commandPublisher.PublishBackup(ctx, server.ServerID, cmd); err != nil {
		// Mark backup as failed if we can't dispatch
		_ = h.backupRepo.UpdateStatus(ctx, backup.BackupID, manman.BackupStatusFailed, nil, nil, strPtr(err.Error()))
		return nil, status.Errorf(codes.Internal, "failed to dispatch backup command: %v", err)
	}

	return &pb.TriggerBackupResponse{BackupId: backup.BackupID}, nil
}

func backupConfigToProto(c *manman.BackupConfig) *pb.BackupConfig {
	pb := &pb.BackupConfig{
		BackupConfigId: c.BackupConfigID,
		VolumeId:       c.VolumeID,
		CadenceMinutes: int32(c.CadenceMinutes),
		BackupPath:     c.BackupPath,
		Enabled:        c.Enabled,
		CreatedAt:      c.CreatedAt.Unix(),
		UpdatedAt:      c.UpdatedAt.Unix(),
	}
	if c.LastBackupAt != nil {
		pb.LastBackupAt = c.LastBackupAt.Unix()
	}
	return pb
}

func buildVolumeHostPath(hostSubpath *string) string {
	if hostSubpath != nil {
		return *hostSubpath
	}
	return ""
}

func renderActionTemplate(tmplStr string, inputs map[string]string) (string, error) {
	tmpl, err := template.New("action").Parse(tmplStr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, inputs); err != nil {
		return "", err
	}
	return buf.String(), nil
}
