package handlers

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type BackupHandler struct {
	backupRepo  repository.BackupRepository
	sessionRepo repository.SessionRepository
	s3Client    *s3.Client
}

func NewBackupHandler(backupRepo repository.BackupRepository, sessionRepo repository.SessionRepository, s3Client *s3.Client) *BackupHandler {
	return &BackupHandler{
		backupRepo:  backupRepo,
		sessionRepo: sessionRepo,
		s3Client:    s3Client,
	}
}

func (h *BackupHandler) CreateBackup(ctx context.Context, req *pb.CreateBackupRequest) (*pb.CreateBackupResponse, error) {
	// Verify session exists
	_, err := h.sessionRepo.Get(ctx, req.SessionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
	}

	// TODO: Phase 4 - Trigger backup via RabbitMQ to host manager
	// The host manager will:
	// 1. Create tarball of /data/gsc-{env}-{sgc_id} directory
	// 2. Upload tarball to S3
	// 3. Report back S3 URL and size via RabbitMQ
	//
	// For now, this is a stub that would fail in production
	return nil, status.Error(codes.Unimplemented, "backup creation requires host manager integration (Phase 4)")

	// This code will be uncommented in Phase 4:
	/*
	backup := &manman.Backup{
		SessionID:          req.SessionId,
		ServerGameConfigID: session.SGCID,
		S3URL:              "", // Will be set by host manager
		SizeBytes:          0,  // Will be set by host manager
		Description:        &req.Description,
		CreatedAt:          time.Now(),
	}

	backup, err = h.backupRepo.Create(ctx, backup)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create backup: %v", err)
	}

	return &pb.CreateBackupResponse{
		Backup: backupToProto(backup),
	}, nil
	*/
}

func (h *BackupHandler) ListBackups(ctx context.Context, req *pb.ListBackupsRequest) (*pb.ListBackupsResponse, error) {
	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}

	offset := 0
	if req.PageToken != "" {
		var err error
		offset, err = decodePageToken(req.PageToken)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid page token: %v", err)
		}
	}

	var sgcID *int64
	if req.ServerGameConfigId > 0 {
		sgcID = &req.ServerGameConfigId
	}

	var sessionID *int64
	if req.SessionId > 0 {
		sessionID = &req.SessionId
	}

	backups, err := h.backupRepo.List(ctx, sgcID, sessionID, pageSize+1, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list backups: %v", err)
	}

	var nextPageToken string
	if len(backups) > pageSize {
		backups = backups[:pageSize]
		nextPageToken = encodePageToken(offset + pageSize)
	}

	pbBackups := make([]*pb.Backup, len(backups))
	for i, b := range backups {
		pbBackups[i] = backupToProto(b)
	}

	return &pb.ListBackupsResponse{
		Backups:       pbBackups,
		NextPageToken: nextPageToken,
	}, nil
}

func (h *BackupHandler) GetBackup(ctx context.Context, req *pb.GetBackupRequest) (*pb.GetBackupResponse, error) {
	backup, err := h.backupRepo.Get(ctx, req.BackupId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "backup not found: %v", err)
	}

	return &pb.GetBackupResponse{
		Backup: backupToProto(backup),
	}, nil
}

func (h *BackupHandler) DeleteBackup(ctx context.Context, req *pb.DeleteBackupRequest) (*pb.DeleteBackupResponse, error) {
	// Get backup to find S3 URL
	backup, err := h.backupRepo.Get(ctx, req.BackupId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "backup not found: %v", err)
	}

	// Extract S3 key from URL (format: s3://bucket/key)
	s3Key, err := extractS3Key(backup.S3URL)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "invalid S3 URL: %v", err)
	}

	// Delete from S3
	if err := h.s3Client.Delete(ctx, s3Key); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete from S3: %v", err)
	}

	// Delete from database
	if err := h.backupRepo.Delete(ctx, req.BackupId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete backup: %v", err)
	}

	return &pb.DeleteBackupResponse{}, nil
}

func backupToProto(b *manman.Backup) *pb.Backup {
	pbBackup := &pb.Backup{
		BackupId:           b.BackupID,
		SessionId:          b.SessionID,
		ServerGameConfigId: b.ServerGameConfigID,
		S3Url:              b.S3URL,
		SizeBytes:          b.SizeBytes,
		CreatedAt:          b.CreatedAt.Unix(),
	}

	if b.Description != nil {
		pbBackup.Description = *b.Description
	}

	return pbBackup
}

// extractS3Key extracts the key from an S3 URL
// Example: "s3://bucket/path/to/file.tar.gz" -> "path/to/file.tar.gz"
func extractS3Key(s3URL string) (string, error) {
	const prefix = "s3://"
	if len(s3URL) < len(prefix) {
		return "", fmt.Errorf("invalid S3 URL: too short")
	}

	if s3URL[:len(prefix)] != prefix {
		return "", fmt.Errorf("invalid S3 URL: must start with s3://")
	}

	// Remove "s3://bucket/" to get just the key
	remainder := s3URL[len(prefix):]

	// Find first slash after bucket name
	slashIdx := -1
	for i, c := range remainder {
		if c == '/' {
			slashIdx = i
			break
		}
	}

	if slashIdx == -1 {
		return "", fmt.Errorf("invalid S3 URL: no key found")
	}

	return remainder[slashIdx+1:], nil
}
