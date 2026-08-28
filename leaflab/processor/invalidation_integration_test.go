//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never even compiles it.
// See the go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest/README.md for how to run an equivalent Postgres-backed
// test -- this file follows the same pattern (testcontainers-go, "manual"
// Bazel target) but against a real RabbitMQ instead of Postgres.
//
// It proves the two FR73/NFR15 properties that need a real broker and
// therefore cannot be covered by handler_test.go's pure unit tests (see
// that file's "FR73: cross-process cache invalidation" section, and
// leaflab/invalidation/BUILD.bazel's doc comment on why broadcast
// behaviour is covered here and not in leaflab/invalidation itself):
//
//   - NFR15's every-replica broadcast constraint: every currently-attached
//     Subscriber receives every Event, not just one of a competing set.
//   - A RabbitMQ connection drop on either the Publisher or a Subscriber is
//     detected and recovered from, without permanently breaking delivery
//     until process restart.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/processor:invalidation_integration_test --test_output=all
package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/whale-net/everything/leaflab/invalidation"
	"github.com/whale-net/everything/libs/go/rmq"
)

// rabbitMQImage is the image every test in this file shares one container
// of (see sharedBroker below) -- mirrors dbtest's DefaultImage in spirit,
// pinned to a management-enabled tag purely for local debugging
// convenience (this file never uses the management API).
const rabbitMQImage = "rabbitmq:3.13-management-alpine"

// sharedBroker is one RabbitMQ container reused across every test in this
// binary, started lazily on first use -- amortizing the container-start
// cost the same way dbtest.getSharedContainer amortizes Postgres's. Unlike
// dbtest, there is no per-test database/role provisioning step here: each
// test instead opens its own rmq.Connection (its own AMQP TCP connection)
// to the shared broker, which is exactly the isolation this file's
// reconnect tests need -- forcibly closing one test's connection must not
// affect any other test's.
var sharedBroker struct {
	once sync.Once
	url  string
	err  error
}

// brokerURL returns the shared RabbitMQ container's amqp:// URL, starting
// the container on first call. Fails t immediately on any setup error.
func brokerURL(ctx context.Context, t *testing.T) string {
	t.Helper()

	sharedBroker.once.Do(func() {
		ctr, err := testcontainers.Run(ctx, rabbitMQImage,
			testcontainers.WithExposedPorts("5672/tcp"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("Server startup complete").WithStartupTimeout(60*time.Second),
			),
		)
		if err != nil {
			sharedBroker.err = fmt.Errorf("invalidation_integration_test: start rabbitmq container: %w", err)
			return
		}

		host, err := ctr.Host(ctx)
		if err != nil {
			sharedBroker.err = fmt.Errorf("invalidation_integration_test: container host: %w", err)
			return
		}
		port, err := ctr.MappedPort(ctx, "5672/tcp")
		if err != nil {
			sharedBroker.err = fmt.Errorf("invalidation_integration_test: mapped port: %w", err)
			return
		}
		sharedBroker.url = fmt.Sprintf("amqp://guest:guest@%s:%s/", host, port.Port())
	})

	if sharedBroker.err != nil {
		t.Fatalf("%v", sharedBroker.err)
	}
	return sharedBroker.url
}

// newConn opens a fresh rmq.Connection (its own AMQP TCP connection) to the
// shared broker, registering t.Cleanup to close it.
func newConn(ctx context.Context, t *testing.T) *rmq.Connection {
	t.Helper()
	conn, err := rmq.NewConnectionFromURL(brokerURL(ctx, t))
	if err != nil {
		t.Fatalf("newConn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// collectOne waits up to timeout for exactly one Event on ch, failing t if
// none arrives.
func collectOne(t *testing.T, ch <-chan invalidation.Event, timeout time.Duration) invalidation.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(timeout):
		t.Fatal("timed out waiting for invalidation.Event")
		return invalidation.Event{}
	}
}

// subscribeToChan starts sub and returns a channel every Event it observes
// is pushed to, so a test can assert on delivery with select/time.After
// instead of the Handler's own goroutine.
func subscribeToChan(ctx context.Context, t *testing.T, sub *invalidation.Subscriber) <-chan invalidation.Event {
	t.Helper()
	ch := make(chan invalidation.Event, 8)
	if err := sub.Start(ctx, func(_ context.Context, ev invalidation.Event) {
		ch <- ev
	}); err != nil {
		t.Fatalf("subscriber.Start: %v", err)
	}
	return ch
}

// TestInvalidation_EveryReplicaFanout is NFR15's load-bearing test: with
// two Subscribers attached (standing in for two API/processor replicas),
// both must receive the same published Event -- not just one of a
// competing-consumer set winning it. See ExchangeName's doc comment: this
// works because every Subscriber declares its own exclusive queue bound to
// a *fanout* exchange, so every bound queue gets its own copy of every
// message.
//
// This test would fail under a competing-consumer configuration (e.g. every
// Subscriber sharing one named queue, or a topic exchange routed to a
// single queue): only one of the two subscribeToChan channels below would
// ever receive the event, and the other's collectOne would time out. That
// is exactly the failure this test is written to catch.
func TestInvalidation_EveryReplicaFanout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pubConn := newConn(ctx, t)
	pub, err := invalidation.NewPublisher(pubConn)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	defer pub.Close() //nolint:errcheck

	sub1Conn := newConn(ctx, t)
	sub1, err := invalidation.NewSubscriber(sub1Conn, nil)
	if err != nil {
		t.Fatalf("NewSubscriber (replica 1): %v", err)
	}
	defer sub1.Close() //nolint:errcheck

	sub2Conn := newConn(ctx, t)
	sub2, err := invalidation.NewSubscriber(sub2Conn, nil)
	if err != nil {
		t.Fatalf("NewSubscriber (replica 2): %v", err)
	}
	defer sub2.Close() //nolint:errcheck

	ch1 := subscribeToChan(ctx, t, sub1)
	ch2 := subscribeToChan(ctx, t, sub2)

	// Give both Subscribers' queues time to bind before publishing --
	// RabbitMQ's fanout exchange only delivers to queues bound at publish
	// time, same as any AMQP fanout.
	time.Sleep(200 * time.Millisecond)

	want := invalidation.Event{
		Kind:       invalidation.KindRegion,
		DeviceID:   "leaflab-fanout-test",
		SensorID:   1,
		SensorName: "temp",
		ObservedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := pub.Publish(ctx, want); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got1 := collectOne(t, ch1, 5*time.Second)
	got2 := collectOne(t, ch2, 5*time.Second)

	if got1.DeviceID != want.DeviceID || got1.SensorName != want.SensorName {
		t.Errorf("replica 1 got %+v, want %+v", got1, want)
	}
	if got2.DeviceID != want.DeviceID || got2.SensorName != want.SensorName {
		t.Errorf("replica 2 got %+v, want %+v", got2, want)
	}
}

// TestInvalidation_PublisherReconnectsAfterConnectionDrop forces the
// Publisher's underlying AMQP TCP connection closed (simulating a RabbitMQ
// connection drop, a broker restart, or a network blip) and asserts that a
// subsequent Publish still succeeds and is still delivered -- i.e. the
// drop is detected and recovered from (publisher.go's declare/reconnect
// logic in Publish), not a silent, permanent stop to this process ever
// publishing another invalidation event again.
func TestInvalidation_PublisherReconnectsAfterConnectionDrop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pubConn := newConn(ctx, t)
	pub, err := invalidation.NewPublisher(pubConn)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	defer pub.Close() //nolint:errcheck

	subConn := newConn(ctx, t)
	sub, err := invalidation.NewSubscriber(subConn, nil)
	if err != nil {
		t.Fatalf("NewSubscriber: %v", err)
	}
	defer sub.Close() //nolint:errcheck
	ch := subscribeToChan(ctx, t, sub)
	time.Sleep(200 * time.Millisecond)

	// Sanity: publish succeeds before any drop.
	pre := invalidation.Event{Kind: invalidation.KindRegion, DeviceID: "leaflab-reconnect-pub", SensorName: "before"}
	if err := pub.Publish(ctx, pre); err != nil {
		t.Fatalf("Publish before drop: %v", err)
	}
	if got := collectOne(t, ch, 5*time.Second); got.SensorName != "before" {
		t.Fatalf("expected %q before drop, got %+v", "before", got)
	}

	// Simulate a RabbitMQ connection drop for the *publisher's* connection
	// only -- pubConn.GetConnection() is the underlying *amqp.Connection
	// rmq.Connection wraps; forcing it closed is indistinguishable, from
	// Publisher's point of view, from the broker restarting or a network
	// blip severing this TCP connection.
	if err := pubConn.GetConnection().Close(); err != nil {
		t.Fatalf("force-close publisher connection: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		post := invalidation.Event{Kind: invalidation.KindRegion, DeviceID: "leaflab-reconnect-pub", SensorName: "after"}
		if lastErr = pub.Publish(ctx, post); lastErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("Publish never recovered after connection drop: %v", lastErr)
	}

	got := collectOne(t, ch, 5*time.Second)
	if got.SensorName != "after" {
		t.Fatalf("expected %q to be delivered after publisher reconnect, got %+v", "after", got)
	}
}

// TestInvalidation_SubscriberReconnectsAfterConnectionDrop forces a
// Subscriber's underlying AMQP TCP connection closed and asserts that an
// Event published *after* the drop is still delivered -- i.e. Start's
// background reconnect loop (subscriber.go's reconnect) redeclares a fresh
// exclusive queue and resumes consuming, rather than silently and
// permanently stopping delivery to this Subscriber until process restart.
func TestInvalidation_SubscriberReconnectsAfterConnectionDrop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pubConn := newConn(ctx, t)
	pub, err := invalidation.NewPublisher(pubConn)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	defer pub.Close() //nolint:errcheck

	subConn := newConn(ctx, t)
	sub, err := invalidation.NewSubscriber(subConn, nil)
	if err != nil {
		t.Fatalf("NewSubscriber: %v", err)
	}
	defer sub.Close() //nolint:errcheck
	ch := subscribeToChan(ctx, t, sub)
	time.Sleep(200 * time.Millisecond)

	pre := invalidation.Event{Kind: invalidation.KindRegion, DeviceID: "leaflab-reconnect-sub", SensorName: "before"}
	if err := pub.Publish(ctx, pre); err != nil {
		t.Fatalf("Publish before drop: %v", err)
	}
	if got := collectOne(t, ch, 5*time.Second); got.SensorName != "before" {
		t.Fatalf("expected %q before drop, got %+v", "before", got)
	}

	// Simulate a RabbitMQ connection drop for the *subscriber's* connection
	// only -- pub/pubConn are entirely separate connections (see newConn),
	// so the publisher is never affected by this.
	if err := subConn.GetConnection().Close(); err != nil {
		t.Fatalf("force-close subscriber connection: %v", err)
	}

	// Give the Subscriber's reconnect goroutine (exponential backoff
	// starting at reconnectMinDelay = 1s) time to redeclare its queue and
	// resume consuming before publishing the event it must still receive.
	time.Sleep(3 * time.Second)

	post := invalidation.Event{Kind: invalidation.KindRegion, DeviceID: "leaflab-reconnect-sub", SensorName: "after"}
	if err := pub.Publish(ctx, post); err != nil {
		t.Fatalf("Publish after subscriber drop: %v", err)
	}

	got := collectOne(t, ch, 15*time.Second)
	if got.SensorName != "after" {
		t.Fatalf("expected %q to be delivered after subscriber reconnect, got %+v", "after", got)
	}
}
