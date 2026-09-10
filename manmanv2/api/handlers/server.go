package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/whale-net/everything/manmanv2/api/repository"
	manman "github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ServerHandler struct {
	repo          repository.ServerRepository
	portRangeRepo repository.ServerPortRangeRepository
	portRepo      repository.ServerPortRepository
}

func NewServerHandler(repo repository.ServerRepository, portRangeRepo repository.ServerPortRangeRepository, portRepo repository.ServerPortRepository) *ServerHandler {
	return &ServerHandler{repo: repo, portRangeRepo: portRangeRepo, portRepo: portRepo}
}

func (h *ServerHandler) ListServers(ctx context.Context, req *pb.ListServersRequest) (*pb.ListServersResponse, error) {
	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 100 {
		pageSize = 100
	}

	offset := 0
	if req.PageToken != "" {
		var err error
		offset, err = decodePageToken(req.PageToken)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid page token: %v", err)
		}
	}

	servers, err := h.repo.List(ctx, pageSize+1, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list servers: %v", err)
	}

	var nextPageToken string
	if len(servers) > pageSize {
		servers = servers[:pageSize]
		nextPageToken = encodePageToken(offset + pageSize)
	}

	pbServers := make([]*pb.Server, len(servers))
	for i, s := range servers {
		pbServers[i] = serverToProto(s)
	}

	return &pb.ListServersResponse{
		Servers:       pbServers,
		NextPageToken: nextPageToken,
	}, nil
}

func (h *ServerHandler) GetServer(ctx context.Context, req *pb.GetServerRequest) (*pb.GetServerResponse, error) {
	server, err := h.repo.Get(ctx, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "server not found: %v", err)
	}

	return &pb.GetServerResponse{
		Server: h.serverToProtoWithRanges(ctx, server),
	}, nil
}

// UpdateServerAllowedPortRanges replaces the server's full allowed
// host-port range set (FR12, task #2095). Empty set = unconstrained
// (SB-1.2); save-time validation beyond basic shape stays out per
// root-plan Decision 7.
func (h *ServerHandler) UpdateServerAllowedPortRanges(ctx context.Context, req *pb.UpdateServerAllowedPortRangesRequest) (*pb.UpdateServerAllowedPortRangesResponse, error) {
	if req.ServerId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	if _, err := h.repo.Get(ctx, req.ServerId); err != nil {
		return nil, status.Errorf(codes.NotFound, "server not found: %v", err)
	}

	ranges := make([]*manman.ServerAllowedPortRange, 0, len(req.Ranges))
	for _, pr := range req.Ranges {
		if pr == nil {
			continue
		}
		ranges = append(ranges, &manman.ServerAllowedPortRange{
			ServerID:  req.ServerId,
			StartPort: pr.Start,
			EndPort:   pr.End,
			Protocol:  pr.Protocol,
		})
	}

	stored, err := h.portRangeRepo.Replace(ctx, req.ServerId, ranges)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to update allowed port ranges: %v", err)
	}

	return &pb.UpdateServerAllowedPortRangesResponse{
		Ranges: portRangesToProto(stored),
	}, nil
}

// ListAllocatedPorts lists a server's port allocations (additive, task
// #2098): a read-only projection of server_ports used by the UI's ports
// editor for in-use warnings and random in-range generation. Guidance
// only -- session-start allocation remains the correctness backstop
// (Decision 7), and no write RPC exists because allocation happens only
// inside session start (FR12 enforcement).
func (h *ServerHandler) ListAllocatedPorts(ctx context.Context, req *pb.ListAllocatedPortsRequest) (*pb.ListAllocatedPortsResponse, error) {
	if req.ServerId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	if _, err := h.repo.Get(ctx, req.ServerId); err != nil {
		return nil, status.Errorf(codes.NotFound, "server not found: %v", err)
	}

	ports, err := h.portRepo.ListAllocatedPorts(ctx, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list allocated ports: %v", err)
	}

	out := make([]*pb.AllocatedPort, 0, len(ports))
	for _, p := range ports {
		var sessionID int64
		if p.SessionID != nil {
			sessionID = *p.SessionID
		}
		out = append(out, &pb.AllocatedPort{
			ServerId:  p.ServerID,
			Port:      int32(p.Port),
			Protocol:  p.Protocol,
			SessionId: sessionID,
		})
	}
	return &pb.ListAllocatedPortsResponse{Ports: out}, nil
}

// serverToProtoWithRanges is serverToProto plus the server's allowed
// host-port ranges (additive, FR12). ListServers keeps the cheap shape;
// ranges ride on GetServer only.
func (h *ServerHandler) serverToProtoWithRanges(ctx context.Context, server *manman.Server) *pb.Server {
	pbServer := serverToProto(server)
	ranges, err := h.portRangeRepo.List(ctx, server.ServerID)
	if err != nil {
		return pbServer
	}
	pbServer.AllowedPortRanges = portRangesToProto(ranges)
	return pbServer
}

func portRangesToProto(ranges []*manman.ServerAllowedPortRange) []*pb.PortRange {
	out := make([]*pb.PortRange, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, &pb.PortRange{Start: r.StartPort, End: r.EndPort, Protocol: r.Protocol})
	}
	return out
}

func (h *ServerHandler) CreateServer(ctx context.Context, req *pb.CreateServerRequest) (*pb.CreateServerResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	server, err := h.repo.Create(ctx, req.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create server: %v", err)
	}

	return &pb.CreateServerResponse{
		Server: serverToProto(server),
	}, nil
}

func (h *ServerHandler) UpdateServer(ctx context.Context, req *pb.UpdateServerRequest) (*pb.UpdateServerResponse, error) {
	server, err := h.repo.Get(ctx, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "server not found: %v", err)
	}

	// Apply field paths
	if len(req.UpdatePaths) == 0 {
		// Update all provided fields
		if req.Name != "" {
			server.Name = req.Name
		}
		if req.Status != "" {
			server.Status = req.Status
		}
		// Only update is_default if explicitly provided
		if req.IsDefault {
			server.IsDefault = req.IsDefault
		}
		// host_public_address is intentionally NOT updated here, even when
		// req.HostPublicAddress != "". Proto3 gives no presence signal for a
		// scalar string, so the only unambiguous way to clear the address is
		// an explicit "host_public_address" entry in update_paths (see below).
		// Mirroring the name/status convention here would make it impossible
		// to ever clear the field, so the asymmetry is deliberate.
	} else {
		// Update only specified fields
		for _, path := range req.UpdatePaths {
			switch path {
			case "name":
				server.Name = req.Name
			case "status":
				server.Status = req.Status
			case "is_default":
				server.IsDefault = req.IsDefault
			case "host_public_address":
				// Non-empty sets the address; empty string clears it back to NULL.
				// This is the only way to unset it, since the field mask entry
				// itself is the presence signal (proto3 scalars have none).
				if req.HostPublicAddress == "" {
					server.HostPublicAddress = nil
				} else {
					addr := req.HostPublicAddress
					server.HostPublicAddress = &addr
				}
			}
		}
	}

	if err := h.repo.Update(ctx, server); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update server: %v", err)
	}

	return &pb.UpdateServerResponse{
		Server: serverToProto(server),
	}, nil
}

func (h *ServerHandler) DeleteServer(ctx context.Context, req *pb.DeleteServerRequest) (*pb.DeleteServerResponse, error) {
	if err := h.repo.Delete(ctx, req.ServerId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete server: %v", err)
	}

	return &pb.DeleteServerResponse{}, nil
}

// DrainServer transitions a host to "draining" (#2360, manmanv2 M6, C29
// groundwork). Inert here: it only flips drain_state and stamps
// drain_requested_at -- no cordon enforcement, no eviction.
// TODO(#eviction-task): hook cordon enforcement / session eviction in here
// once the dependent task lands.
// Idempotent: draining an already-draining or already-drained host is a
// successful no-op change, not an error. No reason field, no audit trail
// beyond the slog.Info below (FR3).
func (h *ServerHandler) DrainServer(ctx context.Context, req *pb.DrainServerRequest) (*pb.DrainServerResponse, error) {
	if req.ServerId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	if _, err := h.repo.Get(ctx, req.ServerId); err != nil {
		return nil, status.Errorf(codes.NotFound, "server not found: %v", err)
	}

	requestedAt := time.Now()
	if err := h.repo.SetDrainState(ctx, req.ServerId, manman.ServerDrainStateDraining, &requestedAt); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to drain server: %v", err)
	}

	server, err := h.repo.Get(ctx, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to reload drained server: %v", err)
	}

	slog.Info("server drained", "server_id", req.ServerId)

	return &pb.DrainServerResponse{Server: serverToProto(server)}, nil
}

// UndrainServer returns a host to "schedulable" and clears
// drain_requested_at. Never restarts anything (FR4). Idempotent:
// undraining an already-schedulable host is a successful no-op change.
func (h *ServerHandler) UndrainServer(ctx context.Context, req *pb.UndrainServerRequest) (*pb.UndrainServerResponse, error) {
	if req.ServerId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	if _, err := h.repo.Get(ctx, req.ServerId); err != nil {
		return nil, status.Errorf(codes.NotFound, "server not found: %v", err)
	}

	if err := h.repo.SetDrainState(ctx, req.ServerId, manman.ServerDrainStateSchedulable, nil); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to undrain server: %v", err)
	}

	server, err := h.repo.Get(ctx, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to reload undrained server: %v", err)
	}

	slog.Info("server undrained", "server_id", req.ServerId)

	return &pb.UndrainServerResponse{Server: serverToProto(server)}, nil
}

func serverToProto(s *manman.Server) *pb.Server {
	pbServer := &pb.Server{
		ServerId:   s.ServerID,
		Name:       s.Name,
		Status:     s.Status,
		IsDefault:  s.IsDefault,
		DrainState: s.DrainState,
	}

	if s.LastSeen != nil {
		pbServer.LastSeen = s.LastSeen.Unix()
	}

	if s.Environment != nil {
		pbServer.Environment = *s.Environment
	}

	if s.HostPublicAddress != nil {
		pbServer.HostPublicAddress = *s.HostPublicAddress
	}

	if s.DrainRequestedAt != nil {
		pbServer.DrainRequestedAt = s.DrainRequestedAt.Unix()
	}

	return pbServer
}

// Pagination token helpers
func encodePageToken(offset int) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", offset)))
}

func decodePageToken(token string) (int, error) {
	data, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(string(data))
}
