# if these ever need to be broken up, we can move it into the class for tighter scope
from typing import Generic, Type, TypeVar

from generated.manman.experience_api.api.default_api import (
    DefaultApi as ManManExperienceDefaultApi,
)
from generated.manman.experience_api.api_client import (
    ApiClient as ManManExperienceApiClient,
)
from generated.manman.experience_api.configuration import (
    Configuration as ManManExperienceConfiguration,
)
from generated.manman.status_api.api.default_api import DefaultApi as StatusDefaultApi
from generated.manman.status_api.api_client import ApiClient as StatusApiClient
from generated.manman.status_api.configuration import (
    Configuration as StatusConfiguration,
)
from generated.manman.worker_dal_api.api.default_api import (
    DefaultApi as WorkerDalDefaultApi,
)
from generated.manman.worker_dal_api.api_client import (
    ApiClient as WorkerDalApiClient,
)
from generated.manman.worker_dal_api.configuration import (
    Configuration as WorkerDalConfiguration,
)

T_Configuration = TypeVar("T_Configuration")
T_Client = TypeVar("T_Client")
T_API = TypeVar("T_API")


class BaseManManAPI(Generic[T_Configuration, T_Client, T_API]):
    """Base class for ManMan API clients.
    
    Supports separate URL initialization for split API architecture.
    """

    _config: T_Configuration = None  # type: ignore
    _client: T_Client = None  # type: ignore

    # These will be set by subclasses
    _configuration_type: Type[T_Configuration] = None  # type: ignore
    _api_client_type: Type[T_Client] = None  # type: ignore
    _api_type: Type[T_API] = None  # type: ignore

    @classmethod
    def init(cls, host: str):
        """Initialize the API client configuration with a full URL.
        
        Args:
            host: Full URL to the API (e.g., http://experience-api.manman.svc.cluster.local)
        """
        if cls._config is not None:
            return
        cls._config = cls._configuration_type(host=host)
        cls._client = cls._api_client_type(configuration=cls._config)

    @classmethod
    def _get_client(cls) -> T_Client:
        """Get the API client."""
        if cls._client is None:
            raise ValueError(
                f"{cls.__name__} API client not initialized. Call init() first."
            )
        return cls._client

    @classmethod
    def get_api(cls) -> T_API:
        """Get the API instance."""
        return cls._api_type(api_client=cls._get_client())


class ManManStatusAPI(
    BaseManManAPI[StatusConfiguration, StatusApiClient, StatusDefaultApi]
):
    _configuration_type = StatusConfiguration
    _api_client_type = StatusApiClient
    _api_type = StatusDefaultApi


class ManManExperienceAPI(
    BaseManManAPI[
        ManManExperienceConfiguration,
        ManManExperienceApiClient,
        ManManExperienceDefaultApi,
    ]
):
    _configuration_type = ManManExperienceConfiguration
    _api_client_type = ManManExperienceApiClient
    _api_type = ManManExperienceDefaultApi


class ManManWorkerDalAPI(
    BaseManManAPI[WorkerDalConfiguration, WorkerDalApiClient, WorkerDalDefaultApi]
):
    _configuration_type = WorkerDalConfiguration
    _api_client_type = WorkerDalApiClient
    _api_type = WorkerDalDefaultApi
