"""
Configuration constants for the ManMan application.

This module contains centralized configuration for well-known API names,
commands, and other constants used throughout the application.
"""

from dataclasses import dataclass
from typing import Literal

# Global service name for logging/observability systems
SERVICE_NAME = "manman"


@dataclass(frozen=True)
class APIConfig:
    """Configuration for a specific API service."""

    name: str
    title: str
    root_path: str
    command: str


class ManManConfig:
    """Centralized configuration for ManMan services."""

    # API service names (used for OpenAPI generation, microservice identification, etc.)
    EXPERIENCE_API = "experience-api"
    STATUS_API = "status-api"
    WORKER_DAL_API = "worker-dal-api"

    # Background processor service names
    STATUS_PROCESSOR = "status-processor"

    # Worker service name
    WORKER = "worker"

    # All well-known API names for validation
    KNOWN_API_NAMES = frozenset(
        [
            EXPERIENCE_API,
            STATUS_API,
            WORKER_DAL_API,
        ]
    )

    # All well-known service names (APIs + processors + worker)
    KNOWN_SERVICE_NAMES = frozenset(
        [
            EXPERIENCE_API,
            STATUS_API,
            WORKER_DAL_API,
            STATUS_PROCESSOR,
            WORKER,
        ]
    )

    # API configurations
    API_CONFIGS = {
        EXPERIENCE_API: APIConfig(
            name=EXPERIENCE_API,
            title="ManMan Experience API",
            root_path="/experience",
            command="start-experience-api",
        ),
        STATUS_API: APIConfig(
            name=STATUS_API,
            title="ManMan Status API",
            root_path="/status",
            command="start-status-api",
        ),
        WORKER_DAL_API: APIConfig(
            name=WORKER_DAL_API,
            title="ManMan Worker DAL API",
            root_path="/workerdal",
            command="start-worker-dal-api",
        ),
    }

    @classmethod
    def get_api_config(cls, api_name: str) -> APIConfig:
        """Get API configuration by name."""
        if api_name not in cls.API_CONFIGS:
            raise ValueError(
                f"Unknown API name: {api_name}. Valid options are: {', '.join(cls.KNOWN_API_NAMES)}"
            )
        return cls.API_CONFIGS[api_name]

    @classmethod
    def validate_api_name(
        cls, api_name: str
    ) -> Literal["experience-api", "status-api", "worker-dal-api"]:
        """Validate and return API name with proper type hint."""
        if api_name not in cls.KNOWN_API_NAMES:
            raise ValueError(
                f"Unknown API name: {api_name}. Valid options are: {', '.join(cls.KNOWN_API_NAMES)}"
            )
        return api_name  # type: ignore

    @classmethod
    def validate_service_name(cls, service_name: str) -> str:
        """Validate and return service name."""
        if service_name not in cls.KNOWN_SERVICE_NAMES:
            raise ValueError(
                f"Unknown service name: {service_name}. Valid options are: {', '.join(cls.KNOWN_SERVICE_NAMES)}"
            )
        return service_name
