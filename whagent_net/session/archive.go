package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ArchiveIndex is a `transcript_archive` row (issue #2240, FR8/LB1): the
// cold-tier pointer for a session whose transcript has been written to S3
// as gzipped JSON Lines at "sessions/{session_id}.jsonl.gz" -- one
// whagent_net/events.Event per line, ascending Seq, never summarized/
// reshaped/dropped (LB1; see whagent_net/ARCHITECTURE.md "Transcript
// storage tiers" for the full cold-object contract this task's readers and
// FR7's archiver both agree on). EventCount/MinSeq/MaxSeq let a reader
// sanity-check what it downloads without opening the object first.
// HotTrimmedAt is written by FR7's archiver once the session's hot-tier
// rows are trimmed; this task's TranscriptStore never reads it -- a row
// existing here is itself the "hydrate from S3" signal, independent of
// whether hot rows have been trimmed yet.
type ArchiveIndex struct {
	SessionID    uuid.UUID
	S3Bucket     string
	S3Key        string
	EventCount   int64
	MinSeq       int64
	MaxSeq       int64
	ArchivedAt   time.Time
	HotTrimmedAt *time.Time
}

// ArchiveStore is the `transcript_archive` table's repository interface
// (issue #2240). FR7's archiver is the sole writer of Put/MarkHotTrimmed;
// TranscriptStore.Read (transcript.go) is Get's reader, deciding whether a
// session has a cold-tier object to hydrate from.
type ArchiveStore interface {
	// Get returns the archive index row for sessionID, or nil if the
	// session has never been archived -- not an error.
	Get(ctx context.Context, sessionID uuid.UUID) (*ArchiveIndex, error)
	// Put inserts the archive index row for a session the archiver has
	// just written to S3. A session is archived at most once in FR7's
	// design (a terminal session's transcript never grows again), so Put
	// is a plain INSERT, not an upsert -- a second Put for the same
	// session_id is a caller bug and surfaces as a primary-key violation
	// rather than being silently accepted.
	Put(ctx context.Context, idx ArchiveIndex) error
	// MarkHotTrimmed records that the archiver has trimmed sessionID's
	// hot-tier transcript_event rows, by setting hot_trimmed_at to now.
	// Returns pgx.ErrNoRows if sessionID has no archive row yet -- the
	// archiver must Put before it trims.
	MarkHotTrimmed(ctx context.Context, sessionID uuid.UUID) error
}

// archiveStore is the Postgres-backed ArchiveStore implementation.
type archiveStore struct{ pool *pgxpool.Pool }

var _ ArchiveStore = archiveStore{}

const archiveIndexColumns = `session_id, s3_bucket, s3_key, event_count, min_seq, max_seq, archived_at, hot_trimmed_at`

func scanArchiveIndex(row pgx.Row) (ArchiveIndex, error) {
	var idx ArchiveIndex
	if err := row.Scan(
		&idx.SessionID, &idx.S3Bucket, &idx.S3Key, &idx.EventCount, &idx.MinSeq, &idx.MaxSeq,
		&idx.ArchivedAt, &idx.HotTrimmedAt,
	); err != nil {
		return ArchiveIndex{}, err
	}
	return idx, nil
}

// Get returns nil (not an error) when sessionID has no archive row.
func (s archiveStore) Get(ctx context.Context, sessionID uuid.UUID) (*ArchiveIndex, error) {
	idx, err := scanArchiveIndex(s.pool.QueryRow(ctx, `
		SELECT `+archiveIndexColumns+`
		FROM transcript_archive
		WHERE session_id = $1
	`, sessionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get transcript archive index: %w", err)
	}
	return &idx, nil
}

// Put inserts idx. A duplicate session_id (a second archive of the same
// session) surfaces as the primary-key violation it is -- see ArchiveStore
// interface doc.
func (s archiveStore) Put(ctx context.Context, idx ArchiveIndex) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO transcript_archive (session_id, s3_bucket, s3_key, event_count, min_seq, max_seq)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, idx.SessionID, idx.S3Bucket, idx.S3Key, idx.EventCount, idx.MinSeq, idx.MaxSeq)
	if err != nil {
		return fmt.Errorf("put transcript archive index: %w", err)
	}
	return nil
}

// MarkHotTrimmed returns pgx.ErrNoRows if sessionID has no archive row.
func (s archiveStore) MarkHotTrimmed(ctx context.Context, sessionID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE transcript_archive SET hot_trimmed_at = NOW() WHERE session_id = $1
	`, sessionID)
	if err != nil {
		return fmt.Errorf("mark transcript archive hot-trimmed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
