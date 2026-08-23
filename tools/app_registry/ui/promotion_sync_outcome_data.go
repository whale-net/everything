package main

import (
	"context"
	"sync"
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// promotionSyncOutcomeFanoutTimeout bounds the concurrent per-promotion-id
// GetPromotionDetails calls fetchPromotionSyncOutcomes fans out -- same
// rationale as environmentStateFanoutTimeout (deployments_data.go) and
// artifactLookupFanoutTimeout (artifact_lookup.go): one slow lookup must
// not hang the whole screen, and a per-id failure is best-effort, never
// fatal to the page.
const promotionSyncOutcomeFanoutTimeout = 8 * time.Second

// fetchPromotionSyncOutcomes resolves a set of promotion ids to their
// PromotionDetails, for Story 3's inline sync/health badge on the
// promotion timeline (issue #1032). There is no batch "get sync outcome
// for N promotions" RPC (widening ListPromotionEvents to carry this data
// itself would touch every other consumer of that RPC -- CLI, any future
// screen -- for a badge only this one timeline needs today), so this fans
// out one GetPromotionDetails call per distinct promotion_id, concurrently
// -- mirroring fetchArtifactsByID's exact pattern (artifact_lookup.go).
// This is the documented performance trade-off issue #1032 explicitly
// permits deferring a general solution for: fine for a timeline showing
// one app's own promotion history (bounded, typically small), not
// something this change generalizes to an unbounded list view.
//
// A failed or missing lookup is simply absent from the returned map --
// never fatal to the caller's page, and the badge is simply omitted for
// that row (see promotionTimelineCard) rather than rendered "Unknown".
func (app *App) fetchPromotionSyncOutcomes(ctx context.Context, promotionIDs []string) map[string]*pb.PromotionDetails {
	fetchCtx, cancel := context.WithTimeout(ctx, promotionSyncOutcomeFanoutTimeout)
	defer cancel()

	unique := make(map[string]bool, len(promotionIDs))
	for _, id := range promotionIDs {
		if id != "" {
			unique[id] = true
		}
	}

	results := make(map[string]*pb.PromotionDetails, len(unique))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for id := range unique {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := app.registry.Promotion.GetPromotionDetails(fetchCtx, &pb.GetPromotionDetailsRequest{PromotionId: id})
			if err != nil || resp.GetDetails() == nil {
				return
			}
			mu.Lock()
			results[id] = resp.GetDetails()
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}
