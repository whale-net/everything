# Before/After: Manual vs Generated Client

This document shows concrete examples of how the manual client compares to the generated client.

## Current Manual Implementation

### File: `manman/src/repository/api_client.py`

```python
class WorkerAPIClient(APIClientBase):
    """Manually written client for Worker DAL API."""
    
    def __init__(
        self,
        base_url: str,
        auth_api_client: AuthAPIClient,
        sa_client_id: str,
        sa_client_secret: str,
        api_prefix: str = "/workerdal",
    ) -> None:
        self._auth_api_client = auth_api_client
        self._sa_client_id = sa_client_id
        self._sa_client_secret = sa_client_secret
        super().__init__(base_url=base_url, api_prefix=api_prefix)
        self._session.auth = None

    def game_server_config(self, game_server_config_id: int) -> GameServerConfig:
        response = self._session.get(
            f"/server/config/{game_server_config_id}",
        )
        if response.status_code != 200:
            raise RuntimeError(response.content)
        return GameServerConfig.model_validate_json(response.content)

    def game_server_instance_create(
        self,
        config: GameServerConfig,
        worker_id: int,
    ) -> GameServerInstance:
        instance = GameServerInstance(
            game_server_config_id=config.game_server_config_id,
            worker_id=worker_id,
        )
        response = self._session.post(
            "/server/instance/create",
            data=instance.model_dump_json(),
        )
        if response.status_code != 200:
            raise RuntimeError(response.content)
        return GameServerInstance.model_validate_json(response.content)

    def worker_create(self):
        response = self._session.post("/worker/create")
        if response.status_code != 200:
            raise RuntimeError(response.content)
        return Worker.model_validate_json(response.content)

    def worker_heartbeat(self, worker: Worker):
        response = self._session.post(
            "/worker/heartbeat",
            data=worker.model_dump_json(),
        )
        if response.status_code != 200:
            raise RuntimeError(response.content)
        return Worker.model_validate_json(response.content)
```

### Problems with Manual Approach

1. **Maintenance Burden**: Every API change requires manual code updates
2. **No Type Hints**: Method signatures lack proper type annotations
3. **Error Handling**: Basic error handling, no proper exception types
4. **No Documentation**: Methods lack docstrings
5. **Boilerplate**: Repetitive serialization/deserialization code
6. **Drift Risk**: Client can drift from actual API contract

---

## After: Generated Client

### Generate the Client

```bash
python tools/generate_clients.py --api=worker-dal-api --strategy=shared
```

### Generated File: `clients/worker-dal-api-client/manman_worker_dal_client/api/default_api.py`

```python
class DefaultApi:
    """Auto-generated API client for Worker DAL."""
    
    def __init__(self, api_client=None):
        if api_client is None:
            api_client = ApiClient()
        self.api_client = api_client

    def game_server_config(
        self, 
        id: int,
        **kwargs
    ) -> GameServerConfig:
        """Get game server configuration by ID.
        
        Args:
            id (int): Configuration ID
            
        Returns:
            GameServerConfig: The server configuration
            
        Raises:
            ApiException: If the API call fails
        """
        kwargs['_return_http_data_only'] = True
        return self.game_server_config_with_http_info(id, **kwargs)
    
    def game_server_instance_create(
        self,
        body: GameServerInstance,
        **kwargs
    ) -> GameServerInstance:
        """Create a new game server instance.
        
        Args:
            body (GameServerInstance): Instance to create
            
        Returns:
            GameServerInstance: The created instance
            
        Raises:
            ApiException: If the API call fails
        """
        kwargs['_return_http_data_only'] = True
        return self.game_server_instance_create_with_http_info(body, **kwargs)
    
    def worker_create(self, **kwargs) -> Worker:
        """Create a new worker.
        
        Returns:
            Worker: The created worker
            
        Raises:
            ApiException: If the API call fails
        """
        kwargs['_return_http_data_only'] = True
        return self.worker_create_with_http_info(**kwargs)
    
    def worker_heartbeat(
        self,
        body: Worker,
        **kwargs
    ) -> Worker:
        """Send worker heartbeat.
        
        Args:
            body (Worker): Worker to send heartbeat for
            
        Returns:
            Worker: Updated worker
            
        Raises:
            ApiException: If the API call fails
        """
        kwargs['_return_http_data_only'] = True
        return self.worker_heartbeat_with_http_info(body, **kwargs)
```

### Benefits of Generated Approach

1. ✅ **Zero Maintenance**: Regenerate on API changes
2. ✅ **Full Type Hints**: Complete type annotations
3. ✅ **Proper Exceptions**: Structured error handling with `ApiException`
4. ✅ **Auto Documentation**: Docstrings generated from OpenAPI
5. ✅ **No Boilerplate**: Serialization handled automatically
6. ✅ **Always in Sync**: Generated from OpenAPI spec = guaranteed match

---

## Usage Comparison

### Manual Client Usage (Before)

```python
from manman.src.repository.api_client import WorkerAPIClient, AuthAPIClient
from manman.src.models import Worker, GameServerConfig

# Complex setup
auth_client = AuthAPIClient(base_url="http://auth.local")
client = WorkerAPIClient(
    base_url="http://dal.manman.local",
    auth_api_client=auth_client,
    sa_client_id="my-client",
    sa_client_secret="secret",
)

# Create worker - no type hints help
worker = client.worker_create()  # IDE doesn't know return type

# Send heartbeat
updated_worker = client.worker_heartbeat(worker)

# Get config
config = client.game_server_config(123)  # No parameter names in IDE
```

### Generated Client Usage (After)

```python
from manman_worker_dal_client import ApiClient, DefaultApi, Configuration
from manman_worker_dal_client.exceptions import ApiException
from manman.src.models import Worker, GameServerConfig

# Simple setup
config = Configuration(host="http://dal.manman.local")
client = ApiClient(configuration=config)
api = DefaultApi(client)

# Create worker - IDE shows return type is Worker
worker: Worker = api.worker_create()  # Type-safe!

# Send heartbeat - IDE shows parameter name and type
updated_worker: Worker = api.worker_heartbeat(body=worker)

# Get config - IDE autocompletes parameter names
try:
    config: GameServerConfig = api.game_server_config(id=123)
except ApiException as e:
    print(f"API error: {e.status} - {e.reason}")
```

---

## Real-World Example: Worker Service

### Before (Manual Client)

```python
# manman/src/worker/main.py
from manman.src.repository.api_client import WorkerAPIClient, AuthAPIClient

class WorkerService:
    def __init__(self):
        # Lots of manual setup
        self.auth = AuthAPIClient(base_url=os.getenv("AUTH_URL"))
        self.client = WorkerAPIClient(
            base_url=os.getenv("DAL_URL"),
            auth_api_client=self.auth,
            sa_client_id=os.getenv("CLIENT_ID"),
            sa_client_secret=os.getenv("CLIENT_SECRET"),
        )
    
    def start(self):
        # No type hints, IDE can't help
        worker = self.client.worker_create()
        print(f"Started worker: {worker.worker_id}")
        
        # Manual error handling
        try:
            config = self.client.game_server_config(1)
        except RuntimeError as e:
            # Generic exception, hard to handle specific errors
            print(f"Error: {e}")
            return
        
        # Create instance
        instance = self.client.game_server_instance_create(
            config=config,
            worker_id=worker.worker_id,
        )
```

### After (Generated Client)

```python
# manman/src/worker/main.py
from manman_worker_dal_client import ApiClient, DefaultApi, Configuration
from manman_worker_dal_client.exceptions import ApiException
from manman.src.models import Worker, GameServerConfig, GameServerInstance

class WorkerService:
    def __init__(self):
        # Simple, clean setup
        config = Configuration(host=os.getenv("DAL_URL"))
        self.api = DefaultApi(ApiClient(configuration=config))
    
    def start(self):
        # Full type hints - IDE provides autocomplete
        worker: Worker = self.api.worker_create()
        print(f"Started worker: {worker.worker_id}")
        
        # Structured error handling
        try:
            config: GameServerConfig = self.api.game_server_config(id=1)
        except ApiException as e:
            if e.status == 404:
                print("Config not found")
            elif e.status == 500:
                print("Server error")
            else:
                print(f"Error: {e.status} - {e.reason}")
            return
        
        # Create instance with type safety
        instance_body = GameServerInstance(
            game_server_config_id=config.game_server_config_id,
            worker_id=worker.worker_id,
        )
        instance: GameServerInstance = self.api.game_server_instance_create(
            body=instance_body
        )
```

---

## Testing Comparison

### Manual Client Testing (Before)

```python
# Hard to test - requires mocking requests
import pytest
from unittest.mock import Mock, patch

def test_worker_create():
    with patch('requests.Session.post') as mock_post:
        mock_post.return_value.status_code = 200
        mock_post.return_value.json.return_value = {
            "worker_id": 1,
            "created_date": "2025-01-01T00:00:00Z"
        }
        
        client = WorkerAPIClient(...)
        worker = client.worker_create()
        
        assert worker.worker_id == 1
```

### Generated Client Testing (After)

```python
# Easy to test - comes with mocking support
import pytest
from manman_worker_dal_client import DefaultApi
from manman.src.models import Worker

def test_worker_create():
    # Generated client includes test utilities
    api = DefaultApi()
    
    # Mock at the API level, not HTTP level
    with patch.object(api, 'worker_create') as mock_create:
        mock_create.return_value = Worker(
            worker_id=1,
            created_date="2025-01-01T00:00:00Z"
        )
        
        worker = api.worker_create()
        assert worker.worker_id == 1
```

---

## Migration Strategy

### Step 1: Install Generated Client

```bash
pip install clients/worker-dal-api-client/dist/*.whl
```

### Step 2: Create Adapter (Compatibility Layer)

```python
# manman/src/repository/api_client_adapter.py
"""
Temporary adapter to migrate from manual to generated client.
"""
from manman_worker_dal_client import DefaultApi, Configuration, ApiClient
from manman.src.models import Worker, GameServerConfig, GameServerInstance

class WorkerAPIClientAdapter:
    """Adapter that matches old WorkerAPIClient interface."""
    
    def __init__(self, base_url: str, **kwargs):
        config = Configuration(host=base_url)
        self._api = DefaultApi(ApiClient(configuration=config))
    
    def worker_create(self) -> Worker:
        return self._api.worker_create()
    
    def game_server_config(self, game_server_config_id: int) -> GameServerConfig:
        return self._api.game_server_config(id=game_server_config_id)
    
    # ... implement other methods
```

### Step 3: Update Imports (Gradual Migration)

```python
# Old code
from manman.src.repository.api_client import WorkerAPIClient

# Temporary (during migration)
from manman.src.repository.api_client_adapter import WorkerAPIClientAdapter as WorkerAPIClient

# New code (after migration complete)
from manman_worker_dal_client import DefaultApi
```

### Step 4: Remove Manual Client

```bash
# Once all code migrated
git rm manman/src/repository/api_client.py
git rm manman/src/repository/api_client_adapter.py
```

---

## Summary

| Aspect | Manual Client | Generated Client |
|--------|--------------|------------------|
| **Lines of Code** | ~300 lines | 0 lines (auto-generated) |
| **Maintenance** | High - manual updates | Low - regenerate script |
| **Type Safety** | Partial | Complete |
| **Documentation** | None | Auto-generated |
| **Error Handling** | Basic | Structured |
| **Testing** | Complex mocking | Built-in test support |
| **Distribution** | Git submodule | Standard pip package |
| **IDE Support** | Limited | Full autocomplete |
| **Sync with API** | Manual, error-prone | Automatic, guaranteed |

**Conclusion:** Generated client eliminates maintenance burden while providing better type safety, documentation, and developer experience.
