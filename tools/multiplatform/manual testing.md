# Container Build Testing TODO

## Testing Platform-Specific Container Builds

This repository now supports platform-specific container builds with proper cross-compilation for Python dependencies. Here are the testing procedures:

### Prerequisites
- Bazel 8.0.0+ 
- Docker daemon running
- Internet access to `bcr.bazel.build` (Bazel Central Registry)

### Platform-Specific Build Commands

#### 🐍 Python Containers (FastAPI & Hello Python)

```bash
# AMD64 Linux containers
bazel run //demo/hello_python:hello_python_image_amd64_load --platforms=//tools:linux_x86_64
bazel run //demo/hello_fastapi:hello_fastapi_image_amd64_load --platforms=//tools:linux_x86_64

# ARM64 Linux containers  
bazel run //demo/hello_python:hello_python_image_arm64_load --platforms=//tools:linux_arm64
bazel run //demo/hello_fastapi:hello_fastapi_image_arm64_load --platforms=//tools:linux_arm64
```

#### 🐹 Go Containers

```bash
# AMD64 Linux containers
bazel run //demo/hello_go:hello_go_image_amd64_load --platforms=//tools:linux_x86_64

# ARM64 Linux containers
bazel run //demo/hello_go:hello_go_image_arm64_load --platforms=//tools:linux_arm64
```

### Testing on Linux AMD64 Platforms

If you're running on a Linux AMD64 machine, the containers should run natively without platform warnings:

```bash
# Test Python containers
docker run --rm demo-hello_python:latest-amd64
docker run --rm demo-hello_fastapi:latest-amd64

# Test Go containers  
docker run --rm demo-hello_go:latest-amd64

# Test ARM64 containers (will show platform warning but should work with emulation)
docker run --rm demo-hello_python:latest-arm64
docker run --rm demo-hello_fastapi:latest-arm64
docker run --rm demo-hello_go:latest-arm64
```

### Expected Outputs

#### Hello Python
```
Hello, world from uv and Bazel BASIL test from Python!
Version: 1.0.0
```

#### Hello FastAPI
```
INFO:     Started server process [1]
INFO:     Waiting for application startup.
INFO:     Application startup complete.
INFO:     Uvicorn running on http://0.0.0.0:8000 (Press CTRL+C to quit)
```

Then test the API:
```bash
curl http://localhost:8000/
# Expected: {"message":"hello world"}
```

#### Hello Go
```
Hello, world from Bazel from Go!
Version: 1.0.0
```

### Validation Steps

1. **Cross-compilation verification**: Check that Linux containers contain Linux wheels:
   ```bash
   # Should show manylinux wheels, not macosx wheels
   docker run --rm --entrypoint=/bin/sh demo-hello_fastapi:latest-amd64 \
     -c "find /app -name '*pydantic_core*' | head -5"
   ```

2. **Platform-specific dependencies**: Verify platform-specific pip dependencies are used:
   ```bash
   # Check AMD64 container uses pip_deps_linux_amd64
   docker run --rm demo-hello_fastapi:latest-amd64 python3 -c "import sys; print(sys.path)"
   
   # Check ARM64 container uses pip_deps_linux_arm64  
   docker run --rm demo-hello_fastapi:latest-arm64 python3 -c "import sys; print(sys.path)"
   ```

3. **Binary verification**: Ensure platform-specific binaries are used:
   ```bash
   # Should show hello_fastapi_linux_amd64 not hello_fastapi_amd64
   docker inspect demo-hello_fastapi:latest-amd64 | jq '.[0].Config.Entrypoint'
   ```

### Known Issues & Limitations

1. **Manual Platform Flags Required**: Platform-specific targets require explicit `--platforms` flags
2. **macOS Host Cross-compilation**: Building Linux containers on macOS requires the explicit platform specification
3. **First Build Time**: Initial builds take 20-40 minutes due to dependency downloads
4. **Network Dependency**: Builds require internet access to Bazel Central Registry

### Troubleshooting

#### Build Failures
- **Network errors**: Ensure `curl -s https://bcr.bazel.build/` succeeds
- **Platform errors**: Always specify `--platforms` flag for cross-platform builds
- **Cache issues**: Run `bazel clean` if seeing stale build artifacts

#### Container Runtime Issues
- **Exec format errors**: Running wrong architecture container (e.g., AMD64 on ARM64 without emulation)
- **Import errors**: Cross-compilation dependency issues - verify correct platform-specific pip dependencies

#### Performance Issues
- **Long build times**: First builds are slow; subsequent builds use cache
- **Large images**: Python containers ~300MB, Go containers ~20MB

### Platform Matrix

| Platform | Host Support | Container Support | Notes |
|----------|-------------|------------------|-------|
| Linux AMD64 | ✅ Native | ✅ Native | Optimal performance |
| Linux ARM64 | ✅ Native | ✅ Native | Optimal performance |  
| macOS ARM64 | ✅ Development | ❌ Cross-compile only | Requires --platforms flag |
| Windows | ❓ Untested | ❓ Untested | Likely requires WSL2 |

### Development Workflow

1. **Make code changes**
2. **Build platform-specific containers**:
   ```bash
   bazel run //demo/app:app_image_amd64_load --platforms=//tools:linux_x86_64
   ```
3. **Test locally**:
   ```bash
   docker run --rm demo-app:latest-amd64
   ```
4. **Commit and push** - CI will build all platforms automatically

### CI/CD Integration

The GitHub Actions CI automatically:
- Builds all platform variants
- Runs tests for each platform
- Publishes multi-platform container manifests
- Uses caching for performance

See `.github/workflows/ci.yml` for implementation details.