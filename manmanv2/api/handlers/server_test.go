package handlers

import (
	"context"
	"testing"

	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
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
	handler := NewServerHandler(repo)

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
	handler := NewServerHandler(repo)

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
	handler := NewServerHandler(repo)

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
	handler := NewServerHandler(repo)

	resp, err := handler.GetServer(context.Background(), &pb.GetServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.Server.HostPublicAddress != "" {
		t.Errorf("Expected host_public_address=\"\" for a NULL address, got: %q", resp.Server.HostPublicAddress)
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
