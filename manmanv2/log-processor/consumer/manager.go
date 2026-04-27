package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// LogMessage represents a log message from RabbitMQ
type LogMessage struct {
	SessionID int64  `json:"session_id"`
	Timestamp int64  `json:"timestamp"`
	Source    string `json:"source"`
	Message   string `json:"message"`
}

// SessionConsumer manages RabbitMQ consumer and broadcasts to multiple gRPC streams
type SessionConsumer struct {
	sessionID     int64
	sgcID         int64
	queueName     string
	consumer      *rmq.Consumer
	subscribers   map[chan *manmanpb.LogMessage]struct{}
	mu            sync.RWMutex
	cancel        context.CancelFunc
	done          chan struct{}
	logsProcessed *int64 // Pointer to manager's counter for atomic increment

	// idleSince is set when subscriber count drops to zero.
	// Zero value means the consumer has active subscribers.
	idleSince time.Time

	// Ring buffer for recent logs
	logBuffer       []*manmanpb.LogMessage
	bufferSize      int
	bufferIndex     int
	bufferFull      bool
	bufferMu        sync.RWMutex
	sequenceCounter int64 // monotonically increasing, protected by bufferMu
}

// Manager manages consumers for multiple sessions
type Manager struct {
	conn          *rmq.Connection
	consumers     map[int64]*SessionConsumer
	mu            sync.RWMutex
	config        *ConsumerConfig
	grpcClient    manmanpb.ManManAPIClient
	archiver      Archiver
	logsProcessed int64      // Total logs processed (atomic)
	statsCtx      context.Context
	statsCancel   context.CancelFunc
	statsWg       sync.WaitGroup
	// retainedLogs holds the last retainLogCount messages from reaped consumers so
	// a reconnecting client still gets recent context. Protected by m.mu.
	retainedLogs map[int64][]*manmanpb.LogMessage
}

// Archiver is the interface for log archival
type Archiver interface {
	AddLog(sgcID, sessionID int64, timestamp time.Time, source, message string)
	FlushSession(ctx context.Context, sessionID int64) error
}

// ConsumerConfig holds configuration for consumers
type ConsumerConfig struct {
	LogBufferTTL     int
	LogBufferMaxMsgs int
	DebugLogOutput   bool
}

// NewManager creates a new consumer manager
func NewManager(conn *rmq.Connection, config *ConsumerConfig, grpcClient manmanpb.ManManAPIClient, archiver Archiver) *Manager {
	ctx, cancel := context.WithCancel(context.Background())

	m := &Manager{
		conn:          conn,
		consumers:     make(map[int64]*SessionConsumer),
		config:        config,
		grpcClient:    grpcClient,
		archiver:      archiver,
		logsProcessed: 0,
		statsCtx:      ctx,
		statsCancel:   cancel,
		retainedLogs:  make(map[int64][]*manmanpb.LogMessage),
	}

	// Start stats logger
	m.statsWg.Add(1)
	go m.logStats()

	return m
}

// Subscription represents a subscription to session logs
type Subscription struct {
	ch      chan *manmanpb.LogMessage
	backlog []*manmanpb.LogMessage
	cancel  func()
}

// Backlog returns buffered log messages that existed at the time of subscription.
// These should be sent to the client before reading from Channel().
func (s *Subscription) Backlog() []*manmanpb.LogMessage {
	return s.backlog
}

// Channel returns the read-only channel for receiving live log messages
func (s *Subscription) Channel() <-chan *manmanpb.LogMessage {
	return s.ch
}

// Unsubscribe closes the subscription
func (s *Subscription) Unsubscribe() {
	s.cancel()
}

// Subscribe subscribes to logs for a session.
// Creates a consumer if one doesn't exist yet.
// afterSequence filters the backlog: only messages with sequence_number > afterSequence are
// included. Pass 0 to receive the full backlog (initial connect). On reconnect, pass the
// last sequence_number the client received to skip already-seen messages.
func (m *Manager) Subscribe(ctx context.Context, sessionID int64, afterSequence int64) (*Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	consumer, exists := m.consumers[sessionID]
	if !exists {
		// Create new consumer
		var err error
		consumer, err = m.createConsumer(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("failed to create consumer: %w", err)
		}
		m.consumers[sessionID] = consumer
	}

	// Create subscriber channel. Capacity only needs to cover live message bursts
	// since backlog is delivered separately via Subscription.Backlog().
	ch := make(chan *manmanpb.LogMessage, 100)
	backlog := consumer.addSubscriber(ch, afterSequence)

	// Create cancel function
	cancel := func() {
		m.mu.Lock()
		defer m.mu.Unlock()

		c, exists := m.consumers[sessionID]
		if !exists {
			return
		}

		c.removeSubscriber(ch)

		// Mark idle instead of closing. The ring buffer stays alive so reconnecting
		// clients get the full backlog. The periodic cleanup in logStats reaps
		// consumers that remain subscriber-less beyond the idle TTL.
		if c.getSubscriberCount() == 0 {
			c.idleSince = time.Now()
		}
	}

	return &Subscription{
		ch:      ch,
		backlog: backlog,
		cancel:  cancel,
	}, nil
}

// createConsumer creates a new RabbitMQ consumer for a session
func (m *Manager) createConsumer(ctx context.Context, sessionID int64) (*SessionConsumer, error) {
	queueName := fmt.Sprintf("logs.session.%d", sessionID)
	routingKey := fmt.Sprintf("logs.session.%d", sessionID)

	// Use background context for the consumer loop and API calls since this consumer
	// is shared natively across all users and shouldn't die when the first viewer disconnects.
	consumerCtx, cancel := context.WithCancel(context.Background())

	// Query session to get SGC ID
	sessionResp, err := m.grpcClient.GetSession(consumerCtx, &manmanpb.GetSessionRequest{
		SessionId: sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get session info: %w", err)
	}

	// Create consumer with durable queue so the queue and accumulated messages
	// survive log-processor restarts. Lifecycle is managed explicitly:
	// DeleteConsumerForSession deletes the queue when the session ends.
	messageTTL := m.config.LogBufferTTL * 1000 // Convert seconds to milliseconds
	consumer, err := rmq.NewConsumerWithOpts(m.conn, queueName, true, false, messageTTL, m.config.LogBufferMaxMsgs)
	if err != nil {
		return nil, fmt.Errorf("failed to create RabbitMQ consumer: %w", err)
	}

	// Bind to exchange with routing key
	if err := consumer.BindExchange("manman", []string{routingKey}); err != nil {
		consumer.Close()
		return nil, fmt.Errorf("failed to bind queue to exchange: %w", err)
	}

	bufferSize := 500 // Keep last 500 log messages in memory
	sc := &SessionConsumer{
		sessionID:     sessionID,
		sgcID:         sessionResp.Session.ServerGameConfigId,
		queueName:     queueName,
		consumer:      consumer,
		subscribers:   make(map[chan *manmanpb.LogMessage]struct{}),
		cancel:        cancel, // This cancel is for the consumerCtx
		done:          make(chan struct{}),
		logsProcessed: &m.logsProcessed,
		logBuffer:     make([]*manmanpb.LogMessage, bufferSize),
		bufferSize:    bufferSize,
		bufferIndex:   0,
		bufferFull:    false,
	}

	// Seed with any logs retained from a previous consumer reap so reconnecting
	// clients get recent context without waiting for RabbitMQ to re-deliver.
	if retained, ok := m.retainedLogs[sessionID]; ok {
		sc.seedBuffer(retained)
		delete(m.retainedLogs, sessionID)
	}


	// Start consuming in background using the detached context
	go sc.consumeLoop(consumerCtx, m.config.DebugLogOutput, m.archiver)

	return sc, nil
}

// CreateConsumerForSession creates a consumer for a session (called by lifecycle handler)
func (m *Manager) CreateConsumerForSession(ctx context.Context, sessionID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if consumer already exists
	if _, exists := m.consumers[sessionID]; exists {
		log.Printf("[consumer-manager] consumer already exists for session %d", sessionID)
		return nil // Idempotent
	}

	// Create new consumer
	consumer, err := m.createConsumer(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to create consumer: %w", err)
	}

	m.consumers[sessionID] = consumer
	log.Printf("[consumer-manager] created consumer for session %d (lifecycle-driven)", sessionID)
	return nil
}

// DeleteConsumerForSession deletes a consumer for a session (called by lifecycle handler)
func (m *Manager) DeleteConsumerForSession(sessionID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	consumer, exists := m.consumers[sessionID]
	if !exists {
		log.Printf("[consumer-manager] no consumer exists for session %d", sessionID)
		return nil // Idempotent
	}

	// Flush pending logs to S3 before closing consumer
	if m.archiver != nil {
		log.Printf("[consumer-manager] flushing logs for session %d before deletion", sessionID)
		if err := m.archiver.FlushSession(context.Background(), sessionID); err != nil {
			log.Printf("[consumer-manager] failed to flush logs for session %d: %v", sessionID, err)
			// Continue anyway - we don't want to block consumer deletion
		}
	}

	// Close consumer (stops consuming, closes channels)
	consumer.close()

	// Explicitly delete the queue
	if err := consumer.consumer.DeleteQueue(); err != nil {
		log.Printf("[consumer-manager] failed to delete queue for session %d: %v", sessionID, err)
		// Continue anyway to clean up the consumer
	}

	// Remove from maps
	delete(m.consumers, sessionID)
	delete(m.retainedLogs, sessionID)
	log.Printf("[consumer-manager] deleted consumer for session %d", sessionID)
	return nil
}

// addLogToBuffer assigns a sequence number and adds a log message to the ring buffer.
// The sequence number is assigned under bufferMu so it is stable once visible to readers.
func (sc *SessionConsumer) addLogToBuffer(msg *manmanpb.LogMessage) {
	sc.bufferMu.Lock()
	defer sc.bufferMu.Unlock()

	sc.sequenceCounter++
	msg.SequenceNumber = sc.sequenceCounter

	sc.logBuffer[sc.bufferIndex] = msg
	sc.bufferIndex++
	if sc.bufferIndex >= sc.bufferSize {
		sc.bufferIndex = 0
		sc.bufferFull = true
	}
}

// getBufferedLogs returns buffered logs in chronological order with sequence_number
// greater than afterSequence. Pass 0 to receive the full backlog.
func (sc *SessionConsumer) getBufferedLogs(afterSequence int64) []*manmanpb.LogMessage {
	sc.bufferMu.RLock()
	defer sc.bufferMu.RUnlock()

	var ordered []*manmanpb.LogMessage
	if !sc.bufferFull {
		ordered = make([]*manmanpb.LogMessage, sc.bufferIndex)
		copy(ordered, sc.logBuffer[:sc.bufferIndex])
	} else {
		// Buffer is full: return logs from bufferIndex to end, then 0 to bufferIndex-1
		ordered = make([]*manmanpb.LogMessage, sc.bufferSize)
		copy(ordered, sc.logBuffer[sc.bufferIndex:])
		copy(ordered[sc.bufferSize-sc.bufferIndex:], sc.logBuffer[:sc.bufferIndex])
	}

	if afterSequence == 0 {
		return ordered
	}

	// Sequence numbers are monotonically increasing, so find the first entry
	// past the client's last-seen sequence and return from there.
	for i, msg := range ordered {
		if msg.SequenceNumber > afterSequence {
			return ordered[i:]
		}
	}
	return nil
}

// seedBuffer pre-populates the ring buffer with msgs whose sequence numbers are already
// assigned (e.g. retained from a previous consumer). The sequenceCounter is advanced to
// the highest sequence number seen so new messages continue the same sequence.
// Must be called before consumeLoop is started (no lock needed at that point).
func (sc *SessionConsumer) seedBuffer(msgs []*manmanpb.LogMessage) {
	sc.bufferMu.Lock()
	defer sc.bufferMu.Unlock()

	for _, msg := range msgs {
		sc.logBuffer[sc.bufferIndex] = msg
		sc.bufferIndex++
		if sc.bufferIndex >= sc.bufferSize {
			sc.bufferIndex = 0
			sc.bufferFull = true
		}
		if msg.SequenceNumber > sc.sequenceCounter {
			sc.sequenceCounter = msg.SequenceNumber
		}
	}
}

// addSubscriber registers a subscriber channel and returns the current backlog atomically.
// The caller must send all backlog messages to the client before reading from ch,
// guaranteeing that no messages are missed or duplicated: any message added to the
// ring buffer after this call will also be broadcast to ch.
// Only backlog messages with sequence_number > afterSequence are returned; pass 0 for the full backlog.
func (sc *SessionConsumer) addSubscriber(ch chan *manmanpb.LogMessage, afterSequence int64) []*manmanpb.LogMessage {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.subscribers[ch] = struct{}{}
	sc.idleSince = time.Time{} // clear idle marker
	return sc.getBufferedLogs(afterSequence)
}

// removeSubscriber removes a subscriber channel
func (sc *SessionConsumer) removeSubscriber(ch chan *manmanpb.LogMessage) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	delete(sc.subscribers, ch)
	close(ch)
}

// getSubscriberCount returns the number of active subscribers
func (sc *SessionConsumer) getSubscriberCount() int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return len(sc.subscribers)
}

// close closes the consumer and all subscribers
func (sc *SessionConsumer) close() {
	sc.cancel()
	<-sc.done

	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Close all subscriber channels
	for ch := range sc.subscribers {
		close(ch)
	}
	sc.subscribers = make(map[chan *manmanpb.LogMessage]struct{})

	// Close RabbitMQ consumer
	if sc.consumer != nil {
		sc.consumer.Close()
	}
}

// consumeLoop consumes messages from RabbitMQ and broadcasts to subscribers
func (sc *SessionConsumer) consumeLoop(ctx context.Context, debugOutput bool, archiver Archiver) {
	defer close(sc.done)

	// Register message handler
	sc.consumer.RegisterHandler("#", func(ctx context.Context, msg rmq.Message) error {
		// Parse log message
		var logMsg LogMessage
		if err := json.Unmarshal(msg.Body, &logMsg); err != nil {
			log.Printf("[log-processor] failed to unmarshal log message: %v", err)
			return nil // Don't retry on unmarshal errors
		}

		// Debug output
		if debugOutput {
			log.Printf("[session %d] [%s] %s", logMsg.SessionID, logMsg.Source, logMsg.Message)
		}

		// Increment processed counter
		if sc.logsProcessed != nil {
			atomic.AddInt64(sc.logsProcessed, 1)
		}

		// Convert timestamp to time.Time (timestamp is in milliseconds, explicitly UTC)
		timestamp := time.UnixMilli(logMsg.Timestamp).UTC()

		// Archive log if archiver is provided
		if archiver != nil {
			archiver.AddLog(sc.sgcID, logMsg.SessionID, timestamp, logMsg.Source, logMsg.Message)
		}

		// Convert to protobuf message
		pbMsg := &manmanpb.LogMessage{
			SessionId: logMsg.SessionID,
			Timestamp: logMsg.Timestamp,
			Source:    logMsg.Source,
			Message:   logMsg.Message,
		}

		// Add to ring buffer for new subscribers
		sc.addLogToBuffer(pbMsg)

		// Broadcast to all subscribers
		sc.mu.RLock()
		for ch := range sc.subscribers {
			select {
			case ch <- pbMsg:
			default:
				// Channel full, skip (slow consumer)
				log.Printf("[log-processor] subscriber channel full for session %d", sc.sessionID)
			}
		}
		sc.mu.RUnlock()

		return nil
	})

	// Start consuming
	if err := sc.consumer.Start(ctx); err != nil {
		log.Printf("[log-processor] failed to start consuming for session %d: %v", sc.sessionID, err)
		return
	}

	// Wait for context cancellation
	<-ctx.Done()
}

// logStats logs processing statistics every 30 seconds
func (m *Manager) logStats() {
	defer m.statsWg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.statsCtx.Done():
			return
		case <-ticker.C:
			m.reapIdleConsumers()

			m.mu.RLock()
			activeSources := len(m.consumers)
			m.mu.RUnlock()

			logsProcessed := atomic.LoadInt64(&m.logsProcessed)
			log.Printf("[log-processor] Stats: %d logs processed from %d active source(s)", logsProcessed, activeSources)
		}
	}
}

const (
	idleConsumerTTL = 5 * time.Minute
	// retainLogCount is the number of recent log messages preserved in memory after
	// a consumer is reaped, so reconnecting clients still receive recent context.
	retainLogCount = 50
)

// reapIdleConsumers closes and removes on-demand consumers that have had no
// subscribers for longer than idleConsumerTTL. Lifecycle-managed consumers are
// cleaned up by DeleteConsumerForSession when the session ends.
func (m *Manager) reapIdleConsumers() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for sessionID, c := range m.consumers {
		c.mu.RLock()
		idle := !c.idleSince.IsZero() && now.Sub(c.idleSince) > idleConsumerTTL
		c.mu.RUnlock()
		if idle {
			// Retain the tail of the ring buffer so a reconnecting client still gets
			// recent context even after the RabbitMQ consumer has been torn down.
			logs := c.getBufferedLogs(0)
			if len(logs) > retainLogCount {
				logs = logs[len(logs)-retainLogCount:]
			}
			if len(logs) > 0 {
				m.retainedLogs[sessionID] = logs
			}
			c.close()
			delete(m.consumers, sessionID)
			log.Printf("[consumer-manager] reaped idle consumer for session %d (retained %d messages)", sessionID, len(logs))
		}
	}
}

// Close closes all consumers
func (m *Manager) Close() {
	// Stop stats logger
	m.statsCancel()
	m.statsWg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	for sessionID, consumer := range m.consumers {
		consumer.close()
		delete(m.consumers, sessionID)
	}
}
