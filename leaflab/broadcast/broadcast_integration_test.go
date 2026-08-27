// Real-broker proof that leaflab/broadcast is a fanout broadcast, not a
// competing-consumer work queue: two independent Listener instances — never
// a shared instance, never a direct in-process callback — must both receive
// every message a Publisher sends. Requires a real RabbitMQ broker
// (libs/go/rmqtest); run with:
//
//	bazel test //leaflab/broadcast:broadcast_integration_test \
//	  --test_tag_filters=-manual --test_output=all

package broadcast_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/whale-net/everything/leaflab/broadcast"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/libs/go/rmqtest"
)

// uniqueExchange returns a per-test exchange name so concurrent tests sharing
// one broker container don't collide.
func uniqueExchange(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("broadcast-test.%s.%d", t.Name(), time.Now().UnixNano())
}

// collectingListener starts a broadcast.Listener on its own independent
// consumer/queue and records every message it receives for routingKey.
type collectingListener struct {
	consumer *rmq.Consumer

	mu       sync.Mutex
	received [][]byte
}

func newCollectingListener(ctx context.Context, t *testing.T, conn *rmq.Connection, exchange, routingKey string) *collectingListener {
	t.Helper()

	consumer, err := broadcast.NewListener(conn, exchange)
	if err != nil {
		t.Fatalf("broadcast.NewListener: %v", err)
	}
	t.Cleanup(func() { _ = consumer.Close() })

	l := &collectingListener{consumer: consumer}
	consumer.RegisterHandler(routingKey, func(_ context.Context, msg rmq.Message) error {
		l.mu.Lock()
		l.received = append(l.received, msg.Body)
		l.mu.Unlock()
		return nil
	})

	if err := consumer.Start(ctx); err != nil {
		t.Fatalf("consumer.Start: %v", err)
	}
	return l
}

func (l *collectingListener) waitFor(t *testing.T, n int, timeout time.Duration) [][]byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		if len(l.received) >= n {
			result := append([][]byte(nil), l.received...)
			l.mu.Unlock()
			return result
		}
		l.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	t.Fatalf("timed out after %v waiting for %d message(s), got %d", timeout, n, len(l.received))
	return nil
}

// TestBroadcast_TwoIndependentListenersBothReceiveEveryMessage is the
// falsifiable N-replica proof NFR15 requires: with two genuinely independent
// subscriber instances (distinct queues, distinct consumer goroutines — never
// a shared Go object and never a direct in-process callback), one published
// message must reach BOTH. A competing-consumer work queue (a single shared
// durable queue, or binding both consumers to the same named queue) would
// deliver the message to only one of them and this test would fail.
func TestBroadcast_TwoIndependentListenersBothReceiveEveryMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn := rmqtest.NewConnection(ctx, t)
	exchange := uniqueExchange(t)
	const routingKey = "test.signal"

	// Two independent listeners — simulating two API replicas — each with
	// their own private queue and consumer goroutine.
	replicaA := newCollectingListener(ctx, t, conn, exchange, routingKey)
	replicaB := newCollectingListener(ctx, t, conn, exchange, routingKey)

	// Give both consumers time to finish declaring/binding their queues
	// before anything is published.
	time.Sleep(200 * time.Millisecond)

	pub, err := rmq.NewPublisher(conn)
	if err != nil {
		t.Fatalf("rmq.NewPublisher: %v", err)
	}
	defer pub.Close() //nolint:errcheck

	broadcastPub, err := broadcast.NewPublisher(conn, pub, exchange)
	if err != nil {
		t.Fatalf("broadcast.NewPublisher: %v", err)
	}

	payload := []byte(`{"hello":"world"}`)
	if err := broadcastPub.Publish(ctx, routingKey, payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	gotA := replicaA.waitFor(t, 1, 10*time.Second)
	gotB := replicaB.waitFor(t, 1, 10*time.Second)

	if string(gotA[0]) != string(payload) {
		t.Errorf("replica A payload = %s, want %s", gotA[0], payload)
	}
	if string(gotB[0]) != string(payload) {
		t.Errorf("replica B payload = %s, want %s", gotB[0], payload)
	}
}

// TestBroadcast_OneExchangeCarriesBothSignalTypes is the real-broker proof
// that config-ack signalling (FR47/#1216) reuses the exact exchange
// leaflab/processor/cache.go's cache-invalidation signalling (FR73/#1203)
// uses, rather than introducing a second mechanism: publishing both a
// CacheInvalidationSignal-shaped payload and a ConfigAckSignal payload — with
// their real, distinct production routing keys — to the SAME exchange is
// enough for ONE listener bound only to that exchange to receive both,
// distinguished purely by routing key.
func TestBroadcast_OneExchangeCarriesBothSignalTypes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn := rmqtest.NewConnection(ctx, t)
	exchange := uniqueExchange(t)

	var mu sync.Mutex
	receivedByKey := map[string][]byte{}

	consumer, err := broadcast.NewListener(conn, exchange)
	if err != nil {
		t.Fatalf("broadcast.NewListener: %v", err)
	}
	defer consumer.Close() //nolint:errcheck

	record := func(_ context.Context, msg rmq.Message) error {
		mu.Lock()
		receivedByKey[msg.RoutingKey] = msg.Body
		mu.Unlock()
		return nil
	}
	consumer.RegisterHandler(broadcast.RoutingKeyCacheInvalidation, record)
	consumer.RegisterHandler(broadcast.RoutingKeyConfigAck, record)

	if err := consumer.Start(ctx); err != nil {
		t.Fatalf("consumer.Start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	pub, err := rmq.NewPublisher(conn)
	if err != nil {
		t.Fatalf("rmq.NewPublisher: %v", err)
	}
	defer pub.Close() //nolint:errcheck

	broadcastPub, err := broadcast.NewPublisher(conn, pub, exchange)
	if err != nil {
		t.Fatalf("broadcast.NewPublisher: %v", err)
	}

	ackSignal, _ := json.Marshal(broadcast.ConfigAckSignal{
		DeviceID:      "device-1",
		ConfigVersion: 5,
		Accepted:      true,
	})
	if err := broadcastPub.Publish(ctx, broadcast.RoutingKeyConfigAck, ackSignal); err != nil {
		t.Fatalf("Publish config-ack: %v", err)
	}

	invalidationSignal, _ := json.Marshal(map[string]any{
		"device_id":   "device-1",
		"sensor_id":   100,
		"change_type": "region",
	})
	if err := broadcastPub.Publish(ctx, broadcast.RoutingKeyCacheInvalidation, invalidationSignal); err != nil {
		t.Fatalf("Publish cache-invalidation: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(receivedByKey)
		mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if _, ok := receivedByKey[broadcast.RoutingKeyConfigAck]; !ok {
		t.Errorf("did not receive config-ack signal on the shared exchange")
	}
	if _, ok := receivedByKey[broadcast.RoutingKeyCacheInvalidation]; !ok {
		t.Errorf("did not receive cache-invalidation signal on the shared exchange")
	}
}
