package handlers

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manmanv2/models"
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
	// Validate time range (max 6 hours)
	maxDuration := int64(6 * 60 * 60) // 6 hours in seconds
	if req.EndTimestamp-req.StartTimestamp > maxDuration {
		return nil, fmt.Errorf("time range too large: maximum 6 hours allowed")
	}

	startTime := time.Unix(req.StartTimestamp, 0)
	endTime := time.Unix(req.EndTimestamp, 0)

	// Set defaults for pagination
	offset := req.Offset
	limit := req.Limit
	if limit == 0 {
		limit = 10000
	}

	// Query database for log references in range by session ID
	logRefs, err := h.logRefRepo.ListBySessionAndTimeRange(ctx, req.SessionId, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query log references: %w", err)
	}

	// Calculate total lines and apply pagination
	totalLines := int32(0)
	for _, ref := range logRefs {
		totalLines += ref.LineCount
	}

	// Download S3 objects and build batches with pagination
	batches := make([]*pb.HistoricalLogBatch, 0)
	currentOffset := int32(0)
	linesLoaded := int32(0)

	for _, logRef := range logRefs {
		// Skip batches before offset
		if currentOffset+logRef.LineCount <= offset {
			currentOffset += logRef.LineCount
			continue
		}

		// Stop if we've loaded enough lines
		if linesLoaded >= limit {
			break
		}

		// Parse S3 URL to extract key
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
		linesLoaded += logRef.LineCount
		currentOffset += logRef.LineCount
	}

	// Get min/max available times for time picker (by session)
	minTime, maxTime, err := h.logRefRepo.GetMinMaxTimesBySession(ctx, req.SessionId)
	if err != nil {
		return nil, fmt.Errorf("failed to get min/max times: %w", err)
	}

	resp := &pb.GetHistoricalLogsResponse{
		Batches:    batches,
		TotalLines: totalLines,
		HasMore:    currentOffset < totalLines,
	}

	if minTime != nil {
		resp.EarliestAvailableTimestamp = minTime.Unix()
	}
	if maxTime != nil {
		resp.LatestAvailableTimestamp = maxTime.Unix()
	}

	return resp, nil
}

func (h *LogsHandler) GetLogHistogram(ctx context.Context, req *pb.GetLogHistogramRequest) (*pb.GetLogHistogramResponse, error) {
	// Get session time range
	minTime, maxTime, err := h.logRefRepo.GetMinMaxTimesBySession(ctx, req.SessionId)
	if err != nil {
		return nil, fmt.Errorf("failed to get session time range: %w", err)
	}

	if minTime == nil || maxTime == nil {
		// No logs for this session
		return &pb.GetLogHistogramResponse{
			Buckets:     []*pb.HistogramBucket{},
			Granularity: "1m",
		}, nil
	}

	// Calculate session duration and determine bucket size
	duration := maxTime.Sub(*minTime)
	var bucketSeconds int64
	var granularity string

	if duration < time.Hour {
		// < 1 hour: 1-minute buckets
		bucketSeconds = 60
		granularity = "1m"
	} else if duration < 24*time.Hour {
		// 1-24 hours: 5-minute buckets
		bucketSeconds = 5 * 60
		granularity = "5m"
	} else {
		// > 24 hours: 1-hour buckets
		bucketSeconds = 60 * 60
		granularity = "1h"
	}

	// Cap at 1000 buckets - adjust granularity if needed
	maxBuckets := int64(1000)
	estimatedBuckets := int64(duration.Seconds()) / bucketSeconds
	if estimatedBuckets > maxBuckets {
		bucketSeconds = int64(duration.Seconds()) / maxBuckets
		if bucketSeconds < 1 {
			bucketSeconds = 1
		}
		granularity = fmt.Sprintf("%ds", bucketSeconds)
	}

	// Safety check: ensure bucketSeconds is at least 1
	if bucketSeconds < 1 {
		bucketSeconds = 1
	}

	// Get histogram data from repository
	histogramData, err := h.logRefRepo.GetHistogramBySession(ctx, req.SessionId, bucketSeconds)
	if err != nil {
		return nil, fmt.Errorf("failed to get histogram data: %w", err)
	}

	// Convert to protobuf format
	buckets := make([]*pb.HistogramBucket, 0, len(histogramData))
	for timestamp, sources := range histogramData {
		bucket := &pb.HistogramBucket{
			Timestamp:   timestamp,
			StdoutLines: sources["stdout"],
			StderrLines: sources["stderr"],
			HostLines:   sources["host"],
		}
		buckets = append(buckets, bucket)
	}

	// Sort buckets by timestamp
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].Timestamp < buckets[j].Timestamp
	})

	return &pb.GetLogHistogramResponse{
		Buckets:      buckets,
		Granularity:  granularity,
		SessionStart: minTime.Unix(),
		SessionEnd:   maxTime.Unix(),
	}, nil
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
