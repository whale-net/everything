package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/whagent_net/session"
)

// Config is Archiver's env-derived configuration (ENV.md "S3 (cold
// tier)"). Config-from-env is pure (no I/O), so -- like
// whagent_net/config.Load's own "pure config shapes ship whole in
// Scaffold" precedent -- ConfigFromEnv ships complete in this task's
// Scaffold phase, unlike Archiver.RunOnce below.
type Config struct {
	// S3Bucket is WHAGENT_S3_BUCKET: the bucket archived transcripts are
	// uploaded to, at key "sessions/{session_id}.jsonl.gz" (ARCHITECTURE.md
	// "Transcript storage tiers" § "Cold-object contract"). Empty disables
	// archiving entirely -- Run refuses to start rather than archiving
	// nothing forever (Implementation phase).
	S3Bucket string
	// TranscriptTTL is WHAGENT_TRANSCRIPT_TTL (FR7): how long a terminal
	// session's transcript stays hot-tier-only before it becomes eligible
	// for archival, measured from `sessions.updated_at` (the
	// compare-and-swap terminal write -- see this task's Implementation
	// section).
	TranscriptTTL time.Duration
	// ScanInterval is WHAGENT_ARCHIVER_SCAN_INTERVAL: how often Run polls
	// for newly-eligible sessions.
	ScanInterval time.Duration
	// BatchSize is WHAGENT_ARCHIVER_BATCH_SIZE: the maximum number of
	// eligible sessions RunOnce processes per scan, so one archiver tick
	// never tries to archive an unbounded backlog in one pass.
	BatchSize int
}

const (
	// DefaultTranscriptTTL is used when WHAGENT_TRANSCRIPT_TTL is unset --
	// 7 days, matching FR7's stated hot-tier retention window.
	DefaultTranscriptTTL = 168 * time.Hour
	// DefaultScanInterval is used when WHAGENT_ARCHIVER_SCAN_INTERVAL is
	// unset.
	DefaultScanInterval = 5 * time.Minute
	// DefaultBatchSize is used when WHAGENT_ARCHIVER_BATCH_SIZE is unset.
	DefaultBatchSize = 50
)

// ConfigFromEnv reads Config from the process environment, applying the
// defaults documented on the Default* consts above and on ENV.md's
// archiver rows. Malformed durations/integers fail loudly (returned as an
// error) rather than silently falling back to a default -- an operator
// who set WHAGENT_TRANSCRIPT_TTL to a typo'd value should see startup fail,
// not archive on an unintended schedule.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		S3Bucket:      os.Getenv("WHAGENT_S3_BUCKET"),
		TranscriptTTL: DefaultTranscriptTTL,
		ScanInterval:  DefaultScanInterval,
		BatchSize:     DefaultBatchSize,
	}

	if v := os.Getenv("WHAGENT_TRANSCRIPT_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("parse WHAGENT_TRANSCRIPT_TTL: %w", err)
		}
		cfg.TranscriptTTL = d
	}
	if v := os.Getenv("WHAGENT_ARCHIVER_SCAN_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("parse WHAGENT_ARCHIVER_SCAN_INTERVAL: %w", err)
		}
		cfg.ScanInterval = d
	}
	if v := os.Getenv("WHAGENT_ARCHIVER_BATCH_SIZE"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
			return Config{}, fmt.Errorf("parse WHAGENT_ARCHIVER_BATCH_SIZE: invalid value %q", v)
		}
		cfg.BatchSize = n
	}

	return cfg, nil
}

// Archiver is FR7's hot-to-cold transcript archiver: periodically scans
// for terminal sessions past Config.TranscriptTTL with no `transcript_archive`
// row yet, and archives each one -- see this task's Implementation section
// for the exact page/gzip/upload/commit/trim write order and its
// crash-safety contract (upload and index-row commit both happen before
// any hot row is ever trimmed).
//
// # Scaffold status (this task)
//
// The store/S3/config wiring below is real: session.Store already exposes
// everything RunOnce needs (Sessions() for selection, Transcript() for
// paging hot rows, Archive() for the index row -- session/sessions.go,
// transcript.go, archive.go). RunOnce itself is deliberately a stub. The
// scan/select/page/gzip/upload/verify/commit/trim pipeline this task's
// Implementation section specifies is one crash-safety contract, not a
// set of independently-shippable pieces, so it lands whole in this task's
// Implementation phase rather than incrementally here.
type Archiver struct {
	store  *session.Store
	s3     *s3.Client
	config Config
	logger *slog.Logger
}

// NewArchiver constructs an Archiver from an already-connected Postgres
// pool and S3 client. Neither pool nor s3Client may be nil -- unlike
// api/worker's optional S3 client (session.WithS3's doc comment), the
// archiver has nothing useful to do without a cold tier to write to, so
// main.go fails startup rather than constructing a no-op Archiver.
func NewArchiver(pool *pgxpool.Pool, s3Client *s3.Client, cfg Config, logger *slog.Logger) *Archiver {
	return &Archiver{
		store:  session.New(pool, nil),
		s3:     s3Client,
		config: cfg,
		logger: logger,
	}
}

// Run polls RunOnce every Config.ScanInterval until ctx is canceled. A
// completed run with archivedCount > 0 is logged at INFO (AGENTS.md §
// Logging Levels: "something notable happened and completed normally");
// an empty run is not logged at all, to keep steady-state idle polling
// quiet.
func (a *Archiver) Run(ctx context.Context) error {
	ticker := time.NewTicker(a.config.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			archived, err := a.RunOnce(ctx)
			if err != nil {
				a.logger.ErrorContext(ctx, "archiver run failed", "error", err)
				continue
			}
			if archived > 0 {
				a.logger.InfoContext(ctx, "archive run complete", "archived_count", archived)
			}
		}
	}
}

// RunOnce scans for up to Config.BatchSize sessions eligible for archival
// (terminal, past Config.TranscriptTTL, no `transcript_archive` row yet --
// this task's Implementation section, "Selection") and archives each one,
// returning how many sessions were successfully archived.
//
// Scaffold status (this task): stub. See the Archiver doc comment above --
// the real selection/page/gzip/upload/commit/trim pipeline lands in this
// task's Implementation phase.
func (a *Archiver) RunOnce(ctx context.Context) (int, error) {
	return 0, nil
}
