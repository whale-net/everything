package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/whagent_net/events"
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
// # Write order / crash safety (Implementation phase)
//
// Per session, in this exact order:
//
//  1. Page the full transcript out of transcript_event in ascending seq
//     (pageWholeTranscript).
//  2. Serialize to the JSONL-gz cold-object-contract format (#2240's
//     ARCHITECTURE.md "Transcript storage tiers"), one events.Event per
//     line (encodeArchiveObject).
//  3. Upload to sessions/{session_id}.jsonl.gz (uploadTranscript).
//  4. Verify the uploaded object round-trips (verifyUploadedObject) --
//     re-download, decode via session.DecodeArchiveObject (the same
//     function Read/hydrateArchive use), count lines.
//  5. Commit the transcript_archive index row (session.ArchiveStore.Put).
//  6. Only then trim the hot-tier rows and stamp hot_trimmed_at
//     (trimHotTier).
//
// A crash before step 5 leaves no transcript_archive row, so
// SessionStore.ListArchiveEligible selects the session again on the next
// scan and archiveSession redoes steps 1-5 from scratch (re-uploading the
// same deterministic key is a no-op, not a duplicate -- a terminal
// session's transcript never grows again). A crash after step 5 but
// before step 6 leaves a transcript_archive row with hot_trimmed_at still
// NULL; ListArchiveEligible's second case selects it again regardless of
// TTL, and archiveSession (seeing an existing index row) skips straight to
// step 6 without re-uploading. TrimHot itself refuses to run without an
// existing index row (session/transcript.go's ErrNoArchiveIndex) -- the
// step 5-before-6 ordering is enforced in code, not just by this doc
// comment or the Testing phase's red/green check.
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
// -- SessionStore.ListArchiveEligible's two cases: terminal and past
// Config.TranscriptTTL with no transcript_archive row yet, or already
// archived but crashed before trimming (this task's Implementation
// section, "Selection") -- and archives each one, returning how many
// sessions were successfully archived. A per-session failure is logged at
// ERROR with the session id (AGENTS.md § Logging Levels: the archiver
// cannot continue *that session's* archival, something failed and needs
// attention) and does not abort the batch -- one bad session must not
// starve every other eligible session of a chance to archive this tick.
func (a *Archiver) RunOnce(ctx context.Context) (int, error) {
	cutoff := time.Now().Add(-a.config.TranscriptTTL)
	ids, err := a.store.Sessions().ListArchiveEligible(ctx, cutoff, a.config.BatchSize)
	if err != nil {
		return 0, fmt.Errorf("list archive-eligible sessions: %w", err)
	}

	archived := 0
	for _, id := range ids {
		if err := a.archiveSession(ctx, id); err != nil {
			a.logger.ErrorContext(ctx, "archive session failed", "session_id", id, "error", err)
			continue
		}
		archived++
	}
	return archived, nil
}

// archiveObjectKey mirrors the cold-object contract's key format
// (ARCHITECTURE.md "Transcript storage tiers", session.ArchiveIndex's doc
// comment): "sessions/{session_id}.jsonl.gz".
func archiveObjectKey(sessionID uuid.UUID) string {
	return fmt.Sprintf("sessions/%s.jsonl.gz", sessionID)
}

// transcriptPageSize bounds each Read call archiveSession issues while
// paging a session's whole transcript out of the hot tier -- same
// pagination shape as worker/context.go's readWholeTranscript
// (transcriptReadPageSize), just a package-local constant since archiver
// has no reason to share worker's.
const transcriptPageSize = 200

// archiveSession runs the write-order contract (see the Archiver doc
// comment) for exactly one sessionID. It is idempotent across crashes at
// any point: called again for a session with no transcript_archive row,
// it redoes upload+verify+commit from scratch; called again for a session
// that already has one (with hot_trimmed_at still NULL), it skips straight
// to trimming.
func (a *Archiver) archiveSession(ctx context.Context, sessionID uuid.UUID) error {
	idx, err := a.store.Archive().Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get archive index: %w", err)
	}

	if idx == nil {
		newIdx, retriedUpload, err := a.uploadTranscript(ctx, sessionID)
		if err != nil {
			return err
		}
		if retriedUpload {
			// The object already existed at this session's deterministic
			// key before this attempt uploaded it -- a prior attempt got
			// as far as uploading but crashed before committing the index
			// row (write-order step 3 succeeded, step 5 did not). This
			// attempt's re-upload then succeeded: the operation completed,
			// but not exactly as expected on the first try (AGENTS.md §
			// Logging Levels).
			a.logger.WarnContext(ctx, "archive upload retried and succeeded", "session_id", sessionID)
		}
		idx = &newIdx
	}

	if idx.HotTrimmedAt != nil {
		// Already fully archived. ListArchiveEligible should never select
		// this session again, but a defensive no-op costs nothing.
		return nil
	}

	return a.trimHotTier(ctx, *idx)
}

// uploadTranscript runs write-order steps 1-5 for a session with no
// existing transcript_archive row: page, encode, upload, verify, commit.
// The returned bool reports whether the object already existed at this
// session's key before this call uploaded it -- see archiveSession's
// WARNING log for what that means.
func (a *Archiver) uploadTranscript(ctx context.Context, sessionID uuid.UUID) (session.ArchiveIndex, bool, error) {
	evs, err := a.pageWholeHotTranscript(ctx, sessionID)
	if err != nil {
		return session.ArchiveIndex{}, false, fmt.Errorf("page transcript: %w", err)
	}

	body, err := encodeArchiveObject(evs)
	if err != nil {
		return session.ArchiveIndex{}, false, fmt.Errorf("encode archive object: %w", err)
	}

	key := archiveObjectKey(sessionID)
	retriedUpload, err := a.s3.Exists(ctx, key)
	if err != nil {
		return session.ArchiveIndex{}, false, fmt.Errorf("check existing archive object: %w", err)
	}

	if _, err := a.s3.Upload(ctx, key, body, &s3.UploadOptions{
		ContentType:     "application/gzip",
		ContentEncoding: "gzip",
	}); err != nil {
		return session.ArchiveIndex{}, false, fmt.Errorf("upload archive object: %w", err)
	}

	if err := a.verifyUploadedObject(ctx, key, len(evs)); err != nil {
		return session.ArchiveIndex{}, false, fmt.Errorf("verify uploaded archive object: %w", err)
	}

	idx := session.ArchiveIndex{
		SessionID:  sessionID,
		S3Bucket:   a.s3.GetBucket(),
		S3Key:      key,
		EventCount: int64(len(evs)),
	}
	if len(evs) > 0 {
		idx.MinSeq = evs[0].Seq
		idx.MaxSeq = evs[len(evs)-1].Seq
	}
	if err := a.store.Archive().Put(ctx, idx); err != nil {
		return session.ArchiveIndex{}, false, fmt.Errorf("commit archive index: %w", err)
	}
	return idx, retriedUpload, nil
}

// pageWholeHotTranscript pages every transcript_event row for sessionID in
// ascending seq via TranscriptStore.Read -- same forward-pagination shape
// as worker/context.go's readWholeTranscript. The store archiveSession's
// caller (NewArchiver) constructs is never given a WithS3 option
// (session.New(pool, nil)), so Read here can only ever serve from the hot
// tier -- exactly what write-order step 1 needs, with no risk of
// double-counting a session's own not-yet-committed archive object.
func (a *Archiver) pageWholeHotTranscript(ctx context.Context, sessionID uuid.UUID) ([]events.Event, error) {
	var all []events.Event
	fromSeq := int64(0)
	for {
		page, err := a.store.Transcript().Read(ctx, sessionID, fromSeq, transcriptPageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < transcriptPageSize {
			return all, nil
		}
		fromSeq = page[len(page)-1].Seq + 1
	}
}

// encodeArchiveObject serializes evs into the cold-object-contract body
// (ARCHITECTURE.md "Transcript storage tiers"): gzip-compressed JSON
// Lines, one events.Event per line, in the order given -- callers
// (uploadTranscript) already page in ascending seq, so this does not
// re-sort. Nothing is summarized, reshaped, or dropped (LB1): each line is
// exactly the same JSON encoding session.DecodeArchiveObject and the
// Postgres row share.
func encodeArchiveObject(evs []events.Event) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	for _, ev := range evs {
		line, err := json.Marshal(ev)
		if err != nil {
			_ = gz.Close()
			return nil, fmt.Errorf("marshal event %s: %w", ev.EventID, err)
		}
		if _, err := gz.Write(line); err != nil {
			_ = gz.Close()
			return nil, fmt.Errorf("write archive object line: %w", err)
		}
		if _, err := gz.Write([]byte("\n")); err != nil {
			_ = gz.Close()
			return nil, fmt.Errorf("write archive object line: %w", err)
		}
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("close archive object gzip writer: %w", err)
	}
	return buf.Bytes(), nil
}

// verifyUploadedObject is write-order step 4: it re-reads the object this
// attempt just uploaded (Exists, then Download+decode) and confirms the
// decoded event count matches wantCount, so an index row this call's
// caller is about to commit never points at a truncated or corrupt
// object. Decoding goes through session.DecodeArchiveObject -- the same
// function the real tier-transparent reader uses -- so a successful verify
// means the object is genuinely readable, not merely present.
func (a *Archiver) verifyUploadedObject(ctx context.Context, key string, wantCount int) error {
	exists, err := a.s3.Exists(ctx, key)
	if err != nil {
		return fmt.Errorf("check uploaded object exists: %w", err)
	}
	if !exists {
		return fmt.Errorf("uploaded object %q not found immediately after upload", key)
	}

	data, err := a.s3.Download(ctx, key)
	if err != nil {
		return fmt.Errorf("re-read uploaded object: %w", err)
	}
	evs, err := session.DecodeArchiveObject(data)
	if err != nil {
		return fmt.Errorf("decode uploaded object: %w", err)
	}
	if len(evs) != wantCount {
		return fmt.Errorf("uploaded object %q has %d events, want %d", key, len(evs), wantCount)
	}
	return nil
}

// trimHotTier is write-order step 6, run only after idx (a
// transcript_archive row for idx.SessionID) is already durably committed
// -- by this call's caller having either just written it (uploadTranscript)
// or having just fetched it from Postgres (archiveSession's idx != nil
// branch). idx.SessionID == uuid.Nil can only reach this function via a
// programmer error (an ArchiveIndex literal built by hand rather than
// obtained from ArchiveStore), not any runtime/environmental condition --
// FR7's crash-safety invariant ("never delete a hot row for a session with
// no committed index row") is enforced a second time here, independent of
// TranscriptStore.TrimHot's own ErrNoArchiveIndex check, so it panics
// rather than returning an error: this is this task's Implementation
// section's "assert this in code, not just in the test".
func (a *Archiver) trimHotTier(ctx context.Context, idx session.ArchiveIndex) error {
	if idx.SessionID == uuid.Nil {
		panic("archiver: refusing to trim hot tier without a committed transcript_archive row")
	}

	if _, err := a.store.Transcript().TrimHot(ctx, idx.SessionID); err != nil {
		return fmt.Errorf("trim hot transcript: %w", err)
	}
	if err := a.store.Archive().MarkHotTrimmed(ctx, idx.SessionID); err != nil {
		return fmt.Errorf("mark hot trimmed: %w", err)
	}
	return nil
}
