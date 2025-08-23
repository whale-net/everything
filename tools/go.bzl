"""Go utilities for the monorepo."""

def _go_cache_init_impl(ctx):
    """Implementation for go_cache_init rule."""
    output = ctx.actions.declare_file(ctx.label.name + "_cache_init.sh")
    
    script_content = """#!/bin/bash
set -euo pipefail

# Initialize Go module cache directories to prevent warnings
echo "Initializing Go cache directories..."

# Create Go module cache directory
GO_MOD_CACHE_DIR="${GOMODCACHE:-${HOME}/go/pkg/mod}"
mkdir -p "$GO_MOD_CACHE_DIR"
echo "Created Go module cache directory: $GO_MOD_CACHE_DIR"

# Create Go build cache directory  
GO_BUILD_CACHE_DIR="${GOCACHE:-${HOME}/.cache/go-build}"
mkdir -p "$GO_BUILD_CACHE_DIR"
echo "Created Go build cache directory: $GO_BUILD_CACHE_DIR"

# Set proper permissions
chmod -R 755 "$GO_MOD_CACHE_DIR" 2>/dev/null || true
chmod -R 755 "$GO_BUILD_CACHE_DIR" 2>/dev/null || true

echo "Go cache directories initialized successfully."

# Print Go environment for debugging
if command -v go >/dev/null 2>&1; then
    echo "Go environment:"
    go env GOMODCACHE || echo "GOMODCACHE: $GO_MOD_CACHE_DIR"
    go env GOCACHE || echo "GOCACHE: $GO_BUILD_CACHE_DIR"
else
    echo "Go not found in PATH, using default cache locations:"
    echo "GOMODCACHE: $GO_MOD_CACHE_DIR"
    echo "GOCACHE: $GO_BUILD_CACHE_DIR"
fi
"""
    
    ctx.actions.write(
        output = output,
        content = script_content,
        is_executable = True,
    )
    
    return [DefaultInfo(
        executable = output,
        runfiles = ctx.runfiles(),
    )]

go_cache_init = rule(
    implementation = _go_cache_init_impl,
    executable = True,
    doc = "Initializes Go cache directories to prevent warnings in CI/CD environments.",
)

def _go_env_info_impl(ctx):
    """Implementation for go_env_info rule."""
    output = ctx.actions.declare_file(ctx.label.name + "_env_info.sh")
    
    script_content = """#!/bin/bash
set -euo pipefail

echo "=== Go Environment Information ==="
if command -v go >/dev/null 2>&1; then
    echo "Go version: $(go version)"
    echo "GOROOT: $(go env GOROOT)"
    echo "GOPATH: $(go env GOPATH)"
    echo "GOMODCACHE: $(go env GOMODCACHE)"
    echo "GOCACHE: $(go env GOCACHE)"
    echo "GOPROXY: $(go env GOPROXY)"
    echo "GOSUMDB: $(go env GOSUMDB)"
else
    echo "Go is not installed or not in PATH"
    exit 1
fi

echo ""
echo "=== Cache Directory Status ==="
GOMODCACHE_DIR=$(go env GOMODCACHE)
GOCACHE_DIR=$(go env GOCACHE)

if [ -d "$GOMODCACHE_DIR" ]; then
    echo "✓ Go module cache exists: $GOMODCACHE_DIR"
    echo "  Size: $(du -sh "$GOMODCACHE_DIR" 2>/dev/null | cut -f1 || echo "unknown")"
else
    echo "✗ Go module cache missing: $GOMODCACHE_DIR"
fi

if [ -d "$GOCACHE_DIR" ]; then
    echo "✓ Go build cache exists: $GOCACHE_DIR"
    echo "  Size: $(du -sh "$GOCACHE_DIR" 2>/dev/null | cut -f1 || echo "unknown")"
else
    echo "✗ Go build cache missing: $GOCACHE_DIR"
fi
"""
    
    ctx.actions.write(
        output = output,
        content = script_content,
        is_executable = True,
    )
    
    return [DefaultInfo(
        executable = output,
        runfiles = ctx.runfiles(),
    )]

go_env_info = rule(
    implementation = _go_env_info_impl,
    executable = True,
    doc = "Displays Go environment information and cache status.",
)
