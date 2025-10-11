"""Test that the experience API client models can be imported and used."""

from __future__ import annotations

import os
import sys
from pathlib import Path


def _ensure_generated_client_on_path() -> None:
    """Add bazel-bin/tools/client_codegen to sys.path when running outside Bazel."""

    # When running under Bazel, the generated package is already on the path via
    # runfiles. For ad-hoc execution (e.g. python tools/client_codegen/test_experience_api_client.py)
    # we need to point Python at the bazel-bin outputs.
    candidate = Path(__file__).resolve().parents[2] / "bazel-bin" / "tools" / "client_codegen"
    if candidate.exists() and str(candidate) not in sys.path:
        sys.path.insert(0, str(candidate))


_ensure_generated_client_on_path()


from external.manman.experience_api import ApiClient, Configuration  # type: ignore[import-not-found]
from external.manman.experience_api.api import DefaultApi  # type: ignore[import-not-found]
from external.manman.experience_api.models import (  # type: ignore[import-not-found]
    CurrentInstanceResponse,
    GameServerConfig,
    GameServerInstance,
    HTTPValidationError,
    StdinCommandRequest,
    ValidationError,
    ValidationErrorLocInner,
    Worker,
)


def test_imports():
    """Test that all model classes can be imported."""
    # If we get here, all imports worked
    assert CurrentInstanceResponse is not None
    assert GameServerConfig is not None
    assert GameServerInstance is not None
    assert HTTPValidationError is not None
    assert StdinCommandRequest is not None
    assert ValidationError is not None
    assert ValidationErrorLocInner is not None
    assert Worker is not None


def test_create_worker_model():
    """Test creating a Worker model instance."""
    worker = Worker(
        worker_id=123,
        created_date="2025-10-10T10:00:00Z",
        end_date=None,
        last_heartbeat="2025-10-10T10:30:00Z",
    )
    
    assert worker.worker_id == 123
    assert worker.created_date is not None
    assert worker.end_date is None
    assert worker.last_heartbeat is not None


def test_create_game_server_instance():
    """Test creating a GameServerInstance model."""
    instance = GameServerInstance(
        game_server_instance_id=456,
        game_server_config_id=789,
        worker_id=123,
        last_heartbeat="2025-10-10T10:30:00Z",
        end_date=None,
    )
    
    assert instance.game_server_instance_id == 456
    assert instance.game_server_config_id == 789
    assert instance.worker_id == 123
    assert instance.last_heartbeat is not None
    assert instance.end_date is None


def test_create_game_server_config():
    """Test creating a GameServerConfig model."""
    config = GameServerConfig(
        game_server_config_id=789,
        game_server_id=1,
        name="Production Config",
        executable="/usr/local/bin/game-server",
        args=["--mode=deathmatch", "--max-players=32"],
        env_var=["GAME_MODE=deathmatch", "MAX_PLAYERS=32"],
        is_default=True,
        is_visible=True,
    )
    
    assert config.game_server_config_id == 789
    assert config.game_server_id == 1
    assert config.name == "Production Config"
    assert config.executable == "/usr/local/bin/game-server"
    assert len(config.args) == 2
    assert len(config.env_var) == 2


def test_create_stdin_command_request():
    """Test creating a StdinCommandRequest model."""
    command = StdinCommandRequest(
        commands=["say Hello, players!", "broadcast Welcome!"],
    )
    
    assert len(command.commands) == 2
    assert command.commands[0] == "say Hello, players!"


def test_create_current_instance_response():
    """Test creating a CurrentInstanceResponse model."""
    worker = Worker(
        worker_id=123,
        created_date="2025-10-10T10:00:00Z",
        end_date=None,
        last_heartbeat="2025-10-10T10:30:00Z",
    )
    
    instance = GameServerInstance(
        game_server_instance_id=456,
        game_server_config_id=789,
        worker_id=123,
        last_heartbeat="2025-10-10T10:30:00Z",
        end_date=None,
    )
    
    config = GameServerConfig(
        game_server_config_id=789,
        game_server_id=1,
        name="Test Config",
        executable="/bin/server",
        args=["--mode=test"],
        env_var=["ENV=test"],
    )
    
    response = CurrentInstanceResponse(
        game_server_instances=[instance],
        workers=[worker],
        configs=[config],
    )
    
    assert len(response.game_server_instances) == 1
    assert len(response.workers) == 1
    assert len(response.configs) == 1


def test_api_client_configuration():
    """Test that ApiClient and Configuration can be instantiated."""
    config = Configuration(host="http://localhost:8000")
    client = ApiClient(configuration=config)
    api = DefaultApi(api_client=client)
    
    assert config.host == "http://localhost:8000"
    assert api is not None


def test_model_serialization():
    """Test that models can be serialized to dict/JSON."""
    worker = Worker(
        worker_id=123,
        created_date="2025-10-10T10:00:00Z",
        end_date=None,
        last_heartbeat="2025-10-10T10:30:00Z",
    )
    
    # Convert to dict
    worker_dict = worker.model_dump()
    assert isinstance(worker_dict, dict)
    assert worker_dict["worker_id"] == 123
    
    # Convert to JSON
    worker_json = worker.model_dump_json()
    assert isinstance(worker_json, str)
    assert "123" in worker_json


def test_validation_error_models():
    """Test that validation error models work."""
    # ValidationErrorLocInner is a union type (int | str), so we construct it with actual_instance
    loc_inner_str = ValidationErrorLocInner(actual_instance="field_name")
    loc_inner_int = ValidationErrorLocInner(actual_instance=0)
    
    validation_error = ValidationError(
        loc=[loc_inner_str, loc_inner_int],
        msg="Field required",
        type="value_error.missing",
    )
    assert len(validation_error.loc) == 2
    assert validation_error.msg == "Field required"
    
    http_error = HTTPValidationError(
        detail=[validation_error]
    )
    assert len(http_error.detail) == 1
    assert http_error.detail[0].msg == "Field required"

