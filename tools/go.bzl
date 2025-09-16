"""Go utilities for the monorepo."""

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
