package ratelimit

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultConfigs are the Phase 1 fallback WindowConfig values, used for any
// bucket whose environment variables (see EnvVarNames) are unset. These are
// conservative starting points, not load-tested numbers -- an operator
// tunes them per environment via leaflab/api/ENV.md's documented variables
// without a code change.
var DefaultConfigs = map[Bucket]WindowConfig{
	// BucketReadDefault: the catch-all applied to every RPC with no more
	// specific bucket (NFR10 "every read endpoint"). 120 calls/minute is a
	// generous per-principal budget for interactive use (BFF page loads,
	// polling) while still bounding a runaway client.
	BucketReadDefault: {Limit: 120, Window: time.Minute},
	// BucketResend (FR42): re-sending is an abuse vector (e.g. re-triggering
	// a notification) more than a throughput concern -- a tight, longer
	// window.
	BucketResend: {Limit: 3, Window: 5 * time.Minute},
	// BucketAckWaitConcurrent (FR47): bounds how often a caller opens a new
	// concurrent wait.
	BucketAckWaitConcurrent: {Limit: 5, Window: time.Minute},
	// BucketClaimOpen (FR76): claim initiation is rare in legitimate use
	// (one board, one household) -- a small hourly budget, per NFR10's
	// forward constraint keyed on device_id and principal, never per-board.
	BucketClaimOpen: {Limit: 5, Window: time.Hour},
	// BucketClaimRound (FR76.2): a claim's discharge rounds are more
	// frequent than opening a claim but still bounded per hour.
	BucketClaimRound: {Limit: 10, Window: time.Hour},
	// BucketSupportReferenceResolve (FR80): resolution is a lookup, not a
	// write, but still bounded against enumeration.
	BucketSupportReferenceResolve: {Limit: 10, Window: time.Minute},
}

// envVarPrefix is common to every bucket's pair of environment variables;
// see EnvVarNames.
const envVarPrefix = "LEAFLAB_API_RATELIMIT_"

// EnvVarNames returns the pair of environment variable names that configure
// bucket: <PREFIX><BUCKET>_LIMIT (call count) and
// <PREFIX><BUCKET>_WINDOW_SECONDS (window length in whole seconds).
// leaflab/api/ENV.md documents these; leaflab/api/main.go and
// env_doc_test.go-style conformance tests (a later validation task) use
// this function rather than hand-building the names twice.
func EnvVarNames(bucket Bucket) (limitVar, windowVar string) {
	upper := strings.ToUpper(string(bucket))
	return envVarPrefix + upper + "_LIMIT", envVarPrefix + upper + "_WINDOW_SECONDS"
}

// LoadConfigFromEnv builds a map[Bucket]WindowConfig for every bucket in
// ratelimit.Buckets, using getenv to read each bucket's pair of environment
// variables (see EnvVarNames) and falling back to DefaultConfigs when a
// variable is unset or empty. getenv is injected (rather than calling
// os.Getenv directly) so this is testable without mutating process
// environment; leaflab/api/main.go calls this with os.Getenv.
//
// A malformed value (not a valid integer) is an error, not a silent
// fallback -- a typo in an environment variable should fail loudly at boot,
// not quietly disable or misconfigure a limit.
func LoadConfigFromEnv(getenv func(string) string) (map[Bucket]WindowConfig, error) {
	configs := make(map[Bucket]WindowConfig, len(Buckets))
	for _, bucket := range Buckets {
		cfg, err := loadBucketConfig(getenv, bucket)
		if err != nil {
			return nil, err
		}
		configs[bucket] = cfg
	}
	return configs, nil
}

func loadBucketConfig(getenv func(string) string, bucket Bucket) (WindowConfig, error) {
	cfg := DefaultConfigs[bucket]
	limitVar, windowVar := EnvVarNames(bucket)

	if raw := getenv(limitVar); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return WindowConfig{}, fmt.Errorf("%s=%q: %w", limitVar, raw, err)
		}
		cfg.Limit = limit
	}
	if raw := getenv(windowVar); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil {
			return WindowConfig{}, fmt.Errorf("%s=%q: %w", windowVar, raw, err)
		}
		cfg.Window = time.Duration(seconds) * time.Second
	}
	return cfg, nil
}
