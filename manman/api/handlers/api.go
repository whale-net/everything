package handlers

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/api/repository"
	"github.com/whale-net/everything/manman/api/repository/postgres"
	pb "github.com/whale-net/everything/manman/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// APIServer implements the ManManAPI gRPC service
type APIServer struct {
	pb.UnimplementedManManAPIServer
	repo *repository.Repository

	serverHandler           *ServerHandler
	gameHandler             *GameHandler
	gameConfigHandler       *GameConfigHandler
	serverGameConfigHandler *ServerGameConfigHandler
	sessionHandler          *SessionHandler
	registrationHandler     *RegistrationHandler
	validationHandler       *ValidationHandler
	logsHandler             *LogsHandler
	backupHandler           *BackupHandler
	strategyHandler         *ConfigurationStrategyHandler
	patchHandler            *ConfigurationPatchHandler
	actionHandler           *ActionHandler
}

func NewAPIServer(repo *repository.Repository, s3Client *s3.Client, rmqConn *rmq.Connection) *APIServer {
	// Create command publisher with RPC support
	commandPublisher, err := NewCommandPublisher(rmqConn)
	if err != nil {
		// Log error but don't fail - API can still serve reads
		log.Printf("Warning: Failed to create command publisher: %v", err)
	} else {
		// Start reply consumer in background
		go func() {
			if err := commandPublisher.Start(context.Background()); err != nil {
				log.Printf("Warning: Command publisher reply consumer stopped: %v", err)
			}
		}()
	}

	return &APIServer{
		repo:                    repo,
		serverHandler:           NewServerHandler(repo.Servers),
		gameHandler:             NewGameHandler(repo.Games),
		gameConfigHandler:       NewGameConfigHandler(repo.GameConfigs),
		serverGameConfigHandler: NewServerGameConfigHandler(repo.ServerGameConfigs, repo.ServerPorts),
		sessionHandler:          NewSessionHandler(repo, commandPublisher),
		registrationHandler:     NewRegistrationHandler(repo.Servers, repo.ServerCapabilities),
		validationHandler:       NewValidationHandler(repo.Servers, repo.GameConfigs),
		logsHandler:             NewLogsHandler(repo.LogReferences, s3Client),
		backupHandler:           NewBackupHandler(repo.Backups, repo.Sessions, s3Client),
		strategyHandler:         NewConfigurationStrategyHandler(repo.ConfigurationStrategies),
		patchHandler:            NewConfigurationPatchHandler(repo.ConfigurationPatches),
		actionHandler:           NewActionHandler(repo.Actions.(*postgres.ActionRepository), repo.Sessions, repo.ServerGameConfigs, repo.GameConfigs, commandPublisher),
	}
}

// Server RPCs
func (s *APIServer) ListServers(ctx context.Context, req *pb.ListServersRequest) (*pb.ListServersResponse, error) {
	return s.serverHandler.ListServers(ctx, req)
}

func (s *APIServer) GetServer(ctx context.Context, req *pb.GetServerRequest) (*pb.GetServerResponse, error) {
	return s.serverHandler.GetServer(ctx, req)
}

func (s *APIServer) CreateServer(ctx context.Context, req *pb.CreateServerRequest) (*pb.CreateServerResponse, error) {
	return s.serverHandler.CreateServer(ctx, req)
}

func (s *APIServer) UpdateServer(ctx context.Context, req *pb.UpdateServerRequest) (*pb.UpdateServerResponse, error) {
	return s.serverHandler.UpdateServer(ctx, req)
}

func (s *APIServer) DeleteServer(ctx context.Context, req *pb.DeleteServerRequest) (*pb.DeleteServerResponse, error) {
	return s.serverHandler.DeleteServer(ctx, req)
}

// Game RPCs
func (s *APIServer) ListGames(ctx context.Context, req *pb.ListGamesRequest) (*pb.ListGamesResponse, error) {
	return s.gameHandler.ListGames(ctx, req)
}

func (s *APIServer) GetGame(ctx context.Context, req *pb.GetGameRequest) (*pb.GetGameResponse, error) {
	return s.gameHandler.GetGame(ctx, req)
}

func (s *APIServer) CreateGame(ctx context.Context, req *pb.CreateGameRequest) (*pb.CreateGameResponse, error) {
	return s.gameHandler.CreateGame(ctx, req)
}

func (s *APIServer) UpdateGame(ctx context.Context, req *pb.UpdateGameRequest) (*pb.UpdateGameResponse, error) {
	return s.gameHandler.UpdateGame(ctx, req)
}

func (s *APIServer) DeleteGame(ctx context.Context, req *pb.DeleteGameRequest) (*pb.DeleteGameResponse, error) {
	return s.gameHandler.DeleteGame(ctx, req)
}

// GameConfig RPCs
func (s *APIServer) ListGameConfigs(ctx context.Context, req *pb.ListGameConfigsRequest) (*pb.ListGameConfigsResponse, error) {
	return s.gameConfigHandler.ListGameConfigs(ctx, req)
}

func (s *APIServer) GetGameConfig(ctx context.Context, req *pb.GetGameConfigRequest) (*pb.GetGameConfigResponse, error) {
	return s.gameConfigHandler.GetGameConfig(ctx, req)
}

func (s *APIServer) CreateGameConfig(ctx context.Context, req *pb.CreateGameConfigRequest) (*pb.CreateGameConfigResponse, error) {
	return s.gameConfigHandler.CreateGameConfig(ctx, req)
}

func (s *APIServer) UpdateGameConfig(ctx context.Context, req *pb.UpdateGameConfigRequest) (*pb.UpdateGameConfigResponse, error) {
	return s.gameConfigHandler.UpdateGameConfig(ctx, req)
}

func (s *APIServer) DeleteGameConfig(ctx context.Context, req *pb.DeleteGameConfigRequest) (*pb.DeleteGameConfigResponse, error) {
	return s.gameConfigHandler.DeleteGameConfig(ctx, req)
}

// ServerGameConfig RPCs
func (s *APIServer) ListServerGameConfigs(ctx context.Context, req *pb.ListServerGameConfigsRequest) (*pb.ListServerGameConfigsResponse, error) {
	return s.serverGameConfigHandler.ListServerGameConfigs(ctx, req)
}

func (s *APIServer) GetServerGameConfig(ctx context.Context, req *pb.GetServerGameConfigRequest) (*pb.GetServerGameConfigResponse, error) {
	return s.serverGameConfigHandler.GetServerGameConfig(ctx, req)
}

func (s *APIServer) DeployGameConfig(ctx context.Context, req *pb.DeployGameConfigRequest) (*pb.DeployGameConfigResponse, error) {
	return s.serverGameConfigHandler.DeployGameConfig(ctx, req)
}

func (s *APIServer) UpdateServerGameConfig(ctx context.Context, req *pb.UpdateServerGameConfigRequest) (*pb.UpdateServerGameConfigResponse, error) {
	return s.serverGameConfigHandler.UpdateServerGameConfig(ctx, req)
}

func (s *APIServer) DeleteServerGameConfig(ctx context.Context, req *pb.DeleteServerGameConfigRequest) (*pb.DeleteServerGameConfigResponse, error) {
	return s.serverGameConfigHandler.DeleteServerGameConfig(ctx, req)
}

// Session RPCs
func (s *APIServer) ListSessions(ctx context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	return s.sessionHandler.ListSessions(ctx, req)
}

func (s *APIServer) GetSession(ctx context.Context, req *pb.GetSessionRequest) (*pb.GetSessionResponse, error) {
	return s.sessionHandler.GetSession(ctx, req)
}

func (s *APIServer) StartSession(ctx context.Context, req *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
	return s.sessionHandler.StartSession(ctx, req)
}

func (s *APIServer) StopSession(ctx context.Context, req *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
	return s.sessionHandler.StopSession(ctx, req)
}

func (s *APIServer) SendInput(ctx context.Context, req *pb.SendInputRequest) (*pb.SendInputResponse, error) {
	return s.sessionHandler.SendInput(ctx, req)
}

// GameConfigHandler handles GameConfig-related RPCs
type GameConfigHandler struct {
	repo repository.GameConfigRepository
}

func NewGameConfigHandler(repo repository.GameConfigRepository) *GameConfigHandler {
	return &GameConfigHandler{repo: repo}
}

func (h *GameConfigHandler) ListGameConfigs(ctx context.Context, req *pb.ListGameConfigsRequest) (*pb.ListGameConfigsResponse, error) {
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

	var gameID *int64
	if req.GameId > 0 {
		gameID = &req.GameId
	}

	configs, err := h.repo.List(ctx, gameID, pageSize+1, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list game configs: %v", err)
	}

	var nextPageToken string
	if len(configs) > pageSize {
		configs = configs[:pageSize]
		nextPageToken = encodePageToken(offset + pageSize)
	}

	pbConfigs := make([]*pb.GameConfig, len(configs))
	for i, c := range configs {
		pbConfigs[i] = gameConfigToProto(c)
	}

	return &pb.ListGameConfigsResponse{
		Configs:       pbConfigs,
		NextPageToken: nextPageToken,
	}, nil
}

func (h *GameConfigHandler) GetGameConfig(ctx context.Context, req *pb.GetGameConfigRequest) (*pb.GetGameConfigResponse, error) {
	config, err := h.repo.Get(ctx, req.ConfigId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "game config not found: %v", err)
	}

	return &pb.GetGameConfigResponse{
		Config: gameConfigToProto(config),
	}, nil
}

func (h *GameConfigHandler) CreateGameConfig(ctx context.Context, req *pb.CreateGameConfigRequest) (*pb.CreateGameConfigResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.Image == "" {
		return nil, status.Error(codes.InvalidArgument, "image is required")
	}

	config := &manman.GameConfig{
		GameID:       req.GameId,
		Name:         req.Name,
		Image:        req.Image,
		ArgsTemplate: stringPtr(req.ArgsTemplate),
		EnvTemplate:  mapToJSONB(req.EnvTemplate),
		Files:        filesToJSONB(req.Files),
		Parameters:   parametersToJSONB(req.Parameters),
		Entrypoint:   stringArrayToJSONB(req.Entrypoint),
		Command:      stringArrayToJSONB(req.Command),
	}

	config, err := h.repo.Create(ctx, config)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create game config: %v", err)
	}

	return &pb.CreateGameConfigResponse{
		Config: gameConfigToProto(config),
	}, nil
}

func (h *GameConfigHandler) UpdateGameConfig(ctx context.Context, req *pb.UpdateGameConfigRequest) (*pb.UpdateGameConfigResponse, error) {
	config, err := h.repo.Get(ctx, req.ConfigId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "game config not found: %v", err)
	}

	// Apply field paths
	if len(req.UpdatePaths) == 0 {
		// Update all provided fields
		if req.Name != "" {
			config.Name = req.Name
		}
		if req.Image != "" {
			config.Image = req.Image
		}
		if req.ArgsTemplate != "" {
			config.ArgsTemplate = stringPtr(req.ArgsTemplate)
		}
		if req.EnvTemplate != nil {
			config.EnvTemplate = mapToJSONB(req.EnvTemplate)
		}
		if req.Files != nil {
			config.Files = filesToJSONB(req.Files)
		}
		if req.Parameters != nil {
			config.Parameters = parametersToJSONB(req.Parameters)
		}
		if req.Entrypoint != nil {
			config.Entrypoint = stringArrayToJSONB(req.Entrypoint)
		}
		if req.Command != nil {
			config.Command = stringArrayToJSONB(req.Command)
		}
	} else {
		// Update only specified fields
		for _, path := range req.UpdatePaths {
			switch path {
			case "name":
				config.Name = req.Name
			case "image":
				config.Image = req.Image
			case "args_template":
				config.ArgsTemplate = stringPtr(req.ArgsTemplate)
			case "env_template":
				config.EnvTemplate = mapToJSONB(req.EnvTemplate)
			case "files":
				config.Files = filesToJSONB(req.Files)
			case "parameters":
				config.Parameters = parametersToJSONB(req.Parameters)
			case "entrypoint":
				config.Entrypoint = stringArrayToJSONB(req.Entrypoint)
			case "command":
				config.Command = stringArrayToJSONB(req.Command)
			}
		}
	}

	if err := h.repo.Update(ctx, config); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update game config: %v", err)
	}

	return &pb.UpdateGameConfigResponse{
		Config: gameConfigToProto(config),
	}, nil
}

func (h *GameConfigHandler) DeleteGameConfig(ctx context.Context, req *pb.DeleteGameConfigRequest) (*pb.DeleteGameConfigResponse, error) {
	if err := h.repo.Delete(ctx, req.ConfigId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete game config: %v", err)
	}

	return &pb.DeleteGameConfigResponse{}, nil
}

func gameConfigToProto(c *manman.GameConfig) *pb.GameConfig {
	pbConfig := &pb.GameConfig{
		ConfigId:    c.ConfigID,
		GameId:      c.GameID,
		Name:        c.Name,
		Image:       c.Image,
		EnvTemplate: jsonbToMap(c.EnvTemplate),
		Files:       jsonbToFiles(c.Files),
		Parameters:  jsonbToParameters(c.Parameters),
		Entrypoint:  jsonbToStringArray(c.Entrypoint),
		Command:     jsonbToStringArray(c.Command),
	}

	if c.ArgsTemplate != nil {
		pbConfig.ArgsTemplate = *c.ArgsTemplate
	}

	return pbConfig
}

// ServerGameConfigHandler handles ServerGameConfig-related RPCs
type ServerGameConfigHandler struct {
	repo     repository.ServerGameConfigRepository
	portRepo repository.ServerPortRepository
}

func NewServerGameConfigHandler(repo repository.ServerGameConfigRepository, portRepo repository.ServerPortRepository) *ServerGameConfigHandler {
	return &ServerGameConfigHandler{
		repo:     repo,
		portRepo: portRepo,
	}
}

func (h *ServerGameConfigHandler) ListServerGameConfigs(ctx context.Context, req *pb.ListServerGameConfigsRequest) (*pb.ListServerGameConfigsResponse, error) {
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

	var serverID *int64
	if req.ServerId > 0 {
		serverID = &req.ServerId
	}

	configs, err := h.repo.List(ctx, serverID, pageSize+1, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list server game configs: %v", err)
	}

	var nextPageToken string
	if len(configs) > pageSize {
		configs = configs[:pageSize]
		nextPageToken = encodePageToken(offset + pageSize)
	}

	pbConfigs := make([]*pb.ServerGameConfig, len(configs))
	for i, c := range configs {
		pbConfigs[i] = serverGameConfigToProto(c)
	}

	return &pb.ListServerGameConfigsResponse{
		Configs:       pbConfigs,
		NextPageToken: nextPageToken,
	}, nil
}

func (h *ServerGameConfigHandler) GetServerGameConfig(ctx context.Context, req *pb.GetServerGameConfigRequest) (*pb.GetServerGameConfigResponse, error) {
	config, err := h.repo.Get(ctx, req.ServerGameConfigId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "server game config not found: %v", err)
	}

	return &pb.GetServerGameConfigResponse{
		Config: serverGameConfigToProto(config),
	}, nil
}

func (h *ServerGameConfigHandler) DeployGameConfig(ctx context.Context, req *pb.DeployGameConfigRequest) (*pb.DeployGameConfigResponse, error) {
	// Convert protobuf port bindings to manman models
	portBindings := make([]*manman.PortBinding, len(req.PortBindings))
	for i, pb := range req.PortBindings {
		portBindings[i] = &manman.PortBinding{
			ContainerPort: pb.ContainerPort,
			HostPort:      pb.HostPort,
			Protocol:      pb.Protocol,
		}
	}

	// Check port availability before creating the ServerGameConfig
	for _, binding := range portBindings {
		available, err := h.portRepo.IsPortAvailable(ctx, req.ServerId, int(binding.HostPort), binding.Protocol)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to check port availability: %v", err)
		}
		if !available {
			return nil, status.Errorf(codes.ResourceExhausted, "port %d/%s is already allocated on server %d", binding.HostPort, binding.Protocol, req.ServerId)
		}
	}

	// Create the ServerGameConfig
	sgc := &manman.ServerGameConfig{
		ServerID:     req.ServerId,
		GameConfigID: req.GameConfigId,
		Status:       manman.SGCStatusInactive,
		PortBindings: portBindingsToJSONB(req.PortBindings),
		Parameters:   mapToJSONB(req.Parameters),
	}

	sgc, err := h.repo.Create(ctx, sgc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to deploy game config: %v", err)
	}

	// Allocate ports for the ServerGameConfig
	if err := h.portRepo.AllocateMultiplePorts(ctx, req.ServerId, portBindings, sgc.SGCID); err != nil {
		// Rollback: delete the created ServerGameConfig
		h.repo.Delete(ctx, sgc.SGCID)
		return nil, status.Errorf(codes.ResourceExhausted, "failed to allocate ports: %v", err)
	}

	return &pb.DeployGameConfigResponse{
		Config: serverGameConfigToProto(sgc),
	}, nil
}

func (h *ServerGameConfigHandler) UpdateServerGameConfig(ctx context.Context, req *pb.UpdateServerGameConfigRequest) (*pb.UpdateServerGameConfigResponse, error) {
	sgc, err := h.repo.Get(ctx, req.ServerGameConfigId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "server game config not found: %v", err)
	}

	// Apply field paths
	if len(req.UpdatePaths) == 0 {
		// Update all provided fields
		if req.PortBindings != nil {
			sgc.PortBindings = portBindingsToJSONB(req.PortBindings)
		}
		if req.Parameters != nil {
			sgc.Parameters = mapToJSONB(req.Parameters)
		}
		if req.Status != "" {
			sgc.Status = req.Status
		}
	} else {
		// Update only specified fields
		for _, path := range req.UpdatePaths {
			switch path {
			case "port_bindings":
				sgc.PortBindings = portBindingsToJSONB(req.PortBindings)
			case "parameters":
				sgc.Parameters = mapToJSONB(req.Parameters)
			case "status":
				sgc.Status = req.Status
			}
		}
	}

	if err := h.repo.Update(ctx, sgc); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update server game config: %v", err)
	}

	return &pb.UpdateServerGameConfigResponse{
		Config: serverGameConfigToProto(sgc),
	}, nil
}

func (h *ServerGameConfigHandler) DeleteServerGameConfig(ctx context.Context, req *pb.DeleteServerGameConfigRequest) (*pb.DeleteServerGameConfigResponse, error) {
	// Port deallocation is handled automatically via database CASCADE when sessions are deleted

	// Delete the ServerGameConfig
	if err := h.repo.Delete(ctx, req.ServerGameConfigId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete server game config: %v", err)
	}

	return &pb.DeleteServerGameConfigResponse{}, nil
}

func serverGameConfigToProto(sgc *manman.ServerGameConfig) *pb.ServerGameConfig {
	return &pb.ServerGameConfig{
		ServerGameConfigId: sgc.SGCID,
		ServerId:           sgc.ServerID,
		GameConfigId:       sgc.GameConfigID,
		PortBindings:       jsonbToPortBindings(sgc.PortBindings),
		Parameters:         jsonbToMap(sgc.Parameters),
		Status:             sgc.Status,
	}
}

// SessionHandler handles Session-related RPCs
type SessionHandler struct {
	repo        *repository.Repository
	sessionRepo repository.SessionRepository
	sgcRepo     repository.ServerGameConfigRepository
	gcRepo      repository.GameConfigRepository
	publisher   *CommandPublisher
}

func NewSessionHandler(repo *repository.Repository, publisher *CommandPublisher) *SessionHandler {
	return &SessionHandler{
		repo:        repo,
		sessionRepo: repo.Sessions,
		sgcRepo:     repo.ServerGameConfigs,
		gcRepo:      repo.GameConfigs,
		publisher:   publisher,
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
		SGCID:      req.ServerGameConfigId,
		Status:     manman.SessionStatusPending,
		Parameters: mapToJSONB(req.Parameters),
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

	// Fetch Configuration Strategies for the game to get volume mounts
	strategies, err := h.repo.ConfigurationStrategies.ListByGame(ctx, gc.GameID)
	if err != nil {
		log.Printf("Warning: Failed to fetch configuration strategies for game %d: %v", gc.GameID, err)
		// Continue anyway, volumes might not be defined as strategies yet
	}

	// Publish start session command to RabbitMQ
	if h.publisher != nil {
		cmd := buildStartSessionCommand(session, sgc, gc, req.Parameters, internalForce, strategies)
		// Increased timeout to allow for image pulling
		if err := h.publisher.PublishStartSession(ctx, sgc.ServerID, cmd, 2*time.Minute); err != nil {
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
func buildStartSessionCommand(session *manman.Session, sgc *manman.ServerGameConfig, gc *manman.GameConfig, sessionParams map[string]string, force bool, strategies []*manman.ConfigurationStrategy) map[string]interface{} {
	// Build game config message
	gameConfig := map[string]interface{}{
		"config_id":       gc.ConfigID,
		"image":           gc.Image,
		"args_template":   gc.ArgsTemplate,
		"env_template":    jsonbToMap(gc.EnvTemplate),
		"files":           convertFilesToMessage(gc.Files),
		"parameters":      convertParametersToMessage(gc.Parameters),
		"entrypoint":      jsonbToStringArray(gc.Entrypoint),
		"command":         jsonbToStringArray(gc.Command),
	}

	// Add volume mounts from strategies
	var volumes []map[string]interface{}
	for _, s := range strategies {
		if s.StrategyType == manman.StrategyTypeVolume {
			vol := map[string]interface{}{
				"name":           s.Name,
				"container_path": s.TargetPath,
			}
			if s.BaseTemplate != nil {
				vol["host_subpath"] = *s.BaseTemplate
			}
			if s.RenderOptions != nil {
				vol["options"] = s.RenderOptions
			}
			volumes = append(volumes, vol)
		}
	}
	gameConfig["volumes"] = volumes

	// Build server game config message
	serverGameConfig := map[string]interface{}{
		"sgc_id":        sgc.SGCID,
		"port_bindings": convertPortBindingsToMessage(sgc.PortBindings),
		"parameters":    jsonbToMap(sgc.Parameters),
	}

	// Merge session-level parameters
	if sessionParams == nil {
		sessionParams = make(map[string]string)
	}

	return map[string]interface{}{
		"session_id":         session.SessionID,
		"sgc_id":             sgc.SGCID,
		"game_config":        gameConfig,
		"server_game_config": serverGameConfig,
		"parameters":         sessionParams,
		"force":              force,
	}
}

// Helper functions to convert JSONB to message format
func convertFilesToMessage(filesJSON manman.JSONB) []interface{} {
	// Files are stored as array of objects in JSONB
	if filesJSON == nil {
		return []interface{}{}
	}
	// Return as-is since it's already in the right format
	if files, ok := filesJSON["files"].([]interface{}); ok {
		return files
	}
	return []interface{}{}
}

func convertParametersToMessage(paramsJSON manman.JSONB) []interface{} {
	// Parameters are stored as array of objects in JSONB
	if paramsJSON == nil {
		return []interface{}{}
	}
	// Return as-is since it's already in the right format
	if params, ok := paramsJSON["parameters"].([]interface{}); ok {
		return params
	}
	return []interface{}{}
}

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
		Parameters:         jsonbToMap(s.Parameters),
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

// Registration RPCs
func (s *APIServer) RegisterServer(ctx context.Context, req *pb.RegisterServerRequest) (*pb.RegisterServerResponse, error) {
	return s.registrationHandler.RegisterServer(ctx, req)
}

func (s *APIServer) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	return s.registrationHandler.Heartbeat(ctx, req)
}

// Log Management RPCs
func (s *APIServer) SendBatchedLogs(ctx context.Context, req *pb.SendBatchedLogsRequest) (*pb.SendBatchedLogsResponse, error) {
	return s.logsHandler.SendBatchedLogs(ctx, req)
}

func (s *APIServer) GetHistoricalLogs(ctx context.Context, req *pb.GetHistoricalLogsRequest) (*pb.GetHistoricalLogsResponse, error) {
	return s.logsHandler.GetHistoricalLogs(ctx, req)
}

// Validation RPCs
func (s *APIServer) ValidateDeployment(ctx context.Context, req *pb.ValidateDeploymentRequest) (*pb.ValidateDeploymentResponse, error) {
	return s.validationHandler.ValidateDeployment(ctx, req)
}

// Backup RPCs
func (s *APIServer) CreateBackup(ctx context.Context, req *pb.CreateBackupRequest) (*pb.CreateBackupResponse, error) {
	return s.backupHandler.CreateBackup(ctx, req)
}

func (s *APIServer) ListBackups(ctx context.Context, req *pb.ListBackupsRequest) (*pb.ListBackupsResponse, error) {
	return s.backupHandler.ListBackups(ctx, req)
}

func (s *APIServer) GetBackup(ctx context.Context, req *pb.GetBackupRequest) (*pb.GetBackupResponse, error) {
	return s.backupHandler.GetBackup(ctx, req)
}

// ConfigurationStrategyHandler handles ConfigurationStrategy-related RPCs
type ConfigurationStrategyHandler struct {
	repo repository.ConfigurationStrategyRepository
}

func NewConfigurationStrategyHandler(repo repository.ConfigurationStrategyRepository) *ConfigurationStrategyHandler {
	return &ConfigurationStrategyHandler{repo: repo}
}

func (h *ConfigurationStrategyHandler) CreateConfigurationStrategy(ctx context.Context, req *pb.CreateConfigurationStrategyRequest) (*pb.CreateConfigurationStrategyResponse, error) {
	strategy := &manman.ConfigurationStrategy{
		GameID:        req.GameId,
		Name:          req.Name,
		Description:   stringPtr(req.Description),
		StrategyType:  req.StrategyType,
		TargetPath:    stringPtr(req.TargetPath),
		BaseTemplate:  stringPtr(req.BaseTemplate),
		RenderOptions: mapToJSONB(req.RenderOptions),
		ApplyOrder:    int(req.ApplyOrder),
	}

	strategy, err := h.repo.Create(ctx, strategy)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create configuration strategy: %v", err)
	}

	return &pb.CreateConfigurationStrategyResponse{
		Strategy: strategyToProto(strategy),
	}, nil
}

func (h *ConfigurationStrategyHandler) ListConfigurationStrategies(ctx context.Context, req *pb.ListConfigurationStrategiesRequest) (*pb.ListConfigurationStrategiesResponse, error) {
	strategies, err := h.repo.ListByGame(ctx, req.GameId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list configuration strategies: %v", err)
	}

	pbStrategies := make([]*pb.ConfigurationStrategy, len(strategies))
	for i, s := range strategies {
		pbStrategies[i] = strategyToProto(s)
	}

	return &pb.ListConfigurationStrategiesResponse{
		Strategies: pbStrategies,
	}, nil
}

func (h *ConfigurationStrategyHandler) UpdateConfigurationStrategy(ctx context.Context, req *pb.UpdateConfigurationStrategyRequest) (*pb.UpdateConfigurationStrategyResponse, error) {
	strategy, err := h.repo.Get(ctx, req.StrategyId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "configuration strategy not found: %v", err)
	}

	// Apply field paths
	if len(req.UpdatePaths) == 0 {
		if req.Name != "" {
			strategy.Name = req.Name
		}
		if req.Description != "" {
			strategy.Description = stringPtr(req.Description)
		}
		if req.StrategyType != "" {
			strategy.StrategyType = req.StrategyType
		}
		if req.TargetPath != "" {
			strategy.TargetPath = stringPtr(req.TargetPath)
		}
		if req.BaseTemplate != "" {
			strategy.BaseTemplate = stringPtr(req.BaseTemplate)
		}
		if req.RenderOptions != nil {
			strategy.RenderOptions = mapToJSONB(req.RenderOptions)
		}
		if req.ApplyOrder != 0 {
			strategy.ApplyOrder = int(req.ApplyOrder)
		}
	} else {
		for _, path := range req.UpdatePaths {
			switch path {
			case "name":
				strategy.Name = req.Name
			case "description":
				strategy.Description = stringPtr(req.Description)
			case "strategy_type":
				strategy.StrategyType = req.StrategyType
			case "target_path":
				strategy.TargetPath = stringPtr(req.TargetPath)
			case "base_template":
				strategy.BaseTemplate = stringPtr(req.BaseTemplate)
			case "render_options":
				strategy.RenderOptions = mapToJSONB(req.RenderOptions)
			case "apply_order":
				strategy.ApplyOrder = int(req.ApplyOrder)
			}
		}
	}

	if err := h.repo.Update(ctx, strategy); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update configuration strategy: %v", err)
	}

	return &pb.UpdateConfigurationStrategyResponse{
		Strategy: strategyToProto(strategy),
	}, nil
}

func (h *ConfigurationStrategyHandler) DeleteConfigurationStrategy(ctx context.Context, req *pb.DeleteConfigurationStrategyRequest) (*pb.DeleteConfigurationStrategyResponse, error) {
	if err := h.repo.Delete(ctx, req.StrategyId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete configuration strategy: %v", err)
	}

	return &pb.DeleteConfigurationStrategyResponse{}, nil
}

func (h *ConfigurationStrategyHandler) GetSessionConfiguration(ctx context.Context, req *pb.GetSessionConfigurationRequest, fullRepo *repository.Repository) (*pb.GetSessionConfigurationResponse, error) {
	// Get session to find game/config IDs
	session, err := fullRepo.Sessions.Get(ctx, req.SessionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
	}

	// Get SGC to find game_config_id and server_id
	sgc, err := fullRepo.ServerGameConfigs.Get(ctx, session.SGCID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "server game config not found: %v", err)
	}

	// Get game config to find game_id
	gc, err := fullRepo.GameConfigs.Get(ctx, sgc.GameConfigID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "game config not found: %v", err)
	}

	// Fetch all strategies for this game
	strategies, err := h.repo.ListByGame(ctx, gc.GameID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fetch strategies: %v", err)
	}

	// Render each strategy
	var renderedConfigs []*pb.RenderedConfiguration
	for _, strategy := range strategies {
		// Skip volume strategies - host-manager handles those separately
		if strategy.StrategyType == manman.StrategyTypeVolume {
			continue
		}

		rendered := &pb.RenderedConfiguration{
			StrategyName:    strategy.Name,
			StrategyType:    strategy.StrategyType,
			RenderedContent: "",
			BaseContent:     "",
		}

		if strategy.TargetPath != nil {
			rendered.TargetPath = *strategy.TargetPath
		}

		// Set base content (may be empty for merge mode)
		if strategy.BaseTemplate != nil {
			rendered.BaseContent = *strategy.BaseTemplate
		}

		// Cascade patches: GameConfig → ServerGameConfig
		patchContent := ""

		// 1. Get game_config level patch
		gcPatch, err := fullRepo.ConfigurationPatches.GetByStrategyAndEntity(ctx, strategy.StrategyID, "game_config", gc.ConfigID)
		if err == nil && gcPatch.PatchContent != nil {
			patchContent = *gcPatch.PatchContent
		}

		// 2. Get server_game_config level patch (overrides game_config)
		sgcPatch, err := fullRepo.ConfigurationPatches.GetByStrategyAndEntity(ctx, strategy.StrategyID, "server_game_config", sgc.SGCID)
		if err == nil && sgcPatch.PatchContent != nil {
			// For properties files, we need to merge the patches
			// For now, SGC patch completely overrides GC patch
			// TODO: Implement smarter merging for properties files
			if patchContent != "" {
				patchContent = patchContent + "\n" + *sgcPatch.PatchContent
			} else {
				patchContent = *sgcPatch.PatchContent
			}
		}

		// Set rendered content to the cascaded patches
		// Host-manager will merge this with existing file if base is empty (merge mode)
		rendered.RenderedContent = patchContent

		renderedConfigs = append(renderedConfigs, rendered)
	}

	return &pb.GetSessionConfigurationResponse{
		Configurations:     renderedConfigs,
		GameId:             gc.GameID,
		GameConfigId:       gc.ConfigID,
		ServerGameConfigId: sgc.SGCID,
	}, nil
}

func (h *ConfigurationStrategyHandler) PreviewConfiguration(ctx context.Context, req *pb.PreviewConfigurationRequest, fullRepo *repository.Repository) (*pb.PreviewConfigurationResponse, error) {
	// Get session configuration (same as GetSessionConfiguration for now)
	sessionResp, err := h.GetSessionConfiguration(ctx, &pb.GetSessionConfigurationRequest{
		SessionId: req.SessionId,
	}, fullRepo)
	if err != nil {
		return nil, err
	}

	// TODO: Apply parameter overrides from req.ParameterOverrides

	return &pb.PreviewConfigurationResponse{
		Configurations: sessionResp.Configurations,
	}, nil
}

func strategyToProto(s *manman.ConfigurationStrategy) *pb.ConfigurationStrategy {
	proto := &pb.ConfigurationStrategy{
		StrategyId:    s.StrategyID,
		GameId:        s.GameID,
		Name:          s.Name,
		StrategyType:  s.StrategyType,
		RenderOptions: jsonbToMap(s.RenderOptions),
		ApplyOrder:    int32(s.ApplyOrder),
	}

	if s.Description != nil {
		proto.Description = *s.Description
	}
	if s.TargetPath != nil {
		proto.TargetPath = *s.TargetPath
	}
	if s.BaseTemplate != nil {
		proto.BaseTemplate = *s.BaseTemplate
	}

	return proto
}

func patchToProto(p *manman.ConfigurationPatch) *pb.ConfigurationPatch {
	proto := &pb.ConfigurationPatch{
		PatchId:     p.PatchID,
		StrategyId:  p.StrategyID,
		PatchLevel:  p.PatchLevel,
		EntityId:    p.EntityID,
		PatchFormat: p.PatchFormat,
	}

	if p.PatchContent != nil {
		proto.PatchContent = *p.PatchContent
	}

	return proto
}

// Configuration Strategy RPCs
func (s *APIServer) CreateConfigurationStrategy(ctx context.Context, req *pb.CreateConfigurationStrategyRequest) (*pb.CreateConfigurationStrategyResponse, error) {
	return s.strategyHandler.CreateConfigurationStrategy(ctx, req)
}

func (s *APIServer) ListConfigurationStrategies(ctx context.Context, req *pb.ListConfigurationStrategiesRequest) (*pb.ListConfigurationStrategiesResponse, error) {
	return s.strategyHandler.ListConfigurationStrategies(ctx, req)
}

func (s *APIServer) UpdateConfigurationStrategy(ctx context.Context, req *pb.UpdateConfigurationStrategyRequest) (*pb.UpdateConfigurationStrategyResponse, error) {
	return s.strategyHandler.UpdateConfigurationStrategy(ctx, req)
}

func (s *APIServer) DeleteConfigurationStrategy(ctx context.Context, req *pb.DeleteConfigurationStrategyRequest) (*pb.DeleteConfigurationStrategyResponse, error) {
	return s.strategyHandler.DeleteConfigurationStrategy(ctx, req)
}

func (s *APIServer) GetSessionConfiguration(ctx context.Context, req *pb.GetSessionConfigurationRequest) (*pb.GetSessionConfigurationResponse, error) {
	return s.strategyHandler.GetSessionConfiguration(ctx, req, s.repo)
}

func (s *APIServer) PreviewConfiguration(ctx context.Context, req *pb.PreviewConfigurationRequest) (*pb.PreviewConfigurationResponse, error) {
	return s.strategyHandler.PreviewConfiguration(ctx, req, s.repo)
}

// ConfigurationPatchHandler handles ConfigurationPatch-related RPCs
type ConfigurationPatchHandler struct {
	repo repository.ConfigurationPatchRepository
}

func NewConfigurationPatchHandler(repo repository.ConfigurationPatchRepository) *ConfigurationPatchHandler {
	return &ConfigurationPatchHandler{repo: repo}
}

func (h *ConfigurationPatchHandler) CreateConfigurationPatch(ctx context.Context, req *pb.CreateConfigurationPatchRequest) (*pb.CreateConfigurationPatchResponse, error) {
	patch := &manman.ConfigurationPatch{
		StrategyID:   req.StrategyId,
		PatchLevel:   req.PatchLevel,
		EntityID:     req.EntityId,
		PatchContent: stringPtr(req.PatchContent),
		PatchFormat:  req.PatchFormat,
	}

	patch, err := h.repo.Create(ctx, patch)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create configuration patch: %v", err)
	}

	return &pb.CreateConfigurationPatchResponse{
		Patch: patchToProto(patch),
	}, nil
}

func (h *ConfigurationPatchHandler) UpdateConfigurationPatch(ctx context.Context, req *pb.UpdateConfigurationPatchRequest) (*pb.UpdateConfigurationPatchResponse, error) {
	patch, err := h.repo.Get(ctx, req.PatchId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "configuration patch not found: %v", err)
	}

	patch.PatchContent = stringPtr(req.PatchContent)
	patch.PatchFormat = req.PatchFormat

	if err := h.repo.Update(ctx, patch); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to update configuration patch: %v", err)
	}

	return &pb.UpdateConfigurationPatchResponse{
		Patch: patchToProto(patch),
	}, nil
}

func (h *ConfigurationPatchHandler) DeleteConfigurationPatch(ctx context.Context, req *pb.DeleteConfigurationPatchRequest) (*pb.DeleteConfigurationPatchResponse, error) {
	if err := h.repo.Delete(ctx, req.PatchId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete configuration patch: %v", err)
	}

	return &pb.DeleteConfigurationPatchResponse{}, nil
}

func (h *ConfigurationPatchHandler) ListConfigurationPatches(ctx context.Context, req *pb.ListConfigurationPatchesRequest) (*pb.ListConfigurationPatchesResponse, error) {
	var strategyID *int64
	var patchLevel *string
	var entityID *int64

	if req.StrategyId != nil {
		strategyID = req.StrategyId
	}
	if req.PatchLevel != nil {
		patchLevel = req.PatchLevel
	}
	if req.EntityId != nil {
		entityID = req.EntityId
	}

	patches, err := h.repo.List(ctx, strategyID, patchLevel, entityID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list configuration patches: %v", err)
	}

	pbPatches := make([]*pb.ConfigurationPatch, len(patches))
	for i, p := range patches {
		pbPatches[i] = patchToProto(p)
	}

	return &pb.ListConfigurationPatchesResponse{
		Patches: pbPatches,
	}, nil
}

// APIServer wrapper methods for ConfigurationPatch
func (s *APIServer) CreateConfigurationPatch(ctx context.Context, req *pb.CreateConfigurationPatchRequest) (*pb.CreateConfigurationPatchResponse, error) {
	return s.patchHandler.CreateConfigurationPatch(ctx, req)
}

func (s *APIServer) UpdateConfigurationPatch(ctx context.Context, req *pb.UpdateConfigurationPatchRequest) (*pb.UpdateConfigurationPatchResponse, error) {
	return s.patchHandler.UpdateConfigurationPatch(ctx, req)
}

func (s *APIServer) DeleteConfigurationPatch(ctx context.Context, req *pb.DeleteConfigurationPatchRequest) (*pb.DeleteConfigurationPatchResponse, error) {
	return s.patchHandler.DeleteConfigurationPatch(ctx, req)
}

func (s *APIServer) ListConfigurationPatches(ctx context.Context, req *pb.ListConfigurationPatchesRequest) (*pb.ListConfigurationPatchesResponse, error) {
	return s.patchHandler.ListConfigurationPatches(ctx, req)
}

// Game Actions RPCs
func (s *APIServer) GetSessionActions(ctx context.Context, req *pb.GetSessionActionsRequest) (*pb.GetSessionActionsResponse, error) {
	return s.actionHandler.GetSessionActions(ctx, req)
}

func (s *APIServer) ExecuteAction(ctx context.Context, req *pb.ExecuteActionRequest) (*pb.ExecuteActionResponse, error) {
	return s.actionHandler.ExecuteAction(ctx, req)
}
