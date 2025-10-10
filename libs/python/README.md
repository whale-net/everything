# Python Libraries

Shared Python utilities and libraries for the Everything monorepo.

## Modules

### log_setup.py

Unified logging configuration that integrates release app properties (app_type, app_name, domain) with deployment properties (environment via APP_ENV).

**Key Features:**
- App metadata integration (from `release_app` macro)
- Environment awareness (APP_ENV)
- OpenTelemetry (OTEL) support
- Consistent log formatting
- Third-party library noise reduction

**Quick Start:**
```python
from libs.python.log_setup import setup_logging

setup_logging(
    level=logging.INFO,
    app_name="my-app",
    app_type="external-api",
    domain="demo",
    enable_otel=False,
)
```

**Log Format:**
```
[domain/app-name/app-type/env] module - LEVEL - message
```

**Documentation:**
- [LOGGING.md](LOGGING.md) - Detailed usage guide
- [MIGRATION_GUIDE.md](MIGRATION_GUIDE.md) - Migration from manman.src.logging_config
- [ARCHITECTURE.md](ARCHITECTURE.md) - System architecture and design

**Example:**
```bash
# Basic example
python3 log_setup_example.py

# Integration example (with metadata from env vars)
python3 integration_example.py
```

### utils.py

Common utilities for Python applications.

```python
from libs.python.utils import format_greeting, get_version
```

## Usage in BUILD.bazel

```starlark
py_binary(
    name = "my_app",
    srcs = ["main.py"],
    deps = [
        "//libs/python",  # Includes all modules
    ],
)
```

## Testing

Run tests:
```bash
bazel test //libs/python:log_setup_test
```

## Contributing

When adding new modules:

1. Add source file to `srcs` in BUILD.bazel
2. Create corresponding test file
3. Update this README
4. Add documentation if needed
