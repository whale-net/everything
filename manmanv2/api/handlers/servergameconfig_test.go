package handlers

import (
	"context"
	"testing"

	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MockSGCCreateRepo is a minimal ServerGameConfigRepository fake for
// DeployGameConfig's cordon coverage (#2364): only Create is exercised.
type MockSGCCreateRepo struct {
	repository.ServerGameConfigRepository
	created []*manman.ServerGameConfig
}

func (m *MockSGCCreateRepo) Create(ctx context.Context, sgc *manman.ServerGameConfig) (*manman.ServerGameConfig, error) {
	sgc.SGCID = int64(len(m.created) + 1)
	m.created = append(m.created, sgc)
	return sgc, nil
}

func TestDeployGameConfig_Cordon(t *testing.T) {
	const serverID = int64(1)

	newHandler := func() (*ServerGameConfigHandler, *MockSGCCreateRepo, *MockCordonServerRepo) {
		sgcRepo := &MockSGCCreateRepo{}
		serverRepo := newMockCordonServerRepo(serverID)
		h := NewServerGameConfigHandler(sgcRepo, &MockServerPortRepo{}, serverRepo)
		return h, sgcRepo, serverRepo
	}

	t.Run("schedulable host: deployment created", func(t *testing.T) {
		h, sgcRepo, _ := newHandler()

		resp, err := h.DeployGameConfig(context.Background(), &pb.DeployGameConfigRequest{ServerId: serverID, GameConfigId: 5})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if resp.Config == nil {
			t.Fatal("expected a config in the response")
		}
		if len(sgcRepo.created) != 1 {
			t.Fatalf("expected exactly one ServerGameConfig created, got %d", len(sgcRepo.created))
		}
	})

	for _, state := range []string{manman.ServerDrainStateDraining, manman.ServerDrainStateDrained} {
		t.Run(state+" host: rejected, no deployment created", func(t *testing.T) {
			h, sgcRepo, serverRepo := newHandler()
			serverRepo.servers[serverID].DrainState = state

			resp, err := h.DeployGameConfig(context.Background(), &pb.DeployGameConfigRequest{ServerId: serverID, GameConfigId: 5})
			if err == nil {
				t.Fatalf("expected error, got nil (resp=%+v)", resp)
			}
			st, ok := status.FromError(err)
			if !ok || st.Code() != codes.FailedPrecondition {
				t.Errorf("expected FailedPrecondition, got %v", err)
			}
			if len(sgcRepo.created) != 0 {
				t.Fatalf("expected zero ServerGameConfigs created, got %d", len(sgcRepo.created))
			}
		})
	}

	t.Run("deployment succeeds again after undrain", func(t *testing.T) {
		h, sgcRepo, serverRepo := newHandler()
		serverRepo.servers[serverID].DrainState = manman.ServerDrainStateDraining
		if _, err := h.DeployGameConfig(context.Background(), &pb.DeployGameConfigRequest{ServerId: serverID, GameConfigId: 5}); err == nil {
			t.Fatal("expected draining host to reject the deploy")
		}

		serverRepo.servers[serverID].DrainState = manman.ServerDrainStateSchedulable
		if _, err := h.DeployGameConfig(context.Background(), &pb.DeployGameConfigRequest{ServerId: serverID, GameConfigId: 5}); err != nil {
			t.Fatalf("expected no error after undrain, got %v", err)
		}
		if len(sgcRepo.created) != 1 {
			t.Fatalf("expected exactly one ServerGameConfig created, got %d", len(sgcRepo.created))
		}
	})
}
