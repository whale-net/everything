package main

import (
	"context"
	"log/slog"
	"time"
)

// DefaultCacheBackstopInterval is how often RunCacheBackstop fully reloads
// SensorCache from the database when Config.CacheBackstopInterval is unset.
//
// FR73's 5 s bound is met by the invalidation.Subscriber wired in main.go,
// not by this interval -- see leaflab/ARCHITECTURE.md. This interval only
// bounds how long a *dropped* invalidation.Event (one lost during a
// RabbitMQ reconnect window, or published before this process's Subscriber
// finished attaching) can leave the cache wrong, so it can be, and is, far
// looser than 5 s.
const DefaultCacheBackstopInterval = 30 * time.Second

// SensorCacheLoader is the subset of SensorRepository RunCacheBackstop
// needs to reload SensorCache's contents from the database. Repository
// already implements it (main.go uses the same two methods for the
// one-time startup pre-warm).
type SensorCacheLoader interface {
	LoadSensorCache(ctx context.Context) (map[string]map[string]SensorInfo, error)
	LoadConfigVersionCache(ctx context.Context) (map[string]int64, error)
}

// RunCacheBackstop periodically reloads SensorCache and the config version
// cache in full from the database, so a dropped or missed invalidation.Event
// self-heals within interval instead of leaving a stale (or, for a dropped
// rename, orphaned) entry in the cache indefinitely. It blocks until ctx is
// cancelled, so callers run it in its own goroutine.
//
// This is a backstop, not the mechanism FR73's 5 s bound depends on -- the
// invalidation.Subscriber wired in main.go is. See leaflab/ARCHITECTURE.md.
func RunCacheBackstop(ctx context.Context, interval time.Duration, repo SensorCacheLoader, cache *SensorCache, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reloadCache(ctx, repo, cache, logger)
		}
	}
}

// reloadCache performs one backstop reload. Split out from RunCacheBackstop
// so it can be exercised directly in tests without waiting on a ticker.
func reloadCache(ctx context.Context, repo SensorCacheLoader, cache *SensorCache, logger *slog.Logger) {
	entries, err := repo.LoadSensorCache(ctx)
	if err != nil {
		if logger != nil {
			logger.Warn("cache backstop: failed to reload sensor cache", "err", err)
		}
	} else {
		cache.ReplaceAll(entries)
	}

	versions, err := repo.LoadConfigVersionCache(ctx)
	if err != nil {
		if logger != nil {
			logger.Warn("cache backstop: failed to reload config version cache", "err", err)
		}
		return
	}
	cache.LoadConfigVersions(versions)
}
