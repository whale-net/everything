package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func addServer(repo *mockServerRepository, s *manman.Server) {
	repo.servers[s.Name] = s
	if s.ServerID >= repo.nextID {
		repo.nextID = s.ServerID + 1
	}
}

func TestUpdateServer_SetHostPublicAddress(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	req := &pb.UpdateServerRequest{
		ServerId:          1,
		HostPublicAddress: "203.0.113.5",
		UpdatePaths:       []string{"host_public_address"},
	}

	resp, err := handler.UpdateServer(context.Background(), req)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.Server.HostPublicAddress != "203.0.113.5" {
		t.Errorf("Expected host_public_address=203.0.113.5 on response, got: %q", resp.Server.HostPublicAddress)
	}

	stored, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Failed to fetch stored server: %v", err)
	}
	if stored.HostPublicAddress == nil || *stored.HostPublicAddress != "203.0.113.5" {
		t.Errorf("Expected stored host_public_address=203.0.113.5, got: %v", stored.HostPublicAddress)
	}
}

func TestUpdateServer_ClearHostPublicAddress(t *testing.T) {
	repo := newMockServerRepository()
	addr := "203.0.113.5"
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, HostPublicAddress: &addr})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	req := &pb.UpdateServerRequest{
		ServerId:          1,
		HostPublicAddress: "",
		UpdatePaths:       []string{"host_public_address"},
	}

	resp, err := handler.UpdateServer(context.Background(), req)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.Server.HostPublicAddress != "" {
		t.Errorf("Expected cleared host_public_address on response, got: %q", resp.Server.HostPublicAddress)
	}

	stored, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Failed to fetch stored server: %v", err)
	}
	if stored.HostPublicAddress != nil {
		t.Errorf("Expected stored host_public_address to be nil, got: %v", *stored.HostPublicAddress)
	}
}

func TestUpdateServer_FieldMaskIsolatesHostPublicAddress(t *testing.T) {
	repo := newMockServerRepository()
	addr := "203.0.113.5"
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, HostPublicAddress: &addr})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	req := &pb.UpdateServerRequest{
		ServerId:          1,
		Name:              "srv-1-renamed",
		HostPublicAddress: "198.51.100.9", // populated, but not in update_paths
		UpdatePaths:       []string{"name"},
	}

	_, err := handler.UpdateServer(context.Background(), req)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	stored, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Failed to fetch stored server: %v", err)
	}
	if stored.Name != "srv-1-renamed" {
		t.Errorf("Expected name to be updated, got: %q", stored.Name)
	}
	if stored.HostPublicAddress == nil || *stored.HostPublicAddress != addr {
		t.Errorf("Expected host_public_address to be untouched (%q), got: %v", addr, stored.HostPublicAddress)
	}
}

func TestGetServer_NullHostPublicAddress(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	resp, err := handler.GetServer(context.Background(), &pb.GetServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.Server.HostPublicAddress != "" {
		t.Errorf("Expected host_public_address=\"\" for a NULL address, got: %q", resp.Server.HostPublicAddress)
	}
}

func TestDrainServer_TransitionsToDrainingWithTimestamp(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateSchedulable})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	resp, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.Server.DrainState != manman.ServerDrainStateDraining {
		t.Errorf("Expected drain_state=draining on response, got: %q", resp.Server.DrainState)
	}
	if resp.Server.DrainRequestedAt == 0 {
		t.Error("Expected a non-zero drain_requested_at on response")
	}

	stored, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Failed to fetch stored server: %v", err)
	}
	if stored.DrainState != manman.ServerDrainStateDraining {
		t.Errorf("Expected stored drain_state=draining, got: %q", stored.DrainState)
	}
	if stored.DrainRequestedAt == nil {
		t.Error("Expected stored drain_requested_at to be set")
	}
}

func TestUndrainServer_ReturnsToSchedulableAndClearsTimestamp(t *testing.T) {
	repo := newMockServerRepository()
	requestedAt := time.Now()
	addServer(repo, &manman.Server{
		ServerID:         1,
		Name:             "srv-1",
		Status:           manman.ServerStatusOnline,
		DrainState:       manman.ServerDrainStateDraining,
		DrainRequestedAt: &requestedAt,
	})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	resp, err := handler.UndrainServer(context.Background(), &pb.UndrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.Server.DrainState != manman.ServerDrainStateSchedulable {
		t.Errorf("Expected drain_state=schedulable on response, got: %q", resp.Server.DrainState)
	}
	if resp.Server.DrainRequestedAt != 0 {
		t.Errorf("Expected drain_requested_at=0 on response, got: %d", resp.Server.DrainRequestedAt)
	}

	stored, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Failed to fetch stored server: %v", err)
	}
	if stored.DrainState != manman.ServerDrainStateSchedulable {
		t.Errorf("Expected stored drain_state=schedulable, got: %q", stored.DrainState)
	}
	if stored.DrainRequestedAt != nil {
		t.Errorf("Expected stored drain_requested_at to be nil, got: %v", *stored.DrainRequestedAt)
	}
}

func TestDrainServer_IdempotentOnAlreadyDrainingHost(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateDraining})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	resp, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected draining an already-draining host to succeed, got error: %v", err)
	}
	if resp.Server.DrainState != manman.ServerDrainStateDraining {
		t.Errorf("Expected drain_state=draining, got: %q", resp.Server.DrainState)
	}
}

func TestDrainServer_IdempotentOnAlreadyDrainedHost(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateDrained})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	resp, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected draining an already-drained host to succeed, got error: %v", err)
	}
	if resp.Server.DrainState != manman.ServerDrainStateDraining {
		t.Errorf("Expected drain_state=draining, got: %q", resp.Server.DrainState)
	}
}

func TestUndrainServer_IdempotentOnAlreadySchedulableHost(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateSchedulable})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	resp, err := handler.UndrainServer(context.Background(), &pb.UndrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected undraining an already-schedulable host to succeed, got error: %v", err)
	}
	if resp.Server.DrainState != manman.ServerDrainStateSchedulable {
		t.Errorf("Expected drain_state=schedulable, got: %q", resp.Server.DrainState)
	}
}

func TestDrainServer_UnknownServerIDReturnsNotFound(t *testing.T) {
	repo := newMockServerRepository()
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	_, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 999})
	if err == nil {
		t.Fatal("Expected error for unknown server_id")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Errorf("Expected codes.NotFound, got: %v", st.Code())
	}
}

func TestUndrainServer_UnknownServerIDReturnsNotFound(t *testing.T) {
	repo := newMockServerRepository()
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	_, err := handler.UndrainServer(context.Background(), &pb.UndrainServerRequest{ServerId: 999})
	if err == nil {
		t.Fatal("Expected error for unknown server_id")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Errorf("Expected codes.NotFound, got: %v", st.Code())
	}
}

func TestServerToProto_HostPublicAddressRoundTrip(t *testing.T) {
	nilServer := &manman.Server{ServerID: 1, Name: "srv-1"}
	pbNil := serverToProto(nilServer)
	if pbNil.HostPublicAddress != "" {
		t.Errorf("Expected empty string for nil HostPublicAddress, got: %q", pbNil.HostPublicAddress)
	}

	addr := "203.0.113.5"
	populatedServer := &manman.Server{ServerID: 1, Name: "srv-1", HostPublicAddress: &addr}
	pbPopulated := serverToProto(populatedServer)
	if pbPopulated.HostPublicAddress != addr {
		t.Errorf("Expected host_public_address=%q, got: %q", addr, pbPopulated.HostPublicAddress)
	}
}
