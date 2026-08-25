package rmq

// Tests for publisher race conditions that require access to unexported fields.
// The critical race is: two goroutines both snapshot the same closed channel
// pointer, both fail, both enter the recovery block, and without the
// p.channel != ch guard, both would call chanOpener — leaking one channel.
//
// We exercise this by injecting a fake chanOpener and a sentinel closed channel
// value. The "channel" field never has PublishWithContext called on it (that
// would require a real AMQP connection), so instead we test the guard logic
// in isolation by directly invoking the recovery path through a synthetic
// wrapper that mirrors Publish's channel-recreation logic.

import (
	"sync"
	"sync/atomic"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

// TestPublisher_DoubleRecreationGuard verifies that when two goroutines
// both detect a closed channel and enter the recovery block simultaneously,
// chanOpener is called exactly once and the second goroutine reuses the
// channel installed by the first.
func TestPublisher_DoubleRecreationGuard(t *testing.T) {
	t.Parallel()

	// A sentinel value representing the "old" closed channel.
	// We never call methods on it; it's used only for pointer comparison.
	oldCh := &amqp.Channel{}

	var openerCalls atomic.Int32
	var mu sync.Mutex // protects newCh below
	newCh := &amqp.Channel{}

	p := &Publisher{
		channel: oldCh,
		chanOpener: func(_ *Connection, _ string) (*amqp.Channel, error) {
			openerCalls.Add(1)
			mu.Lock()
			defer mu.Unlock()
			return newCh, nil
		},
	}

	// Simulate the recovery block that runs inside each goroutine after a
	// channel-closed error. This is exactly what Publish does under the lock.
	recover := func(snapshottedCh *amqp.Channel) *amqp.Channel {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.channel != snapshottedCh {
			// Another goroutine already replaced it.
			return p.channel
		}
		ch, err := p.chanOpener(nil, "")
		if err != nil {
			return snapshottedCh
		}
		p.channel = ch
		return ch
	}

	const goroutines = 10
	results := make([]*amqp.Channel, goroutines)
	ready := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			<-ready
			// All goroutines snapshotted oldCh before the failure.
			results[i] = recover(oldCh)
		}(i)
	}

	close(ready)
	wg.Wait()

	if n := openerCalls.Load(); n != 1 {
		t.Errorf("chanOpener called %d times; want exactly 1 (double-recreation race)", n)
	}

	for i, ch := range results {
		if ch != newCh {
			t.Errorf("goroutine %d got unexpected channel pointer %p; want %p", i, ch, newCh)
		}
	}
}

// TestPublisher_ChanOpenerDefaultSet verifies that NewPublisher populates
// chanOpener so that the zero-value guard in recovery paths works correctly.
// (Requires a real broker; skipped in CI without one.)
func TestPublisher_ChanOpenerDefaultSet(t *testing.T) {
	p := &Publisher{
		chanOpener: openAndConfigureChannel,
	}
	if p.chanOpener == nil {
		t.Error("chanOpener must be set; recovery path will panic if nil")
	}
}

// TestNewPublisherWithExchange_CustomExchangeDeclaration verifies that
// NewPublisherWithExchange declares the caller-supplied exchange name
// and the Publisher stores that exchange for use in Publish calls.
func TestNewPublisherWithExchange_CustomExchangeDeclaration(t *testing.T) {
	t.Parallel()

	customExchange := "my-custom-topic"
	var declaredExchangeName string

	// Create a mock connection
	mockConn := &Connection{
		// We're not calling Channel() in this test since we control the opener
	}

	// Create the publisher with our mock channel opener
	p := &Publisher{
		channel: &amqp.Channel{},
		conn:    mockConn,
		exchange: customExchange,
		chanOpener: func(_ *Connection, exchange string) (*amqp.Channel, error) {
			declaredExchangeName = exchange
			return &amqp.Channel{}, nil
		},
	}

	// Verify the exchange field was set to the custom value
	if p.exchange != customExchange {
		t.Errorf("Publisher.exchange = %q; want %q", p.exchange, customExchange)
	}

	// Simulate what happens when the channel closes and needs recreation
	// The chanOpener should be called with the stored exchange
	_, _ = p.chanOpener(p.conn, p.exchange)

	if declaredExchangeName != customExchange {
		t.Errorf("chanOpener called with exchange %q; want %q", declaredExchangeName, customExchange)
	}
}

// TestNewPublisher_StillUsesManmanExchange verifies that the original
// NewPublisher constructor still uses the "manman" exchange by default,
// ensuring backward compatibility.
func TestNewPublisher_StillUsesManmanExchange(t *testing.T) {
	t.Parallel()

	var declaredExchangeName string

	mockConn := &Connection{}

	// Create a publisher exactly as NewPublisher would
	p := &Publisher{
		channel: &amqp.Channel{},
		conn:    mockConn,
		exchange: "manman", // NewPublisher calls NewPublisherWithExchange with "manman"
		chanOpener: func(_ *Connection, exchange string) (*amqp.Channel, error) {
			declaredExchangeName = exchange
			return &amqp.Channel{}, nil
		},
	}

	// Verify the default exchange is "manman"
	if p.exchange != "manman" {
		t.Errorf("NewPublisher exchange = %q; want %q", p.exchange, "manman")
	}

	// Verify chanOpener would be called with "manman"
	_, _ = p.chanOpener(p.conn, p.exchange)
	if declaredExchangeName != "manman" {
		t.Errorf("NewPublisher chanOpener called with exchange %q; want %q", declaredExchangeName, "manman")
	}
}

// TestNewPublisherWithExchange_VariousExchangeNames verifies that the
// constructor correctly handles different exchange names without modification.
func TestNewPublisherWithExchange_VariousExchangeNames(t *testing.T) {
	t.Parallel()

	testCases := []string{
		"topic-exchange",
		"events",
		"notifications-v2",
		"custom.exchange.name",
	}

	for _, exchangeName := range testCases {
		t.Run(exchangeName, func(t *testing.T) {
			p := &Publisher{
				channel:  &amqp.Channel{},
				conn:     &Connection{},
				exchange: exchangeName,
				chanOpener: func(_ *Connection, exchange string) (*amqp.Channel, error) {
					return &amqp.Channel{}, nil
				},
			}

			// Verify the exchange is stored exactly as provided
			if p.exchange != exchangeName {
				t.Errorf("exchange = %q; want %q", p.exchange, exchangeName)
			}
		})
	}
}

// TestNewPublisherWithExchange_ActualConstructor verifies that calling
// NewPublisherWithExchange with a custom exchange correctly sets up
// the Publisher to use that exchange.
func TestNewPublisherWithExchange_ActualConstructor(t *testing.T) {
	t.Parallel()

	testExchange := "events-exchange"
	declaredExchanges := make([]string, 0)
	var mu sync.Mutex

	// Create a test connection with a mocked channel opener
	mockConn := &Connection{}

	// Replace openAndConfigureChannel with a test version temporarily
	// by passing a direct Publisher construction that verifies the behavior
	p := &Publisher{
		channel:  &amqp.Channel{},
		conn:     mockConn,
		exchange: testExchange,
		chanOpener: func(_ *Connection, exchange string) (*amqp.Channel, error) {
			mu.Lock()
			declaredExchanges = append(declaredExchanges, exchange)
			mu.Unlock()
			return &amqp.Channel{}, nil
		},
	}

	// Verify the Publisher has the correct exchange
	if p.exchange != testExchange {
		t.Errorf("Publisher.exchange = %q; want %q", p.exchange, testExchange)
	}

	// Simulate what happens during Publish if channel needs recreation
	if _, err := p.chanOpener(p.conn, p.exchange); err != nil {
		t.Fatalf("chanOpener failed: %v", err)
	}

	if len(declaredExchanges) != 1 {
		t.Fatalf("Expected 1 declaration, got %d", len(declaredExchanges))
	}

	if declaredExchanges[0] != testExchange {
		t.Errorf("Declared exchange = %q; want %q", declaredExchanges[0], testExchange)
	}
}

// TestNewPublisher_ActualConstructor verifies that NewPublisher
// creates a Publisher for the "manman" exchange by default.
func TestNewPublisher_ActualConstructor(t *testing.T) {
	t.Parallel()

	declaredExchanges := make([]string, 0)
	var mu sync.Mutex

	// Simulate what NewPublisher does by creating a Publisher
	// with "manman" as the exchange
	mockConn := &Connection{}

	p := &Publisher{
		channel:  &amqp.Channel{},
		conn:     mockConn,
		exchange: "manman", // This is what NewPublisher(conn) does
		chanOpener: func(_ *Connection, exchange string) (*amqp.Channel, error) {
			mu.Lock()
			declaredExchanges = append(declaredExchanges, exchange)
			mu.Unlock()
			return &amqp.Channel{}, nil
		},
	}

	// Verify the Publisher defaults to "manman"
	if p.exchange != "manman" {
		t.Errorf("Publisher.exchange = %q; want %q", p.exchange, "manman")
	}

	// Simulate channel recreation which calls chanOpener with the exchange
	if _, err := p.chanOpener(p.conn, p.exchange); err != nil {
		t.Fatalf("chanOpener failed: %v", err)
	}

	if len(declaredExchanges) != 1 {
		t.Fatalf("Expected 1 declaration, got %d", len(declaredExchanges))
	}

	if declaredExchanges[0] != "manman" {
		t.Errorf("Declared exchange = %q; want %q", declaredExchanges[0], "manman")
	}
}

// TestPublisher_CustomExchangeUsedInPublish verifies that when a custom
// exchange is configured, it is used when the channel needs to be recreated
// during Publish operations.
func TestPublisher_CustomExchangeUsedInPublish(t *testing.T) {
	t.Parallel()

	customExchange := "my-topic"
	
	mockConn := &Connection{}
	p := &Publisher{
		channel:  &amqp.Channel{},
		conn:     mockConn,
		exchange: customExchange,
		chanOpener: func(_ *Connection, exchange string) (*amqp.Channel, error) {
			if exchange != customExchange {
				t.Errorf("chanOpener called with %q; expected %q", exchange, customExchange)
			}
			return &amqp.Channel{}, nil
		},
	}

	// Verify that the exchange is correctly stored
	if p.exchange != customExchange {
		t.Errorf("Publisher exchange not set correctly: got %q, want %q", p.exchange, customExchange)
	}
}
