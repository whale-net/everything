//go:build integration

// This file only builds under the "integration" build tag so `bazel test
// //...` (which runs on Docker-less machines too) never even compiles it.
// See //libs/go/dbtest/README.md and
// leaflab/processor/invalidation_integration_test.go for the pattern this
// file follows (testcontainers-go, "manual" Bazel target) against a real
// RabbitMQ instead of Postgres.
//
// It proves NFR15's falsifiable claim, phrased against the piece Phase 4
// actually adds on top of Phase 3's fanout mechanism: "with N API
// replicas, a bounded wait issued against any replica resolves within the
// freshness bound regardless of which replica received the request." Each
// "replica" here is exactly what main.go wires per process: its own
// ackwait.Registry plus its own invalidation.Subscriber bound to the same
// leaflab.invalidation fanout exchange FR73 already uses, whose handler
// calls Registry.Notify on every KindAck event -- not a re-implementation
// of that wiring, the same call shape main.go uses.
//
// TestNFR15_CompetingConsumerTransport_DoesNotSatisfyEveryReplicaConstraint
// is the issue's "configure the transport as competing-consumer ... and
// assert this test fails" requirement, made concrete: rather than only
// documenting in prose that a work-queue transport would break the
// property (as invalidation_integration_test.go's fanout test does), this
// file actually builds a competing-consumer queue against the same broker
// and asserts the every-replica property does NOT hold for it -- proving
// the fanout test above has teeth, not just plausible narration.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:nfr15_ack_broadcast_integration_test --test_output=all
package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/whale-net/everything/leaflab/api/ackwait"
	"github.com/whale-net/everything/leaflab/invalidation"
	"github.com/whale-net/everything/libs/go/rmq"
)

const rabbitMQImage = "rabbitmq:3.13-management-alpine"

// sharedBroker is one RabbitMQ container reused across every test in this
// binary -- see leaflab/processor/invalidation_integration_test.go's
// identical sharedBroker for the amortization rationale. Duplicated here
// (not shared across packages) because this file is its own go_test
// target/binary, same as that package's own doc comment explains for its
// helpers.
var sharedBroker struct {
	once sync.Once
	url  string
	err  error
}

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
			sharedBroker.err = fmt.Errorf("nfr15_ack_broadcast: start rabbitmq container: %w", err)
			return
		}
		host, err := ctr.Host(ctx)
		if err != nil {
			sharedBroker.err = fmt.Errorf("nfr15_ack_broadcast: container host: %w", err)
			return
		}
		port, err := ctr.MappedPort(ctx, "5672/tcp")
		if err != nil {
			sharedBroker.err = fmt.Errorf("nfr15_ack_broadcast: mapped port: %w", err)
			return
		}
		sharedBroker.url = fmt.Sprintf("amqp://guest:guest@%s:%s/", host, port.Port())
	})
	if sharedBroker.err != nil {
		t.Fatalf("%v", sharedBroker.err)
	}
	return sharedBroker.url
}

func newConn(ctx context.Context, t *testing.T) *rmq.Connection {
	t.Helper()
	conn, err := rmq.NewConnectionFromURL(brokerURL(ctx, t))
	if err != nil {
		t.Fatalf("newConn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// apiReplica stands in for one API process's Phase 4 wiring: its own
// ackwait.Registry, and its own invalidation.Subscriber bound to the
// shared fanout exchange whose handler resolves that Registry on every
// KindAck event -- exactly main.go's wiring, reproduced here (not
// depended on directly: main.go's run() also opens a real DB pool, which
// this test has no need of).
type apiReplica struct {
	registry *ackwait.Registry
	sub      *invalidation.Subscriber
}

func newAPIReplica(ctx context.Context, t *testing.T) *apiReplica {
	t.Helper()
	conn := newConn(ctx, t)
	sub, err := invalidation.NewSubscriber(conn, nil)
	if err != nil {
		t.Fatalf("NewSubscriber: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	registry := ackwait.NewRegistry()
	if err := sub.Start(ctx, func(_ context.Context, ev invalidation.Event) {
		if ev.Kind != invalidation.KindAck {
			return
		}
		registry.Notify(ev.DeviceID, ev.Version, ev.Accepted, ev.RejectionReason)
	}); err != nil {
		t.Fatalf("Subscriber.Start: %v", err)
	}
	return &apiReplica{registry: registry, sub: sub}
}

// waitResult is what a bounded Wait call resolves to, captured off the
// goroutine that ran it so the test can assert on it after the fact.
type waitResult struct {
	result  ackwait.Result
	reason  string
	elapsed time.Duration
}

// beginWait starts replica.registry.Wait for (deviceID, version) in a
// background goroutine and returns a channel the result arrives on once
// Wait returns -- letting a test register a waiter, then publish the ack
// that resolves it, without blocking the publish on the wait itself.
func beginWait(ctx context.Context, replica *apiReplica, deviceID string, version int64, timeout time.Duration) <-chan waitResult {
	ch := make(chan waitResult, 1)
	go func() {
		start := time.Now()
		res, reason := replica.registry.Wait(ctx, deviceID, version, timeout)
		ch <- waitResult{result: res, reason: reason, elapsed: time.Since(start)}
	}()
	return ch
}

// TestNFR15_AwaitConfigAck_BoundedWaitResolvesOnEveryReplica_RegardlessOfWhichReceivedIt
// is NFR15's falsifiable claim: two independent API replicas, each with
// its own bounded wait open for the *same* (device_id, version), both
// resolve once a single KindAck event is published -- standing in for
// "the processor writes the ack" -- and each resolves well within NFR15's
// 2s freshness bound. Neither replica's resolution depends on which one a
// caller's AwaitConfigAck request happened to land on.
func TestNFR15_AwaitConfigAck_BoundedWaitResolvesOnEveryReplica_RegardlessOfWhichReceivedIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	replicaA := newAPIReplica(ctx, t)
	replicaB := newAPIReplica(ctx, t)

	pubConn := newConn(ctx, t)
	pub, err := invalidation.NewPublisher(pubConn)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	defer pub.Close() //nolint:errcheck

	const deviceID = "leaflab-nfr15-fanout"
	const version = int64(42)

	// Register a waiter on *both* replicas for the same (device_id,
	// version) before publishing -- a real deployment cannot know in
	// advance which replica a caller's AwaitConfigAck will land on, so
	// both must be observing.
	chA := beginWait(ctx, replicaA, deviceID, version, 10*time.Second)
	chB := beginWait(ctx, replicaB, deviceID, version, 10*time.Second)

	// Give both Subscribers' queues time to bind before publishing --
	// RabbitMQ's fanout exchange only delivers to queues bound at publish
	// time (see leaflab/processor/invalidation_integration_test.go's
	// identical note).
	time.Sleep(200 * time.Millisecond)

	if err := pub.Publish(ctx, invalidation.Event{
		Kind:       invalidation.KindAck,
		DeviceID:   deviceID,
		Version:    version,
		Accepted:   true,
		ObservedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	const freshnessBound = 2 * time.Second
	for name, ch := range map[string]<-chan waitResult{"replica A": chA, "replica B": chB} {
		select {
		case got := <-ch:
			if got.result != ackwait.ResultAccepted {
				t.Errorf("%s: result = %v, want %v", name, got.result, ackwait.ResultAccepted)
			}
			if got.elapsed > freshnessBound {
				t.Errorf("%s: resolved in %v, want within NFR15's %v freshness bound", name, got.elapsed, freshnessBound)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: bounded wait never resolved -- NFR15's every-replica broadcast did not reach it", name)
		}
	}
}

// TestNFR15_AwaitConfigAck_RejectResolvesOnEveryReplicaWithVerbatimReason
// mirrors the accept case for a rejecting ack, proving the verbatim
// rejection reason (FR45/FR47) survives the real cross-process broadcast
// unmodified on both replicas, not just in the in-process
// ackwait.Registry unit tests.
func TestNFR15_AwaitConfigAck_RejectResolvesOnEveryReplicaWithVerbatimReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	replicaA := newAPIReplica(ctx, t)
	replicaB := newAPIReplica(ctx, t)

	pubConn := newConn(ctx, t)
	pub, err := invalidation.NewPublisher(pubConn)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	defer pub.Close() //nolint:errcheck

	const deviceID = "leaflab-nfr15-reject"
	const version = int64(7)
	const reason = "I2C bus TIMEOUT -- addr=0x44"

	chA := beginWait(ctx, replicaA, deviceID, version, 10*time.Second)
	chB := beginWait(ctx, replicaB, deviceID, version, 10*time.Second)
	time.Sleep(200 * time.Millisecond)

	if err := pub.Publish(ctx, invalidation.Event{
		Kind:            invalidation.KindAck,
		DeviceID:        deviceID,
		Version:         version,
		Accepted:        false,
		RejectionReason: reason,
		ObservedAt:      time.Now(),
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	for name, ch := range map[string]<-chan waitResult{"replica A": chA, "replica B": chB} {
		select {
		case got := <-ch:
			if got.result != ackwait.ResultRejected {
				t.Errorf("%s: result = %v, want %v", name, got.result, ackwait.ResultRejected)
			}
			if got.reason != reason {
				t.Errorf("%s: reason = %q, want verbatim %q", name, got.reason, reason)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: bounded wait never resolved", name)
		}
	}
}

// -- The "teeth" proof: a competing-consumer transport does NOT satisfy ---
// -- the every-replica constraint, against the very same broker ----------

// TestNFR15_CompetingConsumerTransport_DoesNotSatisfyEveryReplicaConstraint
// builds a classic work-queue (competing-consumer) delivery against the
// same RabbitMQ broker the fanout tests above use -- one durable queue,
// two independent consumers both registered against it -- and proves
// RabbitMQ's own round-robin dispatch delivers one published message to
// exactly one consumer, never both. This is the issue's own falsifying
// configuration made real: if leaflab/invalidation's ExchangeName were
// ever misconfigured this way (a shared named queue instead of each
// Subscriber's own exclusive fanout-bound queue), the two tests above
// would go red exactly like this -- one replica's AwaitConfigAck wait
// would time out to STILL_PENDING_AT_DEADLINE despite the ack having
// resolved, in clear violation of NFR15's every-replica constraint.
func TestNFR15_CompetingConsumerTransport_DoesNotSatisfyEveryReplicaConstraint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const queueName = "leaflab-nfr15-competing-consumer-test"

	pubConn := newConn(ctx, t)
	pubCh, err := pubConn.Channel()
	if err != nil {
		t.Fatalf("publisher channel: %v", err)
	}
	defer pubCh.Close() //nolint:errcheck
	if _, err := pubCh.QueueDeclare(queueName, false, true, false, false, nil); err != nil {
		t.Fatalf("declare shared queue: %v", err)
	}

	// Two independent consumers, both registered against the *same* named
	// queue -- the defining shape of a competing-consumer configuration,
	// as opposed to each Subscriber declaring its own exclusive queue
	// bound to a fanout exchange (leaflab/invalidation's actual design).
	consume := func() <-chan amqp.Delivery {
		conn := newConn(ctx, t)
		ch, err := conn.Channel()
		if err != nil {
			t.Fatalf("consumer channel: %v", err)
		}
		t.Cleanup(func() { _ = ch.Close() })
		if _, err := ch.QueueDeclare(queueName, false, true, false, false, nil); err != nil {
			t.Fatalf("consumer redeclare shared queue: %v", err)
		}
		msgs, err := ch.Consume(queueName, "", true, false, false, false, nil)
		if err != nil {
			t.Fatalf("consume: %v", err)
		}
		return msgs
	}

	consumerA := consume()
	consumerB := consume()
	time.Sleep(200 * time.Millisecond)

	if err := pubCh.PublishWithContext(ctx, "", queueName, false, false, amqp.Publishing{
		ContentType: "text/plain",
		Body:        []byte("one ack event, competing-consumer delivery"),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	gotA, gotB := waitOrTimeout(consumerA, 2*time.Second), waitOrTimeout(consumerB, 2*time.Second)

	// The load-bearing assertion: NOT both consumers received the message.
	// A fanout-bound configuration (the two tests above) would have both
	// resolve; this competing-consumer configuration must not -- proving
	// that if leaflab/invalidation's real mechanism ever regressed to this
	// shape, the every-replica property this file otherwise proves would
	// break exactly the way NFR15 forbids.
	if gotA && gotB {
		t.Fatal("both competing consumers received the single published message -- this configuration does not exercise competing-consumer delivery at all, so it cannot serve as NFR15's falsifying variant")
	}
	if !gotA && !gotB {
		t.Fatal("neither competing consumer received the single published message -- delivery did not happen at all, so this is not a valid competing-consumer proof either")
	}
}

// waitOrTimeout reports whether a delivery arrived on ch within timeout.
func waitOrTimeout(ch <-chan amqp.Delivery, timeout time.Duration) bool {
	select {
	case _, ok := <-ch:
		return ok
	case <-time.After(timeout):
		return false
	}
}
