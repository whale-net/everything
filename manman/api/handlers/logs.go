package handlers

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/api/repository"
	pb "github.com/whale-net/everything/manman/protos"
)

const logsBaseDir = "/var/lib/manman/logs" // Configurable

type LogsHandler struct {
	logRefRepo repository.LogReferenceRepository
}

func NewLogsHandler(logRefRepo repository.LogReferenceRepository) *LogsHandler {
	return &LogsHandler{
		logRefRepo: logRefRepo,
	}
}

func (h *LogsHandler) SendBatchedLogs(ctx context.Context, req *pb.SendBatchedLogsRequest) (*pb.SendBatchedLogsResponse, error) {
	accepted := int32(0)
	failed := []string{}

	for _, batch := range req.Batches {
		// 1. Decompress logs
		logs, err := decompressLogs(batch.CompressedLogs)
		if err != nil {
			failed = append(failed, batch.BatchId)
			continue
		}

		// 2. Write to local file (stub for S3)
		filePath := filepath.Join(
			logsBaseDir,
			fmt.Sprintf("%d", batch.SessionId),
			fmt.Sprintf("%d-%s.log", batch.StartTimestamp, batch.BatchId),
		)

		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			failed = append(failed, batch.BatchId)
			continue
		}

		if err := os.WriteFile(filePath, logs, 0644); err != nil {
			failed = append(failed, batch.BatchId)
			continue
		}

		// 3. Store reference in database
		logRef := &manman.LogReference{
			SessionID: batch.SessionId,
			FilePath:  filePath, // Local path (not S3 URL)
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
