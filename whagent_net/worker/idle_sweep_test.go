// Unit tests for RunIdleSweep (idle_sweep.go): which sessions get an
// ArmIdleTimer signal, how per-session and list failures are counted, and
// that a repeat run sends the same signals. Workflow-side idempotence of
// the signal itself is covered by idle_test.go and replay_test.go.
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

// fakeSweepLister serves pages keyed by page token. A non-nil err for a
// token fails that List call.
type fakeSweepLister struct {
	pages   map[string][]*session.Session
	next    map[string]string
	errs    map[string]error
	filters []session.SessionFilter
}

func (f *fakeSweepLister) List(_ context.Context, filter session.SessionFilter, page session.SessionPage) ([]*session.Session, session.PageInfo, error) {
	f.filters = append(f.filters, filter)
	if err := f.errs[page.PageToken]; err != nil {
		return nil, session.PageInfo{}, err
	}
	return f.pages[page.PageToken], session.PageInfo{NextPageToken: f.next[page.PageToken]}, nil
}

type fakeSweepResolver struct {
	timeouts map[uuid.UUID]time.Duration
	errs     map[uuid.UUID]error
}

func (f *fakeSweepResolver) ResolveIdleTimeout(_ context.Context, id uuid.UUID) (ResolveIdleTimeoutResult, error) {
	if err := f.errs[id]; err != nil {
		return ResolveIdleTimeoutResult{}, err
	}
	return ResolveIdleTimeoutResult{EffectiveTimeout: f.timeouts[id]}, nil
}

type sweepSignal struct {
	workflowID string
	runID      string
	name       string
	arg        ArmIdleTimerSignal
}

type fakeSweepSignaler struct {
	signals []sweepSignal
	errs    map[string]error
}

func (f *fakeSweepSignaler) SignalWorkflow(_ context.Context, workflowID, runID, signalName string, arg interface{}) error {
	if err := f.errs[workflowID]; err != nil {
		return err
	}
	f.signals = append(f.signals, sweepSignal{workflowID: workflowID, runID: runID, name: signalName, arg: arg.(ArmIdleTimerSignal)})
	return nil
}

func sweepSession() *session.Session {
	return &session.Session{SessionID: uuid.New(), Status: session.StatusAwaitingInput}
}

func TestRunIdleSweep_SignalsEveryAwaitingSessionAcrossPages(t *testing.T) {
	a, b, c := sweepSession(), sweepSession(), sweepSession()
	lister := &fakeSweepLister{
		pages: map[string][]*session.Session{"": {a, b}, "p2": {c}},
		next:  map[string]string{"": "p2"},
	}
	resolver := &fakeSweepResolver{timeouts: map[uuid.UUID]time.Duration{
		a.SessionID: time.Minute, b.SessionID: 2 * time.Minute, c.SessionID: 3 * time.Minute,
	}}
	signaler := &fakeSweepSignaler{}

	result, err := RunIdleSweep(context.Background(), lister, resolver, signaler, 2)
	require.NoError(t, err)
	assert.Equal(t, IdleSweepResult{Signaled: 3, Failed: 0}, result)

	require.Len(t, lister.filters, 2, "one List call per page")
	for _, f := range lister.filters {
		require.NotNil(t, f.State)
		assert.Equal(t, session.StatusAwaitingInput, *f.State, "sweep only reads awaiting_input sessions")
	}

	want := []sweepSignal{
		{workflowID: a.SessionID.String(), name: SignalArmIdleTimer, arg: ArmIdleTimerSignal{EffectiveTimeout: time.Minute}},
		{workflowID: b.SessionID.String(), name: SignalArmIdleTimer, arg: ArmIdleTimerSignal{EffectiveTimeout: 2 * time.Minute}},
		{workflowID: c.SessionID.String(), name: SignalArmIdleTimer, arg: ArmIdleTimerSignal{EffectiveTimeout: 3 * time.Minute}},
	}
	assert.Equal(t, want, signaler.signals)
}

func TestRunIdleSweep_CountsPerSessionFailuresAndContinues(t *testing.T) {
	resolveErr := sweepSession()
	nonPositive := sweepSession()
	signalErr := sweepSession()
	ok := sweepSession()
	lister := &fakeSweepLister{pages: map[string][]*session.Session{
		"": {resolveErr, nonPositive, signalErr, ok},
	}}
	resolver := &fakeSweepResolver{
		timeouts: map[uuid.UUID]time.Duration{
			nonPositive.SessionID: 0,
			signalErr.SessionID:   time.Minute,
			ok.SessionID:          time.Minute,
		},
		errs: map[uuid.UUID]error{resolveErr.SessionID: errors.New("config unavailable")},
	}
	signaler := &fakeSweepSignaler{errs: map[string]error{signalErr.SessionID.String(): errors.New("workflow not found")}}

	result, err := RunIdleSweep(context.Background(), lister, resolver, signaler, 10)
	require.NoError(t, err, "per-session failures never abort the sweep")
	assert.Equal(t, IdleSweepResult{Signaled: 1, Failed: 3}, result)

	require.Len(t, signaler.signals, 1)
	assert.Equal(t, ok.SessionID.String(), signaler.signals[0].workflowID)
}

func TestRunIdleSweep_ListFailureAbortsAfterEarlierPages(t *testing.T) {
	a := sweepSession()
	listErr := errors.New("db unavailable")
	lister := &fakeSweepLister{
		pages: map[string][]*session.Session{"": {a}},
		next:  map[string]string{"": "p2"},
		errs:  map[string]error{"p2": listErr},
	}
	resolver := &fakeSweepResolver{timeouts: map[uuid.UUID]time.Duration{a.SessionID: time.Minute}}
	signaler := &fakeSweepSignaler{}

	result, err := RunIdleSweep(context.Background(), lister, resolver, signaler, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, listErr)
	assert.Equal(t, IdleSweepResult{Signaled: 1, Failed: 0}, result, "sessions on pages read before the failure are still signaled")
	require.Len(t, signaler.signals, 1)
}

func TestRunIdleSweep_RepeatRunSendsSameSignals(t *testing.T) {
	a, b := sweepSession(), sweepSession()
	lister := &fakeSweepLister{pages: map[string][]*session.Session{"": {a, b}}}
	resolver := &fakeSweepResolver{timeouts: map[uuid.UUID]time.Duration{
		a.SessionID: time.Minute, b.SessionID: time.Minute,
	}}
	signaler := &fakeSweepSignaler{}

	first, err := RunIdleSweep(context.Background(), lister, resolver, signaler, 10)
	require.NoError(t, err)
	firstSignals := append([]sweepSignal(nil), signaler.signals...)

	second, err := RunIdleSweep(context.Background(), lister, resolver, signaler, 10)
	require.NoError(t, err)

	assert.Equal(t, first, second, "a repeat run counts the same outcomes")
	require.Len(t, signaler.signals, 2*len(firstSignals))
	assert.Equal(t, firstSignals, signaler.signals[len(firstSignals):],
		"a repeat run re-sends identical ArmIdleTimer signals; the workflow ignores them once a timeout is known")
}

func TestRunIdleSweep_CancelledContextReturnsWithoutSignaling(t *testing.T) {
	a := sweepSession()
	lister := &fakeSweepLister{pages: map[string][]*session.Session{"": {a}}}
	resolver := &fakeSweepResolver{timeouts: map[uuid.UUID]time.Duration{a.SessionID: time.Minute}}
	signaler := &fakeSweepSignaler{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RunIdleSweep(ctx, lister, resolver, signaler, 10)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, signaler.signals)
}
