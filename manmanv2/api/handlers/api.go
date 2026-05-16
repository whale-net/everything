package handlers

import (
	"context"
	"log"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	"github.com/whale-net/everything/manmanv2/api/workshop"
	pb "github.com/whale-net/everything/manmanv2/protos"
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
	backupConfigHandler     *BackupConfigHandler
	strategyHandler         *ConfigurationStrategyHandler
	patchHandler            *ConfigurationPatchHandler
	volumeHandler           *GameConfigVolumeHandler
	actionHandler           *ActionHandler
	restartScheduleHandler  *RestartScheduleHandler
}

func NewAPIServer(repo *repository.Repository, s3Client *s3.Client, rmqConn *rmq.Connection, workshopManager workshop.WorkshopManagerInterface) *APIServer {
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
		sessionHandler:          NewSessionHandler(repo, commandPublisher, workshopManager),
		registrationHandler:     NewRegistrationHandler(repo.Servers, repo.ServerCapabilities),
		validationHandler:       NewValidationHandler(repo.Servers, repo.GameConfigs),
		logsHandler:             NewLogsHandler(repo.LogReferences, s3Client),
		backupHandler:           NewBackupHandler(repo.Backups, repo.Sessions, s3Client),
		backupConfigHandler:     NewBackupConfigHandler(repo.BackupConfigs, repo.Backups, repo.ServerGameConfigs, repo.Sessions, repo.GameConfigVolumes, repo.Servers, repo.Actions.(*postgres.ActionRepository), commandPublisher, s3Client),
		strategyHandler:         NewConfigurationStrategyHandler(repo.ConfigurationStrategies),
		patchHandler:            NewConfigurationPatchHandler(repo.ConfigurationPatches),
		volumeHandler:           NewGameConfigVolumeHandler(repo.GameConfigVolumes),
		actionHandler:           NewActionHandler(repo.Actions.(*postgres.ActionRepository), repo.Sessions, repo.ServerGameConfigs, repo.GameConfigs, commandPublisher),
		restartScheduleHandler:  NewRestartScheduleHandler(repo.RestartSchedules),
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

func (s *APIServer) GetLogHistogram(ctx context.Context, req *pb.GetLogHistogramRequest) (*pb.GetLogHistogramResponse, error) {
	return s.logsHandler.GetLogHistogram(ctx, req)
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

func (s *APIServer) DeleteBackup(ctx context.Context, req *pb.DeleteBackupRequest) (*pb.DeleteBackupResponse, error) {
	return s.backupHandler.DeleteBackup(ctx, req)
}

func (s *APIServer) TriggerBackup(ctx context.Context, req *pb.TriggerBackupRequest) (*pb.TriggerBackupResponse, error) {
	return s.backupConfigHandler.TriggerBackup(ctx, req)
}

// BackupConfig RPCs
func (s *APIServer) CreateBackupConfig(ctx context.Context, req *pb.CreateBackupConfigRequest) (*pb.CreateBackupConfigResponse, error) {
	return s.backupConfigHandler.CreateBackupConfig(ctx, req)
}

func (s *APIServer) GetBackupConfig(ctx context.Context, req *pb.GetBackupConfigRequest) (*pb.GetBackupConfigResponse, error) {
	return s.backupConfigHandler.GetBackupConfig(ctx, req)
}

func (s *APIServer) ListBackupConfigs(ctx context.Context, req *pb.ListBackupConfigsRequest) (*pb.ListBackupConfigsResponse, error) {
	return s.backupConfigHandler.ListBackupConfigs(ctx, req)
}

func (s *APIServer) UpdateBackupConfig(ctx context.Context, req *pb.UpdateBackupConfigRequest) (*pb.UpdateBackupConfigResponse, error) {
	return s.backupConfigHandler.UpdateBackupConfig(ctx, req)
}

func (s *APIServer) DeleteBackupConfig(ctx context.Context, req *pb.DeleteBackupConfigRequest) (*pb.DeleteBackupConfigResponse, error) {
	return s.backupConfigHandler.DeleteBackupConfig(ctx, req)
}

func (s *APIServer) AddBackupConfigAction(ctx context.Context, req *pb.AddBackupConfigActionRequest) (*pb.AddBackupConfigActionResponse, error) {
	return s.backupConfigHandler.AddBackupConfigAction(ctx, req)
}

func (s *APIServer) RemoveBackupConfigAction(ctx context.Context, req *pb.RemoveBackupConfigActionRequest) (*pb.RemoveBackupConfigActionResponse, error) {
	return s.backupConfigHandler.RemoveBackupConfigAction(ctx, req)
}

func (s *APIServer) ListBackupConfigActions(ctx context.Context, req *pb.ListBackupConfigActionsRequest) (*pb.ListBackupConfigActionsResponse, error) {
	return s.backupConfigHandler.ListBackupConfigActions(ctx, req)
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

// ConfigurationPatch RPCs
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

// GameConfigVolume RPCs
func (s *APIServer) CreateGameConfigVolume(ctx context.Context, req *pb.CreateGameConfigVolumeRequest) (*pb.CreateGameConfigVolumeResponse, error) {
	return s.volumeHandler.CreateGameConfigVolume(ctx, req)
}

func (s *APIServer) GetGameConfigVolume(ctx context.Context, req *pb.GetGameConfigVolumeRequest) (*pb.GetGameConfigVolumeResponse, error) {
	return s.volumeHandler.GetGameConfigVolume(ctx, req)
}

func (s *APIServer) ListGameConfigVolumes(ctx context.Context, req *pb.ListGameConfigVolumesRequest) (*pb.ListGameConfigVolumesResponse, error) {
	return s.volumeHandler.ListGameConfigVolumes(ctx, req)
}

func (s *APIServer) UpdateGameConfigVolume(ctx context.Context, req *pb.UpdateGameConfigVolumeRequest) (*pb.UpdateGameConfigVolumeResponse, error) {
	return s.volumeHandler.UpdateGameConfigVolume(ctx, req)
}

func (s *APIServer) DeleteGameConfigVolume(ctx context.Context, req *pb.DeleteGameConfigVolumeRequest) (*pb.DeleteGameConfigVolumeResponse, error) {
	return s.volumeHandler.DeleteGameConfigVolume(ctx, req)
}

// Game Actions RPCs
func (s *APIServer) GetSessionActions(ctx context.Context, req *pb.GetSessionActionsRequest) (*pb.GetSessionActionsResponse, error) {
	return s.actionHandler.GetSessionActions(ctx, req)
}

func (s *APIServer) ExecuteAction(ctx context.Context, req *pb.ExecuteActionRequest) (*pb.ExecuteActionResponse, error) {
	return s.actionHandler.ExecuteAction(ctx, req)
}

func (s *APIServer) CreateActionDefinition(ctx context.Context, req *pb.CreateActionDefinitionRequest) (*pb.CreateActionDefinitionResponse, error) {
	return s.actionHandler.CreateActionDefinition(ctx, req)
}

func (s *APIServer) UpdateActionDefinition(ctx context.Context, req *pb.UpdateActionDefinitionRequest) (*pb.UpdateActionDefinitionResponse, error) {
	return s.actionHandler.UpdateActionDefinition(ctx, req)
}

func (s *APIServer) DeleteActionDefinition(ctx context.Context, req *pb.DeleteActionDefinitionRequest) (*pb.DeleteActionDefinitionResponse, error) {
	return s.actionHandler.DeleteActionDefinition(ctx, req)
}

func (s *APIServer) ListActionDefinitions(ctx context.Context, req *pb.ListActionDefinitionsRequest) (*pb.ListActionDefinitionsResponse, error) {
	return s.actionHandler.ListActionDefinitions(ctx, req)
}

func (s *APIServer) GetActionDefinition(ctx context.Context, req *pb.GetActionDefinitionRequest) (*pb.GetActionDefinitionResponse, error) {
	return s.actionHandler.GetActionDefinition(ctx, req)
}

// RestartSchedule RPCs
func (s *APIServer) CreateRestartSchedule(ctx context.Context, req *pb.CreateRestartScheduleRequest) (*pb.CreateRestartScheduleResponse, error) {
	return s.restartScheduleHandler.CreateRestartSchedule(ctx, req)
}

func (s *APIServer) GetRestartSchedule(ctx context.Context, req *pb.GetRestartScheduleRequest) (*pb.GetRestartScheduleResponse, error) {
	return s.restartScheduleHandler.GetRestartSchedule(ctx, req)
}

func (s *APIServer) ListRestartSchedules(ctx context.Context, req *pb.ListRestartSchedulesRequest) (*pb.ListRestartSchedulesResponse, error) {
	return s.restartScheduleHandler.ListRestartSchedules(ctx, req)
}

func (s *APIServer) UpdateRestartSchedule(ctx context.Context, req *pb.UpdateRestartScheduleRequest) (*pb.UpdateRestartScheduleResponse, error) {
	return s.restartScheduleHandler.UpdateRestartSchedule(ctx, req)
}

func (s *APIServer) DeleteRestartSchedule(ctx context.Context, req *pb.DeleteRestartScheduleRequest) (*pb.DeleteRestartScheduleResponse, error) {
	return s.restartScheduleHandler.DeleteRestartSchedule(ctx, req)
}
