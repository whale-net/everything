// Hot-to-cold transcript archival (FR7/C18), run as a Temporal-scheduled
// job inside this worker rather than as its own binary. The standalone
// `whagent_net/archiver` service (issue #2244) has been removed: Temporal
// is already this repo's scheduled-job engine (ARCHITECTURE.md "Language
// and stack": `//libs/go/temporal`'s `UpsertSchedule`, the same mechanism
// audience_score_system/worker/sync uses for ChannelSyncWorkflow), so a
// second always-on process whose only job is "run this on a timer" added
// deployment surface without adding a capability. ArchiveWorkflow/
// RunArchiveBatch below are a direct port of the archiver binary's
// Archiver.RunOnce/archiveSession pipeline -- see git history
// (whagent_net/archiver, issue #2244) for the original standalone shape
// and its write-order/crash-safety contract, which this port preserves
// unchanged.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/workflow"

	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/session"
)

// ActivityRunArchiveBatch is the activity name ArchiveWorkflow dispatches
// by (same string-name convention as workflow.go's ActivityXxx consts).
const ActivityRunArchiveBatch = "RunArchiveBatch"

// ArchiveScheduleID is the deterministic Temporal schedule id
// ensureArchiveSchedule (main.go) upserts at startup -- one global
// schedule, unlike audience_score_system's per-Channel
// ChannelSyncWorkflow schedules, since a single RunArchiveBatch pass
// already scans across every eligible session in one query.
const ArchiveScheduleID = "whagent-net-archive"

const (
	// DefaultTranscriptTTL is used when WHAGENT_TRANSCRIPT_TTL is unset --
	// 7 days, matching FR7's stated hot-tier retention window.
	DefaultTranscriptTTL = 168 * time.Hour
	// DefaultArchiveInterval is used when WHAGENT_ARCHIVE_INTERVAL is
	// unset -- the schedule's firing cadence, replacing the deprecated
	// archiver binary's in-process ticker (WHAGENT_ARCHIVER_SCAN_INTERVAL).
	DefaultArchiveInterval = 5 * time.Minute
	// DefaultArchiveBatchSize is used when WHAGENT_ARCHIVE_BATCH_SIZE is
	// unset.
	DefaultArchiveBatchSize = 50
)

// ArchiveConfig is ArchiveWorkflow/RunArchiveBatch's argument -- resolved
// once at startup (ArchiveConfigFromEnv, main.go) and passed as the fixed
// workflow input every scheduled run receives (ENV.md "S3 (cold tier)").
type ArchiveConfig struct {
	// S3Bucket is WHAGENT_S3_BUCKET: the bucket archived transcripts are
	// uploaded to, at key "sessions/{session_id}.jsonl.gz" (ARCHITECTURE.md
	// "Cold-object contract"). Empty disables archiving entirely --
	// ensureArchiveSchedule (main.go) skips registering the schedule
	// rather than running a workflow that can only ever fail.
	S3Bucket string
	// TranscriptTTL is WHAGENT_TRANSCRIPT_TTL (FR7): how long a terminal
	// session's transcript stays hot-tier-only before it becomes eligible
	// for archival, measured from `sessions.updated_at`.
	TranscriptTTL time.Duration
	// BatchSize is WHAGENT_ARCHIVE_BATCH_SIZE: the maximum number of
	// eligible sessions RunArchiveBatch processes per run, so one
	// scheduled tick never tries to archive an unbounded backlog in one
	// pass.
	BatchSize int
}

// ArchiveConfigFromEnv reads ArchiveConfig from the process environment,
// applying the Default* consts above. Malformed durations/integers fail
// loudly (returned as an error) rather than silently falling back to a
// default -- an operator who set WHAGENT_TRANSCRIPT_TTL to a typo'd value
// should see startup fail, not archive on an unintended schedule.
func ArchiveConfigFromEnv() (ArchiveConfig, error) {
	cfg := ArchiveConfig{
		S3Bucket:      os.Getenv("WHAGENT_S3_BUCKET"),
		TranscriptTTL: DefaultTranscriptTTL,
		BatchSize:     DefaultArchiveBatchSize,
	}

	if v := os.Getenv("WHAGENT_TRANSCRIPT_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return ArchiveConfig{}, fmt.Errorf("parse WHAGENT_TRANSCRIPT_TTL: %w", err)
		}
		cfg.TranscriptTTL = d
	}
	if v := os.Getenv("WHAGENT_ARCHIVE_BATCH_SIZE"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
			return ArchiveConfig{}, fmt.Errorf("parse WHAGENT_ARCHIVE_BATCH_SIZE: invalid value %q", v)
		}
		cfg.BatchSize = n
	}

	return cfg, nil
}

// ArchiveInterval reads WHAGENT_ARCHIVE_INTERVAL, defaulting to
// DefaultArchiveInterval. Kept separate from ArchiveConfig because it
// configures the Temporal Schedule itself (main.go's
// ensureArchiveSchedule), not the workflow input.
func ArchiveInterval() (time.Duration, error) {
	v := os.Getenv("WHAGENT_ARCHIVE_INTERVAL")
	if v == "" {
		return DefaultArchiveInterval, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("parse WHAGENT_ARCHIVE_INTERVAL: %w", err)
	}
	return d, nil
}

// ArchiveResult is ArchiveWorkflow's return value.
type ArchiveResult struct {
	ArchivedCount int
}

// ArchiveWorkflow is the one-shot workflow the Temporal Schedule
// (ArchiveScheduleID) fires on WHAGENT_ARCHIVE_INTERVAL -- the scheduled-
// job replacement for the deprecated whagent_net/archiver binary's
// internal ticker. Every run executes exactly one RunArchiveBatch
// activity and returns; Temporal's schedule, not an in-process ticker,
// owns the cadence, so a completed run logs nothing itself (unlike the
// deprecated binary's Run loop) -- RunArchiveBatch's own INFO/ERROR logs
// (AGENTS.md § Logging Levels) are the record.
func ArchiveWorkflow(ctx workflow.Context, cfg ArchiveConfig) (ArchiveResult, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var result ArchiveResult
	err := workflow.ExecuteActivity(ctx, ActivityRunArchiveBatch, cfg).Get(ctx, &result)
	return result, err
}

// RunArchiveBatch is FR7/C18's hot-to-cold transcript archival pass,
// ported unchanged from the deprecated whagent_net/archiver binary's
// Archiver.RunOnce: scans for up to cfg.BatchSize sessions eligible for
// archival -- SessionStore.ListArchiveEligible's two cases: terminal and
// past cfg.TranscriptTTL with no transcript_archive row yet, or already
// archived but crashed before trimming -- and archives each one,
// returning how many sessions were successfully archived. A per-session
// failure is logged at ERROR with the session id (AGENTS.md § Logging
// Levels: this run cannot continue *that session's* archival, something
// failed and needs attention) and does not abort the batch -- one bad
// session must not starve every other eligible session of a chance to
// archive this run.
//
// # Write order / crash safety
//
// Per session, in this exact order:
//
//  1. Page the full transcript out of transcript_event in ascending seq
//     (pageWholeHotTranscript).
//  2. Serialize to the JSONL-gz cold-object-contract format
//     (ARCHITECTURE.md "Cold-object contract"), one events.Event per line
//     (encodeArchiveObject).
//  3. Upload to sessions/{session_id}.jsonl.gz (uploadTranscript).
//  4. Verify the uploaded object round-trips (verifyUploadedObject) --
//     re-download, decode via session.DecodeArchiveObject (the same
//     function Read/hydrateArchive use), count lines.
//  5. Commit the transcript_archive index row (session.ArchiveStore.Put).
//  6. Only then trim the hot-tier rows and stamp hot_trimmed_at
//     (trimHotTier).
//
// A crash before step 5 leaves no transcript_archive row, so
// ListArchiveEligible selects the session again on the next run and
// archiveSession redoes steps 1-5 from scratch (re-uploading the same
// deterministic key is a no-op, not a duplicate -- a terminal session's
// transcript never grows again). A crash after step 5 but before step 6
// leaves a transcript_archive row with hot_trimmed_at still NULL;
// ListArchiveEligible's second case selects it again regardless of TTL,
// and archiveSession (seeing an existing index row) skips straight to
// step 6 without re-uploading. TrimHot itself refuses to run without an
// existing index row (session/transcript.go's ErrNoArchiveIndex) -- the
// step 5-before-6 ordering is enforced in code, not just by this doc
// comment.
func (a *Activities) RunArchiveBatch(ctx context.Context, cfg ArchiveConfig) (ArchiveResult, error) {
	if a.S3 == nil {
		return ArchiveResult{}, fmt.Errorf("archive batch requested but no S3 client is configured (WHAGENT_S3_BUCKET unset)")
	}

	cutoff := time.Now().Add(-cfg.TranscriptTTL)
	ids, err := a.Store.Sessions().ListArchiveEligible(ctx, cutoff, cfg.BatchSize)
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("list archive-eligible sessions: %w", err)
	}

	archived := 0
	for _, id := range ids {
		if err := a.archiveSession(ctx, id); err != nil {
			logging.Get("archive").ErrorContext(ctx, "archive session failed", "session_id", id, "error", err)
			continue
		}
		archived++
	}
	return ArchiveResult{ArchivedCount: archived}, nil
}

// archiveObjectKey mirrors the cold-object contract's key format
// (ARCHITECTURE.md "Cold-object contract", session.ArchiveIndex's doc
// comment): "sessions/{session_id}.jsonl.gz".
func archiveObjectKey(sessionID uuid.UUID) string {
	return fmt.Sprintf("sessions/%s.jsonl.gz", sessionID)
}

// transcriptPageSize bounds each Read call archiveSession issues while
// paging a session's whole transcript out of the hot tier -- same
// pagination shape as worker/context.go's readWholeTranscript
// (transcriptReadPageSize), just a file-local constant since archival has
// no reason to share context.go's.
const transcriptPageSize = 200

// archiveSession runs the write-order contract (see RunArchiveBatch's doc
// comment) for exactly one sessionID. It is idempotent across crashes at
// any point: called again for a session with no transcript_archive row,
// it redoes upload+verify+commit from scratch; called again for a session
// that already has one (with hot_trimmed_at still NULL), it skips straight
// to trimming.
func (a *Activities) archiveSession(ctx context.Context, sessionID uuid.UUID) error {
	idx, err := a.Store.Archive().Get(ctx, sessionID)
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
			logging.Get("archive").WarnContext(ctx, "archive upload retried and succeeded", "session_id", sessionID)
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
func (a *Activities) uploadTranscript(ctx context.Context, sessionID uuid.UUID) (session.ArchiveIndex, bool, error) {
	evs, err := a.pageWholeHotTranscript(ctx, sessionID)
	if err != nil {
		return session.ArchiveIndex{}, false, fmt.Errorf("page transcript: %w", err)
	}

	body, err := encodeArchiveObject(evs)
	if err != nil {
		return session.ArchiveIndex{}, false, fmt.Errorf("encode archive object: %w", err)
	}

	key := archiveObjectKey(sessionID)
	retriedUpload, err := a.S3.Exists(ctx, key)
	if err != nil {
		return session.ArchiveIndex{}, false, fmt.Errorf("check existing archive object: %w", err)
	}

	if _, err := a.S3.Upload(ctx, key, body, &s3.UploadOptions{
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
		S3Bucket:   a.S3.GetBucket(),
		S3Key:      key,
		EventCount: int64(len(evs)),
	}
	if len(evs) > 0 {
		idx.MinSeq = evs[0].Seq
		idx.MaxSeq = evs[len(evs)-1].Seq
	}
	if err := a.Store.Archive().Put(ctx, idx); err != nil {
		return session.ArchiveIndex{}, false, fmt.Errorf("commit archive index: %w", err)
	}
	return idx, retriedUpload, nil
}

// pageWholeHotTranscript pages every transcript_event row for sessionID in
// ascending seq via TranscriptStore.Read -- same forward-pagination shape
// as worker/context.go's readWholeTranscript. a.Store is never given a
// WithS3 option, so Read here can only ever serve from the hot tier --
// exactly what write-order step 1 needs, with no risk of double-counting
// a session's own not-yet-committed archive object.
func (a *Activities) pageWholeHotTranscript(ctx context.Context, sessionID uuid.UUID) ([]events.Event, error) {
	var all []events.Event
	fromSeq := int64(0)
	for {
		page, err := a.Store.Transcript().Read(ctx, sessionID, fromSeq, transcriptPageSize)
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
// (ARCHITECTURE.md "Cold-object contract"): gzip-compressed JSON Lines,
// one events.Event per line, in the order given -- callers
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
// function the real tier-transparent reader uses -- so a successful
// verify means the object is genuinely readable, not merely present.
func (a *Activities) verifyUploadedObject(ctx context.Context, key string, wantCount int) error {
	exists, err := a.S3.Exists(ctx, key)
	if err != nil {
		return fmt.Errorf("check uploaded object exists: %w", err)
	}
	if !exists {
		return fmt.Errorf("uploaded object %q not found immediately after upload", key)
	}

	data, err := a.S3.Download(ctx, key)
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
// rather than returning an error.
func (a *Activities) trimHotTier(ctx context.Context, idx session.ArchiveIndex) error {
	if idx.SessionID == uuid.Nil {
		panic("archive: refusing to trim hot tier without a committed transcript_archive row")
	}

	if _, err := a.Store.Transcript().TrimHot(ctx, idx.SessionID); err != nil {
		return fmt.Errorf("trim hot transcript: %w", err)
	}
	if err := a.Store.Archive().MarkHotTrimmed(ctx, idx.SessionID); err != nil {
		return fmt.Errorf("mark hot trimmed: %w", err)
	}
	return nil
}
