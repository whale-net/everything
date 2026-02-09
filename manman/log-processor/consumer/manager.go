package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

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
	sessionID  int64
	queueName  string
	consumer   *rmq.Consumer
	subscribers map[chan *manmanpb.LogMessage]struct{}
	mu         sync.RWMutex
	cancel     context.CancelFunc
	done       chan struct{}
}

// Manager manages consumers for multiple sessions
type Manager struct {
	conn      *rmq.Connection
	consumers map[int64]*SessionConsumer
	mu        sync.RWMutex
	config    *ConsumerConfig
}

// ConsumerConfig holds configuration for consumers
type ConsumerConfig struct {
	LogBufferTTL     int
	LogBufferMaxMsgs int
	DebugLogOutput   bool
}

// NewManager creates a new consumer manager
func NewManager(conn *rmq.Connection, config *ConsumerConfig) *Manager {
	return &Manager{
		conn:      conn,
		consumers: make(map[int64]*SessionConsumer),
		config:    config,
	}
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

	// Create consumer with auto-delete queue (will be removed when last consumer disconnects)
	consumer, err := rmq.NewConsumerWithOpts(m.conn, queueName, false, true)
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
		sessionID:   sessionID,
		queueName:   queueName,
		consumer:    consumer,
		subscribers: make(map[chan *manmanpb.LogMessage]struct{}),
		cancel:      cancel,
		done:        make(chan struct{}),
	}

	// Start consuming in background
	go sc.consumeLoop(ctx, m.config.DebugLogOutput)

	return sc, nil
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
func (sc *SessionConsumer) consumeLoop(ctx context.Context, debugOutput bool) {
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

// Close closes all consumers
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for sessionID, consumer := range m.consumers {
		consumer.close()
		delete(m.consumers, sessionID)
	}
}
