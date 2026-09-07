package handlers

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/models"
)

// fakeReaperRepo is a minimal in-memory PendingRestartRepository fake for
// PendingRestartReaper tests. Unlike fakePendingRestartRepo
// (session_restart_consumer_test.go), it implements ExpireStalled by
// filtering a flat record set the same way the real one-atomic-UPDATE
// implementation would (#1729): only 'pending' records past their
// stall_deadline are ever touched.
type fakeReaperRepo struct {
	repository.PendingRestartRepository

	mu sync.Mutex

	records []*manman.PendingRestart

	// expireStalledErr, when non-nil, is returned by every ExpireStalled
	// call instead of the normal filtered result.
	expireStalledErr error

	expireStalledCalls int
}

func (f *fakeReaperRepo) ExpireStalled(ctx context.Context, now time.Time) ([]*manman.PendingRestart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expireStalledCalls++
	if f.expireStalledErr != nil {
		return nil, f.expireStalledErr
	}

	var expired []*manman.PendingRestart
	for _, r := range f.records {
		if r.Status == manman.PendingRestartStatusPending && !r.StallDeadline.After(now) {
			r.Status = manman.PendingRestartStatusExpired
			expired = append(expired, r)
		}
	}
	return expired, nil
}

func (f *fakeReaperRepo) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.expireStalledCalls
}

func (f *fakeReaperRepo) statusOf(pendingRestartID int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.records {
		if r.PendingRestartID == pendingRestartID {
			return r.Status
		}
	}
	return ""
}

func newTestReaper(repo *fakeReaperRepo, interval time.Duration) (*PendingRestartReaper, *recordingHandler) {
	rh := &recordingHandler{}
	logger := slog.New(rh)
	return NewPendingRestartReaper(repo, interval, logger), rh
}

// recordsOfLevel returns every logged record at exactly the given level, so
// tests can assert on message content, not just max level.
func recordsOfLevel(rh *recordingHandler, level slog.Level) []slog.Record {
	rh.mu.Lock()
	defer rh.mu.Unlock()
	var out []slog.Record
	for _, r := range rh.records {
		if r.Level == level {
			out = append(out, r)
		}
	}
	return out
}

func attrsOf(r slog.Record) map[string]slog.Value {
	out := map[string]slog.Value{}
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value
		return true
	})
	return out
}

func TestTick_ExpiresStalledRecordAndLogsWarning(t *testing.T) {
	now := time.Now()
	repo := &fakeReaperRepo{
		records: []*manman.PendingRestart{
			{
				PendingRestartID:   1,
				ServerGameConfigID: 7,
				GatingSessionID:    42,
				Status:             manman.PendingRestartStatusPending,
				StallDeadline:      now.Add(-time.Second), // past deadline
				CreatedAt:          now.Add(-time.Minute),
			},
		},
	}
	r, rh := newTestReaper(repo, time.Hour)

	r.tick(context.Background())

	if got := repo.statusOf(1); got != manman.PendingRestartStatusExpired {
		t.Fatalf("expected record 1 to be expired, got status %q", got)
	}

	warnings := recordsOfLevel(rh, slog.LevelWarn)
	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 WARNING log, got %d", len(warnings))
	}
	attrs := attrsOf(warnings[0])
	if got := attrs["server_game_config_id"].Any(); got != int64(7) {
		t.Errorf("expected server_game_config_id=7 in WARNING log, got %v", got)
	}
	if got := attrs["gating_session_id"].Any(); got != int64(42) {
		t.Errorf("expected gating_session_id=42 in WARNING log, got %v", got)
	}
	if got := attrs["pending_restart_id"].Any(); got != int64(1) {
		t.Errorf("expected pending_restart_id=1 in WARNING log, got %v", got)
	}
}

func TestTick_RecordBeforeDeadlineIsNotExpired(t *testing.T) {
	now := time.Now()
	repo := &fakeReaperRepo{
		records: []*manman.PendingRestart{
			{
				PendingRestartID:   1,
				ServerGameConfigID: 7,
				GatingSessionID:    42,
				Status:             manman.PendingRestartStatusPending,
				StallDeadline:      now.Add(time.Hour), // well before deadline
				CreatedAt:          now,
			},
		},
	}
	r, rh := newTestReaper(repo, time.Hour)

	r.tick(context.Background())

	if got := repo.statusOf(1); got != manman.PendingRestartStatusPending {
		t.Fatalf("expected record 1 to remain pending, got status %q", got)
	}
	if lvl, logged := rh.maxLevel(); logged {
		t.Errorf("expected no logs, got max level %v", lvl)
	}
}

func TestTick_OnlyPendingRecordsAreExpired(t *testing.T) {
	now := time.Now()
	pastDeadline := now.Add(-time.Second)
	repo := &fakeReaperRepo{
		records: []*manman.PendingRestart{
			{
				PendingRestartID:   1,
				ServerGameConfigID: 7,
				GatingSessionID:    42,
				Status:             manman.PendingRestartStatusStarted,
				StallDeadline:      pastDeadline,
			},
			{
				PendingRestartID:   2,
				ServerGameConfigID: 8,
				GatingSessionID:    43,
				Status:             manman.PendingRestartStatusFailed,
				StallDeadline:      pastDeadline,
			},
		},
	}
	r, _ := newTestReaper(repo, time.Hour)

	r.tick(context.Background())

	if got := repo.statusOf(1); got != manman.PendingRestartStatusStarted {
		t.Errorf("expected 'started' record to be untouched, got status %q", got)
	}
	if got := repo.statusOf(2); got != manman.PendingRestartStatusFailed {
		t.Errorf("expected 'failed' record to be untouched, got status %q", got)
	}
}

func TestTick_ExpireStalledErrorLogsErrorAndSurvives(t *testing.T) {
	repo := &fakeReaperRepo{expireStalledErr: errors.New("db unavailable")}
	r, rh := newTestReaper(repo, time.Hour)

	r.tick(context.Background())

	errs := recordsOfLevel(rh, slog.LevelError)
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 ERROR log, got %d", len(errs))
	}

	// Next tick still runs (the goroutine/ticker isn't killed by an error --
	// simulated here by calling tick again directly and confirming it
	// doesn't panic or short-circuit).
	repo.expireStalledErr = nil
	repo.records = []*manman.PendingRestart{
		{
			PendingRestartID:   1,
			ServerGameConfigID: 7,
			GatingSessionID:    42,
			Status:             manman.PendingRestartStatusPending,
			StallDeadline:      time.Now().Add(-time.Second),
		},
	}
	r.tick(context.Background())

	if got := repo.callCount(); got != 2 {
		t.Fatalf("expected ExpireStalled to be called twice (failed tick + retry tick), got %d", got)
	}
	if got := repo.statusOf(1); got != manman.PendingRestartStatusExpired {
		t.Fatalf("expected retry tick to expire the record, got status %q", got)
	}
}

func TestTick_NothingExpiredIsSilent(t *testing.T) {
	repo := &fakeReaperRepo{}
	r, rh := newTestReaper(repo, time.Hour)

	r.tick(context.Background())

	if lvl, logged := rh.maxLevel(); logged {
		t.Errorf("expected no WARNING/ERROR logs on an idle tick, got max level %v", lvl)
	}
}

func TestStart_CtxCancellationStopsGoroutine(t *testing.T) {
	repo := &fakeReaperRepo{}
	r, rh := newTestReaper(repo, time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	cancel()

	// Poll for the "stopping" INFO log rather than sleeping a fixed
	// duration -- bounds the test time without being flaky under load.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		infos := recordsOfLevel(rh, slog.LevelInfo)
		for _, rec := range infos {
			if rec.Message == "stopping pending restart reaper" {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("expected 'stopping pending restart reaper' INFO log after ctx cancellation, goroutine appears to have leaked")
}

// TestTick_CoexistingClaimedAndStalledRecords is the interaction test with
// #1731's SessionRestartConsumer: a record already claimed via
// ClaimForSession (status 'started', its gating session's work is in
// flight) coexists with a genuinely stalled 'pending' record. The reaper
// must expire only the stalled one and never touch the claimed one -- if it
// did, it would resurrect/interfere with a restart #1731 is actively
// servicing.
func TestTick_CoexistingClaimedAndStalledRecords(t *testing.T) {
	now := time.Now()
	claimed := &manman.PendingRestart{
		PendingRestartID:   1,
		ServerGameConfigID: 7,
		GatingSessionID:    42,
		Status:             manman.PendingRestartStatusStarted,
		StartedSessionID:   int64Ptr(99),
		StallDeadline:      now.Add(-time.Hour), // long past -- must still be ignored
	}
	stalled := &manman.PendingRestart{
		PendingRestartID:   2,
		ServerGameConfigID: 8,
		GatingSessionID:    43,
		Status:             manman.PendingRestartStatusPending,
		StallDeadline:      now.Add(-time.Second),
	}
	repo := &fakeReaperRepo{records: []*manman.PendingRestart{claimed, stalled}}
	r, rh := newTestReaper(repo, time.Hour)

	r.tick(context.Background())

	if got := repo.statusOf(1); got != manman.PendingRestartStatusStarted {
		t.Errorf("expected claimed record to remain 'started', got status %q", got)
	}
	if got := repo.statusOf(2); got != manman.PendingRestartStatusExpired {
		t.Errorf("expected stalled record to be expired, got status %q", got)
	}

	warnings := recordsOfLevel(rh, slog.LevelWarn)
	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 WARNING log (for the stalled record only), got %d", len(warnings))
	}
	attrs := attrsOf(warnings[0])
	if got := attrs["pending_restart_id"].Any(); got != int64(2) {
		t.Errorf("expected WARNING to reference pending_restart_id=2, got %v", got)
	}
}

func int64Ptr(v int64) *int64 { return &v }
