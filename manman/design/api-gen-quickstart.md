# OpenAPI Client Generation - Quick Reference

## TL;DR

Generate Python clients for ManMan APIs with shared models to avoid duplication.

**Quick Start:**
```bash
# Install OpenAPI Generator
npm install @openapitools/openapi-generator-cli -g

# Generate all clients (with duplicated models - works immediately)
python tools/generate_clients.py --strategy=duplicate --build-wheel

# Or generate with shared models (no duplication - requires setup)
python tools/generate_clients.py --strategy=shared --build-wheel
```

**Result:** Distributable Python clients in `clients/*/dist/*.whl`

## What This Does

### Before (Manual Client)
- Hand-written `WorkerAPIClient` in `api_client.py`
- Manual maintenance required for each API change
- Models manually deserialized from responses
- Comment says: `# TODO - is there a way to auto generate this?`

### After (Generated Clients)
- Automatically generated clients from OpenAPI specs
- Type-safe methods with proper return types
- Models shared between client and server (same classes!)
- Regenerate on API changes - zero manual work

### Example Comparison

**Manual (Before):**
```python
# api_client.py - manual implementation
class WorkerAPIClient:
    def worker_create(self):
        response = self._session.post("/worker/create")
        if response.status_code != 200:
            raise RuntimeError(response.content)
        return Worker.model_validate_json(response.content)  # Manual deserialization
```

**Generated (After):**
```python
# Auto-generated
from manman_worker_dal_client import DefaultApi
from manman.src.models import Worker  # Same model as server!

api = DefaultApi()
worker: Worker = api.worker_create()  # Type-safe, auto-deserialized
```

## Architecture

### Current State
```
manman/
├── src/
│   ├── models.py          # ✅ Shared Pydantic models (Worker, GameServerInstance, etc.)
│   └── host/
│       ├── openapi.py     # ✅ Generates OpenAPI specs (already exists!)
│       └── api/
│           ├── experience/    # FastAPI routes
│           ├── status/        # FastAPI routes
│           └── worker_dal/    # FastAPI routes
```

### New Addition
```
tools/
└── generate_clients.py    # 🆕 Client generation script

clients/
├── README.md              # 🆕 Usage documentation
├── BUILD.bazel            # 🆕 Bazel integration
├── experience-api-client/ # 🆕 Generated client
├── status-api-client/     # 🆕 Generated client
└── worker-dal-api-client/ # 🆕 Generated client (replaces manual api_client.py)
```

## Two Strategies Explained

### Strategy 1: Duplicate Models (Phase 1)

**How it works:**
- OpenAPI Generator creates model classes inside each client
- Each client is completely self-contained
- Models are duplicated across clients

**Pros:**
- Works immediately with zero setup
- Standard OpenAPI generator workflow
- Easy to distribute (no external dependencies)

**Cons:**
- Model duplication (Worker class exists in 3 places)
- Type safety issues (client Worker ≠ server Worker)

**When to use:** Quick prototyping, initial implementation, validating approach

### Strategy 2: Shared Models (Phase 2)

**How it works:**
- Configure OpenAPI Generator to skip model generation
- Generated clients import models from `manman.src.models`
- Copy model source into client package for distribution

**Pros:**
- No duplication - DRY principle
- Client and server use identical types
- Single source of truth for models

**Cons:**
- Requires configuration setup
- Slightly more complex build process

**When to use:** Production use, long-term maintenance

### Migration Path

1. **Start with duplicate** → Get clients working quickly
2. **Validate approach** → Test with real usage
3. **Move to shared** → Eliminate duplication
4. **Deprecate manual client** → Remove `api_client.py`

## Key Files

| File | Purpose | Status |
|------|---------|--------|
| `tools/generate_clients.py` | Main generation script | 🆕 Ready to use |
| `clients/README.md` | Usage documentation | 🆕 Complete |
| `clients/BUILD.bazel` | Bazel integration | 🆕 Basic target |
| `manman/design/api-gen-implementation.md` | Detailed guide | 🆕 Comprehensive |
| `manman/src/host/openapi.py` | OpenAPI spec generator | ✅ Already works |
| `manman/src/models.py` | Shared models | ✅ Already exists |
| `manman/src/repository/api_client.py` | Manual client | ⚠️ To be replaced |

## Usage Examples

### Generate All Clients
```bash
python tools/generate_clients.py --strategy=shared --build-wheel
```

### Generate Single Client
```bash
python tools/generate_clients.py --api=worker-dal-api
```

### Use Generated Client
```python
from manman_worker_dal_client import ApiClient, DefaultApi, Configuration
from manman.src.models import Worker

config = Configuration(host="https://dal.manman.local")
client = ApiClient(configuration=config)
api = DefaultApi(client)

# Create worker
worker = api.worker_create()
print(f"Created worker: {worker.worker_id}")

# Get instance
instance = api.server_instance(instance_id=123)
print(f"Instance status: {instance.last_heartbeat}")
```

## Benefits Over Manual Client

| Aspect | Manual (`api_client.py`) | Generated |
|--------|-------------------------|-----------|
| **Maintenance** | Update code for every API change | Regenerate script |
| **Type Safety** | Manual typing, prone to drift | Auto-generated types |
| **Documentation** | Write manually | Auto-generated from OpenAPI |
| **Testing** | Write for each method | Generator includes tests |
| **Models** | Manual deserialization | Automatic with shared models |
| **Distribution** | Copy-paste or git submodule | Standard pip package |

## Implementation Checklist

- [x] Create generation script (`tools/generate_clients.py`)
- [x] Write comprehensive guide (`manman/design/api-gen-implementation.md`)
- [x] Create usage documentation (`clients/README.md`)
- [x] Add Bazel build target (`clients/BUILD.bazel`)
- [ ] Install OpenAPI Generator (`npm install -g ...`)
- [ ] Run generation for one API (test)
- [ ] Validate generated client works
- [ ] Run generation for all APIs
- [ ] Update manual client consumers to use generated clients
- [ ] Deprecate `api_client.py`
- [ ] Add CI/CD automation
- [ ] Publish clients to package repository

## Next Steps

1. **Review the implementation guide:**
   - Read `manman/design/api-gen-implementation.md` for full details

2. **Install prerequisites:**
   ```bash
   npm install @openapitools/openapi-generator-cli -g
   pip install build
   ```

3. **Test with one API:**
   ```bash
   python tools/generate_clients.py --api=worker-dal-api --strategy=duplicate
   ```

4. **Validate it works:**
   ```bash
   cd clients/worker-dal-api-client
   python -m build
   pip install dist/*.whl
   python -c "from manman_worker_dal_client import DefaultApi; print('Success!')"
   ```

5. **Generate all clients:**
   ```bash
   python tools/generate_clients.py --strategy=shared --build-wheel
   ```

6. **Integrate with Bazel:**
   ```bash
   bazel run //clients:generate_clients
   ```

## FAQ

**Q: Do I need to choose between duplicate and shared strategies?**  
A: Start with duplicate (easier), migrate to shared later (better).

**Q: Will this replace the manual `api_client.py`?**  
A: Yes! The generated `worker-dal-api-client` should replace it.

**Q: Can I use generated clients outside the monorepo?**  
A: Yes! Install the wheel in any Python environment.

**Q: What if I make breaking API changes?**  
A: Regenerate clients and bump major version (e.g., 1.x → 2.0).

**Q: How do I add authentication?**  
A: Configure the ApiClient with auth headers or tokens (OpenAPI supports this).

**Q: Can I customize generated code?**  
A: Yes, via OpenAPI Generator templates, but try to avoid customization.

## Resources

- 📖 [Full Implementation Guide](../manman/design/api-gen-implementation.md)
- 📖 [Client Usage Guide](../clients/README.md)
- 🔗 [OpenAPI Generator Docs](https://openapi-generator.tech/)
- 🔗 [FastAPI OpenAPI](https://fastapi.tiangolo.com/advanced/extending-openapi/)

---

**Ready to start?** Run `python tools/generate_clients.py --help` for options!
