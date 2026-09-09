package workshop

import (
	"context"

	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BatchCreateAddons takes a pasted block of mixed raw Workshop IDs and
// Workshop URLs and creates an addon per valid entry, committing valid
// entries even when some entries are invalid (FR2, FR3). Validates the
// request's game/library/preset references, then delegates all per-item
// work -- including partial-failure handling (NFR5) -- to
// WorkshopManager.BatchCreateAddons.
func (h *WorkshopServiceHandler) BatchCreateAddons(ctx context.Context, req *pb.BatchCreateAddonsRequest) (*pb.BatchCreateAddonsResponse, error) {
	if req.GameId == 0 {
		return nil, status.Error(codes.InvalidArgument, "game_id is required")
	}
	if req.Entries == "" {
		return nil, status.Error(codes.InvalidArgument, "entries is required")
	}
	if req.LibraryId != 0 {
		if _, err := h.libraryRepo.Get(ctx, req.LibraryId); err != nil {
			return nil, status.Errorf(codes.NotFound, "library %d not found: %v", req.LibraryId, err)
		}
	}
	if req.PresetId != 0 {
		if _, err := h.presetRepo.Get(ctx, req.PresetId); err != nil {
			return nil, status.Errorf(codes.NotFound, "preset %d not found: %v", req.PresetId, err)
		}
	}

	job, items, err := h.workshopManager.BatchCreateAddons(ctx, req.GameId, req.LibraryId, req.Entries, req.PresetId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to run batch create: %v", err)
	}

	results := make([]*pb.BatchItemResult, len(items))
	for i, item := range items {
		results[i] = batchItemResultToProto(item)
	}

	return &pb.BatchCreateAddonsResponse{
		BatchJobId:     job.BatchJobID,
		TotalItems:     int32(job.TotalItems),
		SucceededItems: int32(job.SucceededItems),
		FailedItems:    int32(job.FailedItems),
		Results:        results,
	}, nil
}

// batchItemResultToProto converts a WorkshopBatchJobItem model to the
// BatchItemResult message shared across the batch-create, collection-add,
// and batch-status RPCs.
func batchItemResultToProto(item *manman.WorkshopBatchJobItem) *pb.BatchItemResult {
	result := &pb.BatchItemResult{
		RawInput: item.RawInput,
		Status:   item.Status,
	}
	if item.WorkshopID != nil {
		result.WorkshopId = *item.WorkshopID
	}
	if item.AddonID != nil {
		result.AddonId = *item.AddonID
	}
	if item.ErrorMessage != nil {
		result.ErrorMessage = *item.ErrorMessage
	}
	return result
}
