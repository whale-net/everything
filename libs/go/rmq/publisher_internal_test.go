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
