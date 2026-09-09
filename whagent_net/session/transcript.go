package session

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/whagent_net/events"
)

// TurnContext is a `turn_context` row (LB1): the ordered event-ID list a
// turn's context was built from, so a debug GetTurnContext (C24) needs no
// schema change. TranscriptStore.SaveTurnContext (issue #2114) is the
// writer, called from the worker's BuildContext activity.
type TurnContext struct {
	SessionID uuid.UUID
	Turn      int
	EventIDs  []uuid.UUID
}

// TranscriptStore is the `transcript_event` table's repository interface.
// The committed row, the RabbitMQ message body, and the S3 jsonl line are
// all the same `events.Event` record (LB1) -- there is exactly one Go type
// for it, owned by whagent_net/events, and this store neither defines nor
// accepts a parallel DTO.
//
// Read and ReadByIDs are tier-transparent (FR8, issue #2240): both serve
// from `transcript_event` first and only consult `transcript_archive` /
// hydrate the archived S3 object when hot rows don't already answer the
// call in full -- see transcriptStore's doc comment for the conditions and
// //whagent_net/ARCHITECTURE.md "Transcript storage tiers" for the full
// contract shared with FR7's archiver.
type TranscriptStore interface {
	// Append allocates the next per-session seq and inserts a new event in
	// the same transaction, returning the committed events.Event (including
	// its assigned Seq and CommittedAt). On success it also publishes the
	// event onto the `whagent/events` bus -- see the transcriptStore.Append
	// doc comment for the commit/publish ordering guarantee.
	Append(ctx context.Context, sessionID uuid.UUID, turn int, eventType string, payload json.RawMessage) (events.Event, error)
	// AppendIfAbsent is Append's retry-safe sibling (issue #2114, FR4/
	// NFR1): if a row for (sessionID, turn, eventType) already exists it is
	// returned unchanged -- no new insert, no re-publish -- instead of
	// appending a duplicate. Temporal's at-least-once activity execution
	// can re-invoke the activity that calls this after a prior attempt's
	// Append already committed but whose success was lost in transit
	// (worker crash, RPC timeout); the worker's BuildContext activity
	// (issue #2114) is the first caller, appending a turn's new user-input
	// event.
	AppendIfAbsent(ctx context.Context, sessionID uuid.UUID, turn int, eventType string, payload json.RawMessage) (events.Event, error)
	// Read returns events for sessionID in commit order (by seq), starting
	// at fromSeq (inclusive) and returning at most limit rows -- the shape
	// ReadTranscript's pagination and resume-from-seq rely on.
	Read(ctx context.Context, sessionID uuid.UUID, fromSeq int64, limit int) ([]events.Event, error)
	// ReadByIDs returns exactly the rows named by eventIDs, in commit order
	// (by seq) -- not necessarily the order eventIDs was given in. Used by
	// the worker's CallModel activity (issue #2114) to re-read a turn's
	// context event-ID list (BuildContext's selection) into transcript
	// bodies at the activity boundary, rather than the bodies ever crossing
	// that boundary themselves (ARCHITECTURE.md "Activity payload
	// discipline": activities pass event IDs, not transcript bodies). An id
	// with no matching row is silently absent from the result, not an
	// error.
	ReadByIDs(ctx context.Context, sessionID uuid.UUID, eventIDs []uuid.UUID) ([]events.Event, error)
	// SaveTurnContext idempotently upserts a `turn_context` row: the
	// ordered event-ID list a turn's context was built from (issue #2114's
	// BuildContext activity is the sole writer). Safe to call more than
	// once for the same (SessionID, Turn) -- a retried BuildContext
	// activity overwrites with the same deterministically-recomputed list
	// rather than erroring on the primary key.
	SaveTurnContext(ctx context.Context, tc TurnContext) error
}

// transcriptStore is the Postgres-backed TranscriptStore implementation.
// pub may be nil (publisher disabled by config): Append then commits
// exactly as if pub were always present, it simply skips the publish step.
//
// s3 may also be nil (issue #2240, FR8) -- "hot only", which is
// `worker`'s configuration (session.New is never called WithS3 there):
// Read/ReadByIDs still work exactly as before this task, they simply never
// consult `transcript_archive`. A nil s3 with a `transcript_archive` row
// present is not an error; it just means this particular store instance
// has nothing to hydrate with, so it serves whatever hot has. Only `api`
// (the sole ReadTranscript-serving process) is expected to construct its
// Store WithS3.
type transcriptStore struct {
	pool *pgxpool.Pool
	pub  events.PublisherInterface
	s3   *s3.Client
}

var _ TranscriptStore = transcriptStore{}

// Append allocates eventID itself (UUIDv7, time-ordered per LB1) and
// serializes seq allocation for sessionID via a transaction-scoped
// advisory lock (pg_advisory_xact_lock, released automatically at
// commit/rollback) keyed on sessionID's hash -- this is what makes
// concurrent Appends to the SAME session produce strictly increasing,
// non-duplicate seq values (the `UNIQUE (session_id, seq)` constraint is
// the last line of defense, not the mechanism); Appends to DIFFERENT
// sessions never contend for this lock and proceed fully in parallel.
//
// Publish-after-commit (NFR2): the event is only published to pub once the
// Postgres transaction has committed, so a publish retry can re-send a
// duplicate but the bus can never see an event that was never durably
// committed. A publish failure is therefore never allowed to roll back or
// fail this call -- it is logged at WARNING (the append completed, but not
// exactly as expected; AGENTS.md § Logging Levels) and Append still
// returns the committed event successfully. Consumers dedup on EventID and
// order on Seq, so the resulting at-least-once delivery is safe.
func (s transcriptStore) Append(ctx context.Context, sessionID uuid.UUID, turn int, eventType string, payload json.RawMessage) (events.Event, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return events.Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := lockSessionTx(ctx, tx, sessionID); err != nil {
		return events.Event{}, err
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		return events.Event{}, fmt.Errorf("generate event id: %w", err)
	}
	ev, err := insertEventTx(ctx, tx, eventID, sessionID, turn, eventType, payload)
	if err != nil {
		return events.Event{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return events.Event{}, fmt.Errorf("commit: %w", err)
	}

	publish(ctx, s.pub, ev)
	return ev, nil
}

// AppendIfAbsent is TranscriptStore.AppendIfAbsent's implementation: the
// existence check and the insert both run inside the same
// lockSessionTx-guarded transaction as Append, so a concurrent Append/
// AppendIfAbsent for the same session can never race between "check" and
// "insert" -- the per-session advisory lock (lockSessionTx) already
// serializes every writer, exactly as it does for Append's seq allocation.
func (s transcriptStore) AppendIfAbsent(ctx context.Context, sessionID uuid.UUID, turn int, eventType string, payload json.RawMessage) (events.Event, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return events.Event{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := lockSessionTx(ctx, tx, sessionID); err != nil {
		return events.Event{}, err
	}

	existing, found, err := findEventTx(ctx, tx, sessionID, turn, eventType)
	if err != nil {
		return events.Event{}, err
	}
	if found {
		// A prior attempt already committed this event -- commit the
		// (otherwise empty) transaction to release the advisory lock
		// cleanly and return the existing row unchanged. Deliberately no
		// re-publish: the bus already saw this event on the attempt that
		// actually inserted it (or will on a later legitimate republish of
		// that same commit), and publishing again here on every retry
		// would just be noise consumers have to dedup for no reason.
		if err := tx.Commit(ctx); err != nil {
			return events.Event{}, fmt.Errorf("commit: %w", err)
		}
		return existing, nil
	}

	eventID, err := uuid.NewV7()
	if err != nil {
		return events.Event{}, fmt.Errorf("generate event id: %w", err)
	}
	ev, err := insertEventTx(ctx, tx, eventID, sessionID, turn, eventType, payload)
	if err != nil {
		return events.Event{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return events.Event{}, fmt.Errorf("commit: %w", err)
	}

	publish(ctx, s.pub, ev)
	return ev, nil
}

// lockSessionTx takes the same per-session advisory lock Append has always
// used to serialize seq allocation (pg_advisory_xact_lock, released
// automatically at commit/rollback), keyed on sessionID's hash. Shared by
// every transcript_event writer (Append, AppendIfAbsent, Store.CommitTurn)
// so the lock key has exactly one definition.
func lockSessionTx(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, sessionID); err != nil {
		return fmt.Errorf("lock session for append: %w", err)
	}
	return nil
}

// insertEventTx inserts one transcript_event row within tx (caller must
// already hold lockSessionTx's lock), allocating the next per-session seq.
// Shared by every writer that inserts a row (Append, AppendIfAbsent,
// Store.CommitTurn) so seq allocation has exactly one implementation.
func insertEventTx(ctx context.Context, tx pgx.Tx, eventID, sessionID uuid.UUID, turn int, eventType string, payload json.RawMessage) (events.Event, error) {
	var ev events.Event
	err := tx.QueryRow(ctx, `
		INSERT INTO transcript_event (event_id, session_id, seq, turn, type, payload)
		SELECT $1, $2, COALESCE(MAX(seq), 0) + 1, $3, $4, $5
		FROM transcript_event
		WHERE session_id = $2
		RETURNING event_id, session_id, seq, turn, type, payload, committed_at
	`, eventID, sessionID, turn, eventType, payload).Scan(
		&ev.EventID, &ev.SessionID, &ev.Seq, &ev.Turn, &ev.Type, &ev.Payload, &ev.CommittedAt,
	)
	if err != nil {
		return events.Event{}, fmt.Errorf("append transcript event: %w", err)
	}
	return ev, nil
}

// findEventTx looks for an existing transcript_event row for (sessionID,
// turn, eventType) within tx -- the existence check AppendIfAbsent and
// Store.CommitTurn both use to decide "already committed by a prior
// attempt" vs. "needs inserting".
func findEventTx(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID, turn int, eventType string) (events.Event, bool, error) {
	var ev events.Event
	err := tx.QueryRow(ctx, `
		SELECT event_id, session_id, seq, turn, type, payload, committed_at
		FROM transcript_event
		WHERE session_id = $1 AND turn = $2 AND type = $3
	`, sessionID, turn, eventType).Scan(
		&ev.EventID, &ev.SessionID, &ev.Seq, &ev.Turn, &ev.Type, &ev.Payload, &ev.CommittedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return events.Event{}, false, nil
		}
		return events.Event{}, false, fmt.Errorf("check existing transcript event: %w", err)
	}
	return ev, true, nil
}

// publish is the shared publish-after-commit step (NFR2, see Append's doc
// comment for the full guarantee) every transcript_event writer uses
// (transcriptStore.Append/AppendIfAbsent, Store.CommitTurn in
// turn_commit.go): a publish failure is logged at WARNING and swallowed,
// never surfaced as an error -- the row is already durably committed by
// the time this runs. pub may be nil (publishing disabled by config), in
// which case this is a no-op.
func publish(ctx context.Context, pub events.PublisherInterface, ev events.Event) {
	if pub == nil {
		return
	}
	if err := pub.Publish(ctx, ev); err != nil {
		slog.WarnContext(ctx, "publish transcript event failed; row already committed",
			"session_id", ev.SessionID,
			"event_id", ev.EventID,
			"seq", ev.Seq,
			"type", ev.Type,
			"error", err,
		)
	}
}

// Read returns up to limit events for sessionID in seq order starting at
// fromSeq (inclusive) -- the shape both plain pagination and
// resume-from-seq rely on.
//
// Tier-transparent (FR8, issue #2240): hot rows are always read first. If
// they already fill the page (len == limit) -- or limit <= 0, i.e. "no
// cap", which hot alone can always answer authoritatively -- that is the
// answer; no S3 call is ever made for a session that hasn't been archived,
// or one that has but is still fully served by hot rows (archived-but-not-
// yet-trimmed). Only when hot did NOT fill the page does Read consult
// `transcript_archive`: no row there means hot's (possibly short) result
// is the whole transcript, not an error. A row there but no S3 client
// configured on this store falls back to hot-only (see transcriptStore's
// doc comment on s3 -- this is `worker`'s normal case, not an error
// either). Only with both a row and a client does Read hydrate the
// archived object and merge it with hot in ascending seq order, hot
// winning any duplicate seq (see mergeTiers).
func (s transcriptStore) Read(ctx context.Context, sessionID uuid.UUID, fromSeq int64, limit int) ([]events.Event, error) {
	hot, err := s.readHot(ctx, sessionID, fromSeq, limit)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || len(hot) == limit {
		return hot, nil
	}

	idx, err := (archiveStore{pool: s.pool}).Get(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("read transcript events: %w", err)
	}
	if idx == nil || s.s3 == nil {
		return hot, nil
	}

	cold, err := s.hydrateArchive(ctx, sessionID, *idx)
	if err != nil {
		return nil, err
	}
	return mergeTiers(hot, cold, fromSeq, limit), nil
}

// readHot is Read's hot-tier-only query -- the same query Read has always
// run, extracted so Read can call it once and separately decide whether
// the cold tier needs consulting.
func (s transcriptStore) readHot(ctx context.Context, sessionID uuid.UUID, fromSeq int64, limit int) ([]events.Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_id, session_id, seq, turn, type, payload, committed_at
		FROM transcript_event
		WHERE session_id = $1 AND seq >= $2
		ORDER BY seq
		LIMIT $3
	`, sessionID, fromSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("read transcript events: %w", err)
	}
	defer rows.Close()

	var evs []events.Event
	for rows.Next() {
		var ev events.Event
		if err := rows.Scan(&ev.EventID, &ev.SessionID, &ev.Seq, &ev.Turn, &ev.Type, &ev.Payload, &ev.CommittedAt); err != nil {
			return nil, fmt.Errorf("scan transcript event: %w", err)
		}
		evs = append(evs, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read transcript events: %w", err)
	}
	return evs, nil
}

// hydrateArchive downloads and decodes idx's S3 object (the cold-object
// contract: gzipped JSON Lines at idx.S3Key, one events.Event per line --
// see ARCHITECTURE.md "Transcript storage tiers"). A download or decode
// failure is logged at ERROR (the archive index row promised an object
// that could not be read -- a genuine failure, not an expected condition;
// AGENTS.md § Logging Levels) and returned as an error, never silently
// swallowed into a truncated transcript.
func (s transcriptStore) hydrateArchive(ctx context.Context, sessionID uuid.UUID, idx ArchiveIndex) ([]events.Event, error) {
	data, err := s.s3.Download(ctx, idx.S3Key)
	if err != nil {
		slog.ErrorContext(ctx, "hydrate archived transcript: download failed",
			"session_id", sessionID, "s3_bucket", idx.S3Bucket, "s3_key", idx.S3Key, "error", err)
		return nil, fmt.Errorf("hydrate archived transcript for session %s: %w", sessionID, err)
	}
	cold, err := decodeArchiveObject(data)
	if err != nil {
		slog.ErrorContext(ctx, "hydrate archived transcript: decode failed",
			"session_id", sessionID, "s3_bucket", idx.S3Bucket, "s3_key", idx.S3Key, "error", err)
		return nil, fmt.Errorf("hydrate archived transcript for session %s: %w", sessionID, err)
	}
	return cold, nil
}

// decodeArchiveObject decodes a cold-object-contract body (gzipped JSON
// Lines, one events.Event per line, ascending seq -- ARCHITECTURE.md
// "Transcript storage tiers") into the events it holds, in file order.
func decodeArchiveObject(gz []byte) ([]events.Event, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("open gzip archive object: %w", err)
	}
	defer zr.Close()

	var evs []events.Event
	scanner := bufio.NewScanner(zr)
	// Default bufio.Scanner token limit (64KiB) is too small for a
	// transcript event whose payload is a large tool result; widen to
	// 16MiB, matching this repo's other line-oriented decoders of
	// arbitrarily large JSON payloads.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev events.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("decode archive object line: %w", err)
		}
		evs = append(evs, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan archive object: %w", err)
	}
	return evs, nil
}

// mergeTiers combines hot and cold (each already ascending by seq --
// readHot's ORDER BY seq and the cold-object contract's ascending seq,
// respectively) into one ascending, duplicate-free sequence starting at
// fromSeq and capped at limit (limit <= 0 means no cap). A seq present in
// both is the same event (LB1) and is only ever emitted once, from hot --
// the tier ReadTranscript/callers already trust for a session that hasn't
// finished being trimmed yet.
func mergeTiers(hot, cold []events.Event, fromSeq int64, limit int) []events.Event {
	// cold covers the whole archived object (potentially back to seq 1);
	// skip straight to fromSeq so the merge below never has to special-case
	// a cold event older than what was actually requested.
	ci := 0
	for ci < len(cold) && cold[ci].Seq < fromSeq {
		ci++
	}

	merged := make([]events.Event, 0, len(hot)+(len(cold)-ci))
	hi := 0
	for hi < len(hot) || ci < len(cold) {
		switch {
		case hi >= len(hot):
			merged = append(merged, cold[ci])
			ci++
		case ci >= len(cold):
			merged = append(merged, hot[hi])
			hi++
		case hot[hi].Seq < cold[ci].Seq:
			merged = append(merged, hot[hi])
			hi++
		case hot[hi].Seq > cold[ci].Seq:
			merged = append(merged, cold[ci])
			ci++
		default: // equal seq: hot wins, cold's copy of the same event is dropped
			merged = append(merged, hot[hi])
			hi++
			ci++
		}
		if limit > 0 && len(merged) == limit {
			return merged
		}
	}
	return merged
}

// ReadByIDs returns eventIDs' rows in seq order -- an id with no matching
// row anywhere (hot or cold) is silently absent from the result, not an
// error. Empty/nil eventIDs short-circuits to an empty result without a
// round trip.
//
// Tier-transparent (FR8, issue #2240): reads hot first; only when at least
// one requested id is still unaccounted for does it consult
// `transcript_archive` and hydrate (same idx==nil / s.s3==nil "nothing to
// hydrate with" fallback to hot-only as Read -- see Read's doc comment).
func (s transcriptStore) ReadByIDs(ctx context.Context, sessionID uuid.UUID, eventIDs []uuid.UUID) ([]events.Event, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}

	hot, err := s.readByIDsHot(ctx, sessionID, eventIDs)
	if err != nil {
		return nil, err
	}

	wanted := make(map[uuid.UUID]struct{}, len(eventIDs))
	for _, id := range eventIDs {
		wanted[id] = struct{}{}
	}
	for _, ev := range hot {
		delete(wanted, ev.EventID)
	}
	if len(wanted) == 0 {
		return hot, nil
	}

	idx, err := (archiveStore{pool: s.pool}).Get(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("read transcript events by id: %w", err)
	}
	if idx == nil || s.s3 == nil {
		return hot, nil
	}

	cold, err := s.hydrateArchive(ctx, sessionID, *idx)
	if err != nil {
		return nil, err
	}

	merged := append([]events.Event{}, hot...)
	for _, ev := range cold {
		if _, ok := wanted[ev.EventID]; ok {
			merged = append(merged, ev)
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Seq < merged[j].Seq })
	return merged, nil
}

// readByIDsHot is ReadByIDs' hot-tier-only query -- the same query
// ReadByIDs has always run, extracted so ReadByIDs can call it once and
// separately decide whether the cold tier needs consulting.
func (s transcriptStore) readByIDsHot(ctx context.Context, sessionID uuid.UUID, eventIDs []uuid.UUID) ([]events.Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_id, session_id, seq, turn, type, payload, committed_at
		FROM transcript_event
		WHERE session_id = $1 AND event_id = ANY($2)
		ORDER BY seq
	`, sessionID, eventIDs)
	if err != nil {
		return nil, fmt.Errorf("read transcript events by id: %w", err)
	}
	defer rows.Close()

	var evs []events.Event
	for rows.Next() {
		var ev events.Event
		if err := rows.Scan(&ev.EventID, &ev.SessionID, &ev.Seq, &ev.Turn, &ev.Type, &ev.Payload, &ev.CommittedAt); err != nil {
			return nil, fmt.Errorf("scan transcript event: %w", err)
		}
		evs = append(evs, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read transcript events by id: %w", err)
	}
	return evs, nil
}

// SaveTurnContext upserts a `turn_context` row -- ON CONFLICT DO UPDATE
// rather than DO NOTHING because a retried BuildContext activity (issue
// #2114) must succeed even if the row already exists, and the
// deterministically-recomputed event_ids on a legitimate retry is expected
// to be identical to what is already stored, so overwriting it is
// equivalent to a no-op in the case that matters. No advisory lock needed:
// `turn_context`'s own primary key (session_id, turn) is exactly the
// uniqueness this upsert relies on.
func (s transcriptStore) SaveTurnContext(ctx context.Context, tc TurnContext) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO turn_context (session_id, turn, event_ids)
		VALUES ($1, $2, $3)
		ON CONFLICT (session_id, turn) DO UPDATE SET event_ids = EXCLUDED.event_ids
	`, tc.SessionID, tc.Turn, tc.EventIDs)
	if err != nil {
		return fmt.Errorf("save turn context: %w", err)
	}
	return nil
}
