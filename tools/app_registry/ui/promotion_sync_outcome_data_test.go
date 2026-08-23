package main

import (
	"context"
	"errors"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// TestFetchPromotionSyncOutcomes_DedupesRepeatedIDs proves distinct
// promotion_ids are looked up exactly once each, even when the caller's
// input slice repeats one -- mirroring fetchArtifactsByID's own dedup-by-
// distinct-id contract (artifact_lookup.go).
func TestFetchPromotionSyncOutcomes_DedupesRepeatedIDs(t *testing.T) {
	promo := &fakePromotionClient{}
	app := newPromotionDetailsTestApp(promo)

	results := app.fetchPromotionSyncOutcomes(context.Background(), []string{"promo-1", "promo-1", "promo-2", "promo-1", ""})

	if got := promo.getDetailsCallCount(); got != 2 {
		t.Fatalf("GetPromotionDetails calls = %d, want 2 (one per distinct non-empty id)", got)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d entries, want 2", len(results))
	}
	if _, ok := results["promo-1"]; !ok {
		t.Error("expected an entry for promo-1")
	}
	if _, ok := results["promo-2"]; !ok {
		t.Error("expected an entry for promo-2")
	}
	if _, ok := results[""]; ok {
		t.Error("an empty promotion_id must never be looked up or appear in the results")
	}
}

// TestFetchPromotionSyncOutcomes_ConcurrentFanout_AllSucceed proves every
// distinct id resolves to its own PromotionDetails when every lookup
// succeeds -- the concurrent-fanout happy path.
func TestFetchPromotionSyncOutcomes_ConcurrentFanout_AllSucceed(t *testing.T) {
	promo := &fakePromotionClient{getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
		return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
			Promotion: &pb.Promotion{PromotionId: in.GetPromotionId()},
			ToVersion: "v-" + in.GetPromotionId(),
		}}, nil
	}}
	app := newPromotionDetailsTestApp(promo)

	ids := []string{"promo-1", "promo-2", "promo-3", "promo-4", "promo-5"}
	results := app.fetchPromotionSyncOutcomes(context.Background(), ids)

	if len(results) != len(ids) {
		t.Fatalf("results = %d entries, want %d", len(results), len(ids))
	}
	for _, id := range ids {
		d, ok := results[id]
		if !ok {
			t.Errorf("missing result for %s", id)
			continue
		}
		if d.GetToVersion() != "v-"+id {
			t.Errorf("result for %s: ToVersion = %q, want %q (each id must resolve to its OWN response, not one shared/raced value)", id, d.GetToVersion(), "v-"+id)
		}
	}
}

// TestFetchPromotionSyncOutcomes_PartialFailure_BestEffort proves a failing
// lookup for one promotion_id never fails the whole batch -- the failed id
// is simply absent from the returned map, matching
// fetchArtifactsByID's exact "missing/failed lookup is simply absent"
// contract (artifact_lookup.go's doc comment).
func TestFetchPromotionSyncOutcomes_PartialFailure_BestEffort(t *testing.T) {
	promo := &fakePromotionClient{getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
		if in.GetPromotionId() == "promo-bad" {
			return nil, errors.New("simulated transport failure")
		}
		return &pb.GetPromotionDetailsResponse{Details: &pb.PromotionDetails{
			Promotion: &pb.Promotion{PromotionId: in.GetPromotionId()},
		}}, nil
	}}
	app := newPromotionDetailsTestApp(promo)

	results := app.fetchPromotionSyncOutcomes(context.Background(), []string{"promo-good", "promo-bad"})

	if len(results) != 1 {
		t.Fatalf("results = %d entries, want 1 (only the successful lookup)", len(results))
	}
	if _, ok := results["promo-good"]; !ok {
		t.Error("expected the successful lookup's result to be present")
	}
	if _, ok := results["promo-bad"]; ok {
		t.Error("a failed lookup must never appear in the results map")
	}
}

// TestFetchPromotionSyncOutcomes_NilDetails_TreatedAsMissing proves a
// response with a nil Details field (technically a successful RPC, but no
// usable data) is also treated as a missing/best-effort-absent result, not
// a nil-pointer panic downstream in promotionTimelineCard.
func TestFetchPromotionSyncOutcomes_NilDetails_TreatedAsMissing(t *testing.T) {
	promo := &fakePromotionClient{getDetailsFunc: func(in *pb.GetPromotionDetailsRequest) (*pb.GetPromotionDetailsResponse, error) {
		return &pb.GetPromotionDetailsResponse{}, nil // Details left nil
	}}
	app := newPromotionDetailsTestApp(promo)

	results := app.fetchPromotionSyncOutcomes(context.Background(), []string{"promo-1"})

	if len(results) != 0 {
		t.Fatalf("results = %d entries, want 0 (nil Details must not populate the map)", len(results))
	}
}

// TestFetchPromotionSyncOutcomes_EmptyInput_NoCallsMadeAndEmptyMap proves
// an empty input never issues any RPC and returns a non-nil, empty map
// (never nil -- promotionTimelineCard indexes it unconditionally).
func TestFetchPromotionSyncOutcomes_EmptyInput_NoCallsMadeAndEmptyMap(t *testing.T) {
	promo := &fakePromotionClient{}
	app := newPromotionDetailsTestApp(promo)

	results := app.fetchPromotionSyncOutcomes(context.Background(), nil)

	if results == nil {
		t.Fatal("expected a non-nil (empty) map for empty input")
	}
	if len(results) != 0 {
		t.Fatalf("results = %d entries, want 0", len(results))
	}
	if promo.getDetailsCallCount() != 0 {
		t.Errorf("GetPromotionDetails calls = %d, want 0 for empty input", promo.getDetailsCallCount())
	}
}
