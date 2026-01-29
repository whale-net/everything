package handlers

import (
	"context"

	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/api/repository"
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
}

func NewAPIServer(repo *repository.Repository) *APIServer {
	return &APIServer{
		repo:                    repo,
		serverHandler:           NewServerHandler(repo.Servers),
		gameHandler:             NewGameHandler(repo.Games),
		gameConfigHandler:       NewGameConfigHandler(repo.GameConfigs),
		serverGameConfigHandler: NewServerGameConfigHandler(repo.ServerGameConfigs),
		sessionHandler:          NewSessionHandler(repo.Sessions),
		registrationHandler:     NewRegistrationHandler(repo.Servers),
		validationHandler:       NewValidationHandler(repo.Servers, repo.GameConfigs),
		logsHandler:             NewLogsHandler(),
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
	}

	if c.ArgsTemplate != nil {
		pbConfig.ArgsTemplate = *c.ArgsTemplate
	}

	return pbConfig
}

// ServerGameConfigHandler handles ServerGameConfig-related RPCs
type ServerGameConfigHandler struct {
	repo repository.ServerGameConfigRepository
}

func NewServerGameConfigHandler(repo repository.ServerGameConfigRepository) *ServerGameConfigHandler {
	return &ServerGameConfigHandler{repo: repo}
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
	repo repository.SessionRepository
}

func NewSessionHandler(repo repository.SessionRepository) *SessionHandler {
	return &SessionHandler{repo: repo}
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

	// TODO: Use enhanced filters (status_filter, started_after, started_before, server_id, live_only)
	// This will require updating the repository layer to support these filters

	sessions, err := h.repo.List(ctx, sgcID, pageSize+1, offset)
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
	session, err := h.repo.Get(ctx, req.SessionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
	}

	return &pb.GetSessionResponse{
		Session: sessionToProto(session),
	}, nil
}

func (h *SessionHandler) StartSession(ctx context.Context, req *pb.StartSessionRequest) (*pb.StartSessionResponse, error) {
	session := &manman.Session{
		SGCID:      req.ServerGameConfigId,
		Status:     manman.SessionStatusPending,
		Parameters: mapToJSONB(req.Parameters),
	}

	session, err := h.repo.Create(ctx, session)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to start session: %v", err)
	}

	// TODO: Publish command to RabbitMQ for host manager to pick up

	return &pb.StartSessionResponse{
		Session: sessionToProto(session),
	}, nil
}

func (h *SessionHandler) StopSession(ctx context.Context, req *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
	session, err := h.repo.Get(ctx, req.SessionId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
	}

	// TODO: Publish stop command to RabbitMQ

	session.Status = manman.SessionStatusStopping
	if err := h.repo.Update(ctx, session); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to stop session: %v", err)
	}

	return &pb.StopSessionResponse{
		Session: sessionToProto(session),
	}, nil
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

// Validation RPCs
func (s *APIServer) ValidateDeployment(ctx context.Context, req *pb.ValidateDeploymentRequest) (*pb.ValidateDeploymentResponse, error) {
	return s.validationHandler.ValidateDeployment(ctx, req)
}
