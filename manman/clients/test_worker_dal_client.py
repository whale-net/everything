"""
Tests for WorkerDALClient - translation layer for Worker DAL API.
"""

from datetime import datetime
from unittest.mock import Mock, MagicMock

import pytest

from manman.clients.worker_dal_client import WorkerDALClient
from manman.src.models import (
    GameServer,
    GameServerConfig,
    GameServerInstance,
    Worker,
    ServerType,
)

# Import generated models for mocking
from generated.py.manman.worker_dal_api.models.game_server import GameServer as GeneratedGameServer
from generated.py.manman.worker_dal_api.models.game_server_config import GameServerConfig as GeneratedGameServerConfig
from generated.py.manman.worker_dal_api.models.game_server_instance import GameServerInstance as GeneratedGameServerInstance
from generated.py.manman.worker_dal_api.models.worker import Worker as GeneratedWorker
from generated.py.manman.worker_dal_api.models.server_type import ServerType as GeneratedServerType


@pytest.fixture
def mock_api():
    """Create a mock DefaultApi."""
    return Mock()


@pytest.fixture
def client(mock_api, monkeypatch):
    """Create a WorkerDALClient with mocked API."""
    # Mock the ApiClient and Configuration to avoid network calls
    mock_api_client_class = Mock()
    mock_configuration_class = Mock()
    
    monkeypatch.setattr(
        "manman.clients.worker_dal_client.ApiClient",
        mock_api_client_class,
    )
    monkeypatch.setattr(
        "manman.clients.worker_dal_client.Configuration",
        mock_configuration_class,
    )
    monkeypatch.setattr(
        "manman.clients.worker_dal_client.DefaultApi",
        lambda api_client: mock_api,
    )
    
    client = WorkerDALClient(base_url="http://test-api")
    return client


class TestModelTranslation:
    """Test model translation helpers."""
    
    def test_to_domain_worker(self, client):
        """Test converting generated Worker to domain Worker."""
        generated = GeneratedWorker(
            worker_id=1,
            status="active",
            last_heartbeat=datetime(2025, 10, 22, 12, 0, 0),
        )
        
        domain = client._to_domain_worker(generated)
        
        assert isinstance(domain, Worker)
        assert domain.worker_id == 1
        assert domain.status == "active"
        assert domain.last_heartbeat == datetime(2025, 10, 22, 12, 0, 0)
    
    def test_to_generated_worker(self, client):
        """Test converting domain Worker to generated Worker."""
        domain = Worker(
            worker_id=2,
            status="idle",
            last_heartbeat=datetime(2025, 10, 22, 13, 0, 0),
        )
        
        generated = client._to_generated_worker(domain)
        
        assert isinstance(generated, GeneratedWorker)
        assert generated.worker_id == 2
        assert generated.status == "idle"
        assert generated.last_heartbeat == datetime(2025, 10, 22, 13, 0, 0)
    
    def test_to_domain_game_server_config(self, client):
        """Test converting generated GameServerConfig to domain."""
        generated = GeneratedGameServerConfig(
            game_server_config_id=10,
            name="Test Server",
            server_type=GeneratedServerType.VALHEIM,
            config={"port": 2456},
        )
        
        domain = client._to_domain_game_server_config(generated)
        
        assert isinstance(domain, GameServerConfig)
        assert domain.game_server_config_id == 10
        assert domain.name == "Test Server"
        assert domain.server_type == ServerType.VALHEIM
        assert domain.config == {"port": 2456}
    
    def test_to_domain_game_server_instance(self, client):
        """Test converting generated GameServerInstance to domain."""
        generated = GeneratedGameServerInstance(
            game_server_instance_id=5,
            game_server_config_id=10,
            worker_id=1,
            status="running",
            last_heartbeat=datetime(2025, 10, 22, 14, 0, 0),
        )
        
        domain = client._to_domain_game_server_instance(generated)
        
        assert isinstance(domain, GameServerInstance)
        assert domain.game_server_instance_id == 5
        assert domain.game_server_config_id == 10
        assert domain.worker_id == 1
        assert domain.status == "running"
    
    def test_to_generated_game_server_instance(self, client):
        """Test converting domain GameServerInstance to generated."""
        domain = GameServerInstance(
            game_server_instance_id=6,
            game_server_config_id=11,
            worker_id=2,
            status="stopped",
        )
        
        generated = client._to_generated_game_server_instance(domain)
        
        assert isinstance(generated, GeneratedGameServerInstance)
        assert generated.game_server_instance_id == 6
        assert generated.game_server_config_id == 11
        assert generated.worker_id == 2
        assert generated.status == "stopped"


class TestWorkerMethods:
    """Test Worker-related API methods."""
    
    def test_create_worker(self, client, mock_api):
        """Test creating a new worker."""
        mock_api.worker_create_worker_create_post.return_value = GeneratedWorker(
            worker_id=100,
            status="active",
            last_heartbeat=datetime(2025, 10, 22, 15, 0, 0),
        )
        
        worker = client.create_worker()
        
        assert isinstance(worker, Worker)
        assert worker.worker_id == 100
        assert worker.status == "active"
        mock_api.worker_create_worker_create_post.assert_called_once()
    
    def test_heartbeat_worker(self, client, mock_api):
        """Test sending worker heartbeat."""
        domain_worker = Worker(
            worker_id=100,
            status="active",
            last_heartbeat=datetime(2025, 10, 22, 15, 0, 0),
        )
        
        mock_api.worker_heartbeat_worker_heartbeat_post.return_value = GeneratedWorker(
            worker_id=100,
            status="active",
            last_heartbeat=datetime(2025, 10, 22, 15, 1, 0),
        )
        
        updated_worker = client.heartbeat_worker(domain_worker)
        
        assert isinstance(updated_worker, Worker)
        assert updated_worker.worker_id == 100
        assert updated_worker.last_heartbeat == datetime(2025, 10, 22, 15, 1, 0)
        mock_api.worker_heartbeat_worker_heartbeat_post.assert_called_once()
    
    def test_shutdown_worker(self, client, mock_api):
        """Test shutting down a worker."""
        domain_worker = Worker(
            worker_id=100,
            status="active",
        )
        
        mock_api.worker_shutdown_worker_shutdown_put.return_value = GeneratedWorker(
            worker_id=100,
            status="shutdown",
        )
        
        shutdown_worker = client.shutdown_worker(domain_worker)
        
        assert isinstance(shutdown_worker, Worker)
        assert shutdown_worker.worker_id == 100
        assert shutdown_worker.status == "shutdown"
        mock_api.worker_shutdown_worker_shutdown_put.assert_called_once()
    
    def test_shutdown_other_workers(self, client, mock_api):
        """Test shutting down other workers."""
        domain_worker = Worker(worker_id=100, status="active")
        
        client.shutdown_other_workers(domain_worker)
        
        mock_api.worker_shutdown_other_worker_shutdown_other_put.assert_called_once()


class TestGameServerMethods:
    """Test GameServer-related API methods."""
    
    def test_get_game_server(self, client, mock_api):
        """Test getting a game server by ID."""
        mock_api.server_server_id_get.return_value = GeneratedGameServer(
            game_server_id=50,
            name="My Valheim Server",
            server_type=GeneratedServerType.VALHEIM,
        )
        
        server = client.get_game_server(50)
        
        assert isinstance(server, GameServer)
        assert server.game_server_id == 50
        assert server.name == "My Valheim Server"
        mock_api.server_server_id_get.assert_called_once_with(50)
    
    def test_get_game_server_config(self, client, mock_api):
        """Test getting a game server config by ID."""
        mock_api.server_config_server_config_id_get.return_value = GeneratedGameServerConfig(
            game_server_config_id=25,
            name="Valheim Config",
            server_type=GeneratedServerType.VALHEIM,
            config={"world": "Dedicated"},
        )
        
        config = client.get_game_server_config(25)
        
        assert isinstance(config, GameServerConfig)
        assert config.game_server_config_id == 25
        assert config.name == "Valheim Config"
        assert config.config == {"world": "Dedicated"}
        mock_api.server_config_server_config_id_get.assert_called_once_with(25)


class TestGameServerInstanceMethods:
    """Test GameServerInstance-related API methods."""
    
    def test_create_game_server_instance(self, client, mock_api):
        """Test creating a game server instance."""
        mock_api.server_instance_create_server_instance_create_post.return_value = (
            GeneratedGameServerInstance(
                game_server_instance_id=200,
                game_server_config_id=25,
                worker_id=100,
                status="starting",
            )
        )
        
        instance = client.create_game_server_instance(
            game_server_config_id=25,
            worker_id=100,
        )
        
        assert isinstance(instance, GameServerInstance)
        assert instance.game_server_instance_id == 200
        assert instance.game_server_config_id == 25
        assert instance.worker_id == 100
        assert instance.status == "starting"
        mock_api.server_instance_create_server_instance_create_post.assert_called_once()
    
    def test_get_game_server_instance(self, client, mock_api):
        """Test getting a game server instance by ID."""
        mock_api.server_instance_server_instance_id_get.return_value = (
            GeneratedGameServerInstance(
                game_server_instance_id=200,
                game_server_config_id=25,
                worker_id=100,
                status="running",
            )
        )
        
        instance = client.get_game_server_instance(200)
        
        assert isinstance(instance, GameServerInstance)
        assert instance.game_server_instance_id == 200
        assert instance.status == "running"
        mock_api.server_instance_server_instance_id_get.assert_called_once_with(200)
    
    def test_heartbeat_game_server_instance(self, client, mock_api):
        """Test sending heartbeat for game server instance."""
        mock_api.server_instance_heartbeat_server_instance_heartbeat_id_post.return_value = (
            GeneratedGameServerInstance(
                game_server_instance_id=200,
                game_server_config_id=25,
                worker_id=100,
                status="running",
                last_heartbeat=datetime(2025, 10, 22, 16, 0, 0),
            )
        )
        
        instance = client.heartbeat_game_server_instance(200)
        
        assert isinstance(instance, GameServerInstance)
        assert instance.game_server_instance_id == 200
        assert instance.last_heartbeat == datetime(2025, 10, 22, 16, 0, 0)
        mock_api.server_instance_heartbeat_server_instance_heartbeat_id_post.assert_called_once_with(200)
    
    def test_shutdown_game_server_instance(self, client, mock_api):
        """Test shutting down a game server instance."""
        domain_instance = GameServerInstance(
            game_server_instance_id=200,
            game_server_config_id=25,
            worker_id=100,
            status="running",
        )
        
        mock_api.server_instance_shutdown_server_instance_shutdown_put.return_value = (
            GeneratedGameServerInstance(
                game_server_instance_id=200,
                game_server_config_id=25,
                worker_id=100,
                status="shutdown",
            )
        )
        
        shutdown_instance = client.shutdown_game_server_instance(domain_instance)
        
        assert isinstance(shutdown_instance, GameServerInstance)
        assert shutdown_instance.game_server_instance_id == 200
        assert shutdown_instance.status == "shutdown"
        mock_api.server_instance_shutdown_server_instance_shutdown_put.assert_called_once()


class TestClientInitialization:
    """Test client initialization with different configurations."""
    
    def test_init_with_base_url_only(self, monkeypatch):
        """Test initializing client with just base URL."""
        mock_config = Mock()
        mock_api_client = Mock()
        mock_api = Mock()
        
        monkeypatch.setattr(
            "manman.clients.worker_dal_client.Configuration",
            lambda host: mock_config,
        )
        monkeypatch.setattr(
            "manman.clients.worker_dal_client.ApiClient",
            lambda config: mock_api_client,
        )
        monkeypatch.setattr(
            "manman.clients.worker_dal_client.DefaultApi",
            lambda api_client: mock_api,
        )
        
        client = WorkerDALClient(base_url="http://api.example.com")
        
        assert client._api == mock_api
    
    def test_init_with_access_token(self, monkeypatch):
        """Test initializing client with access token."""
        mock_config = Mock()
        mock_api_client = Mock()
        mock_api = Mock()
        
        monkeypatch.setattr(
            "manman.clients.worker_dal_client.Configuration",
            lambda host: mock_config,
        )
        monkeypatch.setattr(
            "manman.clients.worker_dal_client.ApiClient",
            lambda config: mock_api_client,
        )
        monkeypatch.setattr(
            "manman.clients.worker_dal_client.DefaultApi",
            lambda api_client: mock_api,
        )
        
        client = WorkerDALClient(
            base_url="http://api.example.com",
            access_token="test-token-123",
        )
        
        assert mock_config.access_token == "test-token-123"
        assert client._api == mock_api
