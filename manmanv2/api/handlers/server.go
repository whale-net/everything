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
	repo                repository.ServerRepository
	portRangeRepo       repository.ServerPortRangeRepository
	portRepo            repository.ServerPortRepository
	sessionRepo         repository.SessionRepository
	pendingRestartsRepo repository.PendingRestartRepository
	sessionStopper      SessionStopper
}

// SessionStopper is the narrow interface DrainServer needs over the
// existing per-session stop path (SessionHandler.StopSession) to evict a
// draining host's live sessions (#2366, FR3). Kept narrow -- following the
// DeferredStarter pattern in session_restart_consumer.go -- so DrainServer
// never duplicates Stop's business logic (its graceful/force timeout stays
// wherever StopSession/SessionManager.StopSession already implement it) and
// stays unit-testable without wiring a full SessionHandler.
type SessionStopper interface {
	StopSession(ctx context.Context, req *pb.StopSessionRequest) (*pb.StopSessionResponse, error)
}

func NewServerHandler(
	repo repository.ServerRepository,
	portRangeRepo repository.ServerPortRangeRepository,
	portRepo repository.ServerPortRepository,
	sessionRepo repository.SessionRepository,
	pendingRestartsRepo repository.PendingRestartRepository,
	sessionStopper SessionStopper,
) *ServerHandler {
	return &ServerHandler{
		repo:                repo,
		portRangeRepo:       portRangeRepo,
		portRepo:            portRepo,
		sessionRepo:         sessionRepo,
		pendingRestartsRepo: pendingRestartsRepo,
		sessionStopper:      sessionStopper,
	}
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

	// FR2: settle draining -> drained opportunistically on this read path
	// (page load / manual refresh) rather than a background reconciliation
	// loop -- see settleDrainedState's doc comment.
	for i, s := range servers {
		servers[i] = h.settleDrainedState(ctx, s)
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

	// FR2: settle draining -> drained opportunistically on this read path
	// (page load / manual refresh) rather than a background reconciliation
	// loop -- see settleDrainedState's doc comment.
	server = h.settleDrainedState(ctx, server)

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

// DrainServer transitions a host to "draining", evicts every live session
// on it, and cancels the pending restarts eviction would otherwise race
// (#2360 groundwork + #2366, manmanv2 M6, C29). Cordon (#2364) already
// blocks new placement onto a draining/drained host -- this RPC is what
// actually empties one.
//
// Idempotent: draining an already-draining or already-drained host is a
// successful no-op change, not an error -- eviction and cancellation still
// run (a harmless no-op if the host is already empty), so a retried Drain
// call is safe. No reason field and no audit trail beyond the slog.Info
// calls below (FR3).
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

	h.evictSessions(ctx, req.ServerId)

	server, err := h.repo.Get(ctx, req.ServerId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to reload drained server: %v", err)
	}

	// A host with no live sessions at drain time settles to drained
	// immediately, in the same call -- no separate reconciliation loop is
	// needed for that case (FR2).
	server = h.settleDrainedState(ctx, server)

	slog.Info("server drain requested", "server_id", req.ServerId, "drain_state", server.DrainState)

	return &pb.DrainServerResponse{Server: serverToProto(server)}, nil
}

// liveSessionListLimit bounds ListWithFilters' page size when eviction and
// drain settlement list a host's live sessions. Generously above any
// realistic per-host session count (host port allocation already bounds
// this far lower in practice) -- this is a safety cap, not a pagination
// contract, so neither path silently misses sessions on an unusually busy
// host.
const liveSessionListLimit = 1000

// evictSessions is DrainServer's FR3/FR18 eviction loop: cancel every
// about-to-be-evicted deployment's pending restart, then stop each live
// session on serverID through the existing StopSession path. Best-effort by
// design -- one session failing to stop is logged and must never abort
// eviction of the rest, and a failure here never fails the DrainServer RPC
// itself (the host is left "draining" for a human or a later read to
// observe, per FR2's read-time settlement rather than a reconciliation
// loop).
//
// Ordering is load-bearing (FR18): cancellation must commit before the stop
// is dispatched for the same batch, or the terminal status the stop
// produces could be claimed by SessionRestartConsumer before the
// cancellation lands.
func (h *ServerHandler) evictSessions(ctx context.Context, serverID int64) {
	if h.sessionRepo == nil || h.sessionStopper == nil {
		// Not wired -- e.g. a test exercising unrelated Server RPCs. Stay a
		// no-op rather than panicking on a wiring gap.
		return
	}

	filters := &repository.SessionFilters{ServerID: &serverID, LiveOnly: true}
	sessions, err := h.sessionRepo.ListWithFilters(ctx, filters, liveSessionListLimit, 0)
	if err != nil {
		slog.Error("failed to list live sessions for drain eviction", "server_id", serverID, "error", err)
		return
	}
	if len(sessions) == 0 {
		return
	}

	if h.pendingRestartsRepo != nil {
		sgcIDs := make([]int64, len(sessions))
		for i, s := range sessions {
			sgcIDs[i] = s.SGCID
		}
		reason := fmt.Sprintf("host %d draining", serverID)
		cancelled, err := h.pendingRestartsRepo.CancelForSGCs(ctx, sgcIDs, reason)
		if err != nil {
			// Cancellation failing to commit does not stop eviction: the
			// cordon (#2364) still rejects any retried Start against this
			// host, this just loses FR18's fail-fast-at-cancellation for
			// whichever deployments failed to cancel.
			slog.Error("failed to cancel pending restarts for drain eviction", "server_id", serverID, "error", err)
		} else if cancelled > 0 {
			slog.Info("cancelled pending restarts for drain eviction", "server_id", serverID, "cancelled_count", cancelled)
		}
	}

	for _, s := range sessions {
		if _, err := h.sessionStopper.StopSession(ctx, &pb.StopSessionRequest{SessionId: s.SessionID}); err != nil {
			slog.Error("failed to stop session for drain eviction", "server_id", serverID, "session_id", s.SessionID, "error", err)
			continue
		}
		slog.Info("evicted session for drain", "server_id", serverID, "session_id", s.SessionID)
	}
}

// settleDrainedState is FR2's read-time drained derivation: a draining host
// with no more live sessions settles to drained, persisted opportunistically
// the next time something reads it (DrainServer's own response, GetServer,
// ListServers) rather than via a separate reconciliation/stalled-drain
// recovery loop -- explicitly out of scope (#2359). Every evicted session's
// stop is bounded by the stop path's own existing timeout, so this always
// terminates without needing to poll on its own.
//
// A no-op (returns server unchanged) for a host that isn't currently
// draining, or when sessionRepo isn't wired.
func (h *ServerHandler) settleDrainedState(ctx context.Context, server *manman.Server) *manman.Server {
	if server == nil || server.DrainState != manman.ServerDrainStateDraining || h.sessionRepo == nil {
		return server
	}

	serverID := server.ServerID
	filters := &repository.SessionFilters{ServerID: &serverID, LiveOnly: true}
	sessions, err := h.sessionRepo.ListWithFilters(ctx, filters, liveSessionListLimit, 0)
	if err != nil {
		slog.Error("failed to check live sessions for drain settlement", "server_id", serverID, "error", err)
		return server
	}
	if len(sessions) > 0 {
		return server
	}

	if err := h.repo.SetDrainState(ctx, serverID, manman.ServerDrainStateDrained, server.DrainRequestedAt); err != nil {
		slog.Error("failed to persist drained transition", "server_id", serverID, "error", err)
		return server
	}

	slog.Info("host drained", "server_id", serverID)
	server.DrainState = manman.ServerDrainStateDrained
	return server
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
