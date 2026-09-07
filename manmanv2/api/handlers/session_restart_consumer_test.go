package handlers

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"testing"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

// fakePendingRestartRepo is a minimal in-memory PendingRestartRepository
// fake for SessionRestartConsumer tests -- no broker, no DB.
type fakePendingRestartRepo struct {
	repository.PendingRestartRepository

	mu sync.Mutex

	// pending, keyed by gating_session_id, is claimable exactly once --
	// ClaimForSession deletes the entry on claim, which is what makes this
	// fake actually exercise NFR10 idempotency the same way the real
	// UPDATE...WHERE status='pending' does.
	pending map[int64]*manman.PendingRestart

	claimCalls  []int64
	startedCall *markStartedCall
	failedCall  *markFailedCall
}

type markStartedCall struct {
	pendingRestartID int64
	startedSessionID int64
}

type markFailedCall struct {
	pendingRestartID int64
	reason           string
}

func (f *fakePendingRestartRepo) ClaimForSession(ctx context.Context, gatingSessionID int64) (*manman.PendingRestart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimCalls = append(f.claimCalls, gatingSessionID)
	rec, ok := f.pending[gatingSessionID]
	if !ok {
		return nil, nil
	}
	delete(f.pending, gatingSessionID)
	return rec, nil
}

func (f *fakePendingRestartRepo) MarkStarted(ctx context.Context, pendingRestartID, startedSessionID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startedCall = &markStartedCall{pendingRestartID: pendingRestartID, startedSessionID: startedSessionID}
	return nil
}

func (f *fakePendingRestartRepo) MarkFailed(ctx context.Context, pendingRestartID int64, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failedCall = &markFailedCall{pendingRestartID: pendingRestartID, reason: reason}
	return nil
}

func (f *fakePendingRestartRepo) claimCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.claimCalls)
}

// fakeDeferredStarter is a DeferredStarter fake that records calls and
// either returns a canned response or a canned error.
type fakeDeferredStarter struct {
	mu sync.Mutex

	calls []*pb.StartSessionRequest

	// startedSessionID is returned as the new session's id on success.
	startedSessionID int64
	// err, when non-nil, is returned instead of a response.
	err error
}

func (f *fakeDeferredStarter) StartSession(ctx context.Context, req *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	return &pb.StartSessionResponse{Session: &pb.Session{SessionId: f.startedSessionID}}, nil
}

func (f *fakeDeferredStarter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// recordingHandler is a slog.Handler that records every record's level, so
// tests can assert "nothing logged at WARNING or above" precisely instead of
// just eyeballing stderr.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(name string) slog.Handler       { return h }

func (h *recordingHandler) maxLevel() (slog.Level, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.records) == 0 {
		return 0, false
	}
	max := h.records[0].Level
	for _, r := range h.records[1:] {
		if r.Level > max {
			max = r.Level
		}
	}
	return max, true
}

func newTestConsumer(repo *fakePendingRestartRepo, starter *fakeDeferredStarter) (*SessionRestartConsumer, *recordingHandler) {
	rh := &recordingHandler{}
	logger := slog.New(rh)
	h := &SessionRestartConsumer{
		pendingRepo: repo,
		starter:     starter,
		logger:      logger,
	}
	return h, rh
}

func statusUpdateBody(t *testing.T, sessionID int64, status string) []byte {
	t.Helper()
	body := `{"session_id":` + strconv.FormatInt(sessionID, 10) + `,"sgc_id":1,"status":"` + status + `"}`
	return []byte(body)
}

func TestHandleStatusUpdate_TerminalStatusDispatchesStart(t *testing.T) {
	terminalStatuses := []string{
		manman.SessionStatusStopped,
		manman.SessionStatusCrashed,
		manman.SessionStatusLost,
	}

	for _, status := range terminalStatuses {
		t.Run(status, func(t *testing.T) {
			gatingSessionID := int64(42)
			sgcID := int64(7)
			repo := &fakePendingRestartRepo{
				pending: map[int64]*manman.PendingRestart{
					gatingSessionID: {
						PendingRestartID:   1,
						ServerGameConfigID: sgcID,
						GatingSessionID:    gatingSessionID,
						Status:             manman.PendingRestartStatusPending,
					},
				},
			}
			starter := &fakeDeferredStarter{startedSessionID: 99}
			h, _ := newTestConsumer(repo, starter)

			err := h.handleStatusUpdate(context.Background(), rmq.Message{
				Body: statusUpdateBody(t, gatingSessionID, status),
			})
			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}

			if got := starter.callCount(); got != 1 {
				t.Fatalf("expected exactly 1 StartSession call, got %d", got)
			}
			if starter.calls[0].ServerGameConfigId != sgcID {
				t.Errorf("expected StartSession called with sgc_id %d, got %d", sgcID, starter.calls[0].ServerGameConfigId)
			}

			if repo.startedCall == nil {
				t.Fatal("expected MarkStarted to be called")
			}
			if repo.startedCall.pendingRestartID != 1 {
				t.Errorf("expected MarkStarted pendingRestartID 1, got %d", repo.startedCall.pendingRestartID)
			}
			if repo.startedCall.startedSessionID != 99 {
				t.Errorf("expected MarkStarted startedSessionID 99, got %d", repo.startedCall.startedSessionID)
			}
			if repo.failedCall != nil {
				t.Errorf("expected MarkFailed not to be called, got %+v", repo.failedCall)
			}
		})
	}
}

func TestHandleStatusUpdate_NonTerminalStatusNoDBAccess(t *testing.T) {
	nonTerminalStatuses := []string{
		manman.SessionStatusPending,
		manman.SessionStatusStarting,
		manman.SessionStatusRunning,
		manman.SessionStatusStopping,
	}

	for _, status := range nonTerminalStatuses {
		t.Run(status, func(t *testing.T) {
			repo := &fakePendingRestartRepo{pending: map[int64]*manman.PendingRestart{}}
			starter := &fakeDeferredStarter{}
			h, _ := newTestConsumer(repo, starter)

			err := h.handleStatusUpdate(context.Background(), rmq.Message{
				Body: statusUpdateBody(t, 1, status),
			})
			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}

			if got := repo.claimCallCount(); got != 0 {
				t.Fatalf("expected zero ClaimForSession calls for non-terminal status %q, got %d", status, got)
			}
			if got := starter.callCount(); got != 0 {
				t.Fatalf("expected zero StartSession calls for non-terminal status %q, got %d", status, got)
			}
		})
	}
}

func TestHandleStatusUpdate_DuplicateDeliveryIsIdempotent(t *testing.T) {
	gatingSessionID := int64(42)
	sgcID := int64(7)
	repo := &fakePendingRestartRepo{
		pending: map[int64]*manman.PendingRestart{
			gatingSessionID: {
				PendingRestartID:   1,
				ServerGameConfigID: sgcID,
				GatingSessionID:    gatingSessionID,
				Status:             manman.PendingRestartStatusPending,
			},
		},
	}
	starter := &fakeDeferredStarter{startedSessionID: 99}
	h, _ := newTestConsumer(repo, starter)

	body := statusUpdateBody(t, gatingSessionID, manman.SessionStatusStopped)

	// First delivery: claims and starts.
	if err := h.handleStatusUpdate(context.Background(), rmq.Message{Body: body}); err != nil {
		t.Fatalf("first delivery: expected nil error, got %v", err)
	}
	// Second (redelivered/duplicate) delivery: ClaimForSession must find
	// nothing left to claim.
	if err := h.handleStatusUpdate(context.Background(), rmq.Message{Body: body}); err != nil {
		t.Fatalf("second delivery: expected nil error, got %v", err)
	}

	if got := repo.claimCallCount(); got != 2 {
		t.Fatalf("expected ClaimForSession to be called twice (claim, then no-op), got %d", got)
	}
	if got := starter.callCount(); got != 1 {
		t.Fatalf("expected exactly 1 StartSession call total across both deliveries, got %d", got)
	}
}

func TestHandleStatusUpdate_TerminalStatusNoPendingRecord(t *testing.T) {
	repo := &fakePendingRestartRepo{pending: map[int64]*manman.PendingRestart{}}
	starter := &fakeDeferredStarter{}
	h, rh := newTestConsumer(repo, starter)

	err := h.handleStatusUpdate(context.Background(), rmq.Message{
		Body: statusUpdateBody(t, 1, manman.SessionStatusStopped),
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got := starter.callCount(); got != 0 {
		t.Fatalf("expected zero StartSession calls, got %d", got)
	}
	if lvl, logged := rh.maxLevel(); logged && lvl >= slog.LevelWarn {
		t.Errorf("expected nothing logged at WARNING or above, got max level %v", lvl)
	}
}

func TestHandleStatusUpdate_StartSessionFailureMarksFailed(t *testing.T) {
	gatingSessionID := int64(42)
	sgcID := int64(7)
	repo := &fakePendingRestartRepo{
		pending: map[int64]*manman.PendingRestart{
			gatingSessionID: {
				PendingRestartID:   1,
				ServerGameConfigID: sgcID,
				GatingSessionID:    gatingSessionID,
				Status:             manman.PendingRestartStatusPending,
			},
		},
	}
	starter := &fakeDeferredStarter{err: errors.New("boom")}
	h, _ := newTestConsumer(repo, starter)

	err := h.handleStatusUpdate(context.Background(), rmq.Message{
		Body: statusUpdateBody(t, gatingSessionID, manman.SessionStatusStopped),
	})
	if err != nil {
		t.Fatalf("expected nil error (no retry loop), got %v", err)
	}

	if repo.failedCall == nil {
		t.Fatal("expected MarkFailed to be called")
	}
	if repo.failedCall.pendingRestartID != 1 {
		t.Errorf("expected MarkFailed pendingRestartID 1, got %d", repo.failedCall.pendingRestartID)
	}
	if repo.failedCall.reason != "boom" {
		t.Errorf("expected MarkFailed reason %q, got %q", "boom", repo.failedCall.reason)
	}
	if repo.startedCall != nil {
		t.Errorf("expected MarkStarted not to be called, got %+v", repo.startedCall)
	}
}

func TestHandleStatusUpdate_MalformedJSONIsDropped(t *testing.T) {
	repo := &fakePendingRestartRepo{pending: map[int64]*manman.PendingRestart{}}
	starter := &fakeDeferredStarter{}
	h, _ := newTestConsumer(repo, starter)

	err := h.handleStatusUpdate(context.Background(), rmq.Message{Body: []byte("{not json")})
	if err != nil {
		t.Fatalf("expected nil error for malformed payload, got %v", err)
	}
	if got := repo.claimCallCount(); got != 0 {
		t.Fatalf("expected zero repository calls for malformed payload, got %d", got)
	}
	if got := starter.callCount(); got != 0 {
		t.Fatalf("expected zero StartSession calls for malformed payload, got %d", got)
	}
}

func TestHandleStatusUpdate_UnknownFieldIsIgnored(t *testing.T) {
	gatingSessionID := int64(42)
	sgcID := int64(7)
	repo := &fakePendingRestartRepo{
		pending: map[int64]*manman.PendingRestart{
			gatingSessionID: {
				PendingRestartID:   1,
				ServerGameConfigID: sgcID,
				GatingSessionID:    gatingSessionID,
				Status:             manman.PendingRestartStatusPending,
			},
		},
	}
	starter := &fakeDeferredStarter{startedSessionID: 99}
	h, _ := newTestConsumer(repo, starter)

	body := []byte(`{"session_id":42,"sgc_id":1,"status":"stopped","future_field":"unused"}`)

	err := h.handleStatusUpdate(context.Background(), rmq.Message{Body: body})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got := starter.callCount(); got != 1 {
		t.Fatalf("expected exactly 1 StartSession call despite unknown field, got %d", got)
	}
}
