package workshop

import (
	"context"

	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetBatchJob retrieves a batch job header plus its per-item outcomes,
// ordered by display_order so the view shows entries in the order they were
// pasted (#2179, plan #2175, FR4).
func (h *WorkshopServiceHandler) GetBatchJob(ctx context.Context, req *pb.GetBatchJobRequest) (*pb.GetBatchJobResponse, error) {
	if req.BatchJobId == 0 {
		return nil, status.Error(codes.InvalidArgument, "batch_job_id is required")
	}

	job, err := h.batchJobRepo.GetBatchJob(ctx, req.BatchJobId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "batch job %d not found: %v", req.BatchJobId, err)
	}

	items, err := h.batchJobRepo.ListBatchJobItems(ctx, req.BatchJobId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list batch job items: %v", err)
	}

	pbItems := make([]*pb.BatchItemResult, len(items))
	for i, item := range items {
		pbItems[i] = batchJobItemToProto(item)
	}

	return &pb.GetBatchJobResponse{
		Job:   batchJobToProto(job),
		Items: pbItems,
	}, nil
}

// ListBatchJobs returns the most recent batch jobs for a game, newest first.
func (h *WorkshopServiceHandler) ListBatchJobs(ctx context.Context, req *pb.ListBatchJobsRequest) (*pb.ListBatchJobsResponse, error) {
	if req.GameId == 0 {
		return nil, status.Error(codes.InvalidArgument, "game_id is required")
	}

	limit := int(req.Limit)

	jobs, err := h.batchJobRepo.ListBatchJobs(ctx, req.GameId, limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list batch jobs: %v", err)
	}

	pbJobs := make([]*pb.WorkshopBatchJob, len(jobs))
	for i, job := range jobs {
		pbJobs[i] = batchJobToProto(job)
	}

	return &pb.ListBatchJobsResponse{
		Jobs: pbJobs,
	}, nil
}

// batchJobToProto converts a WorkshopBatchJob model to protobuf.
func batchJobToProto(job *manman.WorkshopBatchJob) *pb.WorkshopBatchJob {
	pbJob := &pb.WorkshopBatchJob{
		BatchJobId:     job.BatchJobID,
		JobType:        job.JobType,
		GameId:         job.GameID,
		Status:         job.Status,
		TotalItems:     int32(job.TotalItems),
		SucceededItems: int32(job.SucceededItems),
		FailedItems:    int32(job.FailedItems),
		CreatedAt:      job.CreatedAt.Unix(),
		UpdatedAt:      job.UpdatedAt.Unix(),
	}

	if job.LibraryID != nil {
		pbJob.LibraryId = *job.LibraryID
	}
	if job.SourceInput != nil {
		pbJob.SourceInput = *job.SourceInput
	}

	return pbJob
}

// batchJobItemToProto converts a WorkshopBatchJobItem model to the shared
// BatchItemResult message (defined in workshop.proto for #2179; #2177 should
// reuse it rather than defining a second copy).
func batchJobItemToProto(item *manman.WorkshopBatchJobItem) *pb.BatchItemResult {
	pbItem := &pb.BatchItemResult{
		RawInput: item.RawInput,
		Status:   item.Status,
	}

	if item.WorkshopID != nil {
		pbItem.WorkshopId = *item.WorkshopID
	}
	if item.AddonID != nil {
		pbItem.AddonId = *item.AddonID
	}
	if item.ErrorMessage != nil {
		pbItem.ErrorMessage = *item.ErrorMessage
	}

	return pbItem
}
