package handlers

import (
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"testing"
)

// seedReconcileRuns seeds n rows directly against the fake, one second apart
// starting at a fixed base time, IDs "run-0".."run-{n-1}" -- distinct
// AppliedAt values chosen deliberately (rather than driving this through
// ReconcileApps, whose timestamps come from time.Now() and could collide or
// land sub-second apart depending on how fast the test runs) so ordering and
// since-filter assertions below are exact and non-flaky.
func seedReconcileRuns(repo *fake.Registry, n int) []repository.ReconcileRun {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runs := make([]repository.ReconcileRun, n)
	for i := 0; i < n; i++ {
		run := repository.ReconcileRun{
			ReconcileRunID:    reconcileRunTestID(i),
			GitSHA:            "sha",
			SourceCommittedAt: base.Add(time.Duration(i) * time.Second).Unix(),
			AppliedAt:         base.Add(time.Duration(i) * time.Second),
			AppsSeen:          int32(i),
			ChartsSeen:        int32(i),
		}
		repo.SeedReconcileRun(run)
		runs[i] = run
	}
	return runs
}

func reconcileRunTestID(i int) string {
	// Zero-padded so lexical and numeric ordering agree, in case a test
	// prints IDs for debugging -- not load-bearing for the ordering under
	// test itself, which is entirely driven by AppliedAt (and
	// ReconcileRunID only as the tie-break).
	return "run-" + string(rune('a'+i))
}

// TestListReconcileRuns_NoFilter_OrderedMostRecentFirst covers FR1.1/FR1.6:
// with no filter or paging, every recorded run comes back, most-recent
// applied_at first.
func TestListReconcileRuns_NoFilter_OrderedMostRecentFirst(t *testing.T) {
	repo := fake.New()
	srv := NewAppServer(repo)
	ctx := authedCtx()
	seeded := seedReconcileRuns(repo, 5) // run-a .. run-e, oldest to newest

	resp, err := srv.ListReconcileRuns(ctx, &pb.ListReconcileRunsRequest{})
	if err != nil {
		t.Fatalf("ListReconcileRuns: %v", err)
	}
	if len(resp.ReconcileRuns) != len(seeded) {
		t.Fatalf("expected %d runs, got %d", len(seeded), len(resp.ReconcileRuns))
	}
	for i, rr := range resp.ReconcileRuns {
		want := seeded[len(seeded)-1-i] // reverse of insertion order (newest first)
		if rr.ReconcileRunId != want.ReconcileRunID {
			t.Fatalf("position %d: expected %s, got %s", i, want.ReconcileRunID, rr.ReconcileRunId)
		}
	}
	if resp.Page.GetNextPageToken() != "" {
		t.Fatalf("expected no next_page_token for a full unpaged result, got %q", resp.Page.GetNextPageToken())
	}
}

// TestListReconcileRuns_SinceFilter covers FR1.2: since excludes runs
// strictly before it and includes a run applied at exactly since.
func TestListReconcileRuns_SinceFilter(t *testing.T) {
	repo := fake.New()
	srv := NewAppServer(repo)
	ctx := authedCtx()
	seeded := seedReconcileRuns(repo, 5) // run-a (oldest) .. run-e (newest)

	// seeded[2] ("run-c") is the boundary: since == its AppliedAt should
	// include it, and exclude run-a/run-b before it.
	since := seeded[2].AppliedAt

	resp, err := srv.ListReconcileRuns(ctx, &pb.ListReconcileRunsRequest{Since: since.Unix()})
	if err != nil {
		t.Fatalf("ListReconcileRuns: %v", err)
	}
	if len(resp.ReconcileRuns) != 3 {
		t.Fatalf("expected 3 runs (run-c, run-d, run-e), got %d: %+v", len(resp.ReconcileRuns), resp.ReconcileRuns)
	}
	wantIDs := []string{seeded[4].ReconcileRunID, seeded[3].ReconcileRunID, seeded[2].ReconcileRunID}
	for i, rr := range resp.ReconcileRuns {
		if rr.ReconcileRunId != wantIDs[i] {
			t.Fatalf("position %d: expected %s, got %s", i, wantIDs[i], rr.ReconcileRunId)
		}
	}
}

// TestListReconcileRuns_Pagination_NoOverlapNoGap covers FR1.1/NFR2: a
// page_size smaller than the total returns exactly that many rows and a
// non-empty next_page_token; concatenating every page, in order, reproduces
// the unfiltered full-list result exactly, and the last page's
// next_page_token is empty.
func TestListReconcileRuns_Pagination_NoOverlapNoGap(t *testing.T) {
	repo := fake.New()
	srv := NewAppServer(repo)
	ctx := authedCtx()
	seedReconcileRuns(repo, 7)

	full, err := srv.ListReconcileRuns(ctx, &pb.ListReconcileRunsRequest{})
	if err != nil {
		t.Fatalf("ListReconcileRuns (full): %v", err)
	}

	var walked []*pb.ReconcileRun
	token := ""
	for page := 0; ; page++ {
		if page > len(full.ReconcileRuns) {
			t.Fatalf("paged more times than there are rows -- next_page_token never went empty")
		}
		resp, err := srv.ListReconcileRuns(ctx, &pb.ListReconcileRunsRequest{
			Page: &pb.PageRequest{PageSize: 3, PageToken: token},
		})
		if err != nil {
			t.Fatalf("ListReconcileRuns (page %d): %v", page, err)
		}
		if len(resp.ReconcileRuns) == 0 {
			t.Fatalf("page %d: got 0 rows", page)
		}
		if len(walked) < len(full.ReconcileRuns) && len(resp.ReconcileRuns) > 3 {
			t.Fatalf("page %d: expected at most page_size=3 rows, got %d", page, len(resp.ReconcileRuns))
		}
		walked = append(walked, resp.ReconcileRuns...)
		token = resp.Page.GetNextPageToken()
		if token == "" {
			break
		}
	}

	if len(walked) != len(full.ReconcileRuns) {
		t.Fatalf("expected %d total rows across all pages, got %d", len(full.ReconcileRuns), len(walked))
	}
	seen := map[string]bool{}
	for i, rr := range walked {
		if rr.ReconcileRunId != full.ReconcileRuns[i].ReconcileRunId {
			t.Fatalf("position %d: paged result %s does not match unfiltered full-list result %s",
				i, rr.ReconcileRunId, full.ReconcileRuns[i].ReconcileRunId)
		}
		if seen[rr.ReconcileRunId] {
			t.Fatalf("duplicate row %s across pages", rr.ReconcileRunId)
		}
		seen[rr.ReconcileRunId] = true
	}
}

// TestListReconcileRuns_InvalidPageToken covers the malformed-token case:
// a page_token this server never issued must fail with InvalidArgument, not
// panic or silently return a wrong page.
func TestListReconcileRuns_InvalidPageToken(t *testing.T) {
	repo := fake.New()
	srv := NewAppServer(repo)
	ctx := authedCtx()
	seedReconcileRuns(repo, 3)

	_, err := srv.ListReconcileRuns(ctx, &pb.ListReconcileRunsRequest{
		Page: &pb.PageRequest{PageToken: "not-a-real-cursor"},
	})
	if err == nil {
		t.Fatalf("expected an error for a garbage page_token, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
}

// TestListReconcileRuns_DuplicateAppliedAt_TieBreaksDeterministically is the
// case a naive `ORDER BY applied_at DESC LIMIT n` keyset implementation gets
// wrong: two runs sharing the exact same applied_at must still page
// deterministically, tie-broken by reconcile_run_id descending, with no
// duplication or omission across the page boundary.
func TestListReconcileRuns_DuplicateAppliedAt_TieBreaksDeterministically(t *testing.T) {
	repo := fake.New()
	srv := NewAppServer(repo)
	ctx := authedCtx()

	same := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// Deliberately seeded with the higher ID first, to prove ordering comes
	// from the tie-break logic and not insertion order.
	repo.SeedReconcileRun(repository.ReconcileRun{
		ReconcileRunID: "tie-b", GitSHA: "sha", AppliedAt: same, AppsSeen: 1, ChartsSeen: 1,
	})
	repo.SeedReconcileRun(repository.ReconcileRun{
		ReconcileRunID: "tie-a", GitSHA: "sha", AppliedAt: same, AppsSeen: 1, ChartsSeen: 1,
	})
	// An older, distinct-timestamp row so the tied pair isn't the entire
	// result set -- exercises the tie-break across a real page boundary.
	repo.SeedReconcileRun(repository.ReconcileRun{
		ReconcileRunID: "older", GitSHA: "sha", AppliedAt: same.Add(-time.Hour), AppsSeen: 1, ChartsSeen: 1,
	})

	var walked []string
	token := ""
	for page := 0; page < 5; page++ {
		resp, err := srv.ListReconcileRuns(ctx, &pb.ListReconcileRunsRequest{
			Page: &pb.PageRequest{PageSize: 1, PageToken: token},
		})
		if err != nil {
			t.Fatalf("ListReconcileRuns (page %d): %v", page, err)
		}
		if len(resp.ReconcileRuns) != 1 {
			t.Fatalf("page %d: expected exactly 1 row, got %d", page, len(resp.ReconcileRuns))
		}
		walked = append(walked, resp.ReconcileRuns[0].ReconcileRunId)
		token = resp.Page.GetNextPageToken()
		if token == "" {
			break
		}
	}

	want := []string{"tie-b", "tie-a", "older"}
	if len(walked) != len(want) {
		t.Fatalf("expected %d rows across pages, got %v", len(want), walked)
	}
	for i, id := range want {
		if walked[i] != id {
			t.Fatalf("position %d: expected %s, got %s (full order: %v)", i, id, walked[i], walked)
		}
	}
}
