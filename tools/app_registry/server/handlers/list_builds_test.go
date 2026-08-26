package handlers

import (
	"testing"
	"time"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// seedBuilds seeds n builds directly against the fake, one second apart
// starting at a fixed base time, workflow_run_id "run-0".."run-{n-1}" --
// distinct RecordedAt values chosen deliberately (rather than driving this
// through RecordBuild, whose real write path -- both the fake's and
// postgres's -- stamps RecordedAt from the clock, see fake.Registry.SeedBuild's
// doc comment) so ordering and since-filter assertions below are exact and
// non-flaky.
func seedBuilds(repo *fake.Registry, n int) []repository.Build {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	builds := make([]repository.Build, n)
	for i := 0; i < n; i++ {
		startedAt := base.Add(time.Duration(i) * time.Second).Add(-time.Minute)
		b := repository.Build{
			GitSHA:          "sha",
			WorkflowRunID:   listBuildsTestRunID(i),
			WorkflowAttempt: 1,
			StartedAt:       &startedAt,
			RecordedAt:      base.Add(time.Duration(i) * time.Second),
		}
		builds[i] = repo.SeedBuild(b)
	}
	return builds
}

func listBuildsTestRunID(i int) string {
	return "run-" + string(rune('a'+i))
}

// TestListBuilds_NoFilter_OrderedMostRecentFirst covers FR2.1/FR2.3: with no
// filter or paging, every recorded build comes back, most-recent-recorded_at
// first.
func TestListBuilds_NoFilter_OrderedMostRecentFirst(t *testing.T) {
	repo := fake.New()
	srv := NewArtifactServer(repo)
	ctx := authedCtx()
	seeded := seedBuilds(repo, 5) // run-a .. run-e, oldest to newest

	resp, err := srv.ListBuilds(ctx, &pb.ListBuildsRequest{})
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	if len(resp.Builds) != len(seeded) {
		t.Fatalf("expected %d builds, got %d", len(seeded), len(resp.Builds))
	}
	for i, b := range resp.Builds {
		want := seeded[len(seeded)-1-i] // reverse of insertion order (newest first)
		if b.BuildId != want.BuildID {
			t.Fatalf("position %d: expected %s, got %s", i, want.BuildID, b.BuildId)
		}
	}
	if resp.Page.GetNextPageToken() != "" {
		t.Fatalf("expected no next_page_token for a full unpaged result, got %q", resp.Page.GetNextPageToken())
	}
}

// TestListBuilds_NilStartedAt_OrdersByRecordedAtNotStartedAt is the one bug
// class specific to ListBuilds and not shared with ListReconcileRuns (#611):
// a build recorded with started_at unset must still sort by recorded_at, not
// by started_at's presence/absence -- a naive `ORDER BY started_at` would
// float the NULL row to one end regardless of its actual recorded_at.
func TestListBuilds_NilStartedAt_OrdersByRecordedAtNotStartedAt(t *testing.T) {
	repo := fake.New()
	srv := NewArtifactServer(repo)
	ctx := authedCtx()

	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	older := base
	newer := base.Add(time.Hour)

	// "no-started" has a NEWER recorded_at than "with-started", but no
	// started_at at all -- if ordering ever fell back to started_at (or a
	// naive NULLS FIRST/LAST on it), this would sort in the wrong position.
	withStarted := repo.SeedBuild(repository.Build{
		WorkflowRunID: "run-with-started", WorkflowAttempt: 1, GitSHA: "sha",
		StartedAt: &older, RecordedAt: older,
	})
	noStarted := repo.SeedBuild(repository.Build{
		WorkflowRunID: "run-no-started", WorkflowAttempt: 1, GitSHA: "sha",
		StartedAt: nil, RecordedAt: newer,
	})

	resp, err := srv.ListBuilds(ctx, &pb.ListBuildsRequest{})
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	if len(resp.Builds) != 2 {
		t.Fatalf("expected 2 builds, got %d", len(resp.Builds))
	}
	// Most-recent-recorded_at first: noStarted (recorded later) must come
	// before withStarted, despite having no started_at at all.
	if resp.Builds[0].BuildId != noStarted.BuildID {
		t.Fatalf("position 0: expected the nil-started_at build %s (newer recorded_at), got %s", noStarted.BuildID, resp.Builds[0].BuildId)
	}
	if resp.Builds[0].StartedAt != 0 {
		t.Fatalf("expected wire started_at 0 for a nil StartedAt, got %d", resp.Builds[0].StartedAt)
	}
	if resp.Builds[1].BuildId != withStarted.BuildID {
		t.Fatalf("position 1: expected %s, got %s", withStarted.BuildID, resp.Builds[1].BuildId)
	}
}

// TestListBuilds_SinceFilter covers FR2.2: since excludes builds strictly
// before it and includes a build recorded at exactly since -- including one
// whose started_at is unset, confirming a NULL-started_at row can still
// satisfy since (architect's open question 3 resolution on #601).
func TestListBuilds_SinceFilter(t *testing.T) {
	repo := fake.New()
	srv := NewArtifactServer(repo)
	ctx := authedCtx()
	seeded := seedBuilds(repo, 5) // run-a (oldest) .. run-e (newest)

	// seeded[2] ("run-c") is the boundary: since == its RecordedAt should
	// include it, and exclude run-a/run-b before it. Overwrite its
	// started_at to nil in place to prove the NULL row still satisfies since.
	noStartedAtBoundary := seeded[2]
	noStartedAtBoundary.StartedAt = nil
	repo.SeedBuild(noStartedAtBoundary)

	since := seeded[2].RecordedAt

	resp, err := srv.ListBuilds(ctx, &pb.ListBuildsRequest{Since: since.Unix()})
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	if len(resp.Builds) != 3 {
		t.Fatalf("expected 3 builds (run-c, run-d, run-e), got %d: %+v", len(resp.Builds), resp.Builds)
	}
	wantIDs := []string{seeded[4].BuildID, seeded[3].BuildID, seeded[2].BuildID}
	for i, b := range resp.Builds {
		if b.BuildId != wantIDs[i] {
			t.Fatalf("position %d: expected %s, got %s", i, wantIDs[i], b.BuildId)
		}
	}
	// The boundary row (run-c) has no started_at and is still present.
	if resp.Builds[2].StartedAt != 0 {
		t.Fatalf("expected the boundary build's wire started_at to be 0 (nil StartedAt), got %d", resp.Builds[2].StartedAt)
	}
}

// TestListBuilds_Pagination_NoOverlapNoGap covers FR2.1/NFR2: a page_size
// smaller than the total returns exactly that many rows and a non-empty
// next_page_token; concatenating every page, in order, reproduces the
// unfiltered full-list result exactly, and the last page's next_page_token
// is empty.
func TestListBuilds_Pagination_NoOverlapNoGap(t *testing.T) {
	repo := fake.New()
	srv := NewArtifactServer(repo)
	ctx := authedCtx()
	seedBuilds(repo, 7)

	full, err := srv.ListBuilds(ctx, &pb.ListBuildsRequest{})
	if err != nil {
		t.Fatalf("ListBuilds (full): %v", err)
	}

	var walked []*pb.Build
	token := ""
	for page := 0; ; page++ {
		if page > len(full.Builds) {
			t.Fatalf("paged more times than there are rows -- next_page_token never went empty")
		}
		resp, err := srv.ListBuilds(ctx, &pb.ListBuildsRequest{
			Page: &pb.PageRequest{PageSize: 3, PageToken: token},
		})
		if err != nil {
			t.Fatalf("ListBuilds (page %d): %v", page, err)
		}
		if len(resp.Builds) == 0 {
			t.Fatalf("page %d: got 0 rows", page)
		}
		if len(walked) < len(full.Builds) && len(resp.Builds) > 3 {
			t.Fatalf("page %d: expected at most page_size=3 rows, got %d", page, len(resp.Builds))
		}
		walked = append(walked, resp.Builds...)
		token = resp.Page.GetNextPageToken()
		if token == "" {
			break
		}
	}

	if len(walked) != len(full.Builds) {
		t.Fatalf("expected %d total rows across all pages, got %d", len(full.Builds), len(walked))
	}
	seen := map[string]bool{}
	for i, b := range walked {
		if b.BuildId != full.Builds[i].BuildId {
			t.Fatalf("position %d: paged result %s does not match unfiltered full-list result %s",
				i, b.BuildId, full.Builds[i].BuildId)
		}
		if seen[b.BuildId] {
			t.Fatalf("duplicate row %s across pages", b.BuildId)
		}
		seen[b.BuildId] = true
	}
}

// TestListBuilds_InvalidPageToken covers the malformed-token case: a
// page_token this server never issued must fail with InvalidArgument, not
// panic or silently return a wrong page.
func TestListBuilds_InvalidPageToken(t *testing.T) {
	repo := fake.New()
	srv := NewArtifactServer(repo)
	ctx := authedCtx()
	seedBuilds(repo, 3)

	_, err := srv.ListBuilds(ctx, &pb.ListBuildsRequest{
		Page: &pb.PageRequest{PageToken: "not-a-real-cursor"},
	})
	if err == nil {
		t.Fatalf("expected an error for a garbage page_token, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
}

// TestListBuilds_DuplicateRecordedAt_TieBreaksDeterministically is the case
// a naive `ORDER BY recorded_at DESC LIMIT n` keyset implementation gets
// wrong: two builds sharing the exact same recorded_at must still page
// deterministically, tie-broken by build_id descending, with no duplication
// or omission across the page boundary.
func TestListBuilds_DuplicateRecordedAt_TieBreaksDeterministically(t *testing.T) {
	repo := fake.New()
	srv := NewArtifactServer(repo)
	ctx := authedCtx()

	same := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// Deliberately seeded with the higher ID first, to prove ordering comes
	// from the tie-break logic and not insertion order.
	repo.SeedBuild(repository.Build{
		BuildID: "tie-b", WorkflowRunID: "run-tie-b", WorkflowAttempt: 1, GitSHA: "sha", RecordedAt: same,
	})
	repo.SeedBuild(repository.Build{
		BuildID: "tie-a", WorkflowRunID: "run-tie-a", WorkflowAttempt: 1, GitSHA: "sha", RecordedAt: same,
	})
	// An older, distinct-timestamp row so the tied pair isn't the entire
	// result set -- exercises the tie-break across a real page boundary.
	repo.SeedBuild(repository.Build{
		BuildID: "older", WorkflowRunID: "run-older", WorkflowAttempt: 1, GitSHA: "sha", RecordedAt: same.Add(-time.Hour),
	})

	var walked []string
	token := ""
	for page := 0; page < 5; page++ {
		resp, err := srv.ListBuilds(ctx, &pb.ListBuildsRequest{
			Page: &pb.PageRequest{PageSize: 1, PageToken: token},
		})
		if err != nil {
			t.Fatalf("ListBuilds (page %d): %v", page, err)
		}
		if len(resp.Builds) != 1 {
			t.Fatalf("page %d: expected exactly 1 row, got %d", page, len(resp.Builds))
		}
		walked = append(walked, resp.Builds[0].BuildId)
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

// TestListBuilds_GetReleaseRunAndBuildsStatusUnaffected is a regression
// check for FR2.5: ListBuilds is additive -- GetReleaseRun (the existing
// per-run read) must still return the exact same build/artifact shape as
// before, for a build that ListBuilds also lists.
func TestListBuilds_GetReleaseRunAndBuildsStatusUnaffected(t *testing.T) {
	_, artifactSrv, _ := setup(t)
	ctx := authedCtx()
	build := recordBuild(t, artifactSrv, "run-getrun-unaffected")

	if _, err := artifactSrv.RecordArtifact(ctx, &pb.RecordArtifactRequest{
		BuildId: build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:getrun-unaffected", Version: "v1.0.0",
		IdentityDigest: "sha256:getrun-unaffected",
		IdempotencyKey: "record-getrun-unaffected",
	}); err != nil {
		t.Fatalf("RecordArtifact: %v", err)
	}

	run, err := artifactSrv.GetReleaseRun(ctx, &pb.GetReleaseRunRequest{WorkflowRunId: "run-getrun-unaffected"})
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	if run.Build.BuildId != build.BuildId {
		t.Fatalf("GetReleaseRun returned a different build than RecordBuild: got %s, want %s", run.Build.BuildId, build.BuildId)
	}
	if len(run.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact hanging off the run, got %d", len(run.Artifacts))
	}

	// ListBuilds sees the same build, unfiltered.
	list, err := artifactSrv.ListBuilds(ctx, &pb.ListBuildsRequest{})
	if err != nil {
		t.Fatalf("ListBuilds: %v", err)
	}
	found := false
	for _, b := range list.Builds {
		if b.BuildId == build.BuildId {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected ListBuilds to include the build GetReleaseRun already sees, got %+v", list.Builds)
	}
}
