package main

import (
	"context"
	"sync"
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// artifactLookupFanoutTimeout bounds the concurrent per-artifact-id
// GetArtifact calls fetchArtifactsByID fans out — the same rationale as
// environmentStateFanoutTimeout in deployments_data.go: one slow lookup
// must not hang the whole screen, and a per-id failure is best-effort (see
// fetchArtifactsByID's doc comment), never fatal to the page.
const artifactLookupFanoutTimeout = 8 * time.Second

// fetchArtifactsByID resolves a set of artifact ids to their full Artifact
// (and, where recorded, owning Build) records. There is no batch "get
// artifacts by id" RPC, so this fans out one GetArtifact call per distinct
// id, concurrently. Used by:
//   - app version history (screen 13): resolving each listed artifact's
//     owning Build for the "Built from" column — ListArtifacts itself
//     returns Artifact rows only, no Build.
//   - chart detail (screen 22): resolving each pinned image's Provenance —
//     Artifact.contains (ArtifactLink) carries a digest and artifact_id but
//     not provenance, and NFR-11/FR-33 require provenance be tagged
//     wherever an artifact renders, not only on an audit screen.
//
// A failed or missing lookup is simply absent from the returned map — never
// fatal to the caller's page (this is enrichment, not the artifact's own
// identity, which the caller already has from its own primary read).
func (app *App) fetchArtifactsByID(ctx context.Context, ids []string) map[string]*pb.GetArtifactResponse {
	fetchCtx, cancel := context.WithTimeout(ctx, artifactLookupFanoutTimeout)
	defer cancel()

	unique := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			unique[id] = true
		}
	}

	results := make(map[string]*pb.GetArtifactResponse, len(unique))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for id := range unique {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := app.registry.Artifact.GetArtifact(fetchCtx, &pb.GetArtifactRequest{ArtifactId: id})
			if err != nil || resp.GetArtifact() == nil {
				return
			}
			mu.Lock()
			results[id] = resp
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}
