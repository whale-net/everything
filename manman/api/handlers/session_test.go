package handlers

import (
	"context"
	"testing"

	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/api/repository"
	pb "github.com/whale-net/everything/manman/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MockSessionRepo for handler tests
type MockSessionRepo struct {
	repository.SessionRepository
	sessions []*manman.Session
	created  []*manman.Session
}

func (m *MockSessionRepo) ListWithFilters(ctx context.Context, filters *repository.SessionFilters, limit, offset int) ([]*manman.Session, error) {
	var result []*manman.Session
	for _, s := range m.sessions {
		if filters.SGCID != nil && s.SGCID != *filters.SGCID {
			continue
		}
		if len(filters.StatusFilter) > 0 {
			match := false
			for _, st := range filters.StatusFilter {
				if s.Status == st {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		result = append(result, s)
	}
	return result, nil
}

func (m *MockSessionRepo) Create(ctx context.Context, s *manman.Session) (*manman.Session, error) {
	s.SessionID = int64(len(m.sessions) + 1)
	m.sessions = append(m.sessions, s)
	m.created = append(m.created, s)
	return s, nil
}

// MockSGCRepo
type MockSGCRepo struct {
	repository.ServerGameConfigRepository
}

func (m *MockSGCRepo) Get(ctx context.Context, id int64) (*manman.ServerGameConfig, error) {
	return &manman.ServerGameConfig{SGCID: id, ServerID: 1, GameConfigID: 1}, nil
}

// MockGCRepo
type MockGCRepo struct {
	repository.GameConfigRepository
}

func (m *MockGCRepo) Get(ctx context.Context, id int64) (*manman.GameConfig, error) {
	return &manman.GameConfig{ConfigID: id}, nil
}

func TestStartSessionLifecycle(t *testing.T) {
	sessionRepo := &MockSessionRepo{}
	sgcRepo := &MockSGCRepo{}
	gcRepo := &MockGCRepo{}

	h := &SessionHandler{
		sessionRepo: sessionRepo,
		sgcRepo:     sgcRepo,
		gcRepo:      gcRepo,
		publisher:   nil, // Publisher is optional in StartSession
	}

	sgcID := int64(100)

	t.Run("Happy path: no active sessions", func(t *testing.T) {
		req := &pb.StartSessionRequest{ServerGameConfigId: sgcID}
		resp, err := h.StartSession(context.Background(), req)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if resp.Session.Status != manman.SessionStatusPending {
			t.Errorf("Expected status pending, got %s", resp.Session.Status)
		}
	})

	t.Run("Sad path: active session exists, force=false", func(t *testing.T) {
		// Existing running session
		sessionRepo.sessions = []*manman.Session{
			{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusRunning},
		}

		req := &pb.StartSessionRequest{ServerGameConfigId: sgcID, Force: false}
		_, err := h.StartSession(context.Background(), req)
		if err == nil {
			t.Fatal("Expected error, got nil")
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.FailedPrecondition {
			t.Errorf("Expected FailedPrecondition error, got %v", err)
		}
	})

	t.Run("Happy path: active session exists, force=true", func(t *testing.T) {
		// Existing running session
		sessionRepo.sessions = []*manman.Session{
			{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusRunning},
		}
		sessionRepo.created = nil

		req := &pb.StartSessionRequest{ServerGameConfigId: sgcID, Force: true}
		resp, err := h.StartSession(context.Background(), req)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if resp.Session.Status != manman.SessionStatusPending {
			t.Errorf("Expected status pending, got %s", resp.Session.Status)
		}
		if len(sessionRepo.created) != 1 {
			t.Fatal("Expected new session to be created")
		}
	})

	t.Run("Sad path: crashed session is still active, force=false", func(t *testing.T) {
		// Existing crashed session
		sessionRepo.sessions = []*manman.Session{
			{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusCrashed},
		}

		req := &pb.StartSessionRequest{ServerGameConfigId: sgcID, Force: false}
		_, err := h.StartSession(context.Background(), req)
		if err == nil {
			t.Fatal("Expected error, got nil")
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.FailedPrecondition {
			t.Errorf("Expected FailedPrecondition error, got %v", err)
		}
	})

	t.Run("Happy path: crashed session exists, force=true", func(t *testing.T) {
		// Existing crashed session
		sessionRepo.sessions = []*manman.Session{
			{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusCrashed},
		}
		sessionRepo.created = nil

		req := &pb.StartSessionRequest{ServerGameConfigId: sgcID, Force: true}
		resp, err := h.StartSession(context.Background(), req)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if resp.Session.Status != manman.SessionStatusPending {
			t.Errorf("Expected status pending, got %s", resp.Session.Status)
		}
	})
}
