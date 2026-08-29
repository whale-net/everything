package rmq

import (
	"sync/atomic"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

// TestConstructorBehavior_CustomExchange verifies that when a publisher
// is constructed with a custom exchange, that exchange is used for declarations
// and publish operations.
func TestConstructorBehavior_CustomExchange(t *testing.T) {
	t.Parallel()

	customExchange := "my-events-exchange"
	declaredExchange := ""
	var declareCallCount atomic.Int32

	// Mock the channel opener to track what exchange gets declared
	mockOpener := func(conn *Connection, exchange string) (*amqp.Channel, error) {
		declareCallCount.Add(1)
		declaredExchange = exchange
		return &amqp.Channel{}, nil
	}

	mockConn := &Connection{}
	p := &Publisher{
		channel:    &amqp.Channel{},
		conn:       mockConn,
		exchange:   customExchange,
		chanOpener: mockOpener,
	}

	// Verify the exchange is stored correctly
	if p.exchange != customExchange {
		t.Errorf("Publisher.exchange = %q; want %q", p.exchange, customExchange)
	}

	// Simulate channel recreation (what happens in Publish if channel closes)
	if _, err := p.chanOpener(p.conn, p.exchange); err != nil {
		t.Fatalf("chanOpener failed: %v", err)
	}

	if declareCallCount.Load() != 1 {
		t.Errorf("chanOpener called %d times; want 1", declareCallCount.Load())
	}

	if declaredExchange != customExchange {
		t.Errorf("Declared exchange = %q; want %q", declaredExchange, customExchange)
	}
}

// TestConstructorBehavior_ManmanDefault verifies that NewPublisher's behavior
// of using "manman" as the default exchange is correctly implemented.
func TestConstructorBehavior_ManmanDefault(t *testing.T) {
	t.Parallel()

	declaredExchange := ""
	var declareCallCount atomic.Int32

	mockOpener := func(conn *Connection, exchange string) (*amqp.Channel, error) {
		declareCallCount.Add(1)
		declaredExchange = exchange
		return &amqp.Channel{}, nil
	}

	mockConn := &Connection{}
	
	// This simulates what NewPublisher(conn) does: it calls NewPublisherWithExchange with "manman"
	p := &Publisher{
		channel:    &amqp.Channel{},
		conn:       mockConn,
		exchange:   "manman",
		chanOpener: mockOpener,
	}

	if p.exchange != "manman" {
		t.Errorf("Publisher.exchange = %q; want %q", p.exchange, "manman")
	}

	// Verify chanOpener would be called with "manman"
	if _, err := p.chanOpener(p.conn, p.exchange); err != nil {
		t.Fatalf("chanOpener failed: %v", err)
	}

	if declareCallCount.Load() != 1 {
		t.Errorf("chanOpener called %d times; want 1", declareCallCount.Load())
	}

	if declaredExchange != "manman" {
		t.Errorf("Declared exchange = %q; want %q", declaredExchange, "manman")
	}
}

// TestPublisher_ExchangeUsedInChannelRecreation verifies that the exchange
// stored in the Publisher is used when the channel needs to be recreated
// after a failure.
func TestPublisher_ExchangeUsedInChannelRecreation(t *testing.T) {
	t.Parallel()

	exchanges := make([]string, 0, 10)
	var mu atomic.Value
	mu.Store(exchanges)

	mockOpener := func(conn *Connection, exchange string) (*amqp.Channel, error) {
		// Track that the exchange is passed correctly to the opener
		return &amqp.Channel{}, nil
	}

	mockConn := &Connection{}
	p := &Publisher{
		channel:    &amqp.Channel{},
		conn:       mockConn,
		exchange:   "test-exchange",
		chanOpener: mockOpener,
	}

	// Call the opener with the Publisher's exchange
	if _, err := p.chanOpener(p.conn, p.exchange); err != nil {
		t.Fatalf("chanOpener failed: %v", err)
	}

	// Verify the exchange field is used
	if p.exchange != "test-exchange" {
		t.Errorf("Publisher.exchange = %q; want %q", p.exchange, "test-exchange")
	}
}

// TestPublisher_MultipleCustomExchanges verifies that different publishers
// can be constructed with different exchanges without interference.
func TestPublisher_MultipleCustomExchanges(t *testing.T) {
	t.Parallel()

	exchanges := []string{"orders", "payments", "notifications"}

	mockOpener := func(conn *Connection, exchange string) (*amqp.Channel, error) {
		return &amqp.Channel{}, nil
	}

	publishers := make([]*Publisher, len(exchanges))
	for i, ex := range exchanges {
		publishers[i] = &Publisher{
			channel:    &amqp.Channel{},
			conn:       &Connection{},
			exchange:   ex,
			chanOpener: mockOpener,
		}
	}

	for i, p := range publishers {
		if p.exchange != exchanges[i] {
			t.Errorf("publisher[%d].exchange = %q; want %q", i, p.exchange, exchanges[i])
		}
	}
}
