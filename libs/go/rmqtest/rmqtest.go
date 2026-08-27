// Package rmqtest starts a real, throwaway RabbitMQ broker (via
// testcontainers-go) for tests that need to exercise actual AMQP fanout
// semantics — broadcast delivery to independent queues — which an in-process
// fake cannot verify: a fake can only prove the code calls Publish/Consume, not
// that the broker actually delivers one published message to every bound
// queue rather than to one arbitrary competing consumer.
//
// A single RabbitMQ container is shared across every test in a test binary
// (process), started lazily the first time NewConnection is called. Tests
// that declare their own exchanges/queues should use unique, per-test names
// (or let the broker assign one, as leaflab/broadcast.NewListener does) so
// concurrent tests sharing the one broker do not collide.
//
// This package requires a working Docker daemon and network access to pull
// the rabbitmq image. It is meant to be called only from tests whose Bazel
// target is tagged manual/integration/no-sandbox, mirroring libs/go/dbtest —
// see that package's README for the full rationale and invocation.
package rmqtest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/whale-net/everything/libs/go/rmq"
)

// DefaultImage is used when no image override is requested.
const DefaultImage = "rabbitmq:3-alpine"

// sharedContainer is one RabbitMQ container reused across every test in the
// process that requests a given image.
type sharedContainer struct {
	once      sync.Once
	container testcontainers.Container
	amqpURL   string
	err       error
}

var (
	containersMu sync.Mutex
	containers   = map[string]*sharedContainer{}
)

// getSharedContainer returns the (lazily started) container for image,
// starting it at most once per process regardless of how many tests ask for
// it concurrently.
func getSharedContainer(ctx context.Context, image string) *sharedContainer {
	containersMu.Lock()
	sc, ok := containers[image]
	if !ok {
		sc = &sharedContainer{}
		containers[image] = sc
	}
	containersMu.Unlock()

	sc.once.Do(func() {
		req := testcontainers.ContainerRequest{
			Image:        image,
			ExposedPorts: []string{"5672/tcp"},
			// "Server startup complete" is the last line RabbitMQ logs during
			// boot; waiting only for the port to be open races with the broker
			// still initializing (accepts TCP before AMQP handshakes succeed).
			WaitingFor: wait.ForLog("Server startup complete").WithStartupTimeout(60 * time.Second),
		}
		ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			if ctr != nil {
				_ = testcontainers.TerminateContainer(ctr)
			}
			sc.err = fmt.Errorf("rmqtest: start rabbitmq container: %w", err)
			return
		}

		host, err := ctr.Host(ctx)
		if err != nil {
			_ = testcontainers.TerminateContainer(ctr)
			sc.err = fmt.Errorf("rmqtest: get host: %w", err)
			return
		}
		port, err := ctr.MappedPort(ctx, "5672/tcp")
		if err != nil {
			_ = testcontainers.TerminateContainer(ctr)
			sc.err = fmt.Errorf("rmqtest: get mapped port: %w", err)
			return
		}

		sc.container = ctr
		// rabbitmq's default image ships a guest/guest user restricted to
		// localhost by default config, but that restriction only applies to
		// the management plugin, not AMQP — guest/guest works fine here.
		sc.amqpURL = fmt.Sprintf("amqp://guest:guest@%s:%s/", host, port.Port())
	})

	return sc
}

// NewConnection returns a *rmq.Connection to a RabbitMQ broker shared across
// the test binary, started at most once per process (per image). On any
// failure it fails the test immediately (via t.Fatalf). t.Cleanup closes the
// returned connection; the shared container itself is left running for the
// process's lifetime and cleaned up by testcontainers' Ryuk reaper afterwards.
func NewConnection(ctx context.Context, t testing.TB) *rmq.Connection {
	t.Helper()

	sc := getSharedContainer(ctx, DefaultImage)
	if sc.err != nil {
		t.Fatalf("rmqtest: %v", sc.err)
		return nil // unreachable, satisfies compiler
	}

	conn, err := rmq.NewConnectionFromURL(sc.amqpURL)
	if err != nil {
		t.Fatalf("rmqtest: connect: %v", err)
		return nil
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}
