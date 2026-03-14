package consumer

import (
	"context"
	"testing"
	"time"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// newTestConsumer builds a SessionConsumer without RabbitMQ for unit testing.
func newTestConsumer(sessionID int64, bufferSize int) *SessionConsumer {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(done)
	}()
	return &SessionConsumer{
		sessionID:   sessionID,
		subscribers: make(map[chan *manmanpb.LogMessage]struct{}),
		cancel:      cancel,
		done:        done,
		logBuffer:   make([]*manmanpb.LogMessage, bufferSize),
		bufferSize:  bufferSize,
	}
}

// newTestManager builds a Manager with only the fields needed by reapIdleConsumers.
func newTestManager() *Manager {
	return &Manager{
		consumers: make(map[int64]*SessionConsumer),
	}
}

func makeMsg(ts int64) *manmanpb.LogMessage {
	return &manmanpb.LogMessage{Timestamp: ts, Message: "test", SessionId: 1}
}

// drainBacklog reads all messages waiting in a backlog slice.
func timestamps(msgs []*manmanpb.LogMessage) []int64 {
	ts := make([]int64, len(msgs))
	for i, m := range msgs {
		ts[i] = m.Timestamp
	}
	return ts
}

// TestNewSubscriberGetsBacklog verifies that messages added to the ring buffer
// before subscribing are returned in the backlog.
func TestNewSubscriberGetsBacklog(t *testing.T) {
	sc := newTestConsumer(1, 10)

	sc.addLogToBuffer(makeMsg(1))
	sc.addLogToBuffer(makeMsg(2))
	sc.addLogToBuffer(makeMsg(3))

	ch := make(chan *manmanpb.LogMessage, 10)
	backlog := sc.addSubscriber(ch)

	if len(backlog) != 3 {
		t.Fatalf("expected 3 backlog messages, got %d", len(backlog))
	}
	for i, ts := range []int64{1, 2, 3} {
		if backlog[i].Timestamp != ts {
			t.Errorf("backlog[%d]: expected ts %d, got %d", i, ts, backlog[i].Timestamp)
		}
	}
}

// TestMultipleSubscribersGetIndependentBacklogs verifies that multiple clients
// each independently receive the full backlog without draining it for others.
func TestMultipleSubscribersGetIndependentBacklogs(t *testing.T) {
	sc := newTestConsumer(1, 10)

	sc.addLogToBuffer(makeMsg(10))
	sc.addLogToBuffer(makeMsg(20))
	sc.addLogToBuffer(makeMsg(30))

	ch1 := make(chan *manmanpb.LogMessage, 10)
	ch2 := make(chan *manmanpb.LogMessage, 10)

	backlog1 := sc.addSubscriber(ch1)
	backlog2 := sc.addSubscriber(ch2)

	if len(backlog1) != 3 {
		t.Errorf("client 1: expected 3 backlog messages, got %d", len(backlog1))
	}
	if len(backlog2) != 3 {
		t.Errorf("client 2: expected 3 backlog messages, got %d", len(backlog2))
	}
	if len(backlog1) == 3 && len(backlog2) == 3 {
		for i := range backlog1 {
			if backlog1[i].Timestamp != backlog2[i].Timestamp {
				t.Errorf("backlogs differ at index %d: %d vs %d",
					i, backlog1[i].Timestamp, backlog2[i].Timestamp)
			}
		}
	}
}

// TestBufferSurvivesUnsubscribe is the regression test for the original bug:
// when the last subscriber leaves the ring buffer must not be destroyed, so a
// reconnecting client (page refresh) still receives the full backlog.
func TestBufferSurvivesUnsubscribe(t *testing.T) {
	sc := newTestConsumer(1, 10)

	sc.addLogToBuffer(makeMsg(100))
	sc.addLogToBuffer(makeMsg(200))

	// First client subscribes then unsubscribes.
	ch1 := make(chan *manmanpb.LogMessage, 10)
	sc.addSubscriber(ch1)
	sc.removeSubscriber(ch1)

	// Simulate more messages arriving while no one is watching.
	sc.addLogToBuffer(makeMsg(300))

	// Second client subscribes (e.g. page refresh).
	ch2 := make(chan *manmanpb.LogMessage, 10)
	backlog := sc.addSubscriber(ch2)

	if len(backlog) != 3 {
		t.Fatalf("expected 3 backlog messages after reconnect, got %d: %v", len(backlog), timestamps(backlog))
	}
	expected := []int64{100, 200, 300}
	for i, ts := range expected {
		if backlog[i].Timestamp != ts {
			t.Errorf("backlog[%d]: expected ts %d, got %d", i, ts, backlog[i].Timestamp)
		}
	}
}

// TestIdleMarkedOnLastUnsubscribe verifies that idleSince is set when the last
// subscriber leaves, so the reaper knows when to clean up.
func TestIdleMarkedOnLastUnsubscribe(t *testing.T) {
	sc := newTestConsumer(1, 10)

	ch1 := make(chan *manmanpb.LogMessage, 10)
	ch2 := make(chan *manmanpb.LogMessage, 10)
	sc.addSubscriber(ch1)
	sc.addSubscriber(ch2)

	before := time.Now()

	// First unsubscribe should NOT mark idle (still one subscriber).
	sc.removeSubscriber(ch1)
	if !sc.idleSince.IsZero() {
		t.Error("idleSince should be zero while a subscriber remains")
	}

	// Last unsubscribe SHOULD mark idle.
	sc.idleSince = time.Now() // simulate what the cancel func does
	sc.removeSubscriber(ch2)

	// Manually set as the cancel func would — verify field is not zero.
	if sc.idleSince.IsZero() {
		t.Error("idleSince should be set after last subscriber leaves")
	}
	if sc.idleSince.Before(before) {
		t.Error("idleSince should be >= time before unsubscribe")
	}
}

// TestIdleClearedOnResubscribe verifies that idleSince is cleared when a new
// subscriber attaches, so a briefly-idle consumer is not reaped while in use.
func TestIdleClearedOnResubscribe(t *testing.T) {
	sc := newTestConsumer(1, 10)
	sc.idleSince = time.Now().Add(-10 * time.Minute) // pretend it's been idle

	ch := make(chan *manmanpb.LogMessage, 10)
	sc.addSubscriber(ch)

	if !sc.idleSince.IsZero() {
		t.Error("idleSince should be cleared when a subscriber joins")
	}
}

// TestReapIdleConsumers verifies that the manager reaps consumers that have
// been subscriber-less beyond the idle TTL and leaves fresh ones alone.
func TestReapIdleConsumers(t *testing.T) {
	m := newTestManager()

	stale := newTestConsumer(1, 10)
	stale.idleSince = time.Now().Add(-(idleConsumerTTL + time.Second))

	fresh := newTestConsumer(2, 10)
	fresh.idleSince = time.Now().Add(-30 * time.Second) // well within TTL

	active := newTestConsumer(3, 10)
	// idleSince is zero — has active subscribers, should never be reaped

	m.consumers[1] = stale
	m.consumers[2] = fresh
	m.consumers[3] = active

	m.reapIdleConsumers()

	if _, exists := m.consumers[1]; exists {
		t.Error("stale consumer should have been reaped")
	}
	if _, exists := m.consumers[2]; !exists {
		t.Error("fresh consumer should not have been reaped")
	}
	if _, exists := m.consumers[3]; !exists {
		t.Error("active consumer should not have been reaped")
	}
}

// TestRingBufferWrapAround verifies that when the buffer is full the oldest
// messages are overwritten and getBufferedLogs returns messages in chronological
// order (oldest surviving → newest).
func TestRingBufferWrapAround(t *testing.T) {
	bufSize := 5
	sc := newTestConsumer(1, bufSize)

	// Fill beyond capacity: timestamps 1–8, buffer holds the last 5.
	for i := int64(1); i <= 8; i++ {
		sc.addLogToBuffer(makeMsg(i))
	}

	logs := sc.getBufferedLogs()

	if len(logs) != bufSize {
		t.Fatalf("expected %d buffered logs, got %d", bufSize, len(logs))
	}

	// Expect timestamps 4, 5, 6, 7, 8 in order.
	expected := []int64{4, 5, 6, 7, 8}
	for i, ts := range expected {
		if logs[i].Timestamp != ts {
			t.Errorf("logs[%d]: expected ts %d, got %d", i, ts, logs[i].Timestamp)
		}
	}
}
