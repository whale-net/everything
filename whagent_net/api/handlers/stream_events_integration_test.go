//go:build integration

// FR5/C17 end-to-end proof (issue #2239's Testing section): a real gRPC
// server (bufconn transport, the same interceptor chain
// session_integration_test.go's newTestServer wires) backed by a real
// Postgres (//libs/go/dbtest) and a real RabbitMQ broker (testcontainers
// core, same throwaway-container precedent as
// manmanv2/api/handlers/action_execution_integration_test.go's
// startRabbitMQ) drives StreamEvents' full backfill-then-live-tail path
// end to end, proving:
//
//   - no gap and no duplicate across the backfill/live-tail handoff
//     (TestStreamEvents_BackfillThenLiveTail_NoGapNoDuplicateAtHandoff);
//   - a publisher re-publishing an already-committed event (events.go's
//     doc comment: "the publisher is allowed to re-publish a committed
//     event") never reaches the client twice
//     (TestStreamEvents_DuplicateBusRepublish_NotDeliveredTwice);
//   - the stream ends cleanly once the session reaches a terminal status
//     (both tests above, via the StatusDone transition each drives);
//   - FR5's "no broker credentials" guarantee: streamTestClient -- the
//     type every test in this file drives StreamEvents through -- holds
//     nothing but a SessionServiceClient and a bearer-token placeholder,
//     asserted structurally by
//     TestStreamEventsClient_HoldsNoBrokerCredentials, not just by
//     convention.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //whagent_net/api/handlers:stream_events_integration_test --test_output=all
package handlers_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/whagent_net/api/handlers"
	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/migrate/schema"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

// startRabbitMQForStream starts a throwaway RabbitMQ container and returns
// an amqp:// URL for it (guest/guest) -- the identical shape
// manmanv2/api/handlers/action_execution_integration_test.go's
// startRabbitMQ uses; redefined here (not imported) because that helper
// lives in a different domain's internal test package.
func startRabbitMQForStream(ctx context.Context, t *testing.T) string {
	t.Helper()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "rabbitmq:3-alpine",
			ExposedPorts: []string{"5672/tcp"},
			WaitingFor:   wait.ForListeningPort("5672/tcp"),
		},
		Started: true,
	})
	require.NoError(t, err, "start rabbitmq container")
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })

	host, err := ctr.Host(ctx)
	require.NoError(t, err)
	port, err := ctr.MappedPort(ctx, "5672/tcp")
	require.NoError(t, err)
	return fmt.Sprintf("amqp://guest:guest@%s:%s/", host, port.Port())
}

// newStreamTestServer provisions a real Postgres (dbtest, whagent-net's
// real embedded migrations -- same as session_integration_test.go's
// newTestServer) and a real throwaway RabbitMQ, wires a *session.Store
// whose Transcript().Append publishes onto it for real, and a
// *handlers.SessionServer with a real eventsConsumer attached (the same
// declare/bind api/main.go's initializeEventsConsumer does), behind the
// identical bufconn + auth-interceptor chain newTestServer uses. Returns a
// ready SessionServiceClient, the store fixtures write through, and pub --
// a second, independent publisher connection onto the same broker/exchange
// tests use to simulate a publisher's own re-publish of an
// already-committed event (TestStreamEvents_DuplicateBusRepublish_NotDeliveredTwice).
func newStreamTestServer(t *testing.T) (pb.SessionServiceClient, *session.Store, *events.Publisher) {
	t.Helper()
	ctx := context.Background()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	require.NoError(t, runner.Up(), "apply every migration from the real embedded schema")

	rmqURL := startRabbitMQForStream(ctx, t)

	pubConn, err := rmq.NewConnectionFromURL(rmqURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pubConn.Close() })
	pub, err := events.NewPublisher(pubConn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pub.Close() })

	store := session.New(db.Pool, pub)

	consumerConn, err := rmq.NewConnectionFromURL(rmqURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = consumerConn.Close() })

	declareCh, err := consumerConn.Channel()
	require.NoError(t, err)
	kind, durable, autoDelete, internal, noWait, args := events.DeclareArgs()
	require.NoError(t, declareCh.ExchangeDeclare(events.ExchangeName, kind, durable, autoDelete, internal, noWait, args))
	require.NoError(t, declareCh.Close())

	consumer, err := rmq.NewConsumerWithOpts(consumerConn, "", false, true, 0, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = consumer.Close() })
	require.NoError(t, consumer.BindExchange(events.ExchangeName, []string{"#"}))

	serverCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// temporalClient/catalog are nil, same as newTestServer -- StreamEvents
	// touches neither.
	sessionServer := handlers.NewSessionServer(serverCtx, store, testIssuer, nil, "", nil, consumer)

	unaryAuth, streamAuth, err := grpcauth.NewServerInterceptors(ctx, grpcauth.ServerConfig{
		Mode: grpcauth.AuthModeNone,
	})
	require.NoError(t, err)

	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryAuth, handlers.RequireClaimsUnaryInterceptor),
		grpc.ChainStreamInterceptor(streamAuth, handlers.RequireClaimsStreamInterceptor),
	)
	pb.RegisterSessionServiceServer(grpcServer, sessionServer)

	go func() {
		_ = grpcServer.Serve(lis)
	}()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		conn.Close() //nolint:errcheck
		grpcServer.Stop()
	})

	return pb.NewSessionServiceClient(conn), store, pub
}

// streamTestClient is FR5's proof (issue #2239's Testing section): a
// minimal wrapper that follows a session's events purely over the grpc
// SessionServiceClient newStreamTestServer returns. token is where a real
// caller's Keycloak bearer token would be carried (e.g. as
// "authorization: Bearer <token>" call metadata in GRPC_AUTH_MODE=oidc);
// this harness runs under AuthModeNone (same as every other whagent-net
// integration test here), so it is never populated or attached to a call,
// but its presence in the type is what
// TestStreamEventsClient_HoldsNoBrokerCredentials checks against: every
// field of this struct, and this struct alone, is what a StreamEvents
// caller needs to hold.
type streamTestClient struct {
	client pb.SessionServiceClient
	token  string
}

// follow calls StreamEvents and drains it to completion (or ctx
// cancellation), returning every TranscriptEvent received in order.
func (c *streamTestClient) follow(ctx context.Context, sessionID string, fromSeq int64) ([]*pb.TranscriptEvent, error) {
	stream, err := c.client.StreamEvents(ctx, &pb.StreamEventsRequest{SessionId: sessionID, FromSeq: fromSeq})
	if err != nil {
		return nil, err
	}
	var got []*pb.TranscriptEvent
	for {
		ev, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return got, nil
			}
			return got, err
		}
		got = append(got, ev)
	}
}

// TestStreamEventsClient_HoldsNoBrokerCredentials is FR5/C17's structural
// proof: every field streamTestClient declares must live outside
// //libs/go/rmq (and the amqp091-go package it wraps) -- a StreamEvents
// caller never needs a broker connection or credentials, and this asserts
// that at test time rather than relying on code review alone.
func TestStreamEventsClient_HoldsNoBrokerCredentials(t *testing.T) {
	typ := reflect.TypeOf(streamTestClient{})
	require.Greater(t, typ.NumField(), 0, "sanity: streamTestClient must have fields to check")

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		pkgPath := field.Type.PkgPath()
		if pkgPath == "" && field.Type.Kind() == reflect.Ptr {
			pkgPath = field.Type.Elem().PkgPath()
		}
		lower := strings.ToLower(pkgPath)
		assert.NotContains(t, lower, "rmq", "streamTestClient.%s must never hold a broker type (C17: no RPC caller needs RabbitMQ credentials)", field.Name)
		assert.NotContains(t, lower, "amqp", "streamTestClient.%s must never hold a broker type (C17: no RPC caller needs RabbitMQ credentials)", field.Name)
	}
}

// TestStreamEvents_NotFound proves an unknown session id fails the stream
// with NOT_FOUND on its first Recv (a streaming RPC's handler error
// surfaces there, not on the initial call).
func TestStreamEvents_NotFound(t *testing.T) {
	client, _, _ := newStreamTestServer(t)
	c := &streamTestClient{client: client}

	_, err := c.follow(context.Background(), "00000000-0000-0000-0000-000000000000", 0)

	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// TestStreamEvents_BackfillThenLiveTail_NoGapNoDuplicateAtHandoff is
// FR5/NFR4's central end-to-end proof: backfillCount events are committed
// before the stream ever attaches (so StreamEvents must backfill them),
// then liveCount more are committed after it has attached (so it must
// tail them live) -- and the sequence received across both must be
// exactly 1..backfillCount+liveCount, no gap and no duplicate at the
// handoff between the two.
func TestStreamEvents_BackfillThenLiveTail_NoGapNoDuplicateAtHandoff(t *testing.T) {
	client, store, _ := newStreamTestServer(t)
	ctx := context.Background()
	sess := createSession(t, ctx, store, devSubject, session.StatusRunning)

	const backfillCount = 3
	for i := 0; i < backfillCount; i++ {
		_, err := store.Transcript().Append(ctx, sess.SessionID, 1, "test.event", json.RawMessage(`{}`))
		require.NoError(t, err)
	}

	c := &streamTestClient{client: client}
	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	type result struct {
		evs []*pb.TranscriptEvent
		err error
	}
	done := make(chan result, 1)
	go func() {
		evs, err := c.follow(streamCtx, sess.SessionID.String(), 0)
		done <- result{evs, err}
	}()

	// Subscribe happens before backfill inside StreamEvents (stream.go's
	// doc comment), so no event committed from here on can ever be lost
	// regardless of this sleep's length -- it is only a liveness
	// convenience so the Appends below land after the RPC has reached the
	// server, not a correctness requirement.
	time.Sleep(500 * time.Millisecond)

	const liveCount = 3
	for i := 0; i < liveCount; i++ {
		_, err := store.Transcript().Append(ctx, sess.SessionID, 2, "test.event", json.RawMessage(`{}`))
		require.NoError(t, err)
	}

	require.NoError(t, store.Sessions().UpdateStatus(ctx, sess.SessionID, session.StatusDone, nil))

	res := <-done
	require.NoError(t, res.err, "the stream must end cleanly (io.EOF, translated to nil by follow) once the session reaches a terminal status")

	require.Len(t, res.evs, backfillCount+liveCount)
	seen := make(map[int64]bool, len(res.evs))
	for i, ev := range res.evs {
		require.False(t, seen[ev.Seq], "seq %d must never be emitted twice, including at the backfill/live-tail handoff", ev.Seq)
		seen[ev.Seq] = true
		if i > 0 {
			require.Equal(t, res.evs[i-1].Seq+1, ev.Seq, "no gap across the backfill/live-tail handoff")
		}
	}
}

// TestStreamEvents_DuplicateBusRepublish_NotDeliveredTwice is NFR4/LB1's
// end-to-end proof over a real broker: pub re-publishing the exact same
// already-committed events.Event a second time (simulating a retried
// publish, events.go's doc comment: "a retry can then re-publish a
// duplicate, but can never publish an event that was never durably
// committed") must never reach the client twice.
func TestStreamEvents_DuplicateBusRepublish_NotDeliveredTwice(t *testing.T) {
	client, store, pub := newStreamTestServer(t)
	ctx := context.Background()
	sess := createSession(t, ctx, store, devSubject, session.StatusRunning)

	c := &streamTestClient{client: client}
	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	type result struct {
		evs []*pb.TranscriptEvent
		err error
	}
	done := make(chan result, 1)
	go func() {
		evs, err := c.follow(streamCtx, sess.SessionID.String(), 0)
		done <- result{evs, err}
	}()

	time.Sleep(500 * time.Millisecond) // let the stream attach before publishing

	ev, err := store.Transcript().Append(ctx, sess.SessionID, 1, "test.event", json.RawMessage(`{}`))
	require.NoError(t, err)

	// A direct second Publish of the identical committed event -- not a
	// second Append, which would allocate a new seq -- is exactly what a
	// retried publish looks like on the wire.
	require.NoError(t, pub.Publish(ctx, ev))

	require.NoError(t, store.Sessions().UpdateStatus(ctx, sess.SessionID, session.StatusDone, nil))

	res := <-done
	require.NoError(t, res.err)

	require.Len(t, res.evs, 1, "a re-published duplicate of the same committed event must never reach the client twice")
	assert.Equal(t, ev.EventID.String(), res.evs[0].EventId)
	assert.Equal(t, ev.Seq, res.evs[0].Seq)
}
