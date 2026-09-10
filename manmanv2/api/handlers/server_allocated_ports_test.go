package handlers

import (
	"context"
	"testing"

	manman "github.com/whale-net/everything/manmanv2/models"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file guards the additive ListAllocatedPorts API read (task #2098,
// plan #2080): the UI's ports editor gets a server's port allocations as
// a read-only projection of server_ports (FR13/FR14 guidance data).
// Guidance only -- no write RPC exists because allocation happens only
// inside session start (FR12 enforcement), and session-start allocation
// remains the correctness backstop (Decision 7).
//
// mutation-tested (verified red, by hand, then reverted): dropping the
// SessionId field from the handler's proto mapping made
// TestListAllocatedPorts_MapsAllocations fail; reverting restored green.

// mockServerPortRepository implements the ListAllocatedPorts read the
// handler needs; the embedded nil interface panics loudly if the
// handler's call graph ever grows past it.
type mockServerPortRepository struct {
	repository.ServerPortRepository
	ports []*manman.ServerPort
}

func (m *mockServerPortRepository) ListAllocatedPorts(_ context.Context, serverID int64) ([]*manman.ServerPort, error) {
	out := []*manman.ServerPort{}
	for _, p := range m.ports {
		if p.ServerID == serverID {
			out = append(out, p)
		}
	}
	return out, nil
}

func TestListAllocatedPorts_MapsAllocations(t *testing.T) {
	repo := newMockServerRepository()
	if _, err := repo.Create(context.Background(), "srv-7"); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	for _, s := range repo.servers {
		s.ServerID = 7
	}

	portRepo := &mockServerPortRepository{
		ports: []*manman.ServerPort{
			{ServerID: 7, Port: 25566, Protocol: "TCP", SessionID: &[]int64{101}[0]},
			{ServerID: 7, Port: 27016, Protocol: "UDP", SessionID: nil},
			// Another server's row must not leak through.
			{ServerID: 8, Port: 1, Protocol: "TCP", SessionID: nil},
		},
	}
	handler := NewServerHandler(repo, nil, portRepo, nil, nil, nil)

	resp, err := handler.ListAllocatedPorts(context.Background(), &pb.ListAllocatedPortsRequest{ServerId: 7})
	if err != nil {
		t.Fatalf("ListAllocatedPorts: %v", err)
	}
	want := []*pb.AllocatedPort{
		{ServerId: 7, Port: 25566, Protocol: "TCP", SessionId: 101},
		{ServerId: 7, Port: 27016, Protocol: "UDP", SessionId: 0},
	}
	got := resp.GetPorts()
	if len(got) != len(want) {
		t.Fatalf("ports = %+v, want %v", got, want)
	}
	for i := range want {
		if got[i].GetServerId() != want[i].GetServerId() || got[i].GetPort() != want[i].GetPort() ||
			got[i].GetProtocol() != want[i].GetProtocol() || got[i].GetSessionId() != want[i].GetSessionId() {
			t.Errorf("ports[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListAllocatedPorts_InvalidServerID_InvalidArgument(t *testing.T) {
	handler := NewServerHandler(newMockServerRepository(), nil, &mockServerPortRepository{}, nil, nil, nil)
	_, err := handler.ListAllocatedPorts(context.Background(), &pb.ListAllocatedPortsRequest{ServerId: 0})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestListAllocatedPorts_UnknownServer_NotFound(t *testing.T) {
	handler := NewServerHandler(newMockServerRepository(), nil, &mockServerPortRepository{}, nil, nil, nil)
	_, err := handler.ListAllocatedPorts(context.Background(), &pb.ListAllocatedPortsRequest{ServerId: 42})
	if status.Code(err) != codes.NotFound {
		t.Errorf("code = %v, want NotFound", status.Code(err))
	}
}
