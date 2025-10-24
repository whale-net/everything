"""
Worker DAL API client wrapper.

Provides a wrapper around the generated OpenAPI client for the Worker DAL API.
Handles model translation between domain models and generated client models.
Includes automatic retry logic for transient HTTP failures (502, 503, 504, timeouts).

Note: Cannot use generated client directly in repository layer due to circular dependency:
- Generated client depends on OpenAPI spec
- OpenAPI spec is generated from the API
- API depends on repository_core

This wrapper can be used by worker code and other consumers that don't have
this circular dependency issue.
"""

from typing import Optional

from generated.py.manman.worker_dal_api import ApiClient, Configuration
from generated.py.manman.worker_dal_api.api.default_api import DefaultApi
from generated.py.manman.worker_dal_api.models.game_server import GameServer as GeneratedGameServer
from generated.py.manman.worker_dal_api.models.game_server_config import GameServerConfig as GeneratedGameServerConfig
from generated.py.manman.worker_dal_api.models.game_server_instance import GameServerInstance as GeneratedGameServerInstance
from generated.py.manman.worker_dal_api.models.worker import Worker as GeneratedWorker

from manman.src.models import GameServer, GameServerConfig, GameServerInstance, Worker
from libs.python.retry import RetryConfig, retry, is_transient_http_error


class WorkerDALClient:
    """
    Client wrapper for Worker DAL API using generated OpenAPI client.
    
    Translates between domain models and generated models, providing a stable
    interface that matches the domain model structure.
    
    All API calls include automatic retry logic for transient HTTP failures:
    - Connection errors (network issues, DNS failures)
    - Timeouts (connect, read)
    - 502 Bad Gateway
    - 503 Service Unavailable
    - 504 Gateway Timeout
    """

    # Retry configuration for all API calls
    # 5 attempts with exponential backoff: ~1s, ~2s, ~4s, ~8s
    _RETRY_CONFIG = RetryConfig(
        max_attempts=5,
        initial_delay=1.0,
        max_delay=30.0,
        exponential_base=2.0,
        exception_filter=is_transient_http_error,
    )
    
    # Retry configuration for heartbeat calls
    # 1 retry with minimal delay - heartbeats should fail fast and not block the service
    # Heartbeat failures are logged but don't crash the service
    _HEARTBEAT_RETRY_CONFIG = RetryConfig(
        max_attempts=2,  # 1 retry (2 total attempts)
        initial_delay=0.5,
        max_delay=1.0,
        exponential_base=2.0,
        exception_filter=is_transient_http_error,
    )

    def __init__(self, base_url: str, access_token: Optional[str] = None, verify_ssl: bool = True):
        """
        Initialize the Worker DAL client.
        
        Args:
            base_url: Base URL for the Worker DAL API (e.g., "http://dal.manman.local")
            access_token: Optional bearer token for authentication
            verify_ssl: Whether to verify SSL certificates (default: True)
        """
        configuration = Configuration(host=base_url)
        if access_token:
            configuration.access_token = access_token
        configuration.verify_ssl = verify_ssl

        api_client = ApiClient(configuration)
        self._api = DefaultApi(api_client)

    # Model translation helpers
    def _to_domain_game_server(self, generated: GeneratedGameServer) -> GameServer:
        """Convert generated GameServer to domain GameServer."""
        if isinstance(generated, dict):
            return GameServer.model_validate(generated)
        return GameServer.model_validate(generated.to_dict())

    def _to_domain_game_server_config(self, generated: GeneratedGameServerConfig) -> GameServerConfig:
        """Convert generated GameServerConfig to domain GameServerConfig."""
        if isinstance(generated, dict):
            return GameServerConfig.model_validate(generated)
        return GameServerConfig.model_validate(generated.to_dict())

    def _to_domain_game_server_instance(self, generated: GeneratedGameServerInstance) -> GameServerInstance:
        """Convert generated GameServerInstance to domain GameServerInstance."""
        if isinstance(generated, dict):
            return GameServerInstance.model_validate(generated)
        return GameServerInstance.model_validate(generated.to_dict())

    def _to_domain_worker(self, generated: GeneratedWorker) -> Worker:
        """Convert generated Worker to domain Worker."""
        if isinstance(generated, dict):
            return Worker.model_validate(generated)
        return Worker.model_validate(generated.to_dict())

    def _to_generated_game_server_instance(self, domain: GameServerInstance) -> GeneratedGameServerInstance:
        """Convert domain GameServerInstance to generated model.
        
        Creates a minimal object with required fields. The generated model requires all fields
        but the API endpoints typically only use the ID fields.
        """
        return GeneratedGameServerInstance(
            game_server_instance_id=domain.game_server_instance_id or 0,
            game_server_config_id=domain.game_server_config_id or 0,
            worker_id=domain.worker_id or 0,
            end_date=domain.end_date,
            last_heartbeat=domain.last_heartbeat,
        )

    def _to_generated_worker(self, domain: Worker) -> GeneratedWorker:
        """Convert domain Worker to generated model.
        
        Creates a minimal object with required fields. The generated model requires all fields
        but the API endpoints typically only use worker_id.
        """
        return GeneratedWorker(
            worker_id=domain.worker_id,
            created_date=domain.created_date,
            end_date=domain.end_date,
            last_heartbeat=domain.last_heartbeat,
        )

    # Game Server methods
    @retry(_RETRY_CONFIG)
    def get_game_server(self, game_server_id: int) -> GameServer:
        """Get game server by ID."""
        result = self._api.server_server_id_get(game_server_id)
        return self._to_domain_game_server(result)

    @retry(_RETRY_CONFIG)
    def get_game_server_config(self, game_server_config_id: int) -> GameServerConfig:
        """Get game server configuration by ID."""
        result = self._api.server_config_server_config_id_get(game_server_config_id)
        return self._to_domain_game_server_config(result)

    # Game Server Instance methods
    @retry(_RETRY_CONFIG)
    def create_game_server_instance(
        self,
        game_server_config_id: int,
        worker_id: int,
    ) -> GameServerInstance:
        """Create a new game server instance."""
        # Create domain instance with only the fields needed for creation
        # API will set game_server_instance_id, created_date, etc.
        instance = GameServerInstance(
            game_server_config_id=game_server_config_id,
            worker_id=worker_id,
        )
        generated_instance = self._to_generated_game_server_instance(instance)
        result = self._api.server_instance_create_server_instance_create_post(generated_instance)
        return self._to_domain_game_server_instance(result)

    @retry(_RETRY_CONFIG)
    def shutdown_game_server_instance(self, instance: GameServerInstance) -> GameServerInstance:
        """Shutdown a game server instance."""
        generated_instance = self._to_generated_game_server_instance(instance)
        result = self._api.server_instance_shutdown_server_instance_shutdown_put(generated_instance)
        return self._to_domain_game_server_instance(result)

    @retry(_RETRY_CONFIG)
    def get_game_server_instance(self, instance_id: int) -> GameServerInstance:
        """Get game server instance by ID."""
        result = self._api.server_instance_server_instance_id_get(instance_id)
        return self._to_domain_game_server_instance(result)

    @retry(_HEARTBEAT_RETRY_CONFIG)
    def heartbeat_game_server_instance(self, instance_id: int) -> GameServerInstance:
        """
        Send heartbeat for game server instance.
        
        Uses minimal retry (1 retry) to avoid blocking the service run loop.
        Caller should handle exceptions gracefully to prevent service crashes.
        """
        result = self._api.server_instance_heartbeat_server_instance_heartbeat_id_post(instance_id)
        return self._to_domain_game_server_instance(result)

    # Worker methods
    @retry(_RETRY_CONFIG)
    def create_worker(self) -> Worker:
        """Create a new worker."""
        result = self._api.worker_create_worker_create_post()
        return self._to_domain_worker(result)

    @retry(_RETRY_CONFIG)
    def shutdown_worker(self, worker: Worker) -> Worker:
        """Shutdown a worker."""
        generated_worker = self._to_generated_worker(worker)
        result = self._api.worker_shutdown_worker_shutdown_put(generated_worker)
        return self._to_domain_worker(result)

    @retry(_HEARTBEAT_RETRY_CONFIG)
    def heartbeat_worker(self, worker: Worker) -> Worker:
        """
        Send heartbeat for worker.
        
        Uses minimal retry (1 retry) to avoid blocking the service run loop.
        Caller should handle exceptions gracefully to prevent service crashes.
        """
        generated_worker = self._to_generated_worker(worker)
        result = self._api.worker_heartbeat_worker_heartbeat_post(generated_worker)
        return self._to_domain_worker(result)

    @retry(_RETRY_CONFIG)
    def shutdown_other_workers(self, worker: Worker) -> None:
        """Shutdown all other workers except the specified one."""
        generated_worker = self._to_generated_worker(worker)
        self._api.worker_shutdown_other_worker_shutdown_other_put(generated_worker)
