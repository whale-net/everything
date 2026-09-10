package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/whale-net/everything/manmanv2/models"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/workshop"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SessionHandler handles Session-related RPCs
type SessionHandler struct {
	repo                *repository.Repository
	sessionRepo         repository.SessionRepository
	sgcRepo             repository.ServerGameConfigRepository
	gcRepo              repository.GameConfigRepository
	pendingRestartsRepo repository.PendingRestartRepository
	publisher           *CommandPublisher
	workshopManager     workshop.WorkshopManagerInterface
	// restartStallTimeout bounds how long a RestartDeployment-recorded
	// pending_restarts row is allowed to sit 'pending' before the reaper
	// (#1731) expires it -- see RESTART_STALL_TIMEOUT in manmanv2/ENV.md.
	restartStallTimeout time.Duration
}

func NewSessionHandler(repo *repository.Repository, publisher *CommandPublisher, workshopManager workshop.WorkshopManagerInterface, restartStallTimeout time.Duration) *SessionHandler {
	return &SessionHandler{
		repo:                repo,
		sessionRepo:         repo.Sessions,
		sgcRepo:             repo.ServerGameConfigs,
		gcRepo:              repo.GameConfigs,
		pendingRestartsRepo: repo.PendingRestarts,
		publisher:           publisher,
		workshopManager:     workshopManager,
		restartStallTimeout: restartStallTimeout,
	}
}

func (h *SessionHandler) ListSessions(ctx context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
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

	var sgcID *int64
	if req.ServerGameConfigId > 0 {
		sgcID = &req.ServerGameConfigId
	}

	var serverID *int64
	if req.ServerId > 0 {
		serverID = &req.ServerId
	}

	filters := &repository.SessionFilters{
		SGCID:        sgcID,
		ServerID:     serverID,
		StatusFilter: req.StatusFilter,
		LiveOnly:     req.LiveOnly,
	}

	if req.StartedAfter > 0 {
		t := time.Unix(req.StartedAfter, 0)
		filters.StartedAfter = &t
	}
	if req.StartedBefore > 0 {
		t := time.Unix(req.StartedBefore, 0)
		filters.StartedBefore = &t
	}

	sessions, err := h.sessionRepo.ListWithFilters(ctx, filters, pageSize+1, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list sessions: %v", err)
	}

	var nextPageToken string
	if len(sessions) > pageSize {
		sessions = sessions[:pageSize]
		nextPageToken = encodePageToken(offset + pageSize)
	}

	pbSessions := make([]*pb.Session, len(sessions))
	for i, s := range sessions {
		pbSessions[i] = sessionToProto(s)
	}

	return &pb.ListSessionsResponse{
		Sessions:      pbSessions,
		NextPageToken: nextPageToken,
	}, nil
}

func (h *SessionHandler) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
	session, err := h.sessionRepo.Get(ctx, req.SessionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
	}

	return &pb.GetSessionResponse{
		Session: sessionToProto(session),
	}, nil
}

func (h *SessionHandler) StartSession(ctx context.Context, req *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
	// Check for existing active sessions
	allActiveStatuses := []string{
		manman.SessionStatusPending,
		manman.SessionStatusStarting,
		manman.SessionStatusRunning,
		manman.SessionStatusStopping,
		manman.SessionStatusCrashed,
		manman.SessionStatusLost,
	}

	filters := &repository.SessionFilters{
		SGCID:        &req.ServerGameConfigId,
		StatusFilter: allActiveStatuses,
	}

	activeSessions, err := h.sessionRepo.ListWithFilters(ctx, filters, 10, 0) // Fetch a few to check statuses
	if err != nil {
		slog.Warn("failed to check active sessions for start", "sgc_id", req.ServerGameConfigId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to check active sessions: %v", err)
	}

	// Only block if there's a TRULY active session (running, pending, etc.)
	// Crashed or Lost sessions don't block start attempts
	var trulyActive *manman.Session

	for _, s := range activeSessions {
		if s.Status != manman.SessionStatusCrashed && s.Status != manman.SessionStatusLost {
			trulyActive = s
		}
	}

	if trulyActive != nil && !req.Force {
		return nil, status.Errorf(codes.FailedPrecondition,
			"active session %d already exists with status %s. Use force=true to override.", trulyActive.SessionID, trulyActive.Status)
	}

	// Force flag - user must explicitly opt-in via force checkbox
	// Never auto-force based on terminal sessions to prevent data loss
	internalForce := req.Force

	if internalForce {
		// User requested force start: mark other sessions as stopped and deallocate ports
		slog.Info("force start requested", "sgc_id", req.ServerGameConfigId, "active_sessions_invalidated", len(activeSessions))
	}

	// Fetch ServerGameConfig to get server ID and deployment details. Fetched
	// before the session row is created (below) so a cordon rejection
	// (#2364, FR3) leaves no orphaned pending session behind.
	sgc, err := h.sgcRepo.Get(ctx, req.ServerGameConfigId)
	if err != nil {
		slog.Warn("failed to fetch server game config for start", "sgc_id", req.ServerGameConfigId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to fetch server game config: %v", err)
	}

	// Cordon (#2364, FR3/FR4): a deployment pinned to a draining/drained
	// host cannot be started -- covers both a fresh Start and a restart
	// dispatched back onto the same host, since deployments are host-pinned
	// and StartSession never reselects a host.
	if err := assertHostSchedulable(ctx, h.repo.Servers, sgc.ServerID); err != nil {
		return nil, err
	}

	// Create session in database
	session := &manman.Session{
		SGCID:  req.ServerGameConfigId,
		Status: manman.SessionStatusPending,
	}

	session, err = h.sessionRepo.Create(ctx, session)
	if err != nil {
		slog.Warn("failed to create session", "sgc_id", req.ServerGameConfigId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to create session: %v", err)
	}

	slog.Info("session start requested", "session_id", session.SessionID, "sgc_id", req.ServerGameConfigId, "force", internalForce)

	if internalForce {
		// Mark other sessions as stopped in DB immediately
		if err := h.sessionRepo.StopOtherSessionsForSGC(ctx, session.SessionID, req.ServerGameConfigId); err != nil {
			slog.Warn("failed to invalidate other sessions for SGC", "sgc_id", req.ServerGameConfigId, "session_id", session.SessionID, "error", err)
		}
	}

	// Fetch GameConfig to get game details
	gc, err := h.gcRepo.Get(ctx, sgc.GameConfigID)
	if err != nil {
		slog.Warn("failed to fetch game config for start", "config_id", sgc.GameConfigID, "session_id", session.SessionID, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to fetch game config: %v", err)
	}

	// FR5: render the effective environment from the SAME GameConfig snapshot
	// that populates env_template, merged with deployment-level env_vars
	// overrides (Option B, DESIGN_SGC_ENV_OVERRIDES.md). A render failure
	// fails the start legibly here -- synchronous error, session marked
	// crashed -- rather than publishing a template-only command.
	renderedEnv, err := h.renderStartSessionEnv(ctx, sgc, gc)
	if err != nil {
		slog.Error("failed to render session environment", "session_id", session.SessionID, "sgc_id", sgc.SGCID, "error", err)
		session.Status = manman.SessionStatusCrashed
		if uerr := h.sessionRepo.Update(ctx, session); uerr != nil {
			slog.Error("failed to mark session crashed after env render failure", "session_id", session.SessionID, "error", uerr)
		}
		return nil, status.Errorf(codes.Internal, "failed to render session environment: %v", err)
	}

	// If force=true, deallocate ports held by crashed/stopped sessions for this SGC
	if internalForce {
		// Find all terminal sessions (crashed, stopped, lost) for this SGC
		filters := &repository.SessionFilters{
			SGCID:        &sgc.SGCID,
			StatusFilter: []string{manman.SessionStatusCrashed, manman.SessionStatusStopped, manman.SessionStatusLost},
		}
		terminalSessions, err := h.sessionRepo.ListWithFilters(ctx, filters, 100, 0)
		if err != nil {
			slog.Warn("failed to list terminal sessions for force start", "sgc_id", sgc.SGCID, "session_id", session.SessionID, "error", err)
		} else {
			for _, ts := range terminalSessions {
				slog.Info("force start: deallocating ports for terminal session", "session_id", session.SessionID, "terminal_session_id", ts.SessionID, "terminal_status", ts.Status)
				if err := h.repo.ServerPorts.DeallocatePortsBySessionID(ctx, ts.SessionID); err != nil {
					slog.Warn("failed to deallocate ports for terminal session", "terminal_session_id", ts.SessionID, "session_id", session.SessionID, "error", err)
				}
			}
		}
	}

	// Allocate ports for this session
	// Port bindings are defined at SGC level, but allocated per active session.
	// This allows multiple SGCs to use the same ports, as long as only one session uses them at a time.
	pbPortBindings := jsonbToPortBindings(sgc.PortBindings)
	if len(pbPortBindings) > 0 {
		// Convert protobuf PortBindings to model PortBindings
		portBindings := make([]*manman.PortBinding, len(pbPortBindings))
		for i, pb := range pbPortBindings {
			portBindings[i] = &manman.PortBinding{
				ContainerPort: pb.ContainerPort,
				HostPort:      pb.HostPort,
				Protocol:      pb.Protocol,
			}
		}

		// Attempt to allocate ports - will fail if already in use by another session
		if err := h.repo.ServerPorts.AllocateMultiplePorts(ctx, sgc.ServerID, portBindings, session.SessionID); err != nil {
			// Rollback: mark session as failed
			session.Status = manman.SessionStatusCrashed
			h.sessionRepo.Update(ctx, session)
			slog.Warn("failed to allocate ports for session start", "session_id", session.SessionID, "server_id", sgc.ServerID, "error", err)
			return nil, status.Errorf(codes.ResourceExhausted, "failed to allocate ports (ports may be in use by another session): %v", err)
		}
		slog.Info("allocated ports for session", "session_id", session.SessionID, "port_count", len(portBindings), "server_id", sgc.ServerID)
	}

	// Fetch volumes for this GameConfig
	volumes, err := h.repo.GameConfigVolumes.ListByGameConfig(ctx, gc.ConfigID)
	if err != nil {
		slog.Warn("failed to fetch volumes for game config", "config_id", gc.ConfigID, "session_id", session.SessionID, "error", err)
		volumes = []*manman.GameConfigVolume{}
	}

	// Addon downloads are handled blocking by the host manager during session start.
	// No pre-flight needed here.

	// Publish start session command to RabbitMQ
	if h.publisher != nil {
		cmd := buildStartSessionCommand(session, sgc, gc, internalForce, volumes, renderedEnv)
		// Short timeout: host manager replies immediately on receipt (work runs async).
		if err := h.publisher.PublishStartSession(ctx, sgc.ServerID, cmd, 30*time.Second); err != nil {
			slog.Warn("failed to publish start session command", "session_id", session.SessionID, "server_id", sgc.ServerID, "error", err)
			// Don't fail the request - the session is created, operator can manually trigger
		} else {
			slog.Info("start session command published", "session_id", session.SessionID, "server_id", sgc.ServerID)
		}
	}

	return &pb.StartSessionResponse{
		Session: sessionToProto(session),
	}, nil
}

func (h *SessionHandler) StopSession(ctx context.Context, req *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
	session, err := h.sessionRepo.Get(ctx, req.SessionId)
	if err != nil {
		slog.Warn("stop requested for unknown session", "session_id", req.SessionId, "error", err)
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
	}

	slog.Info("session stop requested", "session_id", session.SessionID, "sgc_id", session.SGCID)

	// Fetch ServerGameConfig to get server ID
	sgc, err := h.sgcRepo.Get(ctx, session.SGCID)
	if err != nil {
		slog.Warn("failed to fetch server game config for stop", "sgc_id", session.SGCID, "session_id", session.SessionID, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to fetch server game config: %v", err)
	}

	// Publish stop session command to RabbitMQ
	if h.publisher != nil {
		cmd := map[string]interface{}{
			"session_id": session.SessionID,
			"force":      false,
		}
		// Short timeout: host manager replies immediately on receipt (work runs async).
		if err := h.publisher.PublishStopSession(ctx, sgc.ServerID, cmd, 1*time.Minute); err != nil {
			slog.Warn("failed to publish stop session command", "session_id", session.SessionID, "server_id", sgc.ServerID, "error", err)
		}
	}

	// Update session status
	session.Status = manman.SessionStatusStopping
	if err := h.sessionRepo.Update(ctx, session); err != nil {
		slog.Warn("failed to update session status to stopping", "session_id", session.SessionID, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to update session: %v", err)
	}

	// Deallocate ports for this session to allow other sessions to use them
	if err := h.repo.ServerPorts.DeallocatePortsBySessionID(ctx, session.SessionID); err != nil {
		slog.Warn("failed to deallocate ports for session", "session_id", session.SessionID, "error", err)
		// Don't fail the stop request - ports can be cleaned up later
	} else {
		slog.Info("deallocated ports for stopped session", "session_id", session.SessionID)
	}

	return &pb.StopSessionResponse{
		Session: sessionToProto(session),
	}, nil
}

// RestartDeployment records a durable pending-restart intent in Postgres
// before dispatching the Stop, so the intent survives the caller's pod
// dying (#1730, Track B dispatch half of FR9/FR10). The consumer that fires
// the deferred Start once the gating Stop converges is a separate task
// (#1731); this RPC only records the intent and dispatches the Stop.
//
// Ordering is load-bearing: record-then-dispatch. Dispatching the Stop
// first and recording second would reintroduce exactly the lost-intent
// window FR9 exists to close (pod dies between the two).
func (h *SessionHandler) RestartDeployment(ctx context.Context, req *pb.RestartDeploymentRequest) (*pb.RestartDeploymentResponse, error) {
	sgc, err := h.sgcRepo.Get(ctx, req.ServerGameConfigId)
	if err != nil {
		slog.Warn("restart requested for unknown server game config", "sgc_id", req.ServerGameConfigId, "error", err)
		return nil, status.Errorf(codes.NotFound, "server game config not found: %v", err)
	}

	// Cordon (#2364, FR3): a deployment pinned to a draining/drained host
	// cannot be restarted back onto it -- checked before any live-session
	// lookup, pending_restarts row, or Stop dispatch, whether or not a live
	// session currently gates the restart.
	if err := assertHostSchedulable(ctx, h.repo.Servers, sgc.ServerID); err != nil {
		return nil, err
	}

	live, err := h.getLiveSessionForSGC(ctx, req.ServerGameConfigId)
	if err != nil {
		slog.Warn("failed to check live session for restart", "sgc_id", req.ServerGameConfigId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to check live session: %v", err)
	}

	// No live session: this is the degenerate case restartDeployment already
	// handles synchronously today (manmanv2/ui/handlers_deployment_actions.go)
	// -- there is no lost-intent gap to make durable, so just Start inline
	// via the existing handler logic rather than re-deriving it.
	if live == nil {
		startResp, err := h.StartSession(ctx, &pb.StartSessionRequest{ServerGameConfigId: req.ServerGameConfigId})
		if err != nil {
			return nil, err
		}
		return &pb.RestartDeploymentResponse{StartedSession: startResp.Session}, nil
	}

	stallDeadline := time.Now().Add(h.restartStallTimeout)
	pending, err := h.pendingRestartsRepo.Create(ctx, req.ServerGameConfigId, live.SessionID, stallDeadline)
	if err != nil {
		if errors.Is(err, repository.ErrPendingRestartExists) {
			slog.Info("restart already in flight, no-op", "sgc_id", req.ServerGameConfigId, "gating_session_id", live.SessionID)
			return &pb.RestartDeploymentResponse{AlreadyInFlight: true}, nil
		}
		// Nothing is dispatched here: a failure to record the intent must
		// leave the deployment exactly as it was, rather than stopped with
		// no record of why.
		slog.Warn("failed to record pending restart", "sgc_id", req.ServerGameConfigId, "gating_session_id", live.SessionID, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to record pending restart: %v", err)
	}

	slog.Info("restart dispatched", "sgc_id", req.ServerGameConfigId, "gating_session_id", live.SessionID, "pending_restart_id", pending.PendingRestartID, "stall_deadline", stallDeadline)

	// Only after the record is committed do we dispatch the Stop.
	stopResp, err := h.StopSession(ctx, &pb.StopSessionRequest{SessionId: live.SessionID})
	if err != nil {
		// The record is left 'pending' if we can't mark it failed here; the
		// reaper (#1731) will eventually expire it, just more slowly than
		// this fast-path cleanup.
		if markErr := h.pendingRestartsRepo.MarkFailed(ctx, pending.PendingRestartID, err.Error()); markErr != nil {
			slog.Warn("failed to mark pending restart failed after stop dispatch error", "pending_restart_id", pending.PendingRestartID, "sgc_id", req.ServerGameConfigId, "error", markErr)
		} else {
			slog.Warn("stop dispatch failed for restart; pending restart marked failed", "pending_restart_id", pending.PendingRestartID, "sgc_id", req.ServerGameConfigId, "gating_session_id", live.SessionID, "error", err)
		}
		return nil, err
	}

	return &pb.RestartDeploymentResponse{StoppingSession: stopResp.Session}, nil
}

// getLiveSessionForSGC returns the current live (non-terminal) session for
// sgcID, or nil if there is none. Reuses the same live-session query path
// StartSession/StopSession filtering relies on (repository.SessionFilters.
// LiveOnly) rather than re-deriving the live-status set here.
func (h *SessionHandler) getLiveSessionForSGC(ctx context.Context, sgcID int64) (*manman.Session, error) {
	filters := &repository.SessionFilters{
		SGCID:    &sgcID,
		LiveOnly: true,
	}
	sessions, err := h.sessionRepo.ListWithFilters(ctx, filters, 1, 0)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	return sessions[0], nil
}

func (h *SessionHandler) SendInput(ctx context.Context, req *pb.SendInputRequest) (*pb.SendInputResponse, error) {
	session, err := h.sessionRepo.Get(ctx, req.SessionId)
	if err != nil {
		slog.Warn("send input requested for unknown session", "session_id", req.SessionId, "error", err)
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
	}

	// Only allow sending input to running sessions
	if session.Status != manman.SessionStatusRunning {
		slog.Warn("send input rejected: session not running", "session_id", session.SessionID, "status", session.Status)
		return nil, status.Errorf(codes.FailedPrecondition, "session is not running (status: %s)", session.Status)
	}

	// Fetch ServerGameConfig to get server ID
	sgc, err := h.sgcRepo.Get(ctx, session.SGCID)
	if err != nil {
		slog.Warn("failed to fetch server game config for send input", "sgc_id", session.SGCID, "session_id", session.SessionID, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to fetch server game config: %v", err)
	}

	// Publish send input command to RabbitMQ with 10s timeout
	if h.publisher != nil {
		cmd := map[string]interface{}{
			"session_id": session.SessionID,
			"input":      req.Input,
		}
		if err := h.publisher.PublishSendInput(ctx, sgc.ServerID, cmd, 10*time.Second); err != nil {
			slog.Warn("failed to publish send input command", "session_id", session.SessionID, "server_id", sgc.ServerID, "error", err)
			return nil, status.Errorf(codes.Internal, "failed to send input: %v", err)
		}
		slog.Info("input sent to session", "session_id", session.SessionID)
	} else {
		slog.Warn("send input failed: publisher not configured", "session_id", session.SessionID)
		return nil, status.Errorf(codes.Internal, "publisher not configured")
	}

	return &pb.SendInputResponse{}, nil
}

// ListPendingRestarts is the batched FR12 read path: one RPC for every
// rendered deployment row, never one call per row (#1735's /sessions page
// renders N rows from a single response). An empty id list is a no-op --
// no query, an empty response -- rather than a query with an empty ANY($1),
// which would still round-trip to Postgres for nothing.
func (h *SessionHandler) ListPendingRestarts(ctx context.Context, req *pb.ListPendingRestartsRequest) (*pb.ListPendingRestartsResponse, error) {
	if len(req.ServerGameConfigIds) == 0 {
		return &pb.ListPendingRestartsResponse{}, nil
	}

	latest, err := h.pendingRestartsRepo.GetLatestBySGCIDs(ctx, req.ServerGameConfigIds)
	if err != nil {
		slog.Warn("failed to list pending restarts", "sgc_ids", req.ServerGameConfigIds, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to list pending restarts: %v", err)
	}

	resp := &pb.ListPendingRestartsResponse{}
	for _, sgcID := range req.ServerGameConfigIds {
		pr, ok := latest[sgcID]
		if !ok {
			continue
		}
		resp.States = append(resp.States, pendingRestartToProto(pr))
	}
	return resp, nil
}

// pendingRestartToProto maps a PendingRestart to its wire representation.
// Timestamps are emitted as absolute unix seconds only (NFR11) -- never a
// relative/formatted string -- so the SSE-pushed fragment this feeds stays
// byte-stable across renders for an unchanged restart state.
func pendingRestartToProto(pr *manman.PendingRestart) *pb.PendingRestartState {
	state := &pb.PendingRestartState{
		ServerGameConfigId: pr.ServerGameConfigID,
		PendingRestartId:   pr.PendingRestartID,
		Status:             pr.Status,
		GatingSessionId:    pr.GatingSessionID,
		CreatedAtUnix:      pr.CreatedAt.Unix(),
	}
	if pr.FailureReason != nil {
		state.FailureReason = *pr.FailureReason
	}
	if pr.ResolvedAt != nil {
		state.ResolvedAtUnix = pr.ResolvedAt.Unix()
	}
	return state
}

// buildStartSessionCommand converts database models to RabbitMQ message format.
// renderedEnv is the server-rendered effective environment (FR5): non-nil is
// published as top-level rendered_env and is authoritative for the host (even
// when empty); nil is published as absent so the host falls back to env_template.
func buildStartSessionCommand(session *manman.Session, sgc *manman.ServerGameConfig, gc *manman.GameConfig, force bool, volumes []*manman.GameConfigVolume, renderedEnv map[string]string) map[string]interface{} {
	// Build game config message
	commandArray := jsonbToStringArray(gc.Command)
	slog.Info("building start session command",
		"session_id", session.SessionID,
		"config_id", gc.ConfigID,
		"command_from_db", gc.Command,
		"command_array", commandArray)

	gameConfig := map[string]interface{}{
		"config_id":     gc.ConfigID,
		"image":         gc.Image,
		"args_template": gc.ArgsTemplate,
		"env_template":  jsonbToMap(gc.EnvTemplate),
		"entrypoint":    jsonbToStringArray(gc.Entrypoint),
		"command":       commandArray,
	}

	// Add volume mounts from game_config_volumes
	var volumeMsgs []map[string]interface{}
	for _, vol := range volumes {
		volMsg := map[string]interface{}{
			"name":           vol.Name,
			"container_path": vol.ContainerPath,
		}
		if vol.HostSubpath != nil {
			volMsg["host_subpath"] = *vol.HostSubpath
		}
		if vol.VolumeType != "" {
			volMsg["volume_type"] = vol.VolumeType
		}
		if vol.ReadOnly {
			volMsg["options"] = map[string]interface{}{"read_only": true}
		}
		volumeMsgs = append(volumeMsgs, volMsg)
	}
	gameConfig["volumes"] = volumeMsgs

	// Build server game config message
	serverGameConfig := map[string]interface{}{
		"sgc_id":        sgc.SGCID,
		"port_bindings": convertPortBindingsToMessage(sgc.PortBindings),
	}

	cmd := map[string]interface{}{
		"session_id":         session.SessionID,
		"sgc_id":             sgc.SGCID,
		"game_config":        gameConfig,
		"server_game_config": serverGameConfig,
		"force":              force,
	}
	if renderedEnv != nil {
		cmd["rendered_env"] = renderedEnv
	}
	return cmd
}

// renderStartSessionEnv fetches deployment-level env_vars overrides for this
// deployment (game_config and server_game_config levels) and merges them over
// the GameConfig env template fetched in this same request. Merge order (later
// wins): env_template → game_config patches → server_game_config patches
// (Option B in manmanv2/docs/DESIGN_SGC_ENV_OVERRIDES.md). The result is
// always non-nil: an empty rendered env is authoritative-empty, not absent.
func (h *SessionHandler) renderStartSessionEnv(ctx context.Context, sgc *manman.ServerGameConfig, gc *manman.GameConfig) (map[string]string, error) {
	strategies, err := h.repo.ConfigurationStrategies.ListByGame(ctx, gc.GameID)
	if err != nil {
		return nil, fmt.Errorf("failed to list configuration strategies: %w", err)
	}
	envVarStrategies := make(map[int64]bool)
	for _, s := range strategies {
		if s.StrategyType == manman.StrategyTypeEnvVars {
			envVarStrategies[s.StrategyID] = true
		}
	}

	// Fetch patches per level (all strategies), keeping only env_vars-strategy
	// patches, ordered by patch_order (lower = earlier = lower priority).
	var contents []string
	for _, level := range []struct {
		name     string
		entityID int64
	}{{manman.PatchLevelGameConfig, gc.ConfigID}, {manman.PatchLevelServerGameConfig, sgc.SGCID}} {
		levelName := level.name
		patches, err := h.repo.ConfigurationPatches.List(ctx, nil, &levelName, &level.entityID)
		if err != nil {
			return nil, fmt.Errorf("failed to list %s env patches: %w", level.name, err)
		}
		sort.Slice(patches, func(i, j int) bool { return patches[i].PatchOrder < patches[j].PatchOrder })
		for _, p := range patches {
			if !envVarStrategies[p.StrategyID] {
				continue
			}
			if p.PatchContent == nil {
				continue
			}
			contents = append(contents, *p.PatchContent)
		}
	}

	rendered, err := renderEffectiveEnv(jsonbToMap(gc.EnvTemplate), contents)
	if err != nil {
		return nil, err
	}
	slog.Debug("rendered session environment",
		"sgc_id", sgc.SGCID,
		"template_vars", len(jsonbToMap(gc.EnvTemplate)), "override_patches", len(contents), "rendered_vars", len(rendered))
	return rendered, nil
}

// renderEffectiveEnv merges properties-format (KEY=VALUE lines) override patch
// contents over the env template in order (later wins). Blank lines and #
// comments are skipped; a line without "=" or with an empty key is a render
// error (legible failure per FR5).
func renderEffectiveEnv(envTemplate map[string]string, patchContents []string) (map[string]string, error) {
	rendered := make(map[string]string, len(envTemplate))
	for k, v := range envTemplate {
		rendered[k] = v
	}
	for _, content := range patchContents {
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			key = strings.TrimSpace(key)
			if !ok || key == "" {
				return nil, fmt.Errorf("invalid env override line %q: expected KEY=VALUE", line)
			}
			rendered[key] = strings.TrimSpace(value)
		}
	}
	return rendered, nil
}

// convertPortBindingsToMessage converts JSONB port bindings to RabbitMQ message format
func convertPortBindingsToMessage(bindingsJSON manman.JSONB) []interface{} {
	if bindingsJSON == nil {
		return []interface{}{}
	}
	// Port bindings are stored as map: "25565/TCP" -> 25565
	// Convert to array of port binding messages for RMQ
	var result []interface{}
	for key, value := range bindingsJSON {
		parts := strings.Split(key, "/")
		if len(parts) != 2 {
			continue
		}
		containerPort, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		hostPort := int32(0)
		switch v := value.(type) {
		case float64:
			hostPort = int32(v)
		case int:
			hostPort = int32(v)
		case int32:
			hostPort = v
		}

		result = append(result, map[string]interface{}{
			"container_port": containerPort,
			"host_port":      hostPort,
			"protocol":       parts[1],
		})
	}
	return result
}

func sessionToProto(s *manman.Session) *pb.Session {
	pbSession := &pb.Session{
		SessionId:          s.SessionID,
		ServerGameConfigId: s.SGCID,
		Status:             s.Status,
	}

	if s.StartedAt != nil {
		pbSession.StartedAt = s.StartedAt.Unix()
	}

	if s.EndedAt != nil {
		pbSession.EndedAt = s.EndedAt.Unix()
	}

	if s.ExitCode != nil {
		pbSession.ExitCode = int32(*s.ExitCode)
	}

	return pbSession
}
