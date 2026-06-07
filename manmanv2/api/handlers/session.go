package handlers

import (
	"context"
	"log"
	"log/slog"
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
	repo            *repository.Repository
	sessionRepo     repository.SessionRepository
	sgcRepo         repository.ServerGameConfigRepository
	gcRepo          repository.GameConfigRepository
	publisher       *CommandPublisher
	workshopManager workshop.WorkshopManagerInterface
}

func NewSessionHandler(repo *repository.Repository, publisher *CommandPublisher, workshopManager workshop.WorkshopManagerInterface) *SessionHandler {
	return &SessionHandler{
		repo:            repo,
		sessionRepo:     repo.Sessions,
		sgcRepo:         repo.ServerGameConfigs,
		gcRepo:          repo.GameConfigs,
		publisher:       publisher,
		workshopManager: workshopManager,
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
		log.Printf("Force start requested by user for SGC %d, will invalidate %d active sessions", req.ServerGameConfigId, len(activeSessions))
	}

	// Create session in database
	session := &manman.Session{
		SGCID:  req.ServerGameConfigId,
		Status: manman.SessionStatusPending,
	}

	session, err = h.sessionRepo.Create(ctx, session)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create session: %v", err)
	}

	if internalForce {
		// Mark other sessions as stopped in DB immediately
		if err := h.sessionRepo.StopOtherSessionsForSGC(ctx, session.SessionID, req.ServerGameConfigId); err != nil {
			log.Printf("Warning: Failed to invalidate other sessions for SGC %d: %v", req.ServerGameConfigId, err)
		}
	}

	// Fetch ServerGameConfig to get server ID and deployment details
	sgc, err := h.sgcRepo.Get(ctx, req.ServerGameConfigId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch server game config: %v", err)
	}

	// Fetch GameConfig to get game details
	gc, err := h.gcRepo.Get(ctx, sgc.GameConfigID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch game config: %v", err)
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
			log.Printf("Warning: Failed to list terminal sessions for SGC %d: %v", sgc.SGCID, err)
		} else {
			for _, ts := range terminalSessions {
				log.Printf("[session %d] force=true: deallocating ports for terminal session %d (status: %s)", session.SessionID, ts.SessionID, ts.Status)
				if err := h.repo.ServerPorts.DeallocatePortsBySessionID(ctx, ts.SessionID); err != nil {
					log.Printf("Warning: Failed to deallocate ports for session %d: %v", ts.SessionID, err)
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
			return nil, status.Errorf(codes.ResourceExhausted, "failed to allocate ports (ports may be in use by another session): %v", err)
		}
		log.Printf("[session %d] allocated %d ports on server %d", session.SessionID, len(portBindings), sgc.ServerID)
	}

	// Fetch volumes for this GameConfig
	volumes, err := h.repo.GameConfigVolumes.ListByGameConfig(ctx, gc.ConfigID)
	if err != nil {
		log.Printf("Warning: Failed to fetch volumes for config %d: %v", gc.ConfigID, err)
		volumes = []*manman.GameConfigVolume{}
	}

	// Addon downloads are handled blocking by the host manager during session start.
	// No pre-flight needed here.

	// Publish start session command to RabbitMQ
	if h.publisher != nil {
		cmd := buildStartSessionCommand(session, sgc, gc, internalForce, volumes)
		// Short timeout: host manager replies immediately on receipt (work runs async).
		if err := h.publisher.PublishStartSession(ctx, sgc.ServerID, cmd, 30*time.Second); err != nil {
			log.Printf("Warning: Failed to publish start session command: %v", err)
			// Don't fail the request - the session is created, operator can manually trigger
		}
	}

	return &pb.StartSessionResponse{
		Session: sessionToProto(session),
	}, nil
}

func (h *SessionHandler) StopSession(ctx context.Context, req *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
	session, err := h.sessionRepo.Get(ctx, req.SessionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
	}

	// Fetch ServerGameConfig to get server ID
	sgc, err := h.sgcRepo.Get(ctx, session.SGCID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch server game config: %v", err)
	}

	// Publish stop session command to RabbitMQ
	if h.publisher != nil {
		cmd := map[string]interface{}{
			"session_id": session.SessionID,
			"force":      false,
		}
		if err := h.publisher.PublishStopSession(ctx, sgc.ServerID, cmd, 1*time.Minute); err != nil {
			log.Printf("Warning: Failed to publish stop session command: %v", err)
		}
	}

	// Update session status
	session.Status = manman.SessionStatusStopping
	if err := h.sessionRepo.Update(ctx, session); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update session: %v", err)
	}

	// Deallocate ports for this session to allow other sessions to use them
	if err := h.repo.ServerPorts.DeallocatePortsBySessionID(ctx, session.SessionID); err != nil {
		log.Printf("Warning: Failed to deallocate ports for session %d: %v", session.SessionID, err)
		// Don't fail the stop request - ports can be cleaned up later
	} else {
		log.Printf("[session %d] deallocated ports", session.SessionID)
	}

	return &pb.StopSessionResponse{
		Session: sessionToProto(session),
	}, nil
}

func (h *SessionHandler) SendInput(ctx context.Context, req *pb.SendInputRequest) (*pb.SendInputResponse, error) {
	session, err := h.sessionRepo.Get(ctx, req.SessionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
	}

	// Only allow sending input to running sessions
	if session.Status != manman.SessionStatusRunning {
		return nil, status.Errorf(codes.FailedPrecondition, "session is not running (status: %s)", session.Status)
	}

	// Fetch ServerGameConfig to get server ID
	sgc, err := h.sgcRepo.Get(ctx, session.SGCID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch server game config: %v", err)
	}

	// Publish send input command to RabbitMQ with 10s timeout
	if h.publisher != nil {
		cmd := map[string]interface{}{
			"session_id": session.SessionID,
			"input":      req.Input,
		}
		if err := h.publisher.PublishSendInput(ctx, sgc.ServerID, cmd, 10*time.Second); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to send input: %v", err)
		}
	} else {
		return nil, status.Errorf(codes.Internal, "publisher not configured")
	}

	return &pb.SendInputResponse{}, nil
}

// buildStartSessionCommand converts database models to RabbitMQ message format
func buildStartSessionCommand(session *manman.Session, sgc *manman.ServerGameConfig, gc *manman.GameConfig, force bool, volumes []*manman.GameConfigVolume) map[string]interface{} {
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

	// Add volume mounts from game_config_volumes (only enabled volumes)
	var volumeMsgs []map[string]interface{}
	for _, vol := range volumes {
		if !vol.IsEnabled {
			continue
		}
		volMsg := map[string]interface{}{
			"name":           vol.Name,
			"container_path": vol.ContainerPath,
			"is_enabled":     true,
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

	return map[string]interface{}{
		"session_id":         session.SessionID,
		"sgc_id":             sgc.SGCID,
		"game_config":        gameConfig,
		"server_game_config": serverGameConfig,
		"force":              force,
	}
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
