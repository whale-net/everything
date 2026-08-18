package handlers

import (
	"testing"
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file covers issue #603: ListPromotionEvents, ListArtifacts, and
// ListPromotions accept a PageRequest but historically never enforced it --
// no LIMIT, no cursor, an unbounded query every time. These tests mirror
// list_builds_test.go's pagination coverage (issue #608) for the three RPCs
// that gained real (LIMIT + keyset cursor) pagination here.

// ----------------------------------------------------------------------
// ListArtifacts
// ----------------------------------------------------------------------

func seedArtifacts(repo *fake.Registry, n int) []repository.Artifact {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]repository.Artifact, n)
	for i := 0; i < n; i++ {
		out[i] = repo.SeedArtifact(repository.Artifact{
			Kind:           repository.ArtifactKindImage,
			Repository:     "ghcr.io/acme/pagination-test",
			Version:        "v1.0.0",
			State:          repository.ArtifactStatePublished,
			StateChangedAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	return out
}

func TestListArtifacts_NoFilter_OrderedMostRecentFirst(t *testing.T) {
	repo := fake.New()
	srv := NewArtifactServer(repo)
	ctx := authedCtx()
	seeded := seedArtifacts(repo, 5)

	resp, err := srv.ListArtifacts(ctx, &pb.ListArtifactsRequest{})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(resp.Artifacts) != len(seeded) {
		t.Fatalf("expected %d artifacts, got %d", len(seeded), len(resp.Artifacts))
	}
	for i, a := range resp.Artifacts {
		want := seeded[len(seeded)-1-i] // reverse of insertion order (newest state_changed_at first)
		if a.ArtifactId != want.ArtifactID {
			t.Fatalf("position %d: expected %s, got %s", i, want.ArtifactID, a.ArtifactId)
		}
	}
	if resp.Page.GetNextPageToken() != "" {
		t.Fatalf("expected no next_page_token for a full unpaged result, got %q", resp.Page.GetNextPageToken())
	}
}

func TestListArtifacts_Pagination_NoOverlapNoGap(t *testing.T) {
	repo := fake.New()
	srv := NewArtifactServer(repo)
	ctx := authedCtx()
	seedArtifacts(repo, 7)

	full, err := srv.ListArtifacts(ctx, &pb.ListArtifactsRequest{})
	if err != nil {
		t.Fatalf("ListArtifacts (full): %v", err)
	}

	var walked []*pb.Artifact
	token := ""
	for page := 0; ; page++ {
		if page > len(full.Artifacts) {
			t.Fatalf("paged more times than there are rows -- next_page_token never went empty")
		}
		resp, err := srv.ListArtifacts(ctx, &pb.ListArtifactsRequest{
			Page: &pb.PageRequest{PageSize: 3, PageToken: token},
		})
		if err != nil {
			t.Fatalf("ListArtifacts (page %d): %v", page, err)
		}
		if len(resp.Artifacts) == 0 {
			t.Fatalf("page %d: got 0 rows", page)
		}
		if len(walked) < len(full.Artifacts) && len(resp.Artifacts) > 3 {
			t.Fatalf("page %d: expected at most page_size=3 rows, got %d", page, len(resp.Artifacts))
		}
		walked = append(walked, resp.Artifacts...)
		token = resp.Page.GetNextPageToken()
		if token == "" {
			break
		}
	}

	if len(walked) != len(full.Artifacts) {
		t.Fatalf("expected %d total rows across all pages, got %d", len(full.Artifacts), len(walked))
	}
	seen := map[string]bool{}
	for i, a := range walked {
		if a.ArtifactId != full.Artifacts[i].ArtifactId {
			t.Fatalf("position %d: paged result %s does not match unfiltered full-list result %s",
				i, a.ArtifactId, full.Artifacts[i].ArtifactId)
		}
		if seen[a.ArtifactId] {
			t.Fatalf("duplicate row %s across pages", a.ArtifactId)
		}
		seen[a.ArtifactId] = true
	}
}

func TestListArtifacts_InvalidPageToken(t *testing.T) {
	repo := fake.New()
	srv := NewArtifactServer(repo)
	ctx := authedCtx()
	seedArtifacts(repo, 3)

	_, err := srv.ListArtifacts(ctx, &pb.ListArtifactsRequest{
		Page: &pb.PageRequest{PageToken: "not-a-real-cursor"},
	})
	if err == nil {
		t.Fatalf("expected an error for a garbage page_token, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
}

// TestListArtifacts_PromotableOnly_ComposesWithPagination proves
// PromotableOnly is now enforced in the underlying query (not filtered
// client-side after the page's LIMIT) -- a full page of promotable-only
// results must still be reachable even when non-promotable rows sit between
// them in state_changed_at order.
//
// Promotability is derived live (issue #833) from the owning app's
// deploy_unit -- SeedArtifact can no longer set it directly (that field is
// overwritten by ListArtifacts' live derivation before the PromotableOnly
// filter runs, same as every other read path). Artifacts are seeded against
// two real apps instead: "image-app" (DEPLOY_UNIT_IMAGE -> PROMOTABLE) and
// "none-app" (DEPLOY_UNIT_NONE -> NOT_PROMOTABLE).
func TestListArtifacts_PromotableOnly_ComposesWithPagination(t *testing.T) {
	repo := fake.New()
	appSrv := NewAppServer(repo)
	srv := NewArtifactServer(repo)
	ctx := authedCtx()

	reconcile, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet([]*appmetapb.AppManifest{
			{Domain: "demo", Name: "image-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
			{Domain: "demo", Name: "none-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE},
		}, nil),
		IdempotencyKey: "promotable-only-setup",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	apps := map[string]string{}
	for _, a := range reconcile.CreatedApps {
		apps[a.Name] = a.AppId
	}

	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	var wantIDs []string
	for i := 0; i < 6; i++ {
		ownerAppID := apps["none-app"]
		wantPromotable := false
		if i%2 == 0 {
			ownerAppID = apps["image-app"]
			wantPromotable = true
		}
		a := repo.SeedArtifact(repository.Artifact{
			Kind:           repository.ArtifactKindImage,
			AppID:          ownerAppID,
			Repository:     "ghcr.io/acme/promotable-test",
			Version:        "v1.0.0",
			State:          repository.ArtifactStatePublished,
			StateChangedAt: base.Add(time.Duration(i) * time.Second),
		})
		if wantPromotable {
			wantIDs = append(wantIDs, a.ArtifactID)
		}
	}

	resp, err := srv.ListArtifacts(ctx, &pb.ListArtifactsRequest{
		PromotableOnly: true,
		Page:           &pb.PageRequest{PageSize: 2},
	})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(resp.Artifacts) != 2 {
		t.Fatalf("expected exactly page_size=2 promotable rows, got %d: %+v", len(resp.Artifacts), resp.Artifacts)
	}
	if resp.Page.GetNextPageToken() == "" {
		t.Fatalf("expected a next_page_token: only 2 of %d promotable rows were returned", len(wantIDs))
	}
	for _, a := range resp.Artifacts {
		found := false
		for _, id := range wantIDs {
			if a.ArtifactId == id {
				found = true
			}
		}
		if !found {
			t.Fatalf("returned non-promotable artifact %s", a.ArtifactId)
		}
	}
}

// ----------------------------------------------------------------------
// ListPromotions
// ----------------------------------------------------------------------

func seedPromotions(repo *fake.Registry, n int) []repository.Promotion {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]repository.Promotion, n)
	for i := 0; i < n; i++ {
		out[i] = repo.SeedPromotion(repository.Promotion{
			EnvironmentID:  "env-dev",
			EnvironmentKey: "dev",
			TargetKey:      "image:acme-pagination-test",
			Kind:           repository.ArtifactKindImage,
			State:          repository.PromotionStateActive,
			ValidFrom:      base.Add(time.Duration(i) * time.Second),
		})
	}
	return out
}

func TestListPromotions_Pagination_NoOverlapNoGap(t *testing.T) {
	repo := fake.New()
	srv := NewPromotionServer(repo)
	ctx := authedCtx()
	seedPromotions(repo, 7)

	full, err := srv.ListPromotions(ctx, &pb.ListPromotionsRequest{IncludeHistory: true})
	if err != nil {
		t.Fatalf("ListPromotions (full): %v", err)
	}

	var walked []*pb.Promotion
	token := ""
	for page := 0; ; page++ {
		if page > len(full.Promotions) {
			t.Fatalf("paged more times than there are rows -- next_page_token never went empty")
		}
		resp, err := srv.ListPromotions(ctx, &pb.ListPromotionsRequest{
			IncludeHistory: true,
			Page:           &pb.PageRequest{PageSize: 3, PageToken: token},
		})
		if err != nil {
			t.Fatalf("ListPromotions (page %d): %v", page, err)
		}
		if len(resp.Promotions) == 0 {
			t.Fatalf("page %d: got 0 rows", page)
		}
		if len(walked) < len(full.Promotions) && len(resp.Promotions) > 3 {
			t.Fatalf("page %d: expected at most page_size=3 rows, got %d", page, len(resp.Promotions))
		}
		walked = append(walked, resp.Promotions...)
		token = resp.Page.GetNextPageToken()
		if token == "" {
			break
		}
	}

	if len(walked) != len(full.Promotions) {
		t.Fatalf("expected %d total rows across all pages, got %d", len(full.Promotions), len(walked))
	}
	seen := map[string]bool{}
	for i, p := range walked {
		if p.PromotionId != full.Promotions[i].PromotionId {
			t.Fatalf("position %d: paged result %s does not match unfiltered full-list result %s",
				i, p.PromotionId, full.Promotions[i].PromotionId)
		}
		if seen[p.PromotionId] {
			t.Fatalf("duplicate row %s across pages", p.PromotionId)
		}
		seen[p.PromotionId] = true
	}
}

func TestListPromotions_InvalidPageToken(t *testing.T) {
	repo := fake.New()
	srv := NewPromotionServer(repo)
	ctx := authedCtx()
	seedPromotions(repo, 3)

	_, err := srv.ListPromotions(ctx, &pb.ListPromotionsRequest{
		IncludeHistory: true,
		Page:           &pb.PageRequest{PageToken: "not-a-real-cursor"},
	})
	if err == nil {
		t.Fatalf("expected an error for a garbage page_token, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
}

// ----------------------------------------------------------------------
// ListPromotionEvents
// ----------------------------------------------------------------------

func seedPromotionEvents(repo *fake.Registry, n int) []repository.PromotionEvent {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]repository.PromotionEvent, n)
	for i := 0; i < n; i++ {
		out[i] = repo.SeedPromotionEvent(repository.PromotionEvent{
			PromotionID: "promotion-pagination-test",
			Action:      repository.PromotionActionPromote,
			Actor:       "test-actor",
			OccurredAt:  base.Add(time.Duration(i) * time.Second),
		})
	}
	return out
}

func TestListPromotionEvents_Pagination_NoOverlapNoGap(t *testing.T) {
	repo := fake.New()
	srv := NewPromotionServer(repo)
	ctx := authedCtx()
	seedPromotionEvents(repo, 7)

	full, err := srv.ListPromotionEvents(ctx, &pb.ListPromotionEventsRequest{})
	if err != nil {
		t.Fatalf("ListPromotionEvents (full): %v", err)
	}

	var walked []*pb.PromotionEvent
	token := ""
	for page := 0; ; page++ {
		if page > len(full.Events) {
			t.Fatalf("paged more times than there are rows -- next_page_token never went empty")
		}
		resp, err := srv.ListPromotionEvents(ctx, &pb.ListPromotionEventsRequest{
			Page: &pb.PageRequest{PageSize: 3, PageToken: token},
		})
		if err != nil {
			t.Fatalf("ListPromotionEvents (page %d): %v", page, err)
		}
		if len(resp.Events) == 0 {
			t.Fatalf("page %d: got 0 rows", page)
		}
		if len(walked) < len(full.Events) && len(resp.Events) > 3 {
			t.Fatalf("page %d: expected at most page_size=3 rows, got %d", page, len(resp.Events))
		}
		walked = append(walked, resp.Events...)
		token = resp.Page.GetNextPageToken()
		if token == "" {
			break
		}
	}

	if len(walked) != len(full.Events) {
		t.Fatalf("expected %d total rows across all pages, got %d", len(full.Events), len(walked))
	}
	seen := map[string]bool{}
	for i, e := range walked {
		if e.EventId != full.Events[i].EventId {
			t.Fatalf("position %d: paged result %s does not match unfiltered full-list result %s",
				i, e.EventId, full.Events[i].EventId)
		}
		if seen[e.EventId] {
			t.Fatalf("duplicate row %s across pages", e.EventId)
		}
		seen[e.EventId] = true
	}
}

func TestListPromotionEvents_InvalidPageToken(t *testing.T) {
	repo := fake.New()
	srv := NewPromotionServer(repo)
	ctx := authedCtx()
	seedPromotionEvents(repo, 3)

	_, err := srv.ListPromotionEvents(ctx, &pb.ListPromotionEventsRequest{
		Page: &pb.PageRequest{PageToken: "not-a-real-cursor"},
	})
	if err == nil {
		t.Fatalf("expected an error for a garbage page_token, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
}
