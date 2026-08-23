package writeback

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestStubActivities_Publish_SkipsNoOpWrite is AR-4b's state_hash no-op
// detection guarantee, exercised directly (no Temporal, no gRPC -- Publish
// needs neither): a second Publish call with the same StateHash writes
// nothing and reports Skipped=true; a genuinely different StateHash writes
// again.
func TestStubActivities_Publish_SkipsNoOpWrite(t *testing.T) {
	dir := t.TempDir()
	a := &StubActivities{OutDir: dir}
	ctx := context.Background()

	first, err := a.Publish(ctx, RenderedState{EnvironmentKey: "dev", StateHash: "hash-a", Document: []byte(`{"v":1}`)})
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if first.Skipped {
		t.Fatalf("expected the first publish (nothing previously published) to write, got Skipped=true")
	}
	docPath := filepath.Join(dir, "dev.json")
	first1, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read published document: %v", err)
	}
	if string(first1) != `{"v":1}` {
		t.Fatalf("unexpected published document: %s", first1)
	}

	second, err := a.Publish(ctx, RenderedState{EnvironmentKey: "dev", StateHash: "hash-a", Document: []byte(`{"v":1}`)})
	if err != nil {
		t.Fatalf("second publish (same hash): %v", err)
	}
	if !second.Skipped {
		t.Fatalf("expected a second publish with an unchanged state_hash to be skipped, got %+v", second)
	}

	third, err := a.Publish(ctx, RenderedState{EnvironmentKey: "dev", StateHash: "hash-b", Document: []byte(`{"v":2}`)})
	if err != nil {
		t.Fatalf("third publish (different hash): %v", err)
	}
	if third.Skipped {
		t.Fatalf("expected a publish with a changed state_hash to write, got Skipped=true")
	}
	after, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read republished document: %v", err)
	}
	if string(after) != `{"v":2}` {
		t.Fatalf("expected the document to be overwritten with the new state, got %s", after)
	}
}

// TestStubActivities_Publish_NeverReportsCommitSHA is FR7a's "no-git
// stub/dev-test path reports this field absent" requirement (issue #1029):
// StubActivities never commits to a git repo, so PublishResult.CommitSHA
// must stay at its zero value on every path -- the first (real) write, a
// no-op skip, and a subsequent real re-write all included, so a future
// change that starts stamping a stand-in value on any one of them is
// caught.
func TestStubActivities_Publish_NeverReportsCommitSHA(t *testing.T) {
	dir := t.TempDir()
	a := &StubActivities{OutDir: dir}
	ctx := context.Background()

	first, err := a.Publish(ctx, RenderedState{EnvironmentKey: "dev", StateHash: "hash-a", Document: []byte(`{"v":1}`)})
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if first.CommitSHA != "" {
		t.Fatalf("expected CommitSHA to be empty on StubActivities' no-git path, got %q", first.CommitSHA)
	}

	skipped, err := a.Publish(ctx, RenderedState{EnvironmentKey: "dev", StateHash: "hash-a", Document: []byte(`{"v":1}`)})
	if err != nil {
		t.Fatalf("skipped publish: %v", err)
	}
	if !skipped.Skipped {
		t.Fatalf("expected the second identical publish to be skipped")
	}
	if skipped.CommitSHA != "" {
		t.Fatalf("expected CommitSHA to be empty on the skipped no-op path, got %q", skipped.CommitSHA)
	}

	changed, err := a.Publish(ctx, RenderedState{EnvironmentKey: "dev", StateHash: "hash-b", Document: []byte(`{"v":2}`)})
	if err != nil {
		t.Fatalf("changed publish: %v", err)
	}
	if changed.CommitSHA != "" {
		t.Fatalf("expected CommitSHA to be empty on a real (non-skipped) StubActivities write, got %q", changed.CommitSHA)
	}
}

// TestStubActivities_Publish_DifferentEnvironmentsIndependent proves the
// no-op check is scoped per environment_key, not global -- publishing dev
// must not affect whether stage's next publish is considered a no-op.
func TestStubActivities_Publish_DifferentEnvironmentsIndependent(t *testing.T) {
	dir := t.TempDir()
	a := &StubActivities{OutDir: dir}
	ctx := context.Background()

	if _, err := a.Publish(ctx, RenderedState{EnvironmentKey: "dev", StateHash: "same-hash", Document: []byte(`{"env":"dev"}`)}); err != nil {
		t.Fatalf("publish dev: %v", err)
	}
	stageResult, err := a.Publish(ctx, RenderedState{EnvironmentKey: "stage", StateHash: "same-hash", Document: []byte(`{"env":"stage"}`)})
	if err != nil {
		t.Fatalf("publish stage: %v", err)
	}
	if stageResult.Skipped {
		t.Fatalf("expected stage's first publish to write despite sharing dev's state_hash, got Skipped=true")
	}
}
