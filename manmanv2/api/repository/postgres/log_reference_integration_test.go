//go:build integration

// Real-Postgres coverage for LogReferenceRepository. Unlike the CRUD
// repositories, this one is almost entirely query logic -- time-range
// filtering, min/max bounds, and a bucketed histogram -- so a round-trip test
// would prove almost nothing. These tests pin the boundary semantics each
// query actually has, against the real shipped migrations via
// //manmanv2/migrate/schema.
//
// The behaviours worth knowing before reading, all asserted below:
//
//   - The three time-range/min-max readers disagree on purpose.
//     ListByTimeRange filters on minute_timestamp AND state='complete'.
//     ListBySessionAndTimeRange filters on start_time/end_time overlap
//     (start_time <= rangeEnd AND end_time >= rangeStart) AND
//     state='complete'. GetMinMaxTimes uses minute_timestamp, complete only.
//     GetMinMaxTimesBySession uses start_time/end_time and applies NO state
//     filter at all.
//   - ListBySession is the one listing with no state filter.
//   - GetByMinute returns (nil, nil) -- not an error -- when nothing matches.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:log_reference_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	manman "github.com/whale-net/everything/manmanv2/models"
)

// logRefFixture carries a session (required by log_references' FK) and the
// SGC it belongs to, since several queries scope by sgc_id instead.
type logRefFixture struct {
	repo      *LogReferenceRepository
	pool      *pgxpool.Pool
	sessionID int64
	sgcID     int64
	// second session, so session-scoped queries can be told apart.
	otherSessionID int64
}

func newLogRefHarness(t *testing.T) logRefFixture {
	t.Helper()
	pool := newMigratedPool(t)
	ctx := context.Background()

	// log_references -> sessions -> server_game_configs -> game_configs -> games,
	// so the whole chain has to exist before a row can be inserted.
	var serverID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO servers (name) VALUES ($1) RETURNING server_id`, "logref-server",
	).Scan(&serverID); err != nil {
		t.Fatalf("seed server: %v", err)
	}

	game, err := NewGameRepository(pool).Create(ctx,
		&manman.Game{Name: "logref-game", Metadata: manman.JSONB{}})
	if err != nil {
		t.Fatalf("seed game: %v", err)
	}
	cfg, err := NewGameConfigRepository(pool).Create(ctx, &manman.GameConfig{
		GameID: game.GameID,
		Name:   "logref-config",
		Image:  "example:latest",
	})
	if err != nil {
		t.Fatalf("seed game config: %v", err)
	}
	sgc, err := NewServerGameConfigRepository(pool).Create(ctx, &manman.ServerGameConfig{
		ServerID:     serverID,
		GameConfigID: cfg.ConfigID,
		Status:       "running",
	})
	if err != nil {
		t.Fatalf("seed sgc: %v", err)
	}

	sessionID := seedLogRefSession(t, pool, sgc.SGCID)
	otherSessionID := seedLogRefSession(t, pool, sgc.SGCID)

	return logRefFixture{
		repo:           NewLogReferenceRepository(pool),
		pool:           pool,
		sessionID:      sessionID,
		sgcID:          sgc.SGCID,
		otherSessionID: otherSessionID,
	}
}

func seedLogRefSession(t *testing.T, pool *pgxpool.Pool, sgcID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO sessions (sgc_id, status) VALUES ($1, $2) RETURNING session_id`, sgcID, "running",
	).Scan(&id); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return id
}

// logRefOpts describes a log reference row to insert. StartTime doubles as the
// minute_timestamp when the latter is nil, which keeps the fixtures readable.
type logRefOpts struct {
	start    time.Time
	duration time.Duration
	lines    int32
	source   string
	state    string
	created  time.Time
	// minuteOverride, when set, decouples minute_timestamp from startTime --
	// needed to tell ListByTimeRange (minute_timestamp) apart from
	// ListBySessionAndTimeRange (start_time/end_time).
	minuteOverride *time.Time
}

func (f logRefFixture) insert(t *testing.T, sessionID int64, o logRefOpts) *manman.LogReference {
	t.Helper()

	end := o.start.Add(o.duration)
	minute := o.start
	if o.minuteOverride != nil {
		minute = *o.minuteOverride
	}
	created := o.created
	if created.IsZero() {
		created = o.start
	}
	state := o.state
	if state == "" {
		state = "complete"
	}
	sgc := f.sgcID

	ref := &manman.LogReference{
		SessionID:       sessionID,
		SGCID:           &sgc,
		FilePath:        "/var/log/" + o.source + ".log",
		StartTime:       o.start,
		EndTime:         end,
		LineCount:       o.lines,
		Source:          o.source,
		MinuteTimestamp: &minute,
		State:           state,
		CreatedAt:       created,
	}
	if err := f.repo.Create(context.Background(), ref); err != nil {
		t.Fatalf("insert log ref at %v: %v", o.start, err)
	}
	return ref
}

// base is an arbitrary fixed instant, chosen off a minute boundary so a
// truncation bug can't accidentally pass.
var base = time.Date(2026, 3, 14, 12, 34, 56, 0, time.UTC)

func (f logRefFixture) at(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }

// TestLogReferenceRepository_CreateAssignsIDAndRoundTrips proves Create
// assigns a log_id and that every column -- including the nullable sgc_id,
// minute_timestamp and appended_at -- reads back intact.
func TestLogReferenceRepository_CreateAssignsIDAndRoundTrips(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	start := f.at(0)
	minute := time.Date(2026, 3, 14, 12, 34, 0, 0, time.UTC)
	appended := f.at(90)
	sgc := f.sgcID

	ref := &manman.LogReference{
		SessionID:       f.sessionID,
		SGCID:           &sgc,
		FilePath:        "/var/log/stdout.log",
		StartTime:       start,
		EndTime:         start.Add(60 * time.Second),
		LineCount:       42,
		Source:          "stdout",
		MinuteTimestamp: &minute,
		State:           "complete",
		AppendedAt:      &appended,
		CreatedAt:       start,
	}
	if err := f.repo.Create(ctx, ref); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ref.LogID == 0 {
		t.Fatal("Create did not populate LogID")
	}

	listed, err := f.repo.ListBySession(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListBySession returned %d rows, want 1", len(listed))
	}
	got := listed[0]

	if got.SessionID != f.sessionID {
		t.Errorf("SessionID = %d, want %d", got.SessionID, f.sessionID)
	}
	if got.SGCID == nil || *got.SGCID != f.sgcID {
		t.Errorf("SGCID = %v, want %d", got.SGCID, f.sgcID)
	}
	if got.FilePath != "/var/log/stdout.log" {
		t.Errorf("FilePath = %q, want %q", got.FilePath, "/var/log/stdout.log")
	}
	if !got.StartTime.Equal(start) {
		t.Errorf("StartTime = %v, want %v", got.StartTime, start)
	}
	if !got.EndTime.Equal(start.Add(60 * time.Second)) {
		t.Errorf("EndTime = %v, want %v", got.EndTime, start.Add(60*time.Second))
	}
	if got.LineCount != 42 {
		t.Errorf("LineCount = %d, want 42", got.LineCount)
	}
	if got.Source != "stdout" {
		t.Errorf("Source = %q, want %q", got.Source, "stdout")
	}
	if got.MinuteTimestamp == nil || !got.MinuteTimestamp.Equal(minute) {
		t.Errorf("MinuteTimestamp = %v, want %v", got.MinuteTimestamp, minute)
	}
	if got.State != "complete" {
		t.Errorf("State = %q, want %q", got.State, "complete")
	}
	if got.AppendedAt == nil || !got.AppendedAt.Equal(appended) {
		t.Errorf("AppendedAt = %v, want %v", got.AppendedAt, appended)
	}
}

// TestLogReferenceRepository_ListBySessionOrdersByStartTime proves the
// listing is ordered by start_time (not insertion order), scoped to one
// session, and -- unlike every other query here -- carries NO state filter.
func TestLogReferenceRepository_ListBySessionOrdersByStartTime(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	// Insert out of chronological order to prove the ORDER BY.
	third := f.insert(t, f.sessionID, logRefOpts{start: f.at(200), duration: time.Second, lines: 3, source: "stdout"})
	first := f.insert(t, f.sessionID, logRefOpts{start: f.at(0), duration: time.Second, lines: 1, source: "stdout"})
	second := f.insert(t, f.sessionID, logRefOpts{start: f.at(100), duration: time.Second, lines: 2, source: "stdout"})
	// A pending row: ListBySession must still return it.
	pending := f.insert(t, f.sessionID, logRefOpts{start: f.at(150), duration: time.Second, lines: 9, source: "stdout", state: "pending"})
	// Another session's row must not appear.
	f.insert(t, f.otherSessionID, logRefOpts{start: f.at(0), duration: time.Second, lines: 5, source: "stdout"})

	listed, err := f.repo.ListBySession(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	want := []int64{first.LogID, second.LogID, pending.LogID, third.LogID}
	if len(listed) != len(want) {
		t.Fatalf("ListBySession returned %d rows, want %d", len(listed), len(want))
	}
	for i, id := range want {
		if listed[i].LogID != id {
			t.Fatalf("ListBySession order = %v, want %v (ordered by start_time)",
				logRefIDs(listed), want)
		}
	}
	for _, r := range listed {
		if r.SessionID != f.sessionID {
			t.Errorf("leaked log ref %d from session %d", r.LogID, r.SessionID)
		}
	}
}

// TestLogReferenceRepository_ListByTimeRangeBoundaries pins both ends of
// ListByTimeRange as inclusive on minute_timestamp, and that it keys off
// minute_timestamp rather than start_time.
func TestLogReferenceRepository_ListByTimeRangeBoundaries(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	lo := f.at(100)
	hi := f.at(200)

	onLo := f.insert(t, f.sessionID, logRefOpts{start: f.at(100), duration: time.Second, lines: 1, source: "stdout"})
	mid := f.insert(t, f.sessionID, logRefOpts{start: f.at(150), duration: time.Second, lines: 2, source: "stdout"})
	onHi := f.insert(t, f.sessionID, logRefOpts{start: f.at(200), duration: time.Second, lines: 3, source: "stdout"})
	beforeLo := f.insert(t, f.sessionID, logRefOpts{start: f.at(0), duration: time.Second, lines: 4, source: "stdout"})
	afterHi := f.insert(t, f.sessionID, logRefOpts{start: f.at(300), duration: time.Second, lines: 5, source: "stdout"})

	listed, err := f.repo.ListByTimeRange(ctx, f.sgcID, lo, hi)
	if err != nil {
		t.Fatalf("ListByTimeRange: %v", err)
	}

	got := logRefIDs(listed)
	want := []int64{onLo.LogID, mid.LogID, onHi.LogID}
	if len(got) != len(want) {
		t.Fatalf("ListByTimeRange returned %d rows (%v), want %d (%v) -- both bounds inclusive",
			len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ListByTimeRange = %v, want %v (ordered by minute_timestamp ASC)", got, want)
			break
		}
	}
	for _, excluded := range []int64{beforeLo.LogID, afterHi.LogID} {
		for _, r := range listed {
			if r.LogID == excluded {
				t.Errorf("ListByTimeRange included out-of-range log ref %d", excluded)
			}
		}
	}
}

// TestLogReferenceRepository_ListByTimeRangeUsesMinuteTimestampNotStartTime
// separates the two clocks: a row whose start_time is inside the window but
// whose minute_timestamp is outside must be excluded, proving the query keys
// off minute_timestamp.
func TestLogReferenceRepository_ListByTimeRangeUsesMinuteTimestampNotStartTime(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	inside := f.at(150)
	outside := f.at(10)

	// start_time inside the window, minute_timestamp outside it.
	f.insert(t, f.sessionID, logRefOpts{
		start: inside, duration: time.Second, lines: 1, source: "stdout",
		minuteOverride: &outside,
	})
	// The reverse: minute_timestamp inside, start_time outside.
	f.insert(t, f.sessionID, logRefOpts{
		start: outside, duration: time.Second, lines: 2, source: "stdout",
		minuteOverride: &inside,
	})

	listed, err := f.repo.ListByTimeRange(ctx, f.sgcID, f.at(100), f.at(200))
	if err != nil {
		t.Fatalf("ListByTimeRange: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListByTimeRange returned %d rows, want 1 -- it filters on minute_timestamp, not start_time", len(listed))
	}
	if !listed[0].StartTime.Equal(outside) {
		t.Errorf("matched row has start_time %v, want %v (the one whose minute_timestamp is in range)",
			listed[0].StartTime, outside)
	}
}

// TestLogReferenceRepository_ListByTimeRangeFiltersPendingState proves the
// state='complete' predicate: a pending row inside the window is excluded.
func TestLogReferenceRepository_ListByTimeRangeFiltersPendingState(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	f.insert(t, f.sessionID, logRefOpts{start: f.at(150), duration: time.Second, lines: 1, source: "stdout"})
	f.insert(t, f.sessionID, logRefOpts{start: f.at(150), duration: time.Second, lines: 2, source: "stdout", state: "pending"})

	listed, err := f.repo.ListByTimeRange(ctx, f.sgcID, f.at(100), f.at(200))
	if err != nil {
		t.Fatalf("ListByTimeRange: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListByTimeRange returned %d rows, want 1 (pending must be excluded)", len(listed))
	}
	if listed[0].LineCount != 1 {
		t.Errorf("returned the pending row (lines=%d), want the complete one (lines=1)", listed[0].LineCount)
	}
}

// TestLogReferenceRepository_ListBySessionAndTimeRangeUsesOverlap proves this
// query tests interval OVERLAP, not containment: a row spanning the whole
// window is included even though it extends past both ends, and a row
// entirely outside is excluded.
func TestLogReferenceRepository_ListBySessionAndTimeRangeUsesOverlap(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	// [0, 1000] fully contains the [100,200] window.
	spanning := f.insert(t, f.sessionID, logRefOpts{start: f.at(0), duration: 1000 * time.Second, lines: 1, source: "stdout"})
	// [50, 150] overlaps the low end.
	overlapLow := f.insert(t, f.sessionID, logRefOpts{start: f.at(50), duration: 100 * time.Second, lines: 2, source: "stdout"})
	// [150, 250] overlaps the high end.
	overlapHigh := f.insert(t, f.sessionID, logRefOpts{start: f.at(150), duration: 100 * time.Second, lines: 3, source: "stdout"})
	// [300, 400] is entirely after the window.
	f.insert(t, f.sessionID, logRefOpts{start: f.at(300), duration: 100 * time.Second, lines: 4, source: "stdout"})
	// A pending row that does overlap -- must still be excluded.
	f.insert(t, f.sessionID, logRefOpts{start: f.at(100), duration: 10 * time.Second, lines: 5, source: "stdout", state: "pending"})

	listed, err := f.repo.ListBySessionAndTimeRange(ctx, f.sessionID, f.at(100), f.at(200))
	if err != nil {
		t.Fatalf("ListBySessionAndTimeRange: %v", err)
	}

	got := logRefIDs(listed)
	// Ordered by start_time: the spanning row starts earliest (t+0), then the
	// low overlap (t+50), then the high overlap (t+150).
	want := []int64{spanning.LogID, overlapLow.LogID, overlapHigh.LogID}
	if len(got) != len(want) {
		t.Fatalf("ListBySessionAndTimeRange returned %d rows (%v), want %d (%v) -- overlap, not containment",
			len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ListBySessionAndTimeRange = %v, want %v (ordered by start_time ASC)", got, want)
			break
		}
	}
}

// TestLogReferenceRepository_ListBySessionAndTimeRangeIsSessionScoped proves
// the session filter, not just the time filter.
func TestLogReferenceRepository_ListBySessionAndTimeRangeIsSessionScoped(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	f.insert(t, f.sessionID, logRefOpts{start: f.at(100), duration: 100 * time.Second, lines: 1, source: "stdout"})
	other := f.insert(t, f.otherSessionID, logRefOpts{start: f.at(100), duration: 100 * time.Second, lines: 2, source: "stdout"})

	listed, err := f.repo.ListBySessionAndTimeRange(ctx, f.sessionID, f.at(100), f.at(200))
	if err != nil {
		t.Fatalf("ListBySessionAndTimeRange: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("returned %d rows, want 1", len(listed))
	}
	if listed[0].LogID == other.LogID {
		t.Error("leaked a log reference belonging to the other session")
	}
}

// TestLogReferenceRepository_GetByMinuteReturnsNilNotError pins the
// sentinel-free miss path: an absent minute yields (nil, nil), not an error.
// A caller that treats err != nil as the only miss signal would otherwise
// mis-handle this.
func TestLogReferenceRepository_GetByMinuteReturnsNilNotError(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	got, err := f.repo.GetByMinute(ctx, f.sgcID, base)
	if err != nil {
		t.Fatalf("GetByMinute on an empty table returned error %v, want nil", err)
	}
	if got != nil {
		t.Errorf("GetByMinute on an empty table returned %+v, want nil", got)
	}
}

// TestLogReferenceRepository_GetByMinutePicksNewestCreated proves the exact
// minute_timestamp match and that the ORDER BY created_at DESC LIMIT 1
// resolves a duplicated minute to the most recently created row.
func TestLogReferenceRepository_GetByMinutePicksNewestCreated(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	start := f.at(0)
	minute := time.Date(2026, 3, 14, 12, 34, 0, 0, time.UTC)

	// Same minute_timestamp, different created_at and different payload.
	older := f.insert(t, f.sessionID, logRefOpts{
		start: start, duration: time.Second, lines: 1, source: "stdout",
		minuteOverride: &minute, created: f.at(10),
	})
	newer := f.insert(t, f.sessionID, logRefOpts{
		start: start.Add(30 * time.Second), duration: time.Second, lines: 2, source: "stderr",
		minuteOverride: &minute, created: f.at(50),
	})
	// A different minute, which must not be picked up.
	f.insert(t, f.sessionID, logRefOpts{
		start: start.Add(90 * time.Second), duration: time.Second, lines: 3, source: "stdout",
	})

	got, err := f.repo.GetByMinute(ctx, f.sgcID, minute)
	if err != nil {
		t.Fatalf("GetByMinute: %v", err)
	}
	if got == nil {
		t.Fatal("GetByMinute returned nil, want the newest row for that minute")
	}
	if got.LogID != newer.LogID {
		t.Errorf("GetByMinute returned log_id %d, want %d (created_at DESC, LIMIT 1)", got.LogID, newer.LogID)
	}
	if got.LogID == older.LogID {
		t.Error("GetByMinute returned the older of two rows sharing a minute")
	}
	if got.Source != "stderr" {
		t.Errorf("Source = %q, want %q", got.Source, "stderr")
	}
}

// TestLogReferenceRepository_GetByMinuteIsSGCScoped proves the sgc_id
// predicate: a row for a different SGC sharing the minute is not returned.
func TestLogReferenceRepository_GetByMinuteIsSGCScoped(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	start := f.at(0)
	minute := time.Date(2026, 3, 14, 12, 34, 0, 0, time.UTC)
	f.insert(t, f.sessionID, logRefOpts{start: start, duration: time.Second, lines: 1, source: "stdout", minuteOverride: &minute})

	got, err := f.repo.GetByMinute(ctx, f.sgcID+999999, minute)
	if err != nil {
		t.Fatalf("GetByMinute with a foreign sgc_id returned error %v, want nil", err)
	}
	if got != nil {
		t.Errorf("GetByMinute with a foreign sgc_id returned log_id %d, want nil", got.LogID)
	}
}

// TestLogReferenceRepository_UpdateState proves UpdateState changes only the
// state column, leaving the row's times and payload alone.
func TestLogReferenceRepository_UpdateState(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	ref := f.insert(t, f.sessionID, logRefOpts{
		start: f.at(100), duration: 60 * time.Second, lines: 7, source: "stdout", state: "pending",
	})

	if err := f.repo.UpdateState(ctx, ref.LogID, "complete"); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	listed, err := f.repo.ListBySession(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListBySession returned %d rows, want 1", len(listed))
	}
	got := listed[0]
	if got.State != "complete" {
		t.Errorf("State = %q, want %q", got.State, "complete")
	}
	if got.LineCount != 7 {
		t.Errorf("LineCount = %d, want 7 (UpdateState must not touch it)", got.LineCount)
	}
	if !got.StartTime.Equal(f.at(100)) {
		t.Errorf("StartTime = %v, want %v (UpdateState must not touch it)", got.StartTime, f.at(100))
	}
}

// TestLogReferenceRepository_UpdateStateUnknownIDIsSilentNoOp documents that
// UpdateState does not check RowsAffected.
func TestLogReferenceRepository_UpdateStateUnknownIDIsSilentNoOp(t *testing.T) {
	f := newLogRefHarness(t)

	if err := f.repo.UpdateState(context.Background(), 999999, "complete"); err != nil {
		t.Fatalf("UpdateState on an unknown log_id returned error %v, want nil", err)
	}
}

// TestLogReferenceRepository_GetMinMaxTimes covers the complete-only
// min/max over minute_timestamp, including the empty case where SQL MIN/MAX
// yields NULL and the repository must hand back nil pointers rather than the
// Go zero time.
func TestLogReferenceRepository_GetMinMaxTimes(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	t.Run("empty returns nil bounds", func(t *testing.T) {
		minTime, maxTime, err := f.repo.GetMinMaxTimes(ctx, f.sgcID)
		if err != nil {
			t.Fatalf("GetMinMaxTimes: %v", err)
		}
		if minTime != nil || maxTime != nil {
			t.Errorf("GetMinMaxTimes on an empty table = (%v, %v), want (nil, nil)", minTime, maxTime)
		}
	})

	earliest := f.at(100)
	latest := f.at(500)
	// Inserted out of order; a pending row with an even later minute that
	// must not widen the range.
	f.insert(t, f.sessionID, logRefOpts{start: latest, duration: time.Second, lines: 1, source: "stdout"})
	f.insert(t, f.sessionID, logRefOpts{start: earliest, duration: time.Second, lines: 2, source: "stdout"})
	f.insert(t, f.sessionID, logRefOpts{start: f.at(900), duration: time.Second, lines: 3, source: "stdout", state: "pending"})

	t.Run("bounds are min/max of complete minute_timestamps", func(t *testing.T) {
		minTime, maxTime, err := f.repo.GetMinMaxTimes(ctx, f.sgcID)
		if err != nil {
			t.Fatalf("GetMinMaxTimes: %v", err)
		}
		if minTime == nil || !minTime.Equal(earliest) {
			t.Errorf("minTime = %v, want %v", minTime, earliest)
		}
		if maxTime == nil || !maxTime.Equal(latest) {
			t.Errorf("maxTime = %v, want %v (the pending row at +900s must be excluded)", maxTime, latest)
		}
	})
}

// TestLogReferenceRepository_GetMinMaxTimesBySession proves this sibling
// reader is NOT the same query: it spans start_time/end_time (not
// minute_timestamp) and applies no state filter, so a pending row still
// widens the range.
func TestLogReferenceRepository_GetMinMaxTimesBySession(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	t.Run("empty returns nil bounds", func(t *testing.T) {
		minTime, maxTime, err := f.repo.GetMinMaxTimesBySession(ctx, f.sessionID)
		if err != nil {
			t.Fatalf("GetMinMaxTimesBySession: %v", err)
		}
		if minTime != nil || maxTime != nil {
			t.Errorf("GetMinMaxTimesBySession on an empty session = (%v, %v), want (nil, nil)", minTime, maxTime)
		}
	})

	firstStart := f.at(100)
	lastStart := f.at(400)
	lastEnd := f.at(460)

	f.insert(t, f.sessionID, logRefOpts{start: firstStart, duration: 60 * time.Second, lines: 1, source: "stdout"})
	// Pending, and with a *later* end_time -- it must still count, which is
	// the behavioural difference from GetMinMaxTimes.
	f.insert(t, f.sessionID, logRefOpts{start: lastStart, duration: 60 * time.Second, lines: 2, source: "stdout", state: "pending"})
	// Another session, which must not leak into the bounds.
	f.insert(t, f.otherSessionID, logRefOpts{start: f.at(900), duration: 10 * time.Second, lines: 3, source: "stdout"})

	minTime, maxTime, err := f.repo.GetMinMaxTimesBySession(ctx, f.sessionID)
	if err != nil {
		t.Fatalf("GetMinMaxTimesBySession: %v", err)
	}
	if minTime == nil || !minTime.Equal(firstStart) {
		t.Errorf("minTime = %v, want %v (earliest start_time)", minTime, firstStart)
	}
	if maxTime == nil || !maxTime.Equal(lastEnd) {
		t.Errorf("maxTime = %v, want %v (latest end_time, pending included)", maxTime, lastEnd)
	}
}

// TestLogReferenceRepository_GetHistogramBySessionGroupsAndSums proves the
// histogram buckets by epoch-aligned window, sums line_count within a
// (bucket, source) pair, keys the outer map by bucket and the inner by
// source, and excludes pending rows.
func TestLogReferenceRepository_GetHistogramBySession(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	const bucket = int64(60)
	anchor := f.at(0)

	// Three rows in the same bucket as each other (identical start_time),
	// across two sources, so the inner sum is exercised.
	f.insert(t, f.sessionID, logRefOpts{start: anchor, duration: time.Second, lines: 10, source: "stdout"})
	f.insert(t, f.sessionID, logRefOpts{start: anchor, duration: time.Second, lines: 5, source: "stdout"})
	f.insert(t, f.sessionID, logRefOpts{start: anchor, duration: time.Second, lines: 3, source: "stderr"})
	// Pending, same bucket: must not be counted.
	f.insert(t, f.sessionID, logRefOpts{start: anchor, duration: time.Second, lines: 100, source: "stdout", state: "pending"})
	// A later bucket, an hour out -- always a different bucket for a 60s size.
	far := f.at(3600)
	f.insert(t, f.sessionID, logRefOpts{start: far, duration: time.Second, lines: 7, source: "stdout"})
	// Another session: must not appear.
	f.insert(t, f.otherSessionID, logRefOpts{start: anchor, duration: time.Second, lines: 999, source: "stdout"})

	hist, err := f.repo.GetHistogramBySession(ctx, f.sessionID, bucket, nil, nil)
	if err != nil {
		t.Fatalf("GetHistogramBySession: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("histogram has %d buckets, want 2: %v", len(hist), hist)
	}

	nearBucket := epochBucket(anchor, bucket)
	farBucket := epochBucket(far, bucket)

	if got := hist[nearBucket]; got == nil {
		t.Fatalf("no bucket at %d; got buckets %v", nearBucket, hist)
	} else {
		if got["stdout"] != 15 {
			t.Errorf("bucket %d stdout = %d, want 15 (10+5 summed, pending excluded)", nearBucket, got["stdout"])
		}
		if got["stderr"] != 3 {
			t.Errorf("bucket %d stderr = %d, want 3", nearBucket, got["stderr"])
		}
	}

	if got := hist[farBucket]; got == nil {
		t.Fatalf("no bucket at %d; got buckets %v", farBucket, hist)
	} else if got["stdout"] != 7 {
		t.Errorf("bucket %d stdout = %d, want 7", farBucket, got["stdout"])
	}
}

// TestLogReferenceRepository_GetHistogramBySessionOptionalTimeBounds proves
// the nil-pointer bounds mean "unbounded" (0), and that supplying epoch-second
// bounds filters on start_time.
func TestLogReferenceRepository_GetHistogramBySessionOptionalTimeBounds(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	const bucket = int64(60)
	early := f.at(0)
	late := f.at(3600)

	f.insert(t, f.sessionID, logRefOpts{start: early, duration: time.Second, lines: 4, source: "stdout"})
	f.insert(t, f.sessionID, logRefOpts{start: late, duration: time.Second, lines: 6, source: "stdout"})

	// Bound the window to the early row only. Bounds are epoch seconds of
	// start_time, inclusive on both ends.
	earlyEpoch := early.Unix()

	hist, err := f.repo.GetHistogramBySession(ctx, f.sessionID, bucket, &earlyEpoch, &earlyEpoch)
	if err != nil {
		t.Fatalf("GetHistogramBySession(bounded): %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("bounded histogram has %d buckets, want 1: %v", len(hist), hist)
	}
	if got := hist[epochBucket(early, bucket)]["stdout"]; got != 4 {
		t.Errorf("bounded stdout = %d, want 4", got)
	}

	// A window landing strictly between the two rows excludes both, and
	// yields an empty (non-nil) map.
	betweenEpoch := ((early.Unix() + late.Unix()) / 2)
	empty, err := f.repo.GetHistogramBySession(ctx, f.sessionID, bucket, &betweenEpoch, &betweenEpoch)
	if err != nil {
		t.Fatalf("GetHistogramBySession(between bound): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("histogram bounded between the two rows = %v, want empty", empty)
	}
}

// TestLogReferenceRepository_GetHistogramBySessionClampsBucketSize proves the
// guard: a non-positive bucketSeconds is clamped to 1 rather than dividing by
// zero.
func TestLogReferenceRepository_GetHistogramBySessionClampsBucketSize(t *testing.T) {
	f := newLogRefHarness(t)
	ctx := context.Background()

	f.insert(t, f.sessionID, logRefOpts{start: f.at(0), duration: time.Second, lines: 8, source: "stdout"})

	for _, size := range []int64{0, -5} {
		hist, err := f.repo.GetHistogramBySession(ctx, f.sessionID, size, nil, nil)
		if err != nil {
			t.Fatalf("GetHistogramBySession(bucket=%d): %v", size, err)
		}
		// bucketSeconds is clamped to 1, so the bucket is the second itself.
		if got := hist[f.at(0).Unix()]["stdout"]; got != 8 {
			t.Errorf("bucket=%d: stdout = %d, want 8 (clamped to a 1-second bucket)", size, got)
		}
	}
}

// epochBucket mirrors the SQL's `(EXTRACT(EPOCH FROM start_time)::bigint /
// $2) * $2`. The columns are naive TIMESTAMP and the container runs UTC, so
// the Go-side arithmetic below matches what Postgres computes.
func epochBucket(t time.Time, bucketSeconds int64) int64 {
	epoch := t.Unix()
	return (epoch / bucketSeconds) * bucketSeconds
}

func logRefIDs(refs []*manman.LogReference) []int64 {
	ids := make([]int64, len(refs))
	for i, r := range refs {
		ids[i] = r.LogID
	}
	return ids
}
