package main

import (
	"sync"

	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// buildCommitCache caches a successfully resolved build_id -> git commit
// mapping for the lifetime of the UI process. A build's git_sha is
// immutable once resolved (resolveTargetCommits' own doc comment,
// handlers_release.go, already relies on this), so the first successful
// resolution for a given build_id is reusable forever -- there is no
// staleness window to account for.
//
// The cache is keyed globally by build_id, not scoped to a connection,
// tab, or release run: a build_id is a globally unique, immutable
// identifier that can recur across concurrent connections and different
// release runs (#1699 NFR9). Scoping the cache any narrower would defeat
// its purpose -- e.g. two tabs open on the same run, or two runs that
// happen to reuse a build_id, would each pay their own GetBuild RPC.
//
// No eviction or TTL is implemented, intentionally: entries are tiny (a
// git_sha string plus a derived URL) and keyed by build UUID, so growth is
// bounded in practice by the number of distinct builds a single UI process
// ever renders over its lifetime -- not unbounded, and not worth the
// complexity of an eviction policy.
type buildCommitCache struct {
	mu      sync.RWMutex
	entries map[string]pages.BuildCommitInfo
}

// newBuildCommitCache returns an empty cache, ready for concurrent use.
func newBuildCommitCache() *buildCommitCache {
	return &buildCommitCache{
		entries: make(map[string]pages.BuildCommitInfo),
	}
}

// get returns the cached commit info for buildID, if present.
func (c *buildCommitCache) get(buildID string) (pages.BuildCommitInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	info, ok := c.entries[buildID]
	return info, ok
}

// put records a successfully resolved commit info for buildID. Callers
// must only call put for a resolution that actually succeeded (non-empty
// git_sha) -- a failed or incomplete resolution must never be cached, so
// it can be retried on a later render (see resolveTargetCommits).
func (c *buildCommitCache) put(buildID string, info pages.BuildCommitInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[buildID] = info
}
