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

This repository currently uses only Go standard library packages and internal packages. No external Go dependencies are needed.

### If you need to add external Go dependencies in the future

1. Add `go.mod` file with the dependency
2. Enable and run gazelle rules (currently commented out in BUILD.bazel)
3. Import normally in Go code

**Current state:** All Go code uses standard library (`fmt`, `os`, `encoding/json`, etc.) and internal packages (`github.com/example/everything/libs/go`).
