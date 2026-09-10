package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MockCordonServerRepo is a minimal ServerRepository fake shared by every
// handler test exercising the drain cordon guard (#2364): StartSession,
// RestartDeployment, DeployGameConfig. Only Get is implemented -- that's
// all assertHostSchedulable calls. Every seeded server defaults to
// "schedulable" so callers that don't care about drain state get the
// regression-safe default (AC6) without extra setup; a test that wants a
// draining/drained host mutates DrainState on the returned server directly.
type MockCordonServerRepo struct {
	repository.ServerRepository
	servers map[int64]*manman.Server
	getErr  error
}

func newMockCordonServerRepo(serverIDs ...int64) *MockCordonServerRepo {
	m := &MockCordonServerRepo{servers: make(map[int64]*manman.Server)}
	for _, id := range serverIDs {
		m.servers[id] = &manman.Server{
			ServerID:   id,
			Name:       fmt.Sprintf("srv-%d", id),
			DrainState: manman.ServerDrainStateSchedulable,
		}
	}
	return m
}

func (m *MockCordonServerRepo) Get(ctx context.Context, serverID int64) (*manman.Server, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	s, ok := m.servers[serverID]
	if !ok {
		return nil, fmt.Errorf("server %d not found", serverID)
	}
	return s, nil
}

func TestAssertHostSchedulable(t *testing.T) {
	t.Run("schedulable host is a no-op", func(t *testing.T) {
		repo := newMockCordonServerRepo(1)
		if err := assertHostSchedulable(context.Background(), repo, 1); err != nil {
			t.Fatalf("expected nil error for schedulable host, got %v", err)
		}
	})

	for _, state := range []string{manman.ServerDrainStateDraining, manman.ServerDrainStateDrained} {
		t.Run(state+" host is rejected", func(t *testing.T) {
			repo := newMockCordonServerRepo(1)
			repo.servers[1].DrainState = state
			repo.servers[1].Name = "gameserver-1"

			err := assertHostSchedulable(context.Background(), repo, 1)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			st, ok := status.FromError(err)
			if !ok || st.Code() != codes.FailedPrecondition {
				t.Fatalf("expected FailedPrecondition, got %v", err)
			}
			if !strings.Contains(err.Error(), "gameserver-1") {
				t.Errorf("expected error to name the host, got %q", err.Error())
			}

			// Terminal-vs-retryable distinction (#2364): SessionRestartConsumer
			// must be able to identify this as a cordon rejection specifically,
			// not just any FailedPrecondition.
			var cordonErr *cordonError
			if !errors.As(err, &cordonErr) {
				t.Errorf("expected err to be a *cordonError, got %T", err)
			}
		})
	}

	t.Run("failure to read drain state is Internal, not FailedPrecondition", func(t *testing.T) {
		repo := newMockCordonServerRepo(1)
		repo.getErr = errors.New("db unavailable")

		err := assertHostSchedulable(context.Background(), repo, 1)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.Internal {
			t.Fatalf("expected Internal, got %v", err)
		}
		var cordonErr *cordonError
		if errors.As(err, &cordonErr) {
			t.Error("expected a read failure not to be a *cordonError")
		}
	})
}
