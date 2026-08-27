// Real-broker proof of NFR15's "every API replica" constraint: two genuinely
// independent AckListener instances — each its own RabbitMQ connection, own
// ephemeral queue, own ConfigAckWaiter, own gRPC server — must both resolve a
// bounded wait from a single ack signal published once, over the real
// leaflab/broadcast fanout exchange. A competing-consumer work queue (e.g. two
// consumers sharing one named queue) would deliver the message to only one of
// them and this test would fail. Requires a real RabbitMQ broker
// (libs/go/rmqtest); run with:
//
//	bazel test //leaflab/api:api_test --test_tag_filters=-manual --test_output=all

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/broadcast"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/libs/go/rmqtest"
)

func TestWaitForConfigAck_MultiReplicaFanout_RealBroadcast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	setup := newTestSetup(t, ctx)
	defer setup.cleanup()

	boardID := int64(100)
	version := int64(1)
	deviceID := "multi-replica-device"
	setup.createBoard(ctx, boardID, deviceID)
	setup.insertDeviceConfig(ctx, boardID, version)

	// Two independent replicas: separate RabbitMQ connections, separate
	// ephemeral queues, separate ConfigAckWaiter instances, separate gRPC
	// servers. Nothing here is shared between "replica A" and "replica B"
	// except the one real broadcast exchange they both subscribe to.
	connA := rmqtest.NewConnection(ctx, t)
	connB := rmqtest.NewConnection(ctx, t)

	waiterA := NewConfigAckWaiter()
	waiterB := NewConfigAckWaiter()

	repo := NewRepository(setup.db.Pool)

	listenerA, err := NewAckListener(logging.Get("test-replica-a"), connA, repo, waiterA)
	if err != nil {
		t.Fatalf("NewAckListener (replica A): %v", err)
	}
	defer listenerA.Close() //nolint:errcheck

	listenerB, err := NewAckListener(logging.Get("test-replica-b"), connB, repo, waiterB)
	if err != nil {
		t.Fatalf("NewAckListener (replica B): %v", err)
	}
	defer listenerB.Close() //nolint:errcheck

	listenCtx, listenCancel := context.WithCancel(ctx)
	defer listenCancel()
	if err := listenerA.Start(listenCtx); err != nil {
		t.Fatalf("listenerA.Start: %v", err)
	}
	if err := listenerB.Start(listenCtx); err != nil {
		t.Fatalf("listenerB.Start: %v", err)
	}

	// Let both listeners finish declaring/binding their ephemeral queues
	// before anything is published.
	time.Sleep(300 * time.Millisecond)

	replicaA := setup.newAPIServer(ctx, "replica-a", waiterA)
	replicaB := setup.newAPIServer(ctx, "replica-b", waiterB)

	clientA := pb.NewLeafLabAPIClient(replicaA.conn)
	clientB := pb.NewLeafLabAPIClient(replicaB.conn)

	deadline := time.Now().Add(10 * time.Second)
	makeReq := func() *pb.WaitForConfigAckRequest {
		return &pb.WaitForConfigAckRequest{
			BoardId:         boardID,
			Version:         uint64(version),
			DeadlineSeconds: deadline.Unix(),
		}
	}

	type waitResult struct {
		resp *pb.WaitForConfigAckResponse
		err  error
	}
	resultA := make(chan waitResult, 1)
	resultB := make(chan waitResult, 1)

	// Issue the bounded wait against BOTH replicas concurrently — this is the
	// falsifiable claim: "a bounded wait issued against any replica resolves
	// within the freshness bound regardless of which replica received the
	// request."
	go func() {
		resp, err := clientA.WaitForConfigAck(ctx, makeReq())
		resultA <- waitResult{resp, err}
	}()
	go func() {
		resp, err := clientB.WaitForConfigAck(ctx, makeReq())
		resultB <- waitResult{resp, err}
	}()

	// Give both waits time to register before the ack is published.
	time.Sleep(200 * time.Millisecond)

	// Simulate the processor: publish exactly ONE ack signal, over the real
	// broadcast exchange, using the real wire schema (broadcast.ConfigAckSignal
	// / broadcast.RoutingKeyConfigAck). Neither replica's ConfigAckWaiter is
	// touched directly — delivery must happen over AMQP.
	pubConn := rmqtest.NewConnection(ctx, t)
	rawPub, err := rmq.NewPublisher(pubConn)
	if err != nil {
		t.Fatalf("rmq.NewPublisher: %v", err)
	}
	defer rawPub.Close() //nolint:errcheck

	broadcastPub, err := broadcast.NewPublisher(pubConn, rawPub, broadcast.Exchange)
	if err != nil {
		t.Fatalf("broadcast.NewPublisher: %v", err)
	}

	signal, err := json.Marshal(broadcast.ConfigAckSignal{
		AckedAt:       time.Now(),
		DeviceID:      deviceID,
		ConfigVersion: version,
		Accepted:      true,
	})
	if err != nil {
		t.Fatalf("marshal ack signal: %v", err)
	}
	if err := broadcastPub.Publish(ctx, broadcast.RoutingKeyConfigAck, signal); err != nil {
		t.Fatalf("Publish ack signal: %v", err)
	}

	// Both replicas must resolve within the 2s freshness bound (NFR15). Under
	// a competing-consumer work queue, only one of connA/connB's queues would
	// receive the single published message and the other would time out here.
	const freshnessBound = 2 * time.Second

	select {
	case r := <-resultA:
		if r.err != nil {
			t.Fatalf("WaitForConfigAck on replica A: unexpected error: %v", r.err)
		}
		if r.resp.Resolution != pb.ConfigAckResolution_CONFIG_ACK_RESOLUTION_ACCEPTED {
			t.Errorf("replica A resolution = %v, want ACCEPTED", r.resp.Resolution)
		}
	case <-time.After(freshnessBound):
		t.Fatalf("replica A did not resolve within the %v freshness bound — the ack signal did not reach it", freshnessBound)
	}

	select {
	case r := <-resultB:
		if r.err != nil {
			t.Fatalf("WaitForConfigAck on replica B: unexpected error: %v", r.err)
		}
		if r.resp.Resolution != pb.ConfigAckResolution_CONFIG_ACK_RESOLUTION_ACCEPTED {
			t.Errorf("replica B resolution = %v, want ACCEPTED", r.resp.Resolution)
		}
	case <-time.After(freshnessBound):
		t.Fatalf("replica B did not resolve within the %v freshness bound — the ack signal did not reach it (this is exactly the competing-consumer-queue failure mode NFR15 prohibits)", freshnessBound)
	}
}
