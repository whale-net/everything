package handlers

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manmanv2"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

type LogsHandler struct {
	logRefRepo repository.LogReferenceRepository
	s3Client   *s3.Client
}

func NewLogsHandler(logRefRepo repository.LogReferenceRepository, s3Client *s3.Client) *LogsHandler {
	return &LogsHandler{
		logRefRepo: logRefRepo,
		s3Client:   s3Client,
	}
}

func (h *LogsHandler) SendBatchedLogs(ctx context.Context, req *pb.SendBatchedLogsRequest) (*pb.SendBatchedLogsResponse, error) {
	accepted := int32(0)
	failed := []string{}

	for _, batch := range req.Batches {
		// 1. Decompress logs for storage (we store decompressed in S3)
		logs, err := decompressLogs(batch.CompressedLogs)
		if err != nil {
			failed = append(failed, batch.BatchId)
			continue
		}

		// 2. Upload to S3
		// Key format: logs/{session_id}/{timestamp}-{batch_id}.log
		s3Key := fmt.Sprintf("logs/%d/%d-%s.log", batch.SessionId, batch.StartTimestamp, batch.BatchId)

		s3URL, err := h.s3Client.Upload(ctx, s3Key, logs, &s3.UploadOptions{
			ContentType: "text/plain",
			Metadata: map[string]string{
				"session-id": fmt.Sprintf("%d", batch.SessionId),
				"source":     batch.Source.String(),
				"line-count": fmt.Sprintf("%d", batch.LineCount),
			},
		})
		if err != nil {
			failed = append(failed, batch.BatchId)
			continue
		}

		// 3. Store reference in database
		logRef := &manman.LogReference{
			SessionID: batch.SessionId,
			FilePath:  s3URL, // S3 URL (e.g., s3://bucket/logs/123/...)
			StartTime: time.Unix(batch.StartTimestamp, 0),
			EndTime:   time.Unix(batch.EndTimestamp, 0),
			LineCount: batch.LineCount,
			Source:    batch.Source.String(),
			CreatedAt: time.Now(),
		}

		if err := h.logRefRepo.Create(ctx, logRef); err != nil {
			failed = append(failed, batch.BatchId)
			continue
		}

		accepted++
	}

	return &pb.SendBatchedLogsResponse{
		AcceptedBatches: accepted,
		FailedBatchIds:  failed,
	}, nil
}

func (h *LogsHandler) GetHistoricalLogs(ctx context.Context, req *pb.GetHistoricalLogsRequest) (*pb.GetHistoricalLogsResponse, error) {
	// Validate time range (max 30 minutes)
	maxDuration := int64(30 * 60) // 30 minutes in seconds
	if req.EndTimestamp-req.StartTimestamp > maxDuration {
		return nil, fmt.Errorf("time range too large: maximum 30 minutes allowed")
	}

	startTime := time.Unix(req.StartTimestamp, 0)
	endTime := time.Unix(req.EndTimestamp, 0)

	// Query database for log references in range by session ID
	logRefs, err := h.logRefRepo.ListBySessionAndTimeRange(ctx, req.SessionId, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query log references: %w", err)
	}

	// Download S3 objects and build batches
	batches := make([]*pb.HistoricalLogBatch, 0, len(logRefs))
	for _, logRef := range logRefs {
		// Parse S3 URL to extract key (format: s3://bucket/key)
		s3Key, err := parseS3Key(logRef.FilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to parse S3 URL %s: %w", logRef.FilePath, err)
		}

		// Download gzipped content from S3
		compressedContent, err := h.s3Client.Download(ctx, s3Key)
		if err != nil {
			return nil, fmt.Errorf("failed to download log from S3: %w", err)
		}

		// Decompress gzipped data
		content, err := decompressLogs(compressedContent)
		if err != nil {
			return nil, fmt.Errorf("failed to decompress log content: %w", err)
		}

		batch := &pb.HistoricalLogBatch{
			MinuteTimestamp: logRef.MinuteTimestamp.Unix(),
			Content:         string(content),
			LineCount:       logRef.LineCount,
		}
		batches = append(batches, batch)
	}

	// Get min/max available times for time picker (by session)
	minTime, maxTime, err := h.logRefRepo.GetMinMaxTimesBySession(ctx, req.SessionId)
	if err != nil {
		return nil, fmt.Errorf("failed to get min/max times: %w", err)
	}

	resp := &pb.GetHistoricalLogsResponse{
		Batches: batches,
	}

	if minTime != nil {
		resp.EarliestAvailableTimestamp = minTime.Unix()
	}
	if maxTime != nil {
		resp.LatestAvailableTimestamp = maxTime.Unix()
	}

	return resp, nil
}

func parseS3Key(s3URL string) (string, error) {
	// Expected format: s3://bucket/key
	const prefix = "s3://"
	if len(s3URL) <= len(prefix) {
		return "", fmt.Errorf("invalid S3 URL: too short")
	}
	if s3URL[:len(prefix)] != prefix {
		return "", fmt.Errorf("invalid S3 URL: missing s3:// prefix")
	}

	// Remove prefix and find first slash after bucket name
	remainder := s3URL[len(prefix):]
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

	// Return everything after bucket name
	return remainder[slashIdx+1:], nil
}

func decompressLogs(compressed []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}
