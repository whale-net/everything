// Real-broker proof that RabbitMQAckPublisher (FR47/#1216) and
// RabbitMQInvalidator (FR73/#1203) — the actual production publisher types,
// wired exactly as main.go wires them — publish onto the same fanout
// exchange, not two independently declared ones. Requires a real RabbitMQ
// broker (libs/go/rmqtest); run with:
//
//	bazel test //leaflab/processor:processor_broadcast_integration_test \
//	  --test_tag_filters=-manual --test_output=all

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/whale-net/everything/leaflab/broadcast"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/libs/go/rmqtest"
)

func uniqueExchange(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("processor-broadcast-test.%s.%d", t.Name(), time.Now().UnixNano())
}

// TestProductionPublishersShareOneExchange wires RabbitMQAckPublisher and
// RabbitMQInvalidator exactly as leaflab/processor/main.go does — one
// *rmq.Publisher, one exchange — and asserts a single listener bound only to
// that exchange receives signals from both. If a future change points either
// publisher at a different exchange, this test fails.
func TestProductionPublishersShareOneExchange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn := rmqtest.NewConnection(ctx, t)
	exchange := uniqueExchange(t)
	logger := slog.Default()

	// Mirror main.go: one *rmq.Publisher shared by both signal types.
	sharedPublisher, err := rmq.NewPublisher(conn)
	if err != nil {
		t.Fatalf("rmq.NewPublisher: %v", err)
	}
	defer sharedPublisher.Close() //nolint:errcheck

	invalidator := NewRabbitMQInvalidator(logger, sharedPublisher, exchange)

	broadcastPub, err := broadcast.NewPublisher(conn, sharedPublisher, exchange)
	if err != nil {
		t.Fatalf("broadcast.NewPublisher: %v", err)
	}
	ackPublisher := NewRabbitMQAckPublisher(logger, broadcastPub)

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

	if err := invalidator.PublishInvalidation(ctx, CacheInvalidationSignal{
		DeviceID:   "device-1",
		SensorID:   100,
		ChangeType: "region",
	}); err != nil {
		t.Fatalf("PublishInvalidation: %v", err)
	}

	if err := ackPublisher.PublishAck(ctx, ConfigAckSignal{
		DeviceID:      "device-1",
		ConfigVersion: 3,
		Accepted:      true,
	}); err != nil {
		t.Fatalf("PublishAck: %v", err)
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

	ackBody, ok := receivedByKey[broadcast.RoutingKeyConfigAck]
	if !ok {
		t.Fatalf("did not receive config-ack signal from RabbitMQAckPublisher on the shared exchange")
	}
	var ackSignal ConfigAckSignal
	if err := json.Unmarshal(ackBody, &ackSignal); err != nil {
		t.Fatalf("unmarshal ack signal: %v", err)
	}
	if ackSignal.ConfigVersion != 3 || !ackSignal.Accepted {
		t.Errorf("ack signal = %+v, want ConfigVersion=3 Accepted=true", ackSignal)
	}

	if _, ok := receivedByKey[broadcast.RoutingKeyCacheInvalidation]; !ok {
		t.Fatalf("did not receive cache-invalidation signal from RabbitMQInvalidator on the shared exchange")
	}
}
