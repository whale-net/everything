# Managing Dependencies

This guide covers adding and managing dependencies for both Python and Go applications.

## Python Dependencies

The repository uses **uv** for Python dependency management with **rules_pycross** for cross-platform Bazel builds. This provides unified lock files and native Bazel integration without abstraction layers.

### Steps to add a new dependency

1. **Add to `pyproject.toml`** under the `dependencies` array:
   ```toml
   dependencies = [
       # ... existing dependencies
       "your-new-package",
   ]
   ```

2. **Regenerate the lock file** using uv:
   ```bash
   uv lock --python 3.13
   ```

3. **Use directly in BUILD.bazel** with the `@pypi//` syntax:
   ```starlark
   py_test(
       name = "test_main",
       srcs = ["test_main.py"],
       deps = [
           "@pypi//:pytest",
           "@pypi//:your-new-package",  # Use exact package name with hyphens
       ],
   )
   ```

### Important Notes

- **Package names**: Use exact PyPI package names including hyphens (e.g., `python-jose`, not `python_jose`)
- **Top-level colon**: Always use `@pypi//:package-name` format (colon before package name)
- **No conversion**: Package names are used as-is - pycross preserves the original names
- **Cross-platform**: The uv.lock file includes platform-specific wheels for Linux (amd64/arm64) and macOS (arm64)

### Example - Adding FastAPI

```bash
# 1. Edit pyproject.toml
echo '    "fastapi",' >> pyproject.toml  # Add to dependencies array

# 2. Regenerate lock file
uv lock --python 3.13

# 3. Use in BUILD.bazel
deps = ["@pypi//:fastapi"]
```

## Go Dependencies

This repository uses external Go dependencies with **automated dependency tracking** via `go.mod` and Bazel's Bzlmod system.

### Quick Start - Adding a Go Dependency

```bash
# 1. Add the dependency
go get github.com/package/name@version

# 2. Update BUILD files
bazel run //:gazelle

# 3. Update MODULE.bazel
bazel mod tidy

# 4. Build
bazel build //your/target
```

### How It Works

Dependencies are declared in `go.mod` and automatically synced to `MODULE.bazel`:

1. **`go.mod`** - Declares direct dependencies using standard Go tooling
2. **Gazelle** - Auto-generates BUILD.bazel files from Go imports
3. **`bazel mod tidy`** - Automatically manages `use_repo()` in MODULE.bazel

**Key benefit:** Transitive dependencies are automatically resolved - no manual tracking required!

### Example - Adding UUID Package

```bash
# Add dependency
go get github.com/google/uuid@v1.6.0

# Use in your code
cat > mypackage/id.go <<'EOF'
package mypackage

import "github.com/google/uuid"

func GenerateID() string {
    return uuid.New().String()
}
EOF

# Update Bazel
bazel run //:gazelle
bazel mod tidy

# Build
bazel build //mypackage
```

### Detailed Documentation

For comprehensive documentation on the Go dependency workflow, see [GO_DEPENDENCIES.md](GO_DEPENDENCIES.md).
