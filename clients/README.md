# OpenAPI Client Generation

This directory contains tools and documentation for generating Python client libraries for ManMan APIs.

## 📚 Documentation

- **[api-gen-implementation.md](../manman/design/api-gen-implementation.md)** - Complete implementation guide with detailed strategies
- **[api-gen.md](../manman/design/api-gen.md)** - Original design document

## 🚀 Quick Start

### Prerequisites

1. **Install OpenAPI Generator CLI**

   ```bash
   # Via npm (recommended)
   npm install @openapitools/openapi-generator-cli -g
   
   # Or via Docker
   docker pull openapitools/openapi-generator-cli
   
   # Verify installation
   openapi-generator-cli version
   ```

2. **Install Python build tools** (optional, for building wheels)

   ```bash
   pip install build
   ```

### Generate Clients

**Option 1: With Duplicated Models (Easiest, works immediately)**

```bash
# Generate all clients with duplicated models
python tools/generate_clients.py --strategy=duplicate

# Generate specific API only
python tools/generate_clients.py --strategy=duplicate --api=worker-dal-api

# Generate and build wheels
python tools/generate_clients.py --strategy=duplicate --build-wheel
```

**Option 2: With Shared Models (Ideal, no duplication)**

```bash
# Generate all clients with shared models
python tools/generate_clients.py --strategy=shared

# Generated clients will import from manman.src.models
```

### Generated Output

```
clients/
├── experience-api-client/
│   ├── manman_experience_client/  # Generated package
│   ├── setup.py                    # Package setup
│   ├── README.md                   # Generated docs
│   └── dist/                       # Wheels (if --build-wheel used)
│       └── manman_experience_client-0.1.0-py3-none-any.whl
├── status-api-client/
│   └── ...
└── worker-dal-api-client/
    └── ...
```

## 📦 Using Generated Clients

### Installation

```bash
# Install from wheel
pip install clients/experience-api-client/dist/*.whl

# Or install in development mode
pip install -e clients/experience-api-client/
```

### Example Usage

```python
from manman_experience_client import ApiClient, DefaultApi, Configuration
from manman.src.models import Worker  # Shared models

# Configure client
config = Configuration(
    host="https://experience.manman.local"
)
client = ApiClient(configuration=config)
api = DefaultApi(client)

# Make API calls
worker = api.worker_current()
print(f"Worker ID: {worker.worker_id}")

# The returned object is the same Worker class used by the server
assert isinstance(worker, Worker)
```

## 🔧 Development Workflow

### 1. Make Changes to API

```python
# Edit manman/src/host/api/experience/api.py
@router.get("/worker/new-endpoint")
async def new_endpoint() -> Worker:
    # New endpoint
    pass
```

### 2. Regenerate Clients

```bash
python tools/generate_clients.py --api=experience-api
```

### 3. Test Changes

```python
# The client now has the new method
api.new_endpoint()
```

### 4. Distribute

```bash
cd clients/experience-api-client
python -m build
# Upload to PyPI or internal package repository
```

## 🧪 Testing Generated Clients

### Unit Tests

```python
# tests/test_generated_client.py
import pytest
from manman_experience_client import ApiClient, DefaultApi
from manman.src.models import Worker

def test_shared_models():
    """Verify client uses shared models (if strategy=shared)."""
    from manman_experience_client.api.default_api import DefaultApi
    import inspect
    
    sig = inspect.signature(DefaultApi.worker_current)
    # Should return the actual shared Worker class
    assert sig.return_annotation is Worker
```

### Integration Tests

```python
@pytest.fixture
def test_server():
    """Start test FastAPI server."""
    from manman.src.host.api.experience import create_app
    from fastapi.testclient import TestClient
    return TestClient(create_app())

def test_client_against_server(test_server):
    """Test generated client works with real API."""
    # Configure client to use test server
    config = Configuration(host="http://testserver")
    client = ApiClient(configuration=config)
    api = DefaultApi(client)
    
    # Make actual API call
    worker = api.worker_current()
    assert worker.worker_id > 0
```

## 🎯 Comparison: Duplicate vs Shared Strategies

| Aspect | Duplicate Strategy | Shared Strategy |
|--------|-------------------|-----------------|
| **Setup Complexity** | Low - works immediately | Medium - requires model copying |
| **Model Duplication** | Yes - each client has own models | No - imports from manman.src.models |
| **Type Safety** | Good - but separate types | Excellent - same types as server |
| **Package Size** | Larger - includes duplicate models | Smaller - reuses shared models |
| **Migration Effort** | None initially, migration needed later | More upfront, but clean long-term |
| **External Distribution** | Easy - self-contained | Requires manman.src.models in package |
| **Recommended For** | Prototyping, quick wins | Production, long-term maintenance |

## 🔄 Migration Path

### From Duplicate to Shared

If you start with duplicate models and want to migrate:

1. **Regenerate with shared strategy**
   ```bash
   python tools/generate_clients.py --strategy=shared
   ```

2. **Update consumer imports**
   ```python
   # Old (duplicate strategy)
   from manman_experience_client.models import Worker
   
   # New (shared strategy)
   from manman.src.models import Worker
   ```

3. **Publish new major version**
   - `manman-experience-client` v1.x.x → v2.0.0
   - Document breaking changes in CHANGELOG

## 📝 Current APIs

| API Name | Purpose | Base Path | Status |
|----------|---------|-----------|--------|
| **experience-api** | Worker-facing operations | `/experience` | ✅ Ready |
| **status-api** | Status queries | `/status` | ✅ Ready |
| **worker-dal-api** | Data access layer | `/workerdal` | ✅ Ready |

## 🛠️ Troubleshooting

### Issue: "openapi-generator-cli not found"

**Solution:** Install the CLI tool:
```bash
npm install @openapitools/openapi-generator-cli -g
```

Or use Docker:
```bash
alias openapi-generator-cli='docker run --rm -v ${PWD}:/local openapitools/openapi-generator-cli'
```

### Issue: "ModuleNotFoundError: No module named 'manman'"

**Solution:** For shared strategy, models must be copied into the client package. The script does this automatically, but verify:
```bash
ls clients/experience-api-client/manman/src/models.py
```

### Issue: Generated client has import errors

**Solution:** Check that the OpenAPI spec is valid:
```bash
# Validate spec
openapi-generator-cli validate -i openapi-specs/experience-api.json
```

### Issue: Type mismatches at runtime

**Solution:** Ensure API endpoints use actual model types, not `dict`:
```python
# Good ✅
@router.get("/worker")
async def get_worker() -> Worker:
    return worker

# Bad ❌
@router.get("/worker")
async def get_worker() -> dict:
    return worker.model_dump()
```

## 🚦 CI/CD Integration

Add to your CI pipeline:

```yaml
# .github/workflows/client-generation.yml
name: Generate Clients

on:
  push:
    paths:
      - 'manman/src/host/api/**'
      - 'manman/src/models.py'

jobs:
  generate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      
      - name: Setup Python
        uses: actions/setup-python@v4
        with:
          python-version: '3.11'
      
      - name: Install OpenAPI Generator
        run: npm install @openapitools/openapi-generator-cli -g
      
      - name: Generate Clients
        run: python tools/generate_clients.py --strategy=shared --build-wheel
      
      - name: Upload Artifacts
        uses: actions/upload-artifact@v3
        with:
          name: client-wheels
          path: clients/*/dist/*.whl
```

## 📚 Further Reading

- [OpenAPI Generator Documentation](https://openapi-generator.tech/)
- [FastAPI OpenAPI Documentation](https://fastapi.tiangolo.com/advanced/extending-openapi/)
- [Pydantic Models](https://docs.pydantic.dev/latest/)
- [Full Implementation Guide](../manman/design/api-gen-implementation.md)

## 🤝 Contributing

When adding new API endpoints:

1. Add endpoint to appropriate API module
2. Use shared models from `manman.src.models`
3. Run `python tools/generate_clients.py` to regenerate
4. Test with generated client
5. Update version in client if breaking changes

## 📞 Support

For issues with:
- **Client generation**: See troubleshooting section above
- **API design**: Review `manman/design/api-gen-implementation.md`
- **Model design**: See `manman/src/models.py`
