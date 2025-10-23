"""
Worker DAL API client wrapper.

Provides a wrapper around the generated OpenAPI client for the Worker DAL API.
Handles model translation between domain models and generated client models.

Note: Cannot use generated client directly in repository layer due to circular dependency:
- Generated client depends on OpenAPI spec
- OpenAPI spec is generated from the API
- API depends on repository_core

This wrapper can be used by worker code and other consumers that don't have
this circular dependency issue.
"""

from typing import Optional

from generated.manman.worker_dal_api import ApiClient, Configuration
from generated.manman.worker_dal_api.api.default_api import DefaultApi
from generated.manman.worker_dal_api.models.game_server import GameServer as GeneratedGameServer
from generated.manman.worker_dal_api.models.game_server_config import GameServerConfig as GeneratedGameServerConfig
from generated.manman.worker_dal_api.models.game_server_instance import GameServerInstance as GeneratedGameServerInstance
from generated.manman.worker_dal_api.models.worker import Worker as GeneratedWorker

from manman.src.models import GameServer, GameServerConfig, GameServerInstance, Worker


class WorkerDALClient:
    """
    Client wrapper for Worker DAL API using generated OpenAPI client.
    
    Translates between domain models and generated models, providing a stable
    interface that matches the domain model structure.
    """

    def __init__(self, base_url: str, access_token: Optional[str] = None):
        """
        Initialize the Worker DAL client.
        
        Args:
            base_url: Base URL for the Worker DAL API (e.g., "http://dal.manman.local")
            access_token: Optional bearer token for authentication
        """
        configuration = Configuration(host=base_url)
        if access_token:
            configuration.access_token = access_token

        api_client = ApiClient(configuration)
        self._api = DefaultApi(api_client)

    # Model translation helpers
    def _to_domain_game_server(self, generated: GeneratedGameServer) -> GameServer:
        """Convert generated GameServer to domain GameServer."""
        return GameServer.model_validate(generated.to_dict())

    def _to_domain_game_server_config(self, generated: GeneratedGameServerConfig) -> GameServerConfig:
        """Convert generated GameServerConfig to domain GameServerConfig."""
        return GameServerConfig.model_validate(generated.to_dict())

    def _to_domain_game_server_instance(self, generated: GeneratedGameServerInstance) -> GameServerInstance:
        """Convert generated GameServerInstance to domain GameServerInstance."""
        return GameServerInstance.model_validate(generated.to_dict())

    def _to_domain_worker(self, generated: GeneratedWorker) -> Worker:
        """Convert generated Worker to domain Worker."""
        return Worker.model_validate(generated.to_dict())

    def _to_generated_game_server_instance(self, domain: GameServerInstance) -> GeneratedGameServerInstance:
        """Convert domain GameServerInstance to generated model."""
        return GeneratedGameServerInstance(**domain.model_dump(exclude_none=True))

    def _to_generated_worker(self, domain: Worker) -> GeneratedWorker:
        """Convert domain Worker to generated model."""
        return GeneratedWorker(**domain.model_dump(exclude_none=True))

    # Game Server methods
    def get_game_server(self, game_server_id: int) -> GameServer:
        """Get game server by ID."""
        result = self._api.server_server_id_get(game_server_id)
        return self._to_domain_game_server(result)

    def get_game_server_config(self, game_server_config_id: int) -> GameServerConfig:
        """Get game server configuration by ID."""
        result = self._api.server_config_server_config_id_get(game_server_config_id)
        return self._to_domain_game_server_config(result)

    # Game Server Instance methods
    def create_game_server_instance(
        self,
        game_server_config_id: int,
        worker_id: int,
    ) -> GameServerInstance:
        """Create a new game server instance."""
        instance = GameServerInstance(
            game_server_config_id=game_server_config_id,
            worker_id=worker_id,
        )
        generated_instance = self._to_generated_game_server_instance(instance)
        result = self._api.server_instance_create_server_instance_create_post(generated_instance)
        return self._to_domain_game_server_instance(result)

    def shutdown_game_server_instance(self, instance: GameServerInstance) -> GameServerInstance:
        """Shutdown a game server instance."""
        generated_instance = self._to_generated_game_server_instance(instance)
        result = self._api.server_instance_shutdown_server_instance_shutdown_put(generated_instance)
        return self._to_domain_game_server_instance(result)

    def get_game_server_instance(self, instance_id: int) -> GameServerInstance:
        """Get game server instance by ID."""
        result = self._api.server_instance_server_instance_id_get(instance_id)
        return self._to_domain_game_server_instance(result)

    def heartbeat_game_server_instance(self, instance_id: int) -> GameServerInstance:
        """Send heartbeat for game server instance."""
        result = self._api.server_instance_heartbeat_server_instance_heartbeat_id_post(instance_id)
        return self._to_domain_game_server_instance(result)

    # Worker methods
    def create_worker(self) -> Worker:
        """Create a new worker."""
        result = self._api.worker_create_worker_create_post()
        return self._to_domain_worker(result)

    def shutdown_worker(self, worker: Worker) -> Worker:
        """Shutdown a worker."""
        generated_worker = self._to_generated_worker(worker)
        result = self._api.worker_shutdown_worker_shutdown_put(generated_worker)
        return self._to_domain_worker(result)

    def heartbeat_worker(self, worker: Worker) -> Worker:
        """Send heartbeat for worker."""
        generated_worker = self._to_generated_worker(worker)
        result = self._api.worker_heartbeat_worker_heartbeat_post(generated_worker)
        return self._to_domain_worker(result)

    def shutdown_other_workers(self, worker: Worker) -> None:
        """Shutdown all other workers except the specified one."""
        generated_worker = self._to_generated_worker(worker)
        self._api.worker_shutdown_other_worker_shutdown_other_put(generated_worker)
