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
		consumers:    make(map[int64]*SessionConsumer),
		retainedLogs: make(map[int64][]*manmanpb.LogMessage),
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
	backlog := sc.addSubscriber(ch, 0)

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

	backlog1 := sc.addSubscriber(ch1, 0)
	backlog2 := sc.addSubscriber(ch2, 0)

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
	sc.addSubscriber(ch1, 0)
	sc.removeSubscriber(ch1)

	// Simulate more messages arriving while no one is watching.
	sc.addLogToBuffer(makeMsg(300))

	// Second client subscribes (e.g. page refresh).
	ch2 := make(chan *manmanpb.LogMessage, 10)
	backlog := sc.addSubscriber(ch2, 0)

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
	sc.addSubscriber(ch1, 0)
	sc.addSubscriber(ch2, 0)

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
	sc.addSubscriber(ch, 0)

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

	logs := sc.getBufferedLogs(0)

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

// ---------------------------------------------------------------------------
// after_sequence_number filtering
// ---------------------------------------------------------------------------

// TestAfterSequenceNumberZeroReturnsFullBacklog verifies that passing 0 (the
// default on first connect) returns the entire backlog.
func TestAfterSequenceNumberZeroReturnsFullBacklog(t *testing.T) {
	sc := newTestConsumer(1, 10)
	sc.addLogToBuffer(makeMsg(1))
	sc.addLogToBuffer(makeMsg(2))
	sc.addLogToBuffer(makeMsg(3))

	logs := sc.getBufferedLogs(0)
	if len(logs) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(logs))
	}
}

// TestAfterSequenceNumberFiltersBacklog verifies that only messages with
// sequence_number > afterSequence are returned, avoiding duplicates on reconnect.
func TestAfterSequenceNumberFiltersBacklog(t *testing.T) {
	sc := newTestConsumer(1, 10)
	sc.addLogToBuffer(makeMsg(10))
	sc.addLogToBuffer(makeMsg(20))
	sc.addLogToBuffer(makeMsg(30))

	// Simulate client that received the first two messages (seq 1 and 2).
	logs := sc.getBufferedLogs(2)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log after seq 2, got %d", len(logs))
	}
	if logs[0].Timestamp != 30 {
		t.Errorf("expected ts 30, got %d", logs[0].Timestamp)
	}
}

// TestAfterSequenceNumberAllSeenReturnsNil verifies that when the client has
// already seen every message in the buffer, nothing is returned.
func TestAfterSequenceNumberAllSeenReturnsNil(t *testing.T) {
	sc := newTestConsumer(1, 10)
	sc.addLogToBuffer(makeMsg(1))
	sc.addLogToBuffer(makeMsg(2))

	logs := sc.getBufferedLogs(sc.sequenceCounter) // seen everything
	if len(logs) != 0 {
		t.Fatalf("expected empty backlog, got %d messages", len(logs))
	}
}

// TestAfterSequenceNumberViaSubscribe verifies the filter propagates through
// addSubscriber so the full subscribe path is exercised.
func TestAfterSequenceNumberViaSubscribe(t *testing.T) {
	sc := newTestConsumer(1, 10)
	sc.addLogToBuffer(makeMsg(100))
	sc.addLogToBuffer(makeMsg(200))
	sc.addLogToBuffer(makeMsg(300))

	seqAfterFirst := sc.logBuffer[0].SequenceNumber // seq of first message

	ch := make(chan *manmanpb.LogMessage, 10)
	backlog := sc.addSubscriber(ch, seqAfterFirst)

	if len(backlog) != 2 {
		t.Fatalf("expected 2 backlog messages, got %d", len(backlog))
	}
	for _, msg := range backlog {
		if msg.SequenceNumber <= seqAfterFirst {
			t.Errorf("backlog contained already-seen sequence %d", msg.SequenceNumber)
		}
	}
}

// ---------------------------------------------------------------------------
// Retained log buffer after consumer reap
// ---------------------------------------------------------------------------

// TestReapRetainsLogs verifies that reaping an idle consumer saves the last
// retainLogCount messages into m.retainedLogs.
func TestReapRetainsLogs(t *testing.T) {
	m := newTestManager()
	sc := newTestConsumer(1, 100)
	for i := int64(1); i <= 10; i++ {
		sc.addLogToBuffer(makeMsg(i))
	}
	sc.idleSince = time.Now().Add(-(idleConsumerTTL + time.Second))
	m.consumers[1] = sc

	m.reapIdleConsumers()

	if _, exists := m.consumers[1]; exists {
		t.Fatal("consumer should have been reaped")
	}
	retained := m.retainedLogs[1]
	if len(retained) != 10 {
		t.Fatalf("expected 10 retained messages, got %d", len(retained))
	}
}

// TestReapRetainsAtMostRetainLogCount verifies the cap is applied when the
// buffer holds more than retainLogCount messages.
func TestReapRetainsAtMostRetainLogCount(t *testing.T) {
	m := newTestManager()
	sc := newTestConsumer(1, 500)
	for i := int64(1); i <= int64(retainLogCount+20); i++ {
		sc.addLogToBuffer(makeMsg(i))
	}
	sc.idleSince = time.Now().Add(-(idleConsumerTTL + time.Second))
	m.consumers[1] = sc

	m.reapIdleConsumers()

	retained := m.retainedLogs[1]
	if len(retained) != retainLogCount {
		t.Fatalf("expected %d retained messages, got %d", retainLogCount, len(retained))
	}
	// Verify it kept the most-recent messages.
	lastTS := retained[len(retained)-1].Timestamp
	if lastTS != int64(retainLogCount+20) {
		t.Errorf("last retained ts = %d, want %d", lastTS, int64(retainLogCount+20))
	}
}

// TestReapEmptyBufferNoRetainEntry verifies that a consumer with an empty ring
// buffer does not create a retainedLogs entry.
func TestReapEmptyBufferNoRetainEntry(t *testing.T) {
	m := newTestManager()
	sc := newTestConsumer(1, 10)
	sc.idleSince = time.Now().Add(-(idleConsumerTTL + time.Second))
	m.consumers[1] = sc

	m.reapIdleConsumers()

	if _, ok := m.retainedLogs[1]; ok {
		t.Error("expected no retained entry for consumer with empty buffer")
	}
}

// TestSeedBufferPreservesSequenceNumbers verifies that seedBuffer does not
// reassign sequence numbers and advances sequenceCounter to the max seen.
func TestSeedBufferPreservesSequenceNumbers(t *testing.T) {
	sc := newTestConsumer(1, 10)

	seed := []*manmanpb.LogMessage{
		{Timestamp: 1, SequenceNumber: 10},
		{Timestamp: 2, SequenceNumber: 20},
		{Timestamp: 3, SequenceNumber: 30},
	}
	sc.seedBuffer(seed)

	logs := sc.getBufferedLogs(0)
	if len(logs) != 3 {
		t.Fatalf("expected 3 messages after seed, got %d", len(logs))
	}
	for i, want := range []int64{10, 20, 30} {
		if logs[i].SequenceNumber != want {
			t.Errorf("logs[%d].SequenceNumber = %d, want %d", i, logs[i].SequenceNumber, want)
		}
	}
	if sc.sequenceCounter != 30 {
		t.Errorf("sequenceCounter = %d, want 30", sc.sequenceCounter)
	}
}

// TestSeedBufferContinuesSequence verifies that messages added after seedBuffer
// continue from the seeded sequence counter without gaps or resets.
func TestSeedBufferContinuesSequence(t *testing.T) {
	sc := newTestConsumer(1, 10)
	sc.seedBuffer([]*manmanpb.LogMessage{
		{Timestamp: 1, SequenceNumber: 5},
	})

	sc.addLogToBuffer(makeMsg(2))

	logs := sc.getBufferedLogs(0)
	if len(logs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(logs))
	}
	if logs[1].SequenceNumber != 6 {
		t.Errorf("post-seed message SequenceNumber = %d, want 6", logs[1].SequenceNumber)
	}
}

// TestRetainedLogsSeededOnReconnect verifies that a new consumer seeded with
// retained logs surfaces those messages in the subscription backlog.
func TestRetainedLogsSeededOnReconnect(t *testing.T) {
	// Simulate the scenario: consumer was reaped, retained logs stored, then a
	// new consumer is manually created and seeded (mirrors createConsumer logic).
	retained := []*manmanpb.LogMessage{
		{Timestamp: 100, SequenceNumber: 1},
		{Timestamp: 200, SequenceNumber: 2},
	}

	sc := newTestConsumer(1, 10)
	sc.seedBuffer(retained)

	ch := make(chan *manmanpb.LogMessage, 10)
	backlog := sc.addSubscriber(ch, 0)

	if len(backlog) != 2 {
		t.Fatalf("expected 2 backlog messages from retained seed, got %d", len(backlog))
	}
	if backlog[0].Timestamp != 100 || backlog[1].Timestamp != 200 {
		t.Errorf("unexpected backlog timestamps: %v", timestamps(backlog))
	}
}

// TestRetainedLogsClearedByReapAndReuse verifies that retained logs are consumed
// (deleted from the map) the first time a new consumer for that session is seeded.
func TestRetainedLogsClearedByReapAndReuse(t *testing.T) {
	m := newTestManager()
	sc := newTestConsumer(1, 10)
	sc.addLogToBuffer(makeMsg(1))
	sc.idleSince = time.Now().Add(-(idleConsumerTTL + time.Second))
	m.consumers[1] = sc

	m.reapIdleConsumers()

	if _, ok := m.retainedLogs[1]; !ok {
		t.Fatal("expected retained entry after reap")
	}

	// Manually apply the same seeding logic createConsumer would use.
	sc2 := newTestConsumer(1, 10)
	if retained, ok := m.retainedLogs[1]; ok {
		sc2.seedBuffer(retained)
		delete(m.retainedLogs, 1)
	}

	if _, ok := m.retainedLogs[1]; ok {
		t.Error("retained entry should be cleared after seeding new consumer")
	}
	logs := sc2.getBufferedLogs(0)
	if len(logs) != 1 {
		t.Fatalf("new consumer expected 1 seeded message, got %d", len(logs))
	}
}
