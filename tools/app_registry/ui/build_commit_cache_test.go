package main

import (
	"sync"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// --- buildCommitCache: direct unit coverage ------------------------------
//
// resolveTargetCommits' own tests (handlers_release_test.go) cover the
// RPC-fan-out behavior this cache exists to reduce (#1699 NFR9: one
// GetBuild per build_id, globally, for the life of the process). These
// tests instead cover the cache type in isolation: get/put semantics, the
// nil-receiver fallback resolveTargetCommits relies on for handler tests
// that construct &App{...} without a cache, and concurrency safety.

func TestBuildCommitCache_MissThenPutThenHit(t *testing.T) {
	c := newBuildCommitCache()

	if _, ok := c.get("build-1"); ok {
		t.Fatalf("get on empty cache: got a hit, want a miss")
	}

	want := pages.BuildCommitInfo{GitSha: "deadbeef", URL: "https://github.com/whale-net/everything/commit/deadbeef"}
	c.put("build-1", want)

	got, ok := c.get("build-1")
	if !ok {
		t.Fatalf("get after put: got a miss, want a hit")
	}
	if got != want {
		t.Errorf("get after put = %+v, want %+v", got, want)
	}
}

// A distinct build_id must never see another build_id's entry -- the cache
// is keyed per build_id, not a single shared slot.
func TestBuildCommitCache_DistinctKeysDoNotCollide(t *testing.T) {
	c := newBuildCommitCache()
	c.put("build-1", pages.BuildCommitInfo{GitSha: "sha-one"})
	c.put("build-2", pages.BuildCommitInfo{GitSha: "sha-two"})

	got1, ok1 := c.get("build-1")
	got2, ok2 := c.get("build-2")
	if !ok1 || got1.GitSha != "sha-one" {
		t.Errorf("get(build-1) = %+v, %v; want sha-one, true", got1, ok1)
	}
	if !ok2 || got2.GitSha != "sha-two" {
		t.Errorf("get(build-2) = %+v, %v; want sha-two, true", got2, ok2)
	}
}

// A nil *buildCommitCache must degrade to "always miss, put is a no-op"
// rather than panic -- resolveTargetCommits' own tests construct &App{...}
// literals without a cache for handlers unrelated to this feature, and
// those must keep working unchanged.
func TestBuildCommitCache_NilReceiver_GetIsSafeMiss(t *testing.T) {
	var c *buildCommitCache

	if _, ok := c.get("build-1"); ok {
		t.Errorf("nil cache get: got a hit, want a miss")
	}
}

func TestBuildCommitCache_NilReceiver_PutIsSafeNoOp(t *testing.T) {
	var c *buildCommitCache

	// Must not panic.
	c.put("build-1", pages.BuildCommitInfo{GitSha: "deadbeef"})
}

// Parallel get/put against the same cache and the same key must be
// race-free under `bazel test --features=race` -- this is the property
// that makes the cache safe to share across concurrent SSE renders for
// different open tabs on the same or different release runs.
func TestBuildCommitCache_ConcurrentAccess_RaceFree(t *testing.T) {
	c := newBuildCommitCache()
	const goroutines = 50
	info := pages.BuildCommitInfo{GitSha: "deadbeef", URL: "https://github.com/whale-net/everything/commit/deadbeef"}

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			c.put("build-shared", info)
		}()
		go func() {
			defer wg.Done()
			c.get("build-shared")
		}()
	}
	wg.Wait()

	got, ok := c.get("build-shared")
	if !ok || got != info {
		t.Errorf("get(build-shared) after concurrent access = %+v, %v; want %+v, true", got, ok, info)
	}
}
