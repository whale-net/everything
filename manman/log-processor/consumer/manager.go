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
	manmanpb "github.com/whale-net/everything/manman/protos"
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
}

// Archiver is the interface for log archival
type Archiver interface {
	AddLog(sgcID, sessionID int64, timestamp time.Time, source, message string)
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
	}

	// Start stats logger
	m.statsWg.Add(1)
	go m.logStats()

	return m
}

// Subscription represents a subscription to session logs
type Subscription struct {
	ch     chan *manmanpb.LogMessage
	cancel func()
}

// Channel returns the read-only channel for receiving log messages
func (s *Subscription) Channel() <-chan *manmanpb.LogMessage {
	return s.ch
}

// Unsubscribe closes the subscription
func (s *Subscription) Unsubscribe() {
	s.cancel()
}

// Subscribe subscribes to logs for a session
// Creates a consumer if one doesn't exist yet
func (m *Manager) Subscribe(ctx context.Context, sessionID int64) (*Subscription, error) {
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

	// Create subscriber channel
	ch := make(chan *manmanpb.LogMessage, 100)
	consumer.addSubscriber(ch)

	// Create cancel function
	cancel := func() {
		m.mu.Lock()
		defer m.mu.Unlock()

		c, exists := m.consumers[sessionID]
		if !exists {
			return
		}

		c.removeSubscriber(ch)

		// If no more subscribers, clean up consumer
		if c.getSubscriberCount() == 0 {
			c.close()
			delete(m.consumers, sessionID)
		}
	}

	return &Subscription{
		ch:     ch,
		cancel: cancel,
	}, nil
}

// createConsumer creates a new RabbitMQ consumer for a session
func (m *Manager) createConsumer(ctx context.Context, sessionID int64) (*SessionConsumer, error) {
	queueName := fmt.Sprintf("logs.session.%d", sessionID)
	routingKey := fmt.Sprintf("logs.session.%d", sessionID)

	// Query session to get SGC ID
	sessionResp, err := m.grpcClient.GetSession(ctx, &manmanpb.GetSessionRequest{
		SessionId: sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get session info: %w", err)
	}

	// Create consumer with persistent queue (lifecycle-managed, not auto-deleted)
	consumer, err := rmq.NewConsumerWithOpts(m.conn, queueName, false, false)
	if err != nil {
		return nil, fmt.Errorf("failed to create RabbitMQ consumer: %w", err)
	}

	// Bind to exchange with routing key
	if err := consumer.BindExchange("manman", []string{routingKey}); err != nil {
		consumer.Close()
		return nil, fmt.Errorf("failed to bind queue to exchange: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	sc := &SessionConsumer{
		sessionID:     sessionID,
		sgcID:         sessionResp.Session.ServerGameConfigId,
		queueName:     queueName,
		consumer:      consumer,
		subscribers:   make(map[chan *manmanpb.LogMessage]struct{}),
		cancel:        cancel,
		done:          make(chan struct{}),
		logsProcessed: &m.logsProcessed,
	}

	// Start consuming in background
	go sc.consumeLoop(ctx, m.config.DebugLogOutput, m.archiver)

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

	// Close consumer (stops consuming, closes channels)
	consumer.close()

	// Explicitly delete the queue
	if err := consumer.consumer.DeleteQueue(); err != nil {
		log.Printf("[consumer-manager] failed to delete queue for session %d: %v", sessionID, err)
		// Continue anyway to clean up the consumer
	}

	// Remove from map
	delete(m.consumers, sessionID)
	log.Printf("[consumer-manager] deleted consumer for session %d", sessionID)
	return nil
}

// addSubscriber adds a subscriber channel
func (sc *SessionConsumer) addSubscriber(ch chan *manmanpb.LogMessage) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.subscribers[ch] = struct{}{}
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
			m.mu.RLock()
			activeSources := len(m.consumers)
			m.mu.RUnlock()

			logsProcessed := atomic.LoadInt64(&m.logsProcessed)
			log.Printf("[log-processor] Stats: %d logs processed from %d active source(s)", logsProcessed, activeSources)
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
