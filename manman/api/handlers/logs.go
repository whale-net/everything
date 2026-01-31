package handlers

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/api/repository"
	pb "github.com/whale-net/everything/manman/protos"
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

func decompressLogs(compressed []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}
